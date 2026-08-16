package config

import (
	"strings"

	log "github.com/sirupsen/logrus"
)

// SanitizeModelMapping normalizes global request model rewrite rules.
func (cfg *SDKConfig) SanitizeModelMapping() {
	if cfg == nil {
		return
	}
	cfg.ModelMapping = SanitizeModelMapping(cfg.ModelMapping)
}

// SanitizeModelMapping normalizes global request model rewrite rules on the full config.
func (cfg *Config) SanitizeModelMapping() {
	if cfg == nil {
		return
	}
	cfg.SDKConfig.SanitizeModelMapping()
}

// SanitizeModelMapping trims, infers modes, and drops invalid rewrite rules.
// Invalid regex rules are discarded so a bad config cannot crash the server.
func SanitizeModelMapping(rules []ModelMappingRule) []ModelMappingRule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]ModelMappingRule, 0, len(rules))
	for _, rule := range rules {
		match := strings.TrimSpace(rule.Match)
		target := strings.TrimSpace(rule.Target)
		if match == "" || target == "" {
			continue
		}
		mode, ok := normalizeModelMappingMode(rule.Mode, match)
		if !ok {
			log.Warnf("model-mapping: dropping rule match=%q target=%q: invalid mode %q", match, target, strings.TrimSpace(rule.Mode))
			continue
		}
		if mode == ModelMappingModeRegex {
			if _, err := compileModelMappingRegex(match); err != nil {
				log.Warnf("model-mapping: dropping invalid regex %q: %v", match, err)
				continue
			}
		}
		out = append(out, ModelMappingRule{
			Match:        match,
			Target:       target,
			Mode:         mode,
			Enabled:      rule.Enabled,
			ForceMapping: rule.ForceMapping,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeModelMappingMode(mode, match string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "":
		if strings.Contains(match, "*") {
			return ModelMappingModeWildcard, true
		}
		return ModelMappingModeExact, true
	case ModelMappingModeExact:
		return ModelMappingModeExact, true
	case ModelMappingModeWildcard:
		return ModelMappingModeWildcard, true
	case ModelMappingModeRegex:
		return ModelMappingModeRegex, true
	default:
		return "", false
	}
}
