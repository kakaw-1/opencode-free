package main

// Session identity for OpenCode's free tier, ported from opencode2api.
//
// The free tier fingerprints the client by a header pair: x-opencode-session and
// x-opencode-session-id, both carrying the same `ses_` id. Without this plugin a
// provider config can only supply one constant for both, and a constant passes
// the gate (measured 2026-10-16: per-conversation ids are not required) while
// pinning every conversation to one upstream session, so prompt caching cannot
// tell two conversations apart.
//
// This file derives a per-conversation id the way opencode2api does: reuse the
// client's own `ses_` id when it carries a genuine encoding, otherwise re-encode
// whatever signal the request offers into that same shape, deterministically, so
// one conversation keeps one id.
//
// The derived headers are returned through RequestInterceptResponse.Headers, and
// those only reach the upstream when the provider config asks for them by name:
//
//	headers:
//	  x-opencode-session: "$x-opencode-session"
//	  x-opencode-session-id: "$x-opencode-session-id"
//
// The openai-compatibility executor builds a fresh HTTP request and copies a
// value out of opts.Headers only for $-prefixed config entries
// (util.ApplyCustomHeadersFromAttrs -> extractCustomHeaders). A config that
// states the literal constant instead keeps winning, so both styles work.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strings"
)

const (
	// sessionHeaderKey is sent upstream under both names, carrying the same
	// value. The gate requires both.
	sessionHeaderKey   = "x-opencode-session"
	sessionIDHeaderKey = "x-opencode-session-id"
)

// sessionSignalHeaders are the client headers consulted for an explicit
// conversation signal, in priority order — the same list and order as
// opencode2api's DeriveRequestIDs.
var sessionSignalHeaders = []string{
	sessionHeaderKey,
	sessionIDHeaderKey,
	"x-session-affinity",
	"X-Session-Id",
	"x-session-id",
	"conversation-id",
}

// deriveSessionID resolves the conversation identity for one request and
// returns it in the shape the free tier accepts.
//
// Priority: the session headers above, then the body's conversation_id and
// metadata.session_id, then a seed built from the first user turn (stable as a
// conversation grows), then previous_response_id, then a random fallback for a
// request that carries no signal at all.
func deriveSessionID(headers http.Header, body []byte) string {
	signal := ""
	for _, key := range sessionSignalHeaders {
		if value := headerValue(headers, key); value != "" {
			signal = value
			break
		}
	}
	if signal == "" {
		signal = sessionSignalFromBody(body)
	}
	if signal == "" || signal == "{}" {
		signal = randomFallbackSignal()
	}
	return canonicalSessionID(signal)
}

// headerValue reads one header, tolerating a map that was not keyed with the
// canonical spelling. CPA hands the client's headers across the plugin boundary
// as a plain JSON object, so a lowercase key survives the trip unchanged.
func headerValue(headers http.Header, key string) string {
	if len(headers) == 0 {
		return ""
	}
	if value := strings.TrimSpace(headers.Get(key)); value != "" {
		return value
	}
	for name, values := range headers {
		if !strings.EqualFold(name, key) {
			continue
		}
		for _, value := range values {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// sessionSignalFromBody pulls a conversation signal out of the request body.
// The body is only read, never re-encoded: the payload handed upstream is the
// one the caller sent (plus the tool injection), byte for byte.
func sessionSignalFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return ""
	}
	if signal := stringAt(payload, "conversation_id"); signal != "" {
		return signal
	}
	if signal := stringAt(payload, "metadata", "session_id"); signal != "" {
		return signal
	}
	if signal := conversationSeed(payload); signal != "" {
		return signal
	}
	return stringAt(payload, "previous_response_id")
}

// conversationSeed builds a stable per-conversation signal from the first user
// turn. Using the first turn rather than the whole history keeps the id stable
// as a conversation grows, while two conversations that start differently never
// collide. The seed is hashed before it leaves this package, so no prompt text
// reaches the header.
func conversationSeed(payload map[string]any) string {
	if input, ok := payload["input"].(string); ok && input != "" {
		return input
	}
	for _, field := range []string{"messages", "input"} {
		items, ok := payload[field].([]any)
		if !ok {
			continue
		}
		for _, raw := range items {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if role, _ := item["role"].(string); role != "user" {
				continue
			}
			encoded, err := json.Marshal(item["content"])
			if err != nil || len(encoded) == 0 || string(encoded) == "null" {
				continue
			}
			return string(encoded)
		}
	}
	return ""
}

// stringAt reads a nested string field, returning "" when the path does not
// resolve to a non-blank string.
func stringAt(payload map[string]any, path ...string) string {
	var current any = payload
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current, ok = object[key]
		if !ok {
			return ""
		}
	}
	value, _ := current.(string)
	return strings.TrimSpace(value)
}

