package main

// Model and credential matching for the interceptor filter.
//
// The plugin identifies its upstream by credential, not by model: the hook
// exposes no provider field, but RequestInterceptRequest.Metadata carries
// "selected_auth_id", whose value is
//
//	<provider type>:<group name>:<key hash>
//	openai-compatibility:opencode:6df7070a759c
//
// The middle segment is the `name:` the user gave the group under
// api-keys.openai-compatibility, so it names the provider exactly. See
// discovery.go for the matching itself.
//
// RequestInterceptRequest.Headers is no help for this: it holds the *client's*
// request headers, because the provider's configured headers (x-opencode-client
// and friends) are applied later, inside the executor, by
// ApplyCustomHeadersFromAttrs. ToFormat is no help either — every
// openai-compatibility provider reports "openai".

import "strings"

// selectedAuthID reads the credential id CPA selected for this request. Empty
// when the host did not publish one, which makes the request fail the filter
// rather than being rewritten by mistake.
func selectedAuthID(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	id, _ := metadata[selectedAuthIDMetadataKey].(string)
	return strings.TrimSpace(id)
}

// selectedAuthIDMetadataKey mirrors cliproxyexecutor.SelectedAuthMetadataKey.
// Spelled out rather than imported so the plugin does not depend on an
// internal executor package just for one string.
const selectedAuthIDMetadataKey = "selected_auth_id"

// globMatch reports whether s matches pattern, where "*" matches any run of
// characters (including none) and every other character is literal.
//
// Deliberately not path.Match: model names contain "/" (moonshotai/kimi-k2:free)
// and path.Match refuses to let "*" cross a separator.
func globMatch(pattern, s string) bool {
	// Iterative wildcard match: no recursion, no pathological cases.
	patternIndex, stringIndex := 0, 0
	starPattern, starString := -1, 0

	for stringIndex < len(s) {
		switch {
		case patternIndex < len(pattern) && pattern[patternIndex] == '*':
			starPattern = patternIndex
			starString = stringIndex
			patternIndex++
		case patternIndex < len(pattern) && pattern[patternIndex] == s[stringIndex]:
			patternIndex++
			stringIndex++
		case starPattern >= 0:
			patternIndex = starPattern + 1
			starString++
			stringIndex = starString
		default:
			return false
		}
	}
	for patternIndex < len(pattern) && pattern[patternIndex] == '*' {
		patternIndex++
	}
	return patternIndex == len(pattern)
}

// modelMatches reports whether any configured pattern matches either the
// selected upstream model or the client-requested one. Matching both means a
// config can name whichever the user finds natural — the alias they call, or
// the upstream model id.
func modelMatches(patterns []string, model, requested string) bool {
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		if model != "" && globMatch(pattern, model) {
			return true
		}
		if requested != "" && globMatch(pattern, requested) {
			return true
		}
	}
	return false
}
