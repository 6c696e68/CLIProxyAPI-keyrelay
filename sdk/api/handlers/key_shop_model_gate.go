package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"

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

// KeyShopResolveResult is the key-shop resolve_model response.
// Loaded is false when the plugin is not loaded; the caller keeps the legacy YAML policy.
type KeyShopResolveResult struct {
	Loaded  bool
	Mode    string
	Allowed bool
	Model   string
}

type keyShopModelResolver interface {
	KeyShopResolveModel(ctx context.Context, secret, model, bodyModel string) (KeyShopResolveResult, error)
}

type keyShopModelEntry struct {
	Name  string `yaml:"name"`
	Alias string `yaml:"alias"`
}

type keyShopPolicyConfig struct {
	ModelAccessMode string              `yaml:"model_access_mode"`
	Models          []keyShopModelEntry `yaml:"models"`
}

type keyShopGateStateKey struct{}

// keyShopGateState caches one resolve_model decision for every gate call in a request.
// The pointer is stored on the context so PrepareStreamModelRoute, StreamRouteModel,
// and execution share it. Denial is recorded once, by the execution path.
type keyShopGateState struct {
	mu        sync.Mutex
	evaluated bool
	secret    string
	decision  keyShopGateDecision
	noted     bool
}

type keyShopGateDecision struct {
	kind  string
	model string
}

