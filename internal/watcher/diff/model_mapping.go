package diff

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// DiffModelMappingChanges compares global model-mapping rule lists.
func DiffModelMappingChanges(oldRules, newRules []config.ModelMappingRule) []string {
	oldInfo := summarizeModelMapping(oldRules)
	newInfo := summarizeModelMapping(newRules)
	if oldInfo.hash == newInfo.hash {
		return nil
	}
	switch {
	case oldInfo.count == 0 && newInfo.count > 0:
		return []string{fmt.Sprintf("model-mapping: added (%d entries)", newInfo.count)}
	case oldInfo.count > 0 && newInfo.count == 0:
		return []string{"model-mapping: removed"}
	default:
		return []string{fmt.Sprintf("model-mapping: updated (%d -> %d entries)", oldInfo.count, newInfo.count)}
	}
}

type modelMappingSummary struct {
	hash  string
	count int
}

func summarizeModelMapping(rules []config.ModelMappingRule) modelMappingSummary {
	if len(rules) == 0 {
		return modelMappingSummary{}
	}
	normalized := make([]string, 0, len(rules))
	for _, rule := range rules {
		match := strings.TrimSpace(rule.Match)
		target := strings.TrimSpace(rule.Target)
		if match == "" || target == "" {
			continue
		}
		key := strings.ToLower(match) + "->" + target + "|mode=" + strings.ToLower(strings.TrimSpace(rule.Mode))
		if !config.ModelMappingRuleEnabled(rule) {
			key += "|disabled"
		}
		if !config.ModelMappingForceMapping(rule) {
			key += "|force-mapping=false"
		}
		normalized = append(normalized, key)
	}
	if len(normalized) == 0 {
		return modelMappingSummary{}
	}
	sum := sha256.Sum256([]byte(strings.Join(normalized, "|")))
	return modelMappingSummary{
		hash:  hex.EncodeToString(sum[:]),
		count: len(normalized),
	}
}
