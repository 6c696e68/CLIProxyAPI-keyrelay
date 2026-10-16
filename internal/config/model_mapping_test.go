package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

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

func TestSaveConfigPreserveComments_ClearsModelMapping(t *testing.T) {
	for _, tc := range []struct {
		name, raw              string
		migrate, debug, prefix bool
	}{
		{
			name:  "legacy",
			debug: true,
			raw: `# KEEP PORT
port: 8317
debug: true
model-mapping:
  - match: gpt-5.4
    target: gpt-5.5
`,
		},
		{
			name:    "v8",
			migrate: true,
			prefix:  true,
			raw: `# KEEP PORT
config-version: 8
server:
  port: 8317
routing:
  force-model-prefix: true
  model-mapping:
    - match: gpt-5.4
      target: gpt-5.5
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.ModelMapping) != 1 || cfg.Port != 8317 || cfg.Debug != tc.debug || cfg.ForceModelPrefix != tc.prefix {
				t.Fatalf("fixture port=%d debug=%v prefix=%v mapping=%+v", cfg.Port, cfg.Debug, cfg.ForceModelPrefix, cfg.ModelMapping)
			}
			cfg.ModelMapping = nil
			if err = SaveConfigPreserveComments(path, cfg, tc.migrate); err != nil {
				t.Fatal(err)
			}
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(saved), "KEEP PORT") {
				t.Fatalf("unrelated comment dropped:\n%s", saved)
			}
			var doc yaml.Node
			if err = yaml.Unmarshal(saved, &doc); err != nil {
				t.Fatal(err)
			}
			if yamlPath(doc.Content[0], "model-mapping") != nil || yamlPath(doc.Content[0], "routing.model-mapping") != nil {
				t.Fatalf("cleared model-mapping still present:\n%s", saved)
			}
			reloaded, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(reloaded.ModelMapping) != 0 || reloaded.Port != 8317 || reloaded.Debug != tc.debug || reloaded.ForceModelPrefix != tc.prefix {
				t.Fatalf("reload port=%d debug=%v prefix=%v mapping=%+v", reloaded.Port, reloaded.Debug, reloaded.ForceModelPrefix, reloaded.ModelMapping)
			}
		})
	}
}