// randomFallbackSignal is the last resort for a request with no conversation
// signal anywhere. crypto/rand failing is not a reason to fail a request, so it
// degrades to the empty signal, whose encoding is still a valid id.
func randomFallbackSignal() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return "fallback_" + hex.EncodeToString(buf)
}

// canonicalSessionPattern matches OpenCode's minted session format:
// "ses_" + 12 lowercase hex timestamp characters + 14 Base62 characters. The
// free tier rejects any other shape with 403 FreeTierError.
var canonicalSessionPattern = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

// descendingTimePartPattern matches the time part the official client mints
// with Identifier.descending(): the 48-bit value ~(ts_ms<<12|counter) in hex.
// Bitwise NOT sets the low 12 bits, so a genuine time part ends in
// 0xFFF-counter. The free tier accepts counters 1-63 (low 12 bits 0xFC0-0xFFF)
// and rejects anything beyond with 403 FreeTierError even when the `ses_` shape
// is right, so the pattern accepts exactly that window.
var descendingTimePartPattern = regexp.MustCompile(`^[0-9a-f]{9}f[c-f][0-9a-f]$`)

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// canonicalSessionID returns signal unchanged when it already carries an
// official OpenCode session (preserving upstream prompt-cache affinity). Any
// other downstream identity — a UUID, a foreign client's session, a conversation
// seed — is deterministically re-encoded into that exact layout: inverted 48-bit
// timestamp with counter 1, so the low 12 hex digits read ffe. Being a hash of
// the signal, the encoding keeps one conversation on one id across requests,
// which is all the gate and the prompt cache need.
func canonicalSessionID(signal string) string {
	if canonicalSessionPattern.MatchString(signal) && descendingTimePartPattern.MatchString(signal[4:16]) {
		return signal
	}
	sum := sha256.Sum256([]byte("ses\x00" + signal))
	return "ses_" + descendingTimePart(sum[:6]) + base62Fixed(new(big.Int).SetBytes(sum[6:16]), 14)
}

// descendingTimePart renders six hash bytes as the official client's
// Identifier.descending() layout: a 36-bit timestamp shifted into the top of a
// 48-bit word, ORed with counter 1, then bitwise-NOT-ed so the low 12 hex digits
// read ffe.
func descendingTimePart(hash []byte) string {
	current := (uint64(hash[0])<<28 | uint64(hash[1])<<20 | uint64(hash[2])<<12 |
		uint64(hash[3])<<4 | uint64(hash[4])>>4) << 12
	current |= 1
	return fmt.Sprintf("%012x", ^current&0xFFFFFFFFFFFF)
}

// base62Fixed renders n in base62, zero-padded to width characters.
func base62Fixed(n *big.Int, width int) string {
	base := big.NewInt(62)
	out := make([]byte, width)
	remainder := new(big.Int)
	for i := width - 1; i >= 0; i-- {
		n.DivMod(n, base, remainder)
		out[i] = base62Alphabet[remainder.Int64()]
	}
	return string(out)
}

// sessionIdentityHeaders returns the client headers with both session headers
// set to session. Any client-supplied spelling of those two names is dropped
// first, so exactly one value per name goes upstream.
func sessionIdentityHeaders(client http.Header, session string) http.Header {
	if session == "" {
		return client
	}
	out := make(http.Header, len(client)+2)
	for key, values := range client {
		if isSessionHeader(key) {
			continue
		}
		out[key] = values
	}
	out.Set(sessionHeaderKey, session)
	out.Set(sessionIDHeaderKey, session)
	return out
}

// isSessionHeader reports whether a header name is one of the two the free tier
// fingerprints, ignoring case the way HTTP does.
func isSessionHeader(key string) bool {
	return strings.EqualFold(key, sessionHeaderKey) || strings.EqualFold(key, sessionIDHeaderKey)
}
