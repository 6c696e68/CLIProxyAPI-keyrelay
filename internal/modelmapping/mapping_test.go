package modelmapping

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func boolPtr(v bool) *bool { return &v }

func TestApply_ExactAndSuffix(t *testing.T) {
	rules := []config.ModelMappingRule{
		{Match: "gpt-5.4", Target: "gpt-5.5"},
	}
	got := Apply(rules, "gpt-5.4(high)")
	if !got.Changed || got.Model != "gpt-5.5(high)" || !got.ForceMapping || got.Original != "gpt-5.4(high)" {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestApply_FirstMatchWins(t *testing.T) {
	rules := []config.ModelMappingRule{
		{Match: "gpt-5.4", Target: "gpt-5.5"},
		{Match: "gpt-5.4", Target: "gpt-5.6"},
	}
	got := Apply(rules, "gpt-5.4")
	if got.Model != "gpt-5.5" {
		t.Fatalf("got %q, want gpt-5.5", got.Model)
	}
}

func TestApply_DisabledSkipped(t *testing.T) {
	rules := []config.ModelMappingRule{
		{Match: "gpt-5.4", Target: "gpt-5.5", Enabled: boolPtr(false)},
		{Match: "gpt-5.4", Target: "gpt-5.6"},
	}
	got := Apply(rules, "gpt-5.4")
	if got.Model != "gpt-5.6" {
		t.Fatalf("got %q, want gpt-5.6", got.Model)
	}
}

func TestApply_WildcardAndRegex(t *testing.T) {
	rules := []config.ModelMappingRule{
		{Match: "gpt-5.*", Target: "gpt-5.5", Mode: config.ModelMappingModeWildcard},
	}
	got := Apply(rules, "GPT-5.4")
	if !got.Changed || got.Model != "gpt-5.5" {
		t.Fatalf("wildcard result: %+v", got)
	}

	rules = []config.ModelMappingRule{
		{Match: `^gpt-5\.4(-.*)?$`, Target: "gpt-5.5$1", Mode: config.ModelMappingModeRegex},
	}
	got = Apply(rules, "gpt-5.4-mini")
	if !got.Changed || got.Model != "gpt-5.5-mini" {
		t.Fatalf("regex result: %+v", got)
	}
}

func TestApply_ForceMappingFalse(t *testing.T) {
	rules := []config.ModelMappingRule{
		{Match: "gpt-5.4", Target: "gpt-5.5", ForceMapping: boolPtr(false)},
	}
	got := Apply(rules, "gpt-5.4")
	if !got.Changed || got.ForceMapping {
		t.Fatalf("expected force-mapping false, got %+v", got)
	}
}

func TestApply_NoMatch(t *testing.T) {
	got := Apply([]config.ModelMappingRule{{Match: "gpt-5.4", Target: "gpt-5.5"}}, "gpt-4o")
	if got.Changed || got.Model != "gpt-4o" {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestApply_InvalidRegexSkipped(t *testing.T) {
	rules := []config.ModelMappingRule{
		{Match: "(", Target: "gpt-5.5", Mode: config.ModelMappingModeRegex},
		{Match: "gpt-5.4", Target: "gpt-5.6"},
	}
	got := Apply(rules, "gpt-5.4")
	if got.Model != "gpt-5.6" {
		t.Fatalf("got %q, want gpt-5.6", got.Model)
	}
}
