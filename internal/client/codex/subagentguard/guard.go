// Package subagentguard blocks Codex CLI spawn_agent / multi-agent threads
// at a Responses API proxy boundary (HTTP and WebSocket frames).
package subagentguard

import (
	"encoding/json"
	"net/http"
	"strings"
)

// SubagentNamespaces are Codex tool namespaces used for spawn_agent.
// MCP namespaces use the "mcp__" prefix and must be kept.
var SubagentNamespaces = map[string]struct{}{
	"multi_agent_v1": {},
	"collaboration":  {},
}

const (
	subagentHeader       = "x-openai-subagent"
	spawnedSubagentValue = "collab_spawn"
	turnMetadataHeader   = "x-codex-turn-metadata"
	spawnedSubagentKind  = "thread_spawn"
)

// DisabledErrorPayload is the JSON body returned when a spawned sub-agent
// request is rejected.
func DisabledErrorPayload() []byte {
	return []byte(`{"error":{"message":"Sub-agents are disabled on this server","type":"invalid_request_error","code":"subagents_disabled"}}`)
}

// StripSubagentTools removes Codex sub-agent namespace tool declarations from a
// Responses request body. MCP and other tools are preserved. Returns the updated
// body and the removed namespace names (empty when unchanged).
func StripSubagentTools(body []byte) ([]byte, []string) {
	if len(body) == 0 {
		return body, nil
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return body, nil
	}
	tools, ok := root["tools"].([]any)
	if !ok {
		return body, nil
	}

	kept := make([]any, 0, len(tools))
	removed := make([]string, 0)
	removedSet := make(map[string]struct{})
	for _, entry := range tools {
		tool, ok := entry.(map[string]any)
		if !ok {
			kept = append(kept, entry)
			continue
		}
		name, _ := tool["name"].(string)
		typ, _ := tool["type"].(string)
		if typ == "namespace" {
			if _, block := SubagentNamespaces[name]; block {
				removed = append(removed, name)
				removedSet[name] = struct{}{}
				continue
			}
		}
		kept = append(kept, entry)
	}
	if len(removed) == 0 {
		return body, nil
	}
	if len(kept) > 0 {
		root["tools"] = kept
		dropDanglingToolChoice(root, removedSet)
	} else {
		delete(root, "tools")
		delete(root, "tool_choice")
		delete(root, "parallel_tool_calls")
	}
	out, err := json.Marshal(root)
	if err != nil {
		return body, nil
	}
	return out, removed
}

func dropDanglingToolChoice(root map[string]any, removed map[string]struct{}) {
	choice, ok := root["tool_choice"].(map[string]any)
	if !ok {
		return
	}
	typ, _ := choice["type"].(string)
	switch typ {
	case "namespace":
		name, _ := choice["name"].(string)
		if _, hit := removed[name]; hit {
			root["tool_choice"] = "auto"
		}
	case "allowed_tools":
		entries, ok := choice["tools"].([]any)
		if !ok {
			return
		}
		filtered := make([]any, 0, len(entries))
		for _, entry := range entries {
			item, ok := entry.(map[string]any)
			if !ok {
				filtered = append(filtered, entry)
				continue
			}
			itemType, _ := item["type"].(string)
			name, _ := item["name"].(string)
			if itemType == "namespace" {
				if _, hit := removed[name]; hit {
					continue
				}
			}
			filtered = append(filtered, entry)
		}
		if len(filtered) == 0 {
			root["tool_choice"] = "auto"
			return
		}
		choice["tools"] = filtered
		root["tool_choice"] = choice
	}
}

// IsSpawnedSubagentRequest reports whether the request belongs to a thread
// created by spawn_agent. Only collab_spawn / thread_spawn are matched so
// Codex auxiliary flows (compact, guardian, memory) keep working.
func IsSpawnedSubagentRequest(headers http.Header, body []byte) bool {
	if headerValue(headers, subagentHeader) == spawnedSubagentValue {
		return true
	}
	metadata := clientMetadata(body)
	if stringValue(metadata, subagentHeader) == spawnedSubagentValue {
		return true
	}
	for _, raw := range []string{
		headerValue(headers, turnMetadataHeader),
		stringValue(metadata, turnMetadataHeader),
	} {
		if raw == "" {
			continue
		}
		var turn map[string]any
		if err := json.Unmarshal([]byte(raw), &turn); err != nil {
			continue
		}
		kind, _ := turn["subagent_kind"].(string)
		if kind == spawnedSubagentKind {
			return true
		}
	}
	return false
}

func headerValue(headers http.Header, key string) string {
	if headers == nil {
		return ""
	}
	return strings.TrimSpace(headers.Get(key))
}

func clientMetadata(body []byte) map[string]any {
	if len(body) == 0 {
		return nil
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil
	}
	meta, _ := root["client_metadata"].(map[string]any)
	return meta
}

func stringValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	// JSON object keys are case-sensitive; also accept common mixed-case forms.
	for k, v := range m {
		if strings.EqualFold(k, key) {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}
