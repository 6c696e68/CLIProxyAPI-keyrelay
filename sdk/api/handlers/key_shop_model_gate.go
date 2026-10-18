package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/net/context"
	"gopkg.in/yaml.v3"
)

type keyShopRejectionNotifier interface {
	NoteKeyShopRejection(context.Context, string, string, string, int)
}

type keyShopPolicySource interface {
	KeyShopConfigYAML() []byte
}

type keyShopModelEntry struct {
	Name  string `yaml:"name"`
	Alias string `yaml:"alias"`
}

type keyShopPolicyConfig struct {
	ModelAccessMode string              `yaml:"model_access_mode"`
	Models          []keyShopModelEntry `yaml:"models"`
}

// applyKeyShopModelGate enforces ck_ model access before provider routing.
// An allowed alias is rewritten to its upstream name. Admission stays in the plugin interceptor.
func (h *BaseAPIHandler) applyKeyShopModelGate(ctx context.Context, modelName string, rawJSON []byte) (string, []byte, *interfaces.ErrorMessage) {
	source, ok := h.interceptorHost().(keyShopPolicySource)
	if !ok || source == nil {
		return modelName, rawJSON, nil
	}
	secret := keyShopSecret(headersFromContext(ctx))
	if !strings.HasPrefix(secret, "ck_") {
		return modelName, rawJSON, nil
	}
	raw := source.KeyShopConfigYAML()
	if len(strings.TrimSpace(string(raw))) == 0 {
		return modelName, rawJSON, nil
	}
	var cfg keyShopPolicyConfig
	if yaml.Unmarshal(raw, &cfg) != nil {
		return modelName, rawJSON, keyShopPolicyError(http.StatusServiceUnavailable, "model_policy_unavailable", "model policy unavailable", "server_error")
	}
	if !strings.EqualFold(strings.TrimSpace(cfg.ModelAccessMode), "custom") {
		return modelName, rawJSON, nil
	}
	bodyModel := ""
	if gjson.GetBytes(rawJSON, "model").Exists() {
		bodyModel = strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
	}
	requested := strings.TrimSpace(modelName)
	if requested == "" {
		requested = bodyModel
	}
	rewritten, allowed := resolveKeyShopModel(cfg.Models, requested)
	if !allowed {
		h.noteKeyShopRejection(ctx, secret, "model_not_allowed", http.StatusForbidden)
		return modelName, rawJSON, keyShopDenied()
	}
	if bodyModel != "" && !strings.EqualFold(bodyModel, requested) {
		bodyRewritten, bodyAllowed := resolveKeyShopModel(cfg.Models, bodyModel)
		if !bodyAllowed || bodyRewritten != rewritten {
			h.noteKeyShopRejection(ctx, secret, "model_not_allowed", http.StatusForbidden)
			return modelName, rawJSON, keyShopDenied()
		}
	}
	if rewritten == "" || rewritten == requested && (bodyModel == "" || strings.EqualFold(bodyModel, rewritten)) {
		return modelName, rawJSON, nil
	}
	updated := rawJSON
	if gjson.GetBytes(rawJSON, "model").Exists() {
		if next, err := sjson.SetBytes(rawJSON, "model", rewritten); err == nil {
			updated = next
		}
	}
	if requested == "" {
		return modelName, updated, nil
	}
	return rewritten, updated, nil
}

func (h *BaseAPIHandler) noteKeyShopRejection(ctx context.Context, secret, code string, status int) {
	notifier, ok := h.interceptorHost().(keyShopRejectionNotifier)
	if !ok || notifier == nil {
		return
	}
	notifier.NoteKeyShopRejection(ctx, secret, keyShopAuditID(ctx), code, status)
}

func keyShopAuditID(ctx context.Context) string {
	if id := strings.TrimSpace(coreusage.ExecutionRequestIDFromContext(ctx)); id != "" {
		return id
	}
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return "gate-" + hex.EncodeToString(buf)
}

func keyShopDenied() *interfaces.ErrorMessage {
	return keyShopPolicyError(http.StatusForbidden, "model_not_allowed", "model is not allowed for this key", "invalid_request_error")
}

func keyShopPolicyError(status int, code, message, errType string) *interfaces.ErrorMessage {
	body := []byte(`{"error":{"message":"` + message + `","type":"` + errType + `","code":"` + code + `"}}`)
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	return directTerminationError(status, headers, body)
}

func resolveKeyShopModel(models []keyShopModelEntry, requested string) (string, bool) {
	parsed := thinking.ParseSuffix(strings.TrimSpace(requested))
	base := strings.TrimSpace(parsed.ModelName)
	for _, model := range models {
		name := strings.TrimSpace(model.Name)
		if name == "" {
			continue
		}
		configured := strings.TrimSpace(model.Alias)
		if configured == "" {
			configured = name
		}
		if !strings.EqualFold(base, configured) && !strings.EqualFold(strings.TrimSpace(requested), configured) {
			continue
		}
		if !parsed.HasSuffix {
			return name, true
		}
		return name + "(" + parsed.RawSuffix + ")", true
	}
	return "", false
}

func keyShopSecret(headers http.Header) string {
	if headers == nil {
		return ""
	}
	if secret, ok := keyShopBearer(headers.Get("Authorization")); ok {
		return secret
	}
	if secret := strings.TrimSpace(headers.Get("X-Api-Key")); secret != "" {
		return secret
	}
	return strings.TrimSpace(headers.Get("X-Goog-Api-Key"))
}

func keyShopBearer(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	parts := strings.SplitN(value, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
		secret := strings.TrimSpace(parts[1])
		return secret, secret != ""
	}
	return value, true
}
