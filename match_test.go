package main

import "testing"

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern string
		input   string
		want    bool
	}{
		{"mimo-free", "mimo-free", true},
		{"mimo-free", "mimo-free-2", false},
		{"mimo-*", "mimo-v2.6-flash-free", true},
		{"*-free", "mimo-v2.6-flash-free", true},
		{"*-free", "mimo-v2.6-flash", false},
		{"*", "anything", true},
		{"*", "", true},
		// Model ids contain "/" and ":", which path.Match would refuse to let
		// "*" cross.
		{"moonshotai/*", "moonshotai/kimi-k2:free", true},
		{"*/kimi-k2:free", "moonshotai/kimi-k2:free", true},
		{"glm-*", "mimo-free", false},
		{"", "mimo-free", false},
		{"mimo", "mimo-free", false},
	}
	for _, tc := range cases {
		if got := globMatch(tc.pattern, tc.input); got != tc.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tc.pattern, tc.input, got, tc.want)
		}
	}
}

func TestModelMatchesEitherName(t *testing.T) {
	// The alias the client calls, and the upstream model id, must both work —
	// a config should not have to know which one it is naming.
	if !modelMatches([]string{"mimo-free"}, "mimo-v2.6-flash-free", "mimo-free") {
		t.Fatal("requested-model match failed")
	}
	if !modelMatches([]string{"mimo-v2.6-flash-free"}, "mimo-v2.6-flash-free", "mimo-free") {
		t.Fatal("selected-model match failed")
	}
	if modelMatches([]string{"mimo-*"}, "claude-opus-5-5", "claude-opus") {
		t.Fatal("unrelated model matched")
	}
	// An empty candidate must not be matched by a bare "*".
	if modelMatches([]string{"*"}, "", "") {
		t.Fatal("empty names matched a wildcard")
	}
}

func TestModelMatchesSkipsBlankPatterns(t *testing.T) {
	if modelMatches([]string{"", "  "}, "mimo-free", "mimo-free") {
		t.Fatal("blank patterns matched")
	}
}

// The real value observed from CPA: <provider type>:<group name>:<key hash>.
const observedAuthID = "openai-compatibility:opencode:6df7070a759c"

func TestSelectedAuthID(t *testing.T) {
	if got := selectedAuthID(map[string]any{"selected_auth_id": observedAuthID}); got != observedAuthID {
		t.Fatalf("got %q", got)
	}
	// Absent, nil, and wrong-typed values must all read as "no credential",
	// which fails the filter rather than matching a wildcard.
	for _, metadata := range []map[string]any{
		nil,
		{},
		{"selected_auth_id": nil},
		{"selected_auth_id": 42},
		{"selected_auth_id": "   "},
	} {
		if got := selectedAuthID(metadata); got != "" {
			t.Fatalf("metadata %v gave %q, want empty", metadata, got)
		}
	}
}

func TestProviderSelectionMatches(t *testing.T) {
	cases := []struct {
		name     string
		selected []string
		authID   string
		want     bool
	}{
		{"exact group name", []string{"opencode"}, observedAuthID, true},
		{"one of several", []string{"openrouter", "opencode"}, observedAuthID, true},
		{"other group", []string{"openrouter"}, observedAuthID, false},
		{"no selection", nil, observedAuthID, false},
		{"blank entry ignored", []string{""}, observedAuthID, false},
		// A prefix or substring test would let "opencode" claim "opencode-2".
		{"group name is not a prefix", []string{"opencode"}, "openai-compatibility:opencode-2:abc123", false},
		{"group name is not a substring", []string{"encode"}, observedAuthID, false},
		// Non-OpenAI-compatible credentials never match, whatever their group is called.
		{"different provider type", []string{"opencode"}, "anthropic:opencode:6df7070a759c", false},
		{"unparseable credential", []string{"opencode"}, "opencode", false},
		{"empty credential", []string{"opencode"}, "", false},
		// The wildcard selects every OpenAI-compatible group. It widens the
		// group match only: the provider type is still required, so it cannot
		// reach Claude / Gemini / Codex credentials.
		{"wildcard alone", []string{"*"}, observedAuthID, true},
		{"wildcard among names", []string{"openrouter", "*"}, observedAuthID, true},
		{"wildcard needs the right provider type", []string{"*"}, "anthropic:opencode:6df7070a759c", false},
		{"wildcard needs a parseable credential", []string{"*"}, "opencode", false},
		{"wildcard does not match an empty credential", []string{"*"}, "", false},
	}
	for _, tc := range cases {
		if got := providerSelectionMatches(tc.selected, tc.authID); got != tc.want {
			t.Errorf("%s: providerSelectionMatches(%q, %q) = %v, want %v", tc.name, tc.selected, tc.authID, got, tc.want)
		}
	}
}

func TestAuthIDBelongsToProviderIsCaseInsensitiveOnProviderType(t *testing.T) {
	// The type segment is host-controlled and has been spelled in mixed case
	// historically; the group name is the operator's and stays exact.
	if !authIDBelongsToProvider("OpenAI-Compatibility:opencode:abc123", "opencode") {
		t.Fatal("provider type comparison should ignore case")
	}
	if authIDBelongsToProvider("openai-compatibility:OpenCode:abc123", "opencode") {
		t.Fatal("group name comparison must stay exact")
	}
}
