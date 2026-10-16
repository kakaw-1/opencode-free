package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const groupAuthID = "openai-compatibility:opencode:6df7070a759c"

// intercept drives the interceptor the way the host does and returns the
// response it produced.
func intercept(t *testing.T, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	t.Helper()
	raw, errMarshal := json.Marshal(req)
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rawResp, errCall := interceptAfterAuth(raw)
	if errCall != nil {
		t.Fatalf("interceptAfterAuth: %v", errCall)
	}
	var envelope pluginabi.Envelope
	if errUnmarshal := json.Unmarshal(rawResp, &envelope); errUnmarshal != nil {
		t.Fatalf("decode envelope: %v", errUnmarshal)
	}
	if !envelope.OK {
		t.Fatalf("interceptor returned not-ok: %s", rawResp)
	}
	var resp pluginapi.RequestInterceptResponse
	if errUnmarshal := json.Unmarshal(envelope.Result, &resp); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	return resp
}

// withState installs a plugin configuration for the duration of one test.
func withState(t *testing.T, cfg pluginConfig) {
	t.Helper()
	state.mu.Lock()
	previousConfig, previousCatalogue := state.config, state.catalogue
	state.config, state.catalogue = cfg, testCatalogue()
	state.mu.Unlock()
	t.Cleanup(func() {
		state.mu.Lock()
		state.config, state.catalogue = previousConfig, previousCatalogue
		state.mu.Unlock()
	})
}

func request(authID, model, session string, body string) pluginapi.RequestInterceptRequest {
	headers := http.Header{"Content-Type": []string{"application/json"}}
	if session != "" {
		headers.Set(sessionHeaderKey, session)
		headers.Set(sessionIDHeaderKey, session)
	}
	return pluginapi.RequestInterceptRequest{
		Model:    model,
		Stream:   false,
		Headers:  headers,
		Body:     []byte(body),
		Metadata: map[string]any{selectedAuthIDMetadataKey: authID},
	}
}

// The session pair is what a provider config written as
// `x-opencode-session: "$x-opencode-session"` reads, so it has to be present on
// every request heading to a selected group.
func TestInterceptSetsSessionHeadersOnRewrite(t *testing.T) {
	withState(t, pluginConfig{Providers: stringList{"opencode"}})
	resp := intercept(t, request(groupAuthID, "deepseek-v4.1-flash-free", realOpencodeSession,
		`{"model":"deepseek-v4.1-flash-free","stream":false,"messages":[{"role":"user","content":"hi"}]}`))

	if got := resp.Headers.Get(sessionHeaderKey); got != realOpencodeSession {
		t.Errorf("%s = %q, want the client's own %q", sessionHeaderKey, got, realOpencodeSession)
	}
	if got := resp.Headers.Get(sessionIDHeaderKey); got != realOpencodeSession {
		t.Errorf("%s = %q, want the client's own %q", sessionIDHeaderKey, got, realOpencodeSession)
	}
	// The body half of the rewrite still happens.
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(resp.Body, &payload); errUnmarshal != nil {
		t.Fatalf("decode body: %v", errUnmarshal)
	}
	if streaming, _ := payload["stream"].(bool); !streaming {
		t.Error("stream was not forced on")
	}
}

// A model the operator narrowed away is not rewritten, but it still goes to the
// same group: without the session pair the $-magic config would drop the header
// entirely, which the gate answers with a 403.
func TestInterceptSetsSessionHeadersWhenModelIsNotMatched(t *testing.T) {
	withState(t, pluginConfig{Providers: stringList{"opencode"}, Models: []string{"cat-jam-*"}})
	body := `{"model":"deepseek-v4.1-flash-free","stream":false,"messages":[{"role":"user","content":"hi"}]}`
	resp := intercept(t, request(groupAuthID, "deepseek-v4.1-flash-free", realOpencodeSession, body))

	if got := resp.Headers.Get(sessionHeaderKey); got != realOpencodeSession {
		t.Errorf("%s = %q, want the session pair even though the body was left alone", sessionHeaderKey, got)
	}
	if string(resp.Body) != body {
		t.Error("the body was rewritten although the model filter did not match")
	}
}

// Anything outside the selection stays byte for byte untouched, headers
// included: this hook fires for every provider CPA serves.
func TestInterceptLeavesUnselectedProvidersAlone(t *testing.T) {
	withState(t, pluginConfig{Providers: stringList{"opencode"}})
	body := `{"model":"claude-sonnet-4","stream":false,"messages":[{"role":"user","content":"hi"}]}`
	resp := intercept(t, request("anthropic:claude:abc123", "claude-sonnet-4", "", body))

	if string(resp.Body) != body {
		t.Error("an unselected provider's body was rewritten")
	}
	if len(resp.Headers) != 1 || resp.Headers.Get("Content-Type") == "" {
		t.Errorf("headers = %v, want the client's headers echoed unchanged", resp.Headers)
	}
}

func TestInterceptStaysInertWithoutSelection(t *testing.T) {
	withState(t, pluginConfig{})
	body := `{"model":"deepseek-v4.1-flash-free","stream":false,"messages":[{"role":"user","content":"hi"}]}`
	resp := intercept(t, request(groupAuthID, "deepseek-v4.1-flash-free", "", body))

	if string(resp.Body) != body {
		t.Error("the body was rewritten with no `providers` selection")
	}
	if resp.Headers.Get(sessionHeaderKey) != "" {
		t.Error("a session header was invented with no `providers` selection")
	}
}

// A request with no session signal still gets a pair, derived from the first
// user turn, so a config using $-magic never drops the header.
func TestInterceptDerivesSessionWithoutClientSignal(t *testing.T) {
	withState(t, pluginConfig{Providers: stringList{"opencode"}})
	body := `{"model":"cat-jam-free","stream":true,"messages":[{"role":"user","content":"read /etc/hostname"}]}`
	first := intercept(t, request(groupAuthID, "cat-jam-free", "", body))
	second := intercept(t, request(groupAuthID, "cat-jam-free", "", body))

	got := first.Headers.Get(sessionHeaderKey)
	if !canonicalSessionPattern.MatchString(got) {
		t.Fatalf("%s = %q, want a ses_ id", sessionHeaderKey, got)
	}
	if second.Headers.Get(sessionHeaderKey) != got {
		t.Error("the same conversation produced two session ids")
	}
}

// The wildcard selection widens the group match but must not reach Claude,
// Gemini or Codex credentials.
func TestInterceptWildcardStaysInsideOpenAICompat(t *testing.T) {
	withState(t, pluginConfig{Providers: stringList{wildcardProvider}})
	body := `{"model":"x","stream":false,"messages":[{"role":"user","content":"hi"}]}`

	matched := intercept(t, request(groupAuthID, "cat-jam-free", "", body))
	if matched.Headers.Get(sessionHeaderKey) == "" {
		t.Error("the wildcard did not match an openai-compatibility credential")
	}

	other := intercept(t, request("anthropic:claude:abc123", "claude-sonnet-4", "", body))
	if other.Headers.Get(sessionHeaderKey) != "" {
		t.Error("the wildcard matched a non-openai-compatibility credential")
	}
}
