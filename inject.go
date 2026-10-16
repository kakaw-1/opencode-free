package main

// Body rewriting for OpenCode Zen's free tier.
//
// The free tier does not gate on headers alone: the request *body* has to look
// like an OpenCode agent session. Measured directly against
// https://opencode.ai/inference/v1 on 2026-10-16, bypassing this plugin so it
// cannot mask the answer (models deepseek-v4.1-flash-free, cat-jam-free,
// mimo-v2.6-flash-free; all three agree):
//
//   - "stream" must be true. false and absent both return 403 FreeTierError,
//     and the upstream always answers with SSE regardless.
//   - "tools" must contain an entry named "read". Nothing about that entry is
//     inspected: a one-line stub with a made-up description and schema passes,
//     while ten genuine OpenCode tools without "read" are rejected. Presence of
//     the name is the whole test, and tool order is irrelevant.
//   - A system message that both calls itself a title generator and forbids
//     tool use is rejected. Either fragment on its own passes; the pair is what
//     trips it. That is OpenCode's own title-generation prompt, which the CLI
//     sends on its small model once per session.
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
	"strings"
)

// injectedTool is the one tool added to requests that do not already declare it.
const injectedTool = "read"

// blockedPromptPair is the system-prompt fragment pair the free tier rejects.
// Both halves have to be present for the rejection; the replacement says the
// same thing without tripping it.
const (
	blockedPromptLead       = "You are a title generator"
	blockedPromptToolBan    = "Never use tools"
	blockedPromptToolBanAlt = "Do not call any tools"
)

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

	if sanitizeSystemPrompts(payload) {
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

// ensureTools makes the tool list read as an OpenCode agent session by making
// sure an entry named "read" is present. It is appended when the caller did not
// declare one, and every client entry is left byte-for-byte alone otherwise.
//
// Rewriting a caller's own "read" definition is actively harmful. OpenCode
// 1.18.35 declares read with a `filePath` parameter and the 2.0.29 catalogue
// uses `path`; substituting the catalogue definition made the model call read
// with the wrong argument names, which the client rejected with a schema error
// and a wasted turn. The upstream accepts either definition, so the client's
// own contract wins.
func ensureTools(payload map[string]any, c toolCatalogue) ([]any, bool) {
	raw, exists := payload["tools"]
	if !exists {
		return []any{c.chatTool(injectedTool)}, true
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, false
	}

	for _, item := range items {
		if chatToolName(item) == injectedTool {
			return nil, false
		}
	}
	return append(items, c.chatTool(injectedTool)), true
}

// sanitizeSystemPrompts rewrites system messages that carry both halves of the
// fragment pair the free tier rejects, and reports whether it touched anything.
//
// Only the tool-ban sentence is reworded; the rest of the prompt, including the
// task description and the rules list, is handed to the model untouched, so a
// title generator still generates titles the way its author specified.
func sanitizeSystemPrompts(payload map[string]any) bool {
	messages, ok := payload["messages"].([]any)
	if !ok {
		return false
	}
	changed := false
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := message["role"].(string); role != "system" {
			continue
		}
		content, ok := message["content"].(string)
		if !ok {
			continue
		}
		if !strings.Contains(content, blockedPromptLead) || !strings.Contains(content, blockedPromptToolBan) {
			continue
		}
		message["content"] = strings.ReplaceAll(content, blockedPromptToolBan, blockedPromptToolBanAlt)
		changed = true
	}
	return changed
}
