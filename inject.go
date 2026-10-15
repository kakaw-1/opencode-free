package main

// Body rewriting for OpenCode Zen's free tier.
//
// The free tier does not gate on headers alone: the request *body* has to look
// like an OpenCode agent session. Measured directly against
// https://opencode.ai/inference on 2026-10-15 (model mimo-v2.6-flash-free):
//
//   - "stream" must be true. false and absent both return 403 FreeTierError,
//     and the upstream always answers with SSE regardless.
//   - "tools" must contain the genuine OpenCode "read" definition. A stub that
//     carries the right name but a made-up description/schema is rejected.
//   - Every *recognised* OpenCode tool name present in the list must carry its
//     genuine definition, for the same reason. Tool order is irrelevant.
//
// Names the upstream does not recognise are left exactly as the client declared
// them, so downstream function calling keeps working.
//
// Only "read" is injected. It is the minimal set that passes on its own, and
// keeping the list short matters: the model really does call the tools it is
// given, so every extra tool is another way for it to derail a request the
// caller never asked to be agentic.

import (
	"encoding/json"
	"reflect"
	"strings"
)

// injectedTool is the one tool added to requests that do not already declare it.
const injectedTool = "read"

// toolDef is one entry of the OpenCode tool catalogue: the pieces the free tier
// compares against the official client's definitions.
type toolDef struct {
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// toolCatalogue maps an OpenCode tool name to its genuine definition.
type toolCatalogue map[string]toolDef

// chatToolName pulls the function name out of an OpenAI Chat Completions tool
// entry. CPA hands us this shape: the openai-compatibility provider speaks Chat
// Completions upstream.
func chatToolName(entry any) string {
	obj, ok := entry.(map[string]any)
	if !ok {
		return ""
	}
	fn, ok := obj["function"].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := fn["name"].(string)
	return strings.TrimSpace(name)
}

// chatTool renders one catalogue entry in Chat Completions tool shape.
func (c toolCatalogue) chatTool(name string) map[string]any {
	def := c[name]
	params := def.Parameters
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": def.Description,
			"parameters":  params,
		},
	}
}

// chatToolIsGenuine reports whether a client-declared tool entry already
// carries the catalogue's definition, so an already-correct entry is left
// byte-for-byte alone instead of being re-encoded on every pass.
func chatToolIsGenuine(entry any, def toolDef) bool {
	obj, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	fn, ok := obj["function"].(map[string]any)
	if !ok {
		return false
	}
	description, _ := fn["description"].(string)
	if description != def.Description {
		return false
	}
	return reflect.DeepEqual(fn["parameters"], def.Parameters)
}

// prepareBody rewrites an upstream Chat Completions body so the free tier
// accepts it. It reports whether anything changed. Anything it does not
// understand (non-JSON, non-object, "tools" of an unexpected type) is left
// alone rather than risking a mangled request.
func prepareBody(body []byte, forceStream bool, c toolCatalogue) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, false
	}
	if payload == nil {
		return body, false
	}

	changed := false
	if forceStream {
		if streaming, ok := payload["stream"].(bool); !ok || !streaming {
			payload["stream"] = true
			changed = true
		}
	}

	tools, toolsChanged := ensureTools(payload, c)
	if toolsChanged {
		payload["tools"] = tools
		changed = true
	}

	if !changed {
		return body, false
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return body, false
	}
	return encoded, true
}

// ensureTools makes the tool list read as an OpenCode agent session: every
// recognised name carries its genuine definition, and "read" is appended when
// the caller did not declare it. Unrecognised client tools pass through
// untouched.
func ensureTools(payload map[string]any, c toolCatalogue) ([]any, bool) {
	raw, exists := payload["tools"]
	if !exists {
		return []any{c.chatTool(injectedTool)}, true
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, false
	}

	changed := false
	present := make(map[string]bool, len(items))
	for i, item := range items {
		name := chatToolName(item)
		if name == "" {
			continue
		}
		present[name] = true
		// A recognised name carrying anything other than the genuine definition
		// would be rejected, so it is replaced even though the caller supplied
		// one — but only when it actually differs, so a second pass is a no-op.
		if def, known := c[name]; known && !chatToolIsGenuine(item, def) {
			items[i] = c.chatTool(name)
			changed = true
		}
	}
	if !present[injectedTool] {
		items = append(items, c.chatTool(injectedTool))
		changed = true
	}
	if !changed {
		return nil, false
	}
	return items, true
}
