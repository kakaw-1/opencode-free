package main

// Matching a live request to the providers the operator selected.
//
// A request interceptor sees no provider field, but
// RequestInterceptRequest.Metadata carries "selected_auth_id", built as
//
//	openai-compatibility:<group name>:<key hash>
//
// so the middle segment is the `name:` given to the group under
// api-keys.openai-compatibility — exactly the name the operator writes in
// `providers`. Discovery of those names lives in configfile.go.

import (
	"strings"
)

// openAICompatProviderType is the leading segment of a credential id belonging
// to an api-keys.openai-compatibility group.
const openAICompatProviderType = "openai-compatibility"

// splitAuthID splits a credential id into its three segments. Ids are built as
// <provider type>:<group name>:<key hash>; anything else is reported as not
// parseable rather than guessed at.
func splitAuthID(id string) (providerType, group string, ok bool) {
	parts := strings.Split(strings.TrimSpace(id), ":")
	if len(parts) != 3 {
		return "", "", false
	}
	providerType = strings.TrimSpace(parts[0])
	group = strings.TrimSpace(parts[1])
	if providerType == "" || group == "" {
		return "", "", false
	}
	return providerType, group, true
}

// authIDBelongsToProvider reports whether a selected_auth_id belongs to the
// named OpenAI-compatible group. Exact match on the group segment: a prefix or
// substring test would let "opencode" match "opencode-2".
func authIDBelongsToProvider(authID, provider string) bool {
	providerType, group, ok := splitAuthID(authID)
	if !ok {
		return false
	}
	return strings.EqualFold(providerType, openAICompatProviderType) && group == provider
}

// providerSelectionMatches reports whether the request's credential is one the
// operator selected. An unselected provider passes through untouched.
func providerSelectionMatches(selected []string, authID string) bool {
	for _, provider := range selected {
		if provider != "" && authIDBelongsToProvider(authID, provider) {
			return true
		}
	}
	return false
}
