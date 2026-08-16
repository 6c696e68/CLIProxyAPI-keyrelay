package diff

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestDiffModelMappingChanges(t *testing.T) {
	oldRules := []config.ModelMappingRule{{Match: "gpt-5.4", Target: "gpt-5.5"}}
	newRules := []config.ModelMappingRule{{Match: "gpt-5.4", Target: "gpt-5.6"}}
	changes := DiffModelMappingChanges(oldRules, newRules)
	if len(changes) != 1 || changes[0] != "model-mapping: updated (1 -> 1 entries)" {
		t.Fatalf("unexpected changes: %#v", changes)
	}
	if changes := DiffModelMappingChanges(oldRules, oldRules); len(changes) != 0 {
		t.Fatalf("expected no changes, got %#v", changes)
	}
}
