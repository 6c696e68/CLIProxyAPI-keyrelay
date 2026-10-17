package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/codex"
	"github.com/tidwall/gjson"
)

func TestPrefixedAliasKeepsCanonicalThinkingLevel(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-prefixed-thinking"
	reg.RegisterClient(clientID, "codex", []*registry.ModelInfo{
		{ID: "vippro/gpt-6-sol", Type: "codex"},
		// A dynamic shell must not hide the static thinking definition.
		{ID: "gpt-6-sol", Type: "codex"},
	})
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	body := []byte(`{"model":"vippro/gpt-6-sol","input":"hi","reasoning":{"effort":"max"}}`)
	out, err := thinking.ApplyThinking(body, "vippro/gpt-6-sol", "codex", "codex", "codex")
	if err != nil {
		t.Fatalf("ApplyThinking() error = %v", err)
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "max" {
		t.Fatalf("reasoning.effort = %q, want max; body=%s", got, out)
	}
}
