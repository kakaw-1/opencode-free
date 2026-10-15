package main

// Live checks against OpenCode's real inference endpoint. Skipped unless
// OPENCODE_LIVE=1, because they need network access and spend free-tier quota.
//
//	OPENCODE_LIVE=1 go test -run TestLive -v
//
// These exist because the gate is not a documented contract: it was reverse
// engineered, it has changed twice (2026-09-16, 2026-10-14), and the only way
// to know the plugin still works is to ask the upstream.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	liveEndpoint = "https://opencode.ai/inference/v1/chat/completions"
	liveModel    = "mimo-v2.6-flash-free"

	// A constant session id is enough: the gate checks the shape, and
	// per-conversation minting only affects prompt-cache affinity. Shape:
	// ses_ + 12 hex (time part matching ^[0-9a-f]{9}f[c-f][0-9a-f]$) + 14 base62.
	liveSession = "ses_c70c671a8ffe559SNDjUqHfi7a"
	liveUA      = "opencode/latest/2.0.29/cli"

	// Written by ./fetch-tools.sh.
	liveToolsPath = "opencode_tools.json"
)

// liveCatalogue loads the fetched tool definitions. The repo does not ship them
// (they are GPL-3.0-or-later data and this plugin is MIT), so these tests need
// ./fetch-tools.sh to have been run.
func liveCatalogue(t *testing.T) toolCatalogue {
	t.Helper()
	catalogue, errLoad := loadCatalogue(liveToolsPath)
	if errLoad != nil {
		t.Fatalf("load tool catalogue (run ./fetch-tools.sh first): %v", errLoad)
	}
	return catalogue
}

func liveEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("OPENCODE_LIVE") != "1" {
		t.Skip("set OPENCODE_LIVE=1 to run live checks against opencode.ai")
	}
}

// postUpstream sends body to the real endpoint with the fingerprint headers the
// CPA provider config is expected to supply, and returns the status and a
// short excerpt of the response.
func postUpstream(t *testing.T, body []byte) (int, string) {
	t.Helper()
	req, errRequest := http.NewRequest(http.MethodPost, liveEndpoint, bytes.NewReader(body))
	if errRequest != nil {
		t.Fatalf("build request: %v", errRequest)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", liveUA)
	req.Header.Set("x-opencode-client", "cli")
	req.Header.Set("x-opencode-session", liveSession)
	req.Header.Set("x-opencode-session-id", liveSession)
	req.Header.Set("Authorization", "Bearer public")

	client := &http.Client{Timeout: 90 * time.Second}
	resp, errDo := client.Do(req)
	if errDo != nil {
		t.Fatalf("call upstream: %v", errDo)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, strings.ReplaceAll(string(raw), "\n", " ")
}

func liveRequest(t *testing.T, extra map[string]any) []byte {
	t.Helper()
	payload := map[string]any{
		"model":      liveModel,
		"max_tokens": 32,
		"messages":   []any{map[string]any{"role": "user", "content": "Reply with exactly: OK"}},
	}
	for k, v := range extra {
		payload[k] = v
	}
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		t.Fatalf("marshal: %v", errMarshal)
	}
	return raw
}

// TestLiveGateRejectsUnpreparedRequest is the control: it proves the gate is
// still there, so a passing TestLivePluginOutputIsAccepted means something.
func TestLiveGateRejectsUnpreparedRequest(t *testing.T) {
	liveEnabled(t)
	status, excerpt := postUpstream(t, liveRequest(t, nil))
	if status != http.StatusForbidden {
		t.Fatalf("control expected 403 from an unprepared request, got %d: %s", status, excerpt)
	}
	t.Logf("control 403 as expected: %s", excerpt)
}

// TestLivePluginOutputIsAccepted is the real check: a bare client request,
// rewritten by prepareBody, must be accepted by the free tier.
func TestLivePluginOutputIsAccepted(t *testing.T) {
	liveEnabled(t)
	catalogue := liveCatalogue(t)
	out, changed := prepareBody(liveRequest(t, nil), true, catalogue)
	if !changed {
		t.Fatal("prepareBody left a bare request untouched")
	}
	status, excerpt := postUpstream(t, out)
	if status != http.StatusOK {
		t.Fatalf("rewritten request rejected: %d %s", status, excerpt)
	}
	t.Logf("rewritten request accepted: %d", status)
}

// TestLiveClientToolsSurvive confirms the plugin does not break ordinary
// function calling: a caller's own tool must still be usable, alongside the
// injected read.
func TestLiveClientToolsSurvive(t *testing.T) {
	liveEnabled(t)
	catalogue := liveCatalogue(t)
	clientTools := []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "get_weather",
			"description": "Get the current weather for a city",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"city": map[string]any{"type": "string"}},
				"required":   []any{"city"},
			},
		},
	}}
	out, changed := prepareBody(liveRequest(t, map[string]any{"tools": clientTools}), true, catalogue)
	if !changed {
		t.Fatal("prepareBody left the request untouched")
	}
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(out, &payload); errUnmarshal != nil {
		t.Fatalf("rewritten body is not JSON: %v", errUnmarshal)
	}
	items, _ := payload["tools"].([]any)
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, chatToolName(item))
	}
	if len(names) != 2 {
		t.Fatalf("want the client tool plus read, got %v", names)
	}
	status, excerpt := postUpstream(t, out)
	if status != http.StatusOK {
		t.Fatalf("rewritten request rejected: %d %s", status, excerpt)
	}
	t.Logf("client tool preserved (%v), request accepted: %d", names, status)
}
