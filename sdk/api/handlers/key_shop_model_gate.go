package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelmapping"
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
// Limited is true only when the key's group has a model list. A missing field from an
// old plugin keeps the legacy mapped path, so the host never guesses all-models.
type KeyShopResolveResult struct {
	Loaded  bool
	Mode    string
	Allowed bool
	Limited bool
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
	mu            sync.Mutex
	evaluated     bool
	authoritative bool
	secret        string
	decision      keyShopGateDecision
	noted         bool
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

// resolveKeyShopRequestModel applies the key-shop group decision before the global
// model mapping for a grouped ck_ key. A limited group owns the client-visible name,
// so routing.model-mapping must not rewrite it. Legacy keys, all-models groups,
// groups that do not admit the raw name, and a missing plugin keep the legacy order:
// mapping first, then the gate on the mapped name. The returned mapping result is
// zero on the authoritative group path so response echo keeps the client model.
func (h *BaseAPIHandler) resolveKeyShopRequestModel(ctx context.Context, modelName string, rawJSON []byte) (context.Context, string, []byte, modelmapping.Result, *interfaces.ErrorMessage) {
	secret := keyShopSecret(headersFromContext(ctx))
	if !strings.HasPrefix(secret, "ck_") {
		mappedName, mappedBody, result := h.applyConfiguredModelMapping(modelName, rawJSON)
		return ctx, mappedName, mappedBody, result, nil
	}
	state, ctx := keyShopGateStateFrom(ctx)
	state.mu.Lock()
	defer state.mu.Unlock()
	var probe keyShopRawResolution
	if !state.evaluated {
		state.secret = secret
		probe = h.resolveKeyShopRaw(ctx, secret, modelName, rawJSON)
		if decision, authoritative := probe.authoritative(modelName, rawJSON); authoritative {
			state.decision = decision
			state.authoritative = true
			state.evaluated = true
			nextName, nextBody, errMsg := decision.apply(modelName, rawJSON)
			return ctx, nextName, nextBody, modelmapping.Result{}, errMsg
		}
	}
	if state.evaluated && state.authoritative {
		nextName, nextBody, errMsg := state.decision.apply(modelName, rawJSON)
		return ctx, nextName, nextBody, modelmapping.Result{}, errMsg
	}
	mappedName, mappedBody, result := h.applyConfiguredModelMapping(modelName, rawJSON)
	if !state.evaluated {
		state.decision = h.evaluateKeyShopMappedDecision(ctx, secret, modelName, rawJSON, mappedName, mappedBody, probe)
		state.evaluated = true
	}
	nextName, nextBody, errMsg := state.decision.apply(mappedName, mappedBody)
	return ctx, nextName, nextBody, result, errMsg
}

// keyShopRawResolution is the plugin's answer for the raw client model.
type keyShopRawResolution struct {
	resolved KeyShopResolveResult
	ok       bool
}

func (h *BaseAPIHandler) resolveKeyShopRaw(ctx context.Context, secret, modelName string, rawJSON []byte) keyShopRawResolution {
	resolver, ok := h.interceptorHost().(keyShopModelResolver)
	if !ok || resolver == nil {
		return keyShopRawResolution{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	resolved, err := resolver.KeyShopResolveModel(ctx, secret, modelName, keyShopBodyModel(rawJSON))
	if err != nil {
		return keyShopRawResolution{}
	}
	return keyShopRawResolution{resolved: resolved, ok: true}
}

// authoritative reports that a limited group admitted the raw client model, so its
// rewrite (or keep) wins over the global mapping for this request. Limited must be
// set explicitly: an old plugin omits it, and its all-models reply must stay on the
// legacy mapped path.
func (p keyShopRawResolution) authoritative(modelName string, rawJSON []byte) (keyShopGateDecision, bool) {
	if !p.ok || !p.resolved.Loaded || !p.resolved.Limited || !strings.EqualFold(strings.TrimSpace(p.resolved.Mode), "group") || !p.resolved.Allowed {
		return keyShopGateDecision{}, false
	}
	if keyShopResolvedModelUnchanged(modelName, keyShopBodyModel(rawJSON), p.resolved.Model) {
		return keyShopGateDecision{kind: "keep"}, true
	}
	return keyShopGateDecision{kind: "rewrite", model: p.resolved.Model}, true
}

// evaluateKeyShopMappedDecision keeps the legacy mapped-name gate for the fallback
// paths. It reuses the raw probe where the answer is already known so a plain
// denial still costs one resolve_model call per request.
func (h *BaseAPIHandler) evaluateKeyShopMappedDecision(ctx context.Context, secret, rawModel string, rawJSON []byte, mappedModel string, mappedBody []byte, probe keyShopRawResolution) keyShopGateDecision {
	if probe.ok && probe.resolved.Loaded && strings.EqualFold(strings.TrimSpace(probe.resolved.Mode), "group") {
		if !probe.resolved.Allowed && keyShopSameModelRequest(rawModel, rawJSON, mappedModel, mappedBody) {
			return keyShopGateDecision{kind: "deny"}
		}
	}
	return h.evaluateKeyShopGate(ctx, secret, mappedModel, mappedBody)
}

func keyShopSameModelRequest(rawModel string, rawJSON []byte, mappedModel string, mappedBody []byte) bool {
	return strings.EqualFold(strings.TrimSpace(rawModel), strings.TrimSpace(mappedModel)) &&
		strings.EqualFold(keyShopBodyModel(rawJSON), keyShopBodyModel(mappedBody))
}

func keyShopBodyModel(rawJSON []byte) string {
	if len(rawJSON) == 0 || !gjson.GetBytes(rawJSON, "model").Exists() {
		return ""
	}
	return strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
}

// keyShopAuthoritativeFromContext reports that the cached decision was made on the
// raw client model and must not be re-mapped.
func keyShopAuthoritativeFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	state, ok := ctx.Value(keyShopGateStateKey{}).(*keyShopGateState)
	if !ok || state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.authoritative
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
	bodyModel := keyShopBodyModel(rawJSON)
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