func keyShopGateStateFrom(ctx context.Context) (*keyShopGateState, context.Context) {
	if ctx != nil {
		if state, ok := ctx.Value(keyShopGateStateKey{}).(*keyShopGateState); ok && state != nil {
			return state, ctx
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	state := &keyShopGateState{}
	return state, context.WithValue(ctx, keyShopGateStateKey{}, state)
}

// applyKeyShopModelGate enforces ck_ model access before provider routing.
// A grouped key is resolved by the plugin and does not consult the global YAML list.
// An allowed alias is rewritten to its upstream name. The returned context carries
// the cached decision. Admission stays in the plugin interceptor. Denial is not
// recorded here; the execution path records it once with the lifecycle request id.
func (h *BaseAPIHandler) applyKeyShopModelGate(ctx context.Context, modelName string, rawJSON []byte) (context.Context, string, []byte, *interfaces.ErrorMessage) {
	secret := keyShopSecret(headersFromContext(ctx))
	if !strings.HasPrefix(secret, "ck_") {
		return ctx, modelName, rawJSON, nil
	}
	state, ctx := keyShopGateStateFrom(ctx)
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.evaluated {
		state.secret = secret
		state.decision = h.evaluateKeyShopGate(ctx, secret, modelName, rawJSON)
		state.evaluated = true
	}
	nextName, nextBody, errMsg := state.decision.apply(modelName, rawJSON)
	return ctx, nextName, nextBody, errMsg
}

func (d keyShopGateDecision) apply(modelName string, rawJSON []byte) (string, []byte, *interfaces.ErrorMessage) {
	switch d.kind {
	case "deny":
		return modelName, rawJSON, keyShopDenied()
	case "unavailable":
		return modelName, rawJSON, keyShopPolicyError(http.StatusServiceUnavailable, "model_policy_unavailable", "model policy unavailable", "server_error")
	case "rewrite":
		nextName, nextBody := applyKeyShopRewrite(modelName, rawJSON, d.model)
		return nextName, nextBody, nil
	default:
		return modelName, rawJSON, nil
	}
}

func (h *BaseAPIHandler) evaluateKeyShopGate(ctx context.Context, secret, modelName string, rawJSON []byte) keyShopGateDecision {
	bodyModel := ""
	if gjson.GetBytes(rawJSON, "model").Exists() {
		bodyModel = strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
	}
	host := h.interceptorHost()
	if resolver, ok := host.(keyShopModelResolver); ok && resolver != nil {
		resolved, err := resolver.KeyShopResolveModel(ctx, secret, modelName, bodyModel)
		if err != nil {
			return keyShopGateDecision{kind: "unavailable"}
		}
		if resolved.Loaded && strings.EqualFold(strings.TrimSpace(resolved.Mode), "group") {
			if !resolved.Allowed {
				return keyShopGateDecision{kind: "deny"}
			}
			if keyShopResolvedModelUnchanged(modelName, bodyModel, resolved.Model) {
				return keyShopGateDecision{kind: "keep"}
			}
			return keyShopGateDecision{kind: "rewrite", model: resolved.Model}
		}
	}
	source, ok := host.(keyShopPolicySource)
	if !ok || source == nil {
		return keyShopGateDecision{kind: "keep"}
	}
	raw := source.KeyShopConfigYAML()
	if len(strings.TrimSpace(string(raw))) == 0 {
		return keyShopGateDecision{kind: "keep"}
	}
	var cfg keyShopPolicyConfig
	if yaml.Unmarshal(raw, &cfg) != nil {
		return keyShopGateDecision{kind: "unavailable"}
	}
	if !strings.EqualFold(strings.TrimSpace(cfg.ModelAccessMode), "custom") {
		return keyShopGateDecision{kind: "keep"}
	}
	requested := strings.TrimSpace(modelName)
	if requested == "" {
		requested = bodyModel
	}
	rewritten, allowed := resolveKeyShopModel(cfg.Models, requested)
	if !allowed {
		return keyShopGateDecision{kind: "deny"}
	}
	if bodyModel != "" && !strings.EqualFold(bodyModel, requested) {
		bodyRewritten, bodyAllowed := resolveKeyShopModel(cfg.Models, bodyModel)
		if !bodyAllowed || bodyRewritten != rewritten {
			return keyShopGateDecision{kind: "deny"}
		}
	}
	if keyShopResolvedModelUnchanged(modelName, bodyModel, rewritten) {
		return keyShopGateDecision{kind: "keep"}
	}
	return keyShopGateDecision{kind: "rewrite", model: rewritten}
}

// recordKeyShopGateDenial writes one rejected row for an execution-path denial.
// Prepare and route checks must not call it. The lifecycle request id is used so
// a repeated note cannot mint another gate- id and burn another quota unit.
func (h *BaseAPIHandler) recordKeyShopGateDenial(ctx context.Context, sourceFormat, model string, stream bool, gateErr *interfaces.ErrorMessage) {
	if gateErr == nil || gateErr.StatusCode != http.StatusForbidden || !strings.Contains(string(gateErr.Body), `"model_not_allowed"`) {
		return
	}
	state, ctx := keyShopGateStateFrom(ctx)
	state.mu.Lock()
	if state.noted {
		state.mu.Unlock()
		return
	}
	state.noted = true
	secret := state.secret
	state.mu.Unlock()
	if strings.TrimSpace(coreusage.ExecutionRequestIDFromContext(ctx)) == "" {
		lifecycle := h.newRequestLifecycleTracker(ctx, sourceFormat, model, model, stream, nil, "")
		ctx = coreusage.WithExecutionRequestID(ctx, lifecycle.requestID())
	}
	if secret == "" {
		secret = keyShopSecret(headersFromContext(ctx))
	}
	h.noteKeyShopRejection(ctx, secret, "model_not_allowed", http.StatusForbidden)
}

// keyShopResolvedModelUnchanged reports that the plugin kept the requested model.
// all_models does this, and a differing body model must not be overwritten.
func keyShopResolvedModelUnchanged(modelName, bodyModel, resolved string) bool {
	resolved = strings.TrimSpace(resolved)
	requested := strings.TrimSpace(modelName)
	if requested == "" {
		requested = strings.TrimSpace(bodyModel)
	}
	return resolved == "" || resolved == requested
}

func applyKeyShopRewrite(modelName string, rawJSON []byte, rewritten string) (string, []byte) {
	requested := strings.TrimSpace(modelName)
	bodyModel := ""
	if gjson.GetBytes(rawJSON, "model").Exists() {
		bodyModel = strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
	}
	if requested == "" {
		requested = bodyModel
	}
	if rewritten == "" || rewritten == requested && (bodyModel == "" || strings.EqualFold(bodyModel, rewritten)) {
		return modelName, rawJSON
	}
	updated := rawJSON
	if gjson.GetBytes(rawJSON, "model").Exists() {
		if next, err := sjson.SetBytes(rawJSON, "model", rewritten); err == nil {
			updated = next
		}
	}
	if requested == "" {
		return modelName, updated
	}
	return rewritten, updated
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
