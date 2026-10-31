package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"gopkg.in/yaml.v3"
)

// KeyShopModelEntry is one upstream model and the optional client alias.
type KeyShopModelEntry struct {
	Name  string
	Alias string
}

// KeyShopModelList is the key-shop list_models response.
// Loaded is false when the plugin is not loaded; the caller keeps the legacy YAML policy.
type KeyShopModelList struct {
	Loaded    bool
	Mode      string
	AllModels bool
	Models    []KeyShopModelEntry
}

type keyShopModelLister interface {
	KeyShopListModels(ctx context.Context, secret string) (KeyShopModelList, error)
}

// FilterKeyShopModels narrows a model listing for a ck_ key. A grouped key uses
// its group, and an ungrouped key keeps the legacy custom YAML policy. An alias
// is shown under the client-visible alias. Listing never reserves tokens or
// records a rejection. A plugin error fails closed.
func (h *BaseAPIHandler) FilterKeyShopModels(c *gin.Context, models []map[string]any) ([]map[string]any, *interfaces.ErrorMessage) {
	if c == nil || c.Request == nil {
		return models, nil
	}
	secret := keyShopSecret(c.Request.Header)
	if !strings.HasPrefix(secret, "ck_") {
		return models, nil
	}
	host := h.interceptorHost()
	if lister, ok := host.(keyShopModelLister); ok && lister != nil {
		result, err := lister.KeyShopListModels(c.Request.Context(), secret)
		if err != nil {
			return nil, keyShopModelPolicyUnavailable()
		}
		if result.Loaded && strings.EqualFold(strings.TrimSpace(result.Mode), "group") {
			if result.AllModels {
				return models, nil
			}
			return filterKeyShopModelList(models, result.Models), nil
		}
	}
	source, ok := host.(keyShopPolicySource)
	if !ok || source == nil {
		return models, nil
	}
	raw := source.KeyShopConfigYAML()
	if len(strings.TrimSpace(string(raw))) == 0 {
		return models, nil
	}
	var cfg keyShopPolicyConfig
	if yaml.Unmarshal(raw, &cfg) != nil {
		return nil, keyShopModelPolicyUnavailable()
	}
	if !strings.EqualFold(strings.TrimSpace(cfg.ModelAccessMode), "custom") {
		return models, nil
	}
	allowed := make([]KeyShopModelEntry, 0, len(cfg.Models))
	for _, entry := range cfg.Models {
		allowed = append(allowed, KeyShopModelEntry{Name: entry.Name, Alias: entry.Alias})
	}
	return filterKeyShopModelList(models, allowed), nil
}

func keyShopModelPolicyUnavailable() *interfaces.ErrorMessage {
	return keyShopPolicyError(http.StatusServiceUnavailable, "model_policy_unavailable", "model policy unavailable", "server_error")
}

// filterKeyShopModelList keeps the server order and copies each allowed server
// map before it renames the visible model. The input maps are never mutated, and
// an entry that is not on the server list is not emitted.
func filterKeyShopModelList(models []map[string]any, allowed []KeyShopModelEntry) []map[string]any {
	filtered := make([]map[string]any, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		key := ""
		if id, ok := model["id"].(string); ok {
			key = strings.TrimSpace(id)
		}
		if key == "" {
			if name, ok := model["name"].(string); ok {
				key = strings.TrimSpace(strings.TrimPrefix(name, "models/"))
			}
		}
		if key == "" {
			continue
		}
		for _, entry := range allowed {
			name := strings.TrimSpace(entry.Name)
			if name == "" || !strings.EqualFold(name, key) {
				continue
			}
			clientName := strings.TrimSpace(entry.Alias)
			if clientName == "" {
				clientName = name
			}
			next := make(map[string]any, len(model))
			for k, v := range model {
				next[k] = v
			}
			next["id"] = clientName
			if nameValue, ok := model["name"].(string); ok && strings.HasPrefix(nameValue, "models/") {
				next["name"] = "models/" + clientName
			}
			filtered = append(filtered, next)
		}
	}
	return filtered
}
