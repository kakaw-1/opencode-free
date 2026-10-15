package main

import (
	"encoding/json"
	"testing"
)

// testCatalogue mirrors the shape of opencode_tools.json with the two entries
// the cases below exercise. The real catalogue is 12 entries; only the ones a
// test names need to exist here.
func testCatalogue() toolCatalogue {
	return toolCatalogue{
		"read": {
			Description: "Read the contents of a file or directory.",
			Parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"path":   map[string]any{"type": "string"},
					"offset": map[string]any{"type": "integer", "minimum": float64(0)},
					"limit":  map[string]any{"type": "integer", "minimum": float64(0)},
				},
				"required": []any{"path"},
			},
		},
		"shell": {
			Description: "Execute a shell command and return its output.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"command": map[string]any{"type": "string"}},
				"required":   []any{"command"},
			},
		},
	}
}

func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("rewritten body is not JSON: %v", err)
	}
	return out
}

func toolNames(t *testing.T, payload map[string]any) []string {
	t.Helper()
	items, _ := payload["tools"].([]any)
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, chatToolName(item))
	}
	return names
}

func hasTool(t *testing.T, payload map[string]any, name string) bool {
	t.Helper()
	for _, n := range toolNames(t, payload) {
		if n == name {
			return true
		}
	}
	return false
}

// defOf returns the description the payload carries for a tool name.
func defOf(t *testing.T, payload map[string]any, name string) string {
	t.Helper()
	items, _ := payload["tools"].([]any)
	for _, item := range items {
		if chatToolName(item) != name {
			continue
		}
		fn, _ := item.(map[string]any)["function"].(map[string]any)
		desc, _ := fn["description"].(string)
		return desc
	}
	return ""
}

func TestInjectsReadWhenToolsAbsent(t *testing.T) {
	c := testCatalogue()
	body := []byte(`{"model":"m","stream":true,"messages":[]}`)
	out, changed := prepareBody(body, true, c)
	if !changed {
		t.Fatal("expected the body to change")
	}
	payload := decode(t, out)
	if !hasTool(t, payload, "read") {
		t.Fatalf("read not injected: %v", toolNames(t, payload))
	}
}

func TestInjectsReadWhenToolsEmpty(t *testing.T) {
	c := testCatalogue()
	body := []byte(`{"model":"m","stream":true,"tools":[]}`)
	out, changed := prepareBody(body, true, c)
	if !changed {
		t.Fatal("expected the body to change")
	}
	if names := toolNames(t, decode(t, out)); len(names) != 1 || names[0] != "read" {
		t.Fatalf("want exactly [read], got %v", names)
	}
}

func TestKeepsClientToolsAndAppendsRead(t *testing.T) {
	c := testCatalogue()
	body := []byte(`{"model":"m","stream":true,"tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]}`)
	out, changed := prepareBody(body, true, c)
	if !changed {
		t.Fatal("expected the body to change")
	}
	payload := decode(t, out)
	if !hasTool(t, payload, "get_weather") {
		t.Fatalf("client tool was dropped: %v", toolNames(t, payload))
	}
	if !hasTool(t, payload, "read") {
		t.Fatalf("read not injected: %v", toolNames(t, payload))
	}
	if desc := defOf(t, payload, "get_weather"); desc != "Get weather" {
		t.Fatalf("client tool definition was rewritten: %q", desc)
	}
}

// A client that declares "read" itself (Claude Code's own tool set, or another
// OpenCode-shaped caller) must get the genuine definition, because the gate
// rejects a recognised name carrying anything else.
func TestReplacesStubReadWithGenuine(t *testing.T) {
	c := testCatalogue()
	body := []byte(`{"model":"m","stream":true,"tools":[{"type":"function","function":{"name":"read","description":"Read a file.","parameters":{"type":"object","properties":{"file_path":{"type":"string"}}}}}]}`)
	out, changed := prepareBody(body, true, c)
	if !changed {
		t.Fatal("expected the stub to be replaced")
	}
	payload := decode(t, out)
	if names := toolNames(t, payload); len(names) != 1 {
		t.Fatalf("want exactly [read], got %v", names)
	}
	if desc := defOf(t, payload, "read"); desc != c["read"].Description {
		t.Fatalf("stub read survived: %q", desc)
	}
}

func TestReplacesRecognisedNonCoreName(t *testing.T) {
	c := testCatalogue()
	body := []byte(`{"model":"m","stream":true,"tools":[{"type":"function","function":{"name":"shell","description":"agent tool shell","parameters":{"type":"object","properties":{}}}}]}`)
	out, _ := prepareBody(body, true, c)
	if desc := defOf(t, decode(t, out), "shell"); desc != c["shell"].Description {
		t.Fatalf("recognised name kept its stub: %q", desc)
	}
}

// Idempotence: running the rewrite over its own output must be a no-op, so a
// retry or a second interception pass cannot grow the tool list.
func TestIdempotent(t *testing.T) {
	c := testCatalogue()
	body := []byte(`{"model":"m","messages":[]}`)
	once, _ := prepareBody(body, true, c)
	twice, changed := prepareBody(once, true, c)
	if changed {
		t.Fatal("second pass reported a change")
	}
	if string(once) != string(twice) {
		t.Fatalf("second pass altered the body:\n%s\n%s", once, twice)
	}
}

func TestForcesStream(t *testing.T) {
	c := testCatalogue()
	for _, body := range []string{
		`{"model":"m","messages":[]}`,
		`{"model":"m","stream":false,"messages":[]}`,
	} {
		out, changed := prepareBody([]byte(body), true, c)
		if !changed {
			t.Fatalf("expected a change for %s", body)
		}
		if got := decode(t, out)["stream"]; got != true {
			t.Fatalf("stream not forced for %s: %v", body, got)
		}
	}
	// An already-streaming request must not be re-encoded.
	body := []byte(`{"model":"m","stream":true,"tools":[{"type":"function","function":{"name":"read","description":"Read the contents of a file or directory.","parameters":{"type":"object","additionalProperties":false,"properties":{"path":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":0}},"required":["path"]}}}]}`)
	if _, changed := prepareBody(body, true, c); changed {
		t.Fatal("a request that already satisfies the gate was rewritten")
	}
}

func TestForceStreamDisabledLeavesStreamAlone(t *testing.T) {
	c := testCatalogue()
	body := []byte(`{"model":"m","stream":false,"messages":[]}`)
	out, _ := prepareBody(body, false, c)
	if got := decode(t, out)["stream"]; got != false {
		t.Fatalf("forceStream=false still rewrote stream: %v", got)
	}
}

func TestNonJSONPassesThroughUntouched(t *testing.T) {
	c := testCatalogue()
	for _, body := range []string{"", "not json at all", "[1,2,3]", `"a string"`} {
		out, changed := prepareBody([]byte(body), true, c)
		if changed {
			t.Fatalf("changed a body it does not understand: %q", body)
		}
		if string(out) != body {
			t.Fatalf("body round-tripped: %q -> %q", body, out)
		}
	}
}

// A "tools" value of the wrong type is left alone rather than replaced: the
// upstream will reject it, but inventing a list would hide the caller's bug.
func TestToolsOfWrongTypeLeftAlone(t *testing.T) {
	c := testCatalogue()
	body := []byte(`{"model":"m","stream":true,"tools":{"read":{}}}`)
	_, changed := prepareBody(body, true, c)
	if changed {
		t.Fatal("a non-array tools value was rewritten")
	}
}
