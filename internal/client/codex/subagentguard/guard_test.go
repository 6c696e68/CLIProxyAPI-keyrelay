package subagentguard

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/tidwall/gjson"
)

func TestStripSubagentTools(t *testing.T) {
	t.Parallel()

	t.Run("removes multi_agent_v1 keeps mcp", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
			"tools":[
				{"type":"namespace","name":"multi_agent_v1","tools":[{"type":"function","name":"spawn_agent"}]},
				{"type":"namespace","name":"mcp__demo","tools":[{"type":"function","name":"ping"}]}
			],
			"tool_choice":{"type":"namespace","name":"multi_agent_v1"}
		}`)
		out, removed := StripSubagentTools(body)
		if len(removed) != 1 || removed[0] != "multi_agent_v1" {
			t.Fatalf("removed = %v, want [multi_agent_v1]", removed)
		}
		if gjson.GetBytes(out, "tools.#").Int() != 1 {
			t.Fatalf("tools count = %d, want 1; out=%s", gjson.GetBytes(out, "tools.#").Int(), out)
		}
		if got := gjson.GetBytes(out, "tools.0.name").String(); got != "mcp__demo" {
			t.Fatalf("kept tool = %q, want mcp__demo", got)
		}
		if got := gjson.GetBytes(out, "tool_choice").String(); got != "auto" {
			t.Fatalf("tool_choice = %q, want auto", got)
		}
	})

	t.Run("removes collaboration namespace", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]},{"type":"function","name":"clock"}]}`)
		out, removed := StripSubagentTools(body)
		if len(removed) != 1 || removed[0] != "collaboration" {
			t.Fatalf("removed = %v, want [collaboration]", removed)
		}
		if gjson.GetBytes(out, "tools.0.name").String() != "clock" {
			t.Fatalf("unexpected tools: %s", out)
		}
	})

	t.Run("clears tools when only subagent namespaces remain", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"tools":[{"type":"namespace","name":"multi_agent_v1","tools":[]}],"tool_choice":"required","parallel_tool_calls":true}`)
		out, removed := StripSubagentTools(body)
		if len(removed) != 1 {
			t.Fatalf("removed = %v", removed)
		}
		for _, key := range []string{"tools", "tool_choice", "parallel_tool_calls"} {
			if gjson.GetBytes(out, key).Exists() {
				t.Fatalf("%s still present: %s", key, out)
			}
		}
	})

	t.Run("noop when no subagent namespace", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"tools":[{"type":"function","name":"clock"}]}`)
		out, removed := StripSubagentTools(body)
		if len(removed) != 0 {
			t.Fatalf("removed = %v, want empty", removed)
		}
		if string(out) != string(body) {
			t.Fatalf("payload changed: %s", out)
		}
	})

	t.Run("filters allowed_tools choice", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
			"tools":[
				{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]},
				{"type":"function","name":"clock"}
			],
			"tool_choice":{"type":"allowed_tools","tools":[
				{"type":"namespace","name":"collaboration"},
				{"type":"function","name":"clock"}
			]}
		}`)
		out, _ := StripSubagentTools(body)
		choice := gjson.GetBytes(out, "tool_choice")
		if choice.Get("type").String() != "allowed_tools" {
			t.Fatalf("tool_choice = %s", choice.Raw)
		}
		if choice.Get("tools.#").Int() != 1 || choice.Get("tools.0.name").String() != "clock" {
			t.Fatalf("allowed tools = %s", choice.Raw)
		}
	})
}

func TestIsSpawnedSubagentRequest(t *testing.T) {
	t.Parallel()

	t.Run("header collab_spawn", func(t *testing.T) {
		t.Parallel()
		headers := http.Header{}
		headers.Set("X-Openai-Subagent", "collab_spawn")
		if !IsSpawnedSubagentRequest(headers, nil) {
			t.Fatal("expected true for collab_spawn header")
		}
	})

	t.Run("body client_metadata header mirror", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"client_metadata":{"x-openai-subagent":"collab_spawn"}}`)
		if !IsSpawnedSubagentRequest(nil, body) {
			t.Fatal("expected true for body metadata")
		}
	})

	t.Run("turn metadata subagent_kind", func(t *testing.T) {
		t.Parallel()
		meta, _ := json.Marshal(map[string]any{"thread_source": "subagent", "subagent_kind": "thread_spawn"})
		headers := http.Header{}
		headers.Set("X-Codex-Turn-Metadata", string(meta))
		if !IsSpawnedSubagentRequest(headers, nil) {
			t.Fatal("expected true for thread_spawn turn metadata")
		}
	})

	t.Run("body turn metadata", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"subagent_kind\":\"thread_spawn\"}"}}`)
		if !IsSpawnedSubagentRequest(http.Header{}, body) {
			t.Fatal("expected true for body turn metadata")
		}
	})

	t.Run("parent and compact are false", func(t *testing.T) {
		t.Parallel()
		parent := []byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"thread_source\":\"user\"}"}}`)
		if IsSpawnedSubagentRequest(http.Header{}, parent) {
			t.Fatal("parent request must be false")
		}
		headers := http.Header{}
		headers.Set("X-Openai-Subagent", "compact")
		if IsSpawnedSubagentRequest(headers, nil) {
			t.Fatal("compact must be false")
		}
	})
}

func TestDisabledErrorPayload(t *testing.T) {
	t.Parallel()
	raw := DisabledErrorPayload()
	if gjson.GetBytes(raw, "error.code").String() != "subagents_disabled" {
		t.Fatalf("unexpected payload: %s", raw)
	}
	if gjson.GetBytes(raw, "error.type").String() != "invalid_request_error" {
		t.Fatalf("unexpected type: %s", raw)
	}
}
