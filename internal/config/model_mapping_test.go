package config

import "testing"

func TestSanitizeModelMapping(t *testing.T) {
	falseVal := false
	rules := SanitizeModelMapping([]ModelMappingRule{
		{Match: " gpt-5.4 ", Target: " gpt-5.5 "},
		{Match: "gpt-5.*", Target: "gpt-5.5"},
		{Match: `^gpt-5\.4(-.*)?$`, Target: "gpt-5.5$1", Mode: "REGEX"},
		{Match: "(", Target: "x", Mode: ModelMappingModeRegex},
		{Match: "a", Target: "b", Mode: "glob"},
		{Match: "", Target: "x"},
		{Match: "keep", Target: "me", Enabled: &falseVal, ForceMapping: &falseVal},
	})
	if len(rules) != 4 {
		t.Fatalf("expected 4 sanitized rules, got %d: %+v", len(rules), rules)
	}
	if rules[0].Match != "gpt-5.4" || rules[0].Target != "gpt-5.5" || rules[0].Mode != ModelMappingModeExact {
		t.Fatalf("unexpected exact rule: %+v", rules[0])
	}
	if rules[1].Mode != ModelMappingModeWildcard {
		t.Fatalf("expected inferred wildcard, got %+v", rules[1])
	}
	if rules[2].Mode != ModelMappingModeRegex {
		t.Fatalf("expected regex mode, got %+v", rules[2])
	}
	if rules[3].Match != "keep" || ModelMappingRuleEnabled(rules[3]) || ModelMappingForceMapping(rules[3]) {
		t.Fatalf("unexpected preserved flags: %+v", rules[3])
	}
}

func TestModelMappingDefaults(t *testing.T) {
	rule := ModelMappingRule{Match: "a", Target: "b"}
	if !ModelMappingRuleEnabled(rule) || !ModelMappingForceMapping(rule) {
		t.Fatal("omitted enabled/force-mapping should default to true")
	}
}

func TestParseConfigBytes_ModelMapping(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
port: 8317
model-mapping:
  - match: "gpt-5.4"
    target: "gpt-5.5"
  - match: "gpt-5.*"
    target: "gpt-5.5"
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if len(cfg.ModelMapping) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(cfg.ModelMapping))
	}
	if cfg.ModelMapping[0].Mode != ModelMappingModeExact || cfg.ModelMapping[1].Mode != ModelMappingModeWildcard {
		t.Fatalf("unexpected sanitized modes: %+v", cfg.ModelMapping)
	}
}
