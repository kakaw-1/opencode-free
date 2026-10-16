package main

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// realOpencodeSession is a session id minted by OpenCode 1.18.35, captured from
// its outbound request. Its time part matches the descending counter window, so
// it has to survive canonicalisation unchanged and keep its prompt-cache
// affinity upstream.
const realOpencodeSession = "ses_ebd1a1fa7ffeZKJzmyss33slPz"

func TestCanonicalSessionIDKeepsMintedSessions(t *testing.T) {
	for _, session := range []string{
		realOpencodeSession,
		"ses_c70c671a8ffe559SNDjUqHfi7a", // the constant form CPA configs used to carry
	} {
		if got := canonicalSessionID(session); got != session {
			t.Errorf("canonicalSessionID(%q) = %q, want it unchanged", session, got)
		}
	}
}

func TestCanonicalSessionIDReencodesForeignSignals(t *testing.T) {
	shape := regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
	window := regexp.MustCompile(`^[0-9a-f]{9}f[c-f][0-9a-f]$`)
	signals := []string{
		"", // no signal at all
		"5f2c1e7a-1f4b-4f4e-9a5f-8d3d0f6b2a11",
		"ses_5e2ac1b7c9fd4a1e8b3f0d6a2c7e9b41", // right prefix, wrong encoding
		"mimo-free",                            // model-shaped signal
		`[{"type":"text","text":"hi"}]`,        // conversation seed
	}
	for _, signal := range signals {
		first := canonicalSessionID(signal)
		if !shape.MatchString(first) {
			t.Errorf("canonicalSessionID(%q) = %q, want the ses_ shape", signal, first)
			continue
		}
		if !window.MatchString(first[4:16]) {
			t.Errorf("canonicalSessionID(%q) = %q, time part outside the counter window", signal, first)
		}
		if again := canonicalSessionID(signal); again != first {
			t.Errorf("canonicalSessionID(%q) is not deterministic: %q then %q", signal, first, again)
		}
	}
}

func TestCanonicalSessionIDSeparatesDifferentSignals(t *testing.T) {
	if canonicalSessionID("conversation one") == canonicalSessionID("conversation two") {
		t.Error("two different signals produced one session id")
	}
}

// The client's own session header is the highest-priority signal, and it beats
// everything in the body.
func TestDeriveSessionIDPrefersClientHeaders(t *testing.T) {
	headers := http.Header{sessionHeaderKey: []string{realOpencodeSession}}
	body := []byte(`{"conversation_id":"from-body","messages":[{"role":"user","content":"hi"}]}`)
	if got := deriveSessionID(headers, body); got != realOpencodeSession {
		t.Errorf("deriveSessionID() = %q, want the client's own %q", got, realOpencodeSession)
	}
}

func TestDeriveSessionIDHeaderPriority(t *testing.T) {
	headers := http.Header{
		sessionIDHeaderKey: []string{realOpencodeSession},
		"X-Session-Id":     []string{"ses_0123456789abZZZZZZZZZZZZZZ"},
	}
	if got := deriveSessionID(headers, nil); got != realOpencodeSession {
		t.Errorf("deriveSessionID() = %q, want x-opencode-session-id to win over X-Session-Id", got)
	}
}

// Without a client session, the body carriers come first (opencode2api order).
func TestDeriveSessionIDBodyCarriers(t *testing.T) {
	conversation := []byte(`{"conversation_id":"conv-123","previous_response_id":"resp-9"}`)
	got := deriveSessionID(nil, conversation)
	want := canonicalSessionID("conv-123")
	if got != want {
		t.Errorf("deriveSessionID() = %q, want conversation_id to win: %q", got, want)
	}

	metadata := []byte(`{"metadata":{"session_id":"meta-7"}}`)
	if got, want := deriveSessionID(nil, metadata), canonicalSessionID("meta-7"); got != want {
		t.Errorf("deriveSessionID() = %q, want metadata.session_id: %q", got, want)
	}

	previous := []byte(`{"previous_response_id":"resp-9"}`)
	if got, want := deriveSessionID(nil, previous), canonicalSessionID("resp-9"); got != want {
		t.Errorf("deriveSessionID() = %q, want previous_response_id: %q", got, want)
	}
}

// A conversation with no session header keeps one id as it grows: the seed is
// the first user turn, not the whole history.
func TestDeriveSessionIDSeedIsStableAcrossTurns(t *testing.T) {
	first := []byte(`{"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"read /etc/hostname"}]}`)
	later := []byte(`{"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"read /etc/hostname"},{"role":"assistant","content":"ok"},{"role":"user","content":"and now /etc/hosts"}]}`)
	firstID := deriveSessionID(nil, first)
	if firstID != deriveSessionID(nil, later) {
		t.Error("the seeded session id changed as the conversation grew")
	}

	other := []byte(`{"messages":[{"role":"user","content":"read /etc/hosts"}]}`)
	if deriveSessionID(nil, other) == firstID {
		t.Error("two conversations with different first turns share one session id")
	}
}

// Codex CLI speaks the Responses API, whose turns arrive under "input".
func TestDeriveSessionIDSeedFromResponsesInput(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"read /etc/hostname"}]}]}`)
	first := deriveSessionID(nil, body)
	second := deriveSessionID(nil, body)
	if first != second {
		t.Errorf("responses input seed is not deterministic: %q then %q", first, second)
	}
	if !strings.HasPrefix(first, "ses_") {
		t.Errorf("deriveSessionID() = %q, want a ses_ id", first)
	}
}

func TestDeriveSessionIDFallsBackToRandom(t *testing.T) {
	first := deriveSessionID(nil, []byte(`{}`))
	second := deriveSessionID(nil, []byte(`{}`))
	if first == second {
		t.Error("two signal-less requests reused one session id; want a random fallback")
	}
	if !canonicalSessionPattern.MatchString(first) {
		t.Errorf("fallback id %q does not carry the ses_ shape", first)
	}
}

func TestDeriveSessionIDSurvivesUnparseableBody(t *testing.T) {
	got := deriveSessionID(nil, []byte("not json"))
	if !canonicalSessionPattern.MatchString(got) {
		t.Errorf("deriveSessionID() = %q, want a ses_ id", got)
	}
}

func TestSessionIdentityHeadersReplaceClientSpelling(t *testing.T) {
	client := http.Header{
		// The client's headers arrive across the plugin boundary as a plain JSON
		// object, so a lowercase spelling survives: both spellings have to be
		// replaced, not just the canonical one.
		"x-opencode-session":    []string{"stale-lowercase"},
		"X-Opencode-Session-Id": []string{"stale-canonical"},
		"Authorization":         []string{"Bearer cap-key"},
	}
	out := sessionIdentityHeaders(client, realOpencodeSession)

	if got := out.Values("x-opencode-session"); len(got) != 1 || got[0] != realOpencodeSession {
		t.Errorf("x-opencode-session = %v, want exactly [%s]", got, realOpencodeSession)
	}
	if got := out.Values("x-opencode-session-id"); len(got) != 1 || got[0] != realOpencodeSession {
		t.Errorf("x-opencode-session-id = %v, want exactly [%s]", got, realOpencodeSession)
	}
	if got := out.Get("Authorization"); got != "Bearer cap-key" {
		t.Errorf("Authorization = %q, want the client header preserved", got)
	}
	for key := range out {
		if key != "X-Opencode-Session" && key != "X-Opencode-Session-Id" && key != "Authorization" {
			t.Errorf("unexpected header %q survived", key)
		}
	}
	if headerValue(client, sessionHeaderKey) != "stale-lowercase" {
		t.Error("the client's header map was modified in place")
	}
}
