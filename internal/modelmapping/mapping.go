// Package modelmapping rewrites client-requested model names before provider routing.
package modelmapping

import (
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

// Result is the outcome of applying configured model-mapping rules.
type Result struct {
	// Model is the model name used for routing and upstream execution.
	Model string
	// Original is the client-requested model name, including any thinking suffix.
	Original string
	// Changed reports whether Model differs from Original.
	Changed bool
	// ForceMapping reports whether responses should echo Original.
	ForceMapping bool
}

// Apply rewrites modelName using the first enabled matching rule.
func Apply(rules []config.ModelMappingRule, modelName string) Result {
	original := strings.TrimSpace(modelName)
	result := Result{Model: original, Original: original}
	if original == "" || len(rules) == 0 {
		return result
	}
	parsed := thinking.ParseSuffix(original)
	base := strings.TrimSpace(parsed.ModelName)
	if base == "" {
		base = original
	}
	for _, rule := range rules {
		if !config.ModelMappingRuleEnabled(rule) {
			continue
		}
		mappedBase, ok := applyRule(rule, base)
		if !ok {
			continue
		}
		mapped := preserveSuffix(mappedBase, parsed)
		if mapped == "" || mapped == original {
			return result
		}
		result.Model = mapped
		result.Changed = true
		result.ForceMapping = config.ModelMappingForceMapping(rule)
		return result
	}
	return result
}

func applyRule(rule config.ModelMappingRule, base string) (string, bool) {
	match := strings.TrimSpace(rule.Match)
	target := strings.TrimSpace(rule.Target)
	if match == "" || target == "" {
		return "", false
	}
	switch strings.ToLower(strings.TrimSpace(rule.Mode)) {
	case "", config.ModelMappingModeExact:
		if !strings.EqualFold(base, match) {
			return "", false
		}
		return target, true
	case config.ModelMappingModeWildcard:
		if !matchWildcard(match, base) {
			return "", false
		}
		return target, true
	case config.ModelMappingModeRegex:
		re, err := regexp.Compile(match)
		if err != nil {
			return "", false
		}
		if !re.MatchString(base) {
			return "", false
		}
		replaced := strings.TrimSpace(re.ReplaceAllString(base, target))
		if replaced == "" {
			return "", false
		}
		return replaced, true
	default:
		return "", false
	}
}

func preserveSuffix(mappedBase string, request thinking.SuffixResult) string {
	mappedBase = strings.TrimSpace(mappedBase)
	if mappedBase == "" {
		return ""
	}
	if thinking.ParseSuffix(mappedBase).HasSuffix {
		return mappedBase
	}
	if request.HasSuffix && request.RawSuffix != "" {
		return mappedBase + "(" + request.RawSuffix + ")"
	}
	return mappedBase
}

// matchWildcard performs case-insensitive glob matching where '*' matches any substring.
func matchWildcard(pattern, value string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	value = strings.ToLower(strings.TrimSpace(value))
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	pi, si := 0, 0
	starIdx := -1
	matchIdx := 0
	for si < len(value) {
		if pi < len(pattern) && pattern[pi] == value[si] {
			pi++
			si++
			continue
		}
		if pi < len(pattern) && pattern[pi] == '*' {
			starIdx = pi
			matchIdx = si
			pi++
			continue
		}
		if starIdx != -1 {
			pi = starIdx + 1
			matchIdx++
			si = matchIdx
			continue
		}
		return false
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}
