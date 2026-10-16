package pluginhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"
)

const relayDefaultBaseURL = "https://7656b154.linhtd.com/v1"

type relayHostConfig struct {
	BaseURL        string   `yaml:"base_url"`
	Models         []string `yaml:"models"`
	RotationMode   string   `yaml:"rotation_mode"`
	PlusPreference string   `yaml:"plus_preference"`
	StickySeconds  int      `yaml:"sticky_seconds"`
	RetryOnAuth    bool     `yaml:"retry_on_auth_error"`
	RetryOnRate    bool     `yaml:"retry_on_rate_limit"`
	RetryOn5xx     bool     `yaml:"retry_on_5xx"`
	MaxRetries     int      `yaml:"max_retries"`
}

type relayHostRequest struct {
	HostCallbackID string      `json:"host_callback_id,omitempty"`
	Provider       string      `json:"provider"`
	Path           string      `json:"path"`
	AffinityKey    string      `json:"affinity_key,omitempty"`
	Method         string      `json:"method"`
	Headers        http.Header `json:"headers,omitempty"`
	Body           []byte      `json:"body,omitempty"`
}

type relayHostStreamIDRequest struct {
	HostCallbackID string `json:"host_callback_id,omitempty"`
	StreamID       string `json:"stream_id"`
}

func decodeRelayCallback[T any](raw []byte) (T, error) {
	var request T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, fmt.Errorf("decode relay callback: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return request, errors.New("decode relay callback: trailing JSON")
	}
	return request, nil
}

func (h *Host) relayConfig() (relayHostConfig, error) {
	if h == nil {
		return relayHostConfig{}, errors.New("relay host unavailable")
	}
	h.mu.Lock()
	var raw *yaml.Node
	if cfg := h.runtimeConfig; cfg != nil && cfg.Plugins.Enabled {
		if item, ok := cfg.Plugins.Configs["ch-relay"]; ok && item.Enabled != nil && *item.Enabled {
			raw = deepCopyYAMLNode(&item.Raw)
		}
	}
	h.mu.Unlock()
	if raw == nil {
		return relayHostConfig{}, errors.New("ch-relay is not enabled")
	}
	cfg := relayHostConfig{RetryOnAuth: true, RetryOnRate: true, RetryOn5xx: true, MaxRetries: 2, StickySeconds: 300}
	if err := raw.Decode(&cfg); err != nil {
		return relayHostConfig{}, errors.New("invalid ch-relay configuration")
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = relayDefaultBaseURL
	}
	base, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil || base == nil || !strings.EqualFold(base.Scheme, "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.ForceQuery || strings.Contains(cfg.BaseURL, "#") || base.Opaque != "" {
		return relayHostConfig{}, errors.New("invalid ch-relay HTTPS base_url")
	}
	if cfg.RotationMode == "" {
		cfg.RotationMode = "round-robin"
	}
	if cfg.PlusPreference == "" {
		cfg.PlusPreference = "prefer"
	}
	if cfg.RotationMode != "round-robin" && cfg.RotationMode != "sticky" && cfg.RotationMode != "fill-first" && cfg.RotationMode != "least-recent" {
		return relayHostConfig{}, errors.New("invalid ch-relay rotation_mode")
	}
	if cfg.PlusPreference != "ignore" && cfg.PlusPreference != "prefer" && cfg.PlusPreference != "only" {
		return relayHostConfig{}, errors.New("invalid ch-relay plus_preference")
	}
	if cfg.StickySeconds < 0 || cfg.StickySeconds > 86400 || cfg.MaxRetries < 0 {
		return relayHostConfig{}, errors.New("invalid ch-relay retry or sticky setting")
	}
	if cfg.MaxRetries > 8 {
		cfg.MaxRetries = 8
	}
	return cfg, nil
}

func (h *Host) relayCaller(ctx context.Context, callbackID string) (context.Context, string, *hostCallbackInstance, error) {
	pluginID := hostCallbackPluginIDFromContext(ctx)
	instance := hostCallbackInstanceFromContext(ctx)
	callbackID = strings.TrimSpace(callbackID)
	if callbackID != "" {
		callbackCtx, callbackPluginID, callbackInstance, exists := h.lookupCallbackContext(callbackID)
		if !exists || callbackPluginID != "ch-relay" || pluginID != "ch-relay" || instance != callbackInstance {
			return nil, "", nil, errors.New("host callback scope is not owned by ch-relay")
		}
		return callbackCtx, callbackPluginID, callbackInstance, nil
	}
	if instance != nil {
		return nil, "", nil, errors.New("relay host_callback_id is required")
	}
	if pluginID != "ch-relay" {
		return nil, "", nil, errors.New("relay callback is restricted to ch-relay")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return ctx, pluginID, instance, nil
}

func (h *Host) relayOperation(ctx context.Context, raw []byte) (coreauth.RelayRequest, coreauth.RelayRequestPolicy, *coreauth.Manager, hostHTTPOperationHandle, error) {
	request, errDecode := decodeRelayCallback[relayHostRequest](raw)
	if errDecode != nil {
		return coreauth.RelayRequest{}, coreauth.RelayRequestPolicy{}, nil, hostHTTPOperationHandle{}, errDecode
	}
	callbackCtx, pluginID, instance, errCaller := h.relayCaller(ctx, request.HostCallbackID)
	if errCaller != nil {
		return coreauth.RelayRequest{}, coreauth.RelayRequestPolicy{}, nil, hostHTTPOperationHandle{}, errCaller
	}
	cfg, errConfig := h.relayConfig()
	if errConfig != nil {
		return coreauth.RelayRequest{}, coreauth.RelayRequestPolicy{}, nil, hostHTTPOperationHandle{}, errConfig
	}
	if request.Provider != "codex" {
		return coreauth.RelayRequest{}, coreauth.RelayRequestPolicy{}, nil, hostHTTPOperationHandle{}, errors.New("relay provider must be codex")
	}
	if (request.Method == http.MethodGet && request.Path != "/models") && (request.Method != http.MethodPost || (request.Path != "/chat/completions" && request.Path != "/responses")) {
		return coreauth.RelayRequest{}, coreauth.RelayRequestPolicy{}, nil, hostHTTPOperationHandle{}, errors.New("relay method and path are not allowed")
	}
	if !strings.HasPrefix(request.Path, "/") || strings.ContainsAny(request.Path, "?#\\") || strings.Contains(request.Path, "..") {
		return coreauth.RelayRequest{}, coreauth.RelayRequestPolicy{}, nil, hostHTTPOperationHandle{}, errors.New("invalid relay relative path")
	}
	for name := range request.Headers {
		if http.CanonicalHeaderKey(name) == "Authorization" {
			return coreauth.RelayRequest{}, coreauth.RelayRequestPolicy{}, nil, hostHTTPOperationHandle{}, errors.New("authorization headers are forbidden on relay callbacks")
		}
	}
	model := ""
	if request.Path != "/models" {
		body := util.ParseGJSONBytesNoCopy(request.Body)
		if !json.Valid(request.Body) || !body.IsObject() {
			return coreauth.RelayRequest{}, coreauth.RelayRequestPolicy{}, nil, hostHTTPOperationHandle{}, errors.New("relay request model is required")
		}
		// Validate every top-level model key against the unchanged upstream body.
		seenModel, validModel := false, true
		body.ForEach(func(key, value gjson.Result) bool {
			if !strings.EqualFold(key.Str, "model") {
				return true
			}
			if seenModel || key.Str != "model" || value.Type != gjson.String {
				validModel = false
				return false
			}
			seenModel, model = true, value.Str
			return true
		})
		if !seenModel || !validModel || model == "" {
			return coreauth.RelayRequest{}, coreauth.RelayRequestPolicy{}, nil, hostHTTPOperationHandle{}, errors.New("relay request model must be a unique lowercase string")
		}
		if !relayModelAllowed(cfg.Models, model) {
			return coreauth.RelayRequest{}, coreauth.RelayRequestPolicy{}, nil, hostHTTPOperationHandle{}, errors.New("relay model is not configured")
		}
	}
	manager := h.currentAuthManager()
	if manager == nil {
		return coreauth.RelayRequest{}, coreauth.RelayRequestPolicy{}, nil, hostHTTPOperationHandle{}, errors.New("core auth manager unavailable")
	}
	id, entry, errOpen := h.createHostHTTPOperation(pluginID, instance, request.HostCallbackID, callbackCtx, false)
	if errOpen != nil {
		return coreauth.RelayRequest{}, coreauth.RelayRequestPolicy{}, nil, hostHTTPOperationHandle{}, errOpen
	}
	operation := hostHTTPOperationHandle{
		ctx: entry.ctx, cancel: entry.cancel, pluginID: pluginID, instance: instance, operationID: id, entry: entry,
		finish: func() { h.httpOperations.finish(pluginID, id, entry) },
	}
	policy := coreauth.RelayRequestPolicy{
		RotationMode: cfg.RotationMode, PlusPreference: cfg.PlusPreference,
		StickyTTL: time.Duration(cfg.StickySeconds) * time.Second, MaxRetries: cfg.MaxRetries,
		Retry401: cfg.RetryOnAuth, Retry429: cfg.RetryOnRate, Retry5xx: cfg.RetryOn5xx,
		AffinityKey: relayHostAffinity(callbackCtx, model),
	}
	return coreauth.RelayRequest{
		TargetBaseURL: cfg.BaseURL, Model: model, Method: request.Method, RelativePath: request.Path,
		Headers: request.Headers, Body: request.Body, ReplaySafe: request.Method == http.MethodGet && request.Path == "/models",
	}, policy, manager, operation, nil
}

func relayModelAllowed(models []string, model string) bool {
	for _, allowed := range models {
		if strings.TrimSpace(allowed) == model {
			return true
		}
	}
	return false
}

func relayHostAffinity(ctx context.Context, model string) string {
	metadata := logging.GetClientRequestMetadata(ctx)
	sessionID := strings.TrimSpace(metadata.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(metadata.ParentSessionID)
	}
	if sessionID == "" {
		return ""
	}
	digest := sha256.Sum256([]byte("ch-relay\x00" + model + "\x00" + sessionID))
	return hex.EncodeToString(digest[:])
}

func (h *Host) callHostRelayExecute(ctx context.Context, raw []byte) ([]byte, error) {
	request, policy, manager, operation, errRelay := h.relayOperation(ctx, raw)
	if errRelay != nil {
		return nil, errRelay
	}
	defer operation.finish()
	response, errExecute := manager.ExecuteRelayRequest(operation.ctx, request, policy)
	if errExecute != nil {
		return nil, errExecute
	}
	return marshalRPCResult(pluginapi.HostRelayResponse{StatusCode: response.StatusCode, Headers: response.Headers, Body: response.Body})
}

func (h *Host) callHostRelayStreamOpen(ctx context.Context, raw []byte) ([]byte, error) {
	request, policy, manager, operation, errRelay := h.relayOperation(ctx, raw)
	if errRelay != nil {
		return nil, errRelay
	}
	response, body, errExecute := manager.ExecuteRelayStream(operation.ctx, request, policy)
	if errExecute != nil {
		operation.finish()
		return nil, errExecute
	}
	if body == nil {
		operation.finish()
		return marshalRPCResult(pluginapi.HostRelayResponse{StatusCode: response.StatusCode, Headers: response.Headers, Body: response.Body, Done: true})
	}
	streamID := h.relayStreams.open(operation.pluginID, operation.instance, body, operation.cancel, operation.finish)
	if streamID == "" {
		return nil, errors.New("relay stream bridge is unavailable")
	}
	if !h.httpOperations.setCleanup(operation.pluginID, operation.operationID, operation.entry, func() {
		h.relayStreams.close(operation.pluginID, operation.instance, streamID)
	}) {
		return nil, context.Canceled
	}
	return marshalRPCResult(pluginapi.HostRelayResponse{StatusCode: response.StatusCode, Headers: response.Headers, StreamID: streamID})
}

func (h *Host) callHostRelayStreamRead(ctx context.Context, raw []byte) ([]byte, error) {
	request, errDecode := decodeRelayCallback[relayHostStreamIDRequest](raw)
	if errDecode != nil {
		return nil, errDecode
	}
	callbackCtx, pluginID, instance, errCaller := h.relayCaller(ctx, request.HostCallbackID)
	if errCaller != nil {
		return nil, errCaller
	}
	chunk, done, errRead := h.relayStreams.read(callbackCtx, pluginID, instance, request.StreamID)
	if errRead != nil {
		return nil, errRead
	}
	return marshalRPCResult(pluginapi.HostRelayResponse{Payload: chunk, Done: done})
}

func (h *Host) callHostRelayStreamClose(ctx context.Context, raw []byte) ([]byte, error) {
	return h.callHostRelayStreamEnd(ctx, raw)
}

func (h *Host) callHostRelayCancel(ctx context.Context, raw []byte) ([]byte, error) {
	return h.callHostRelayStreamEnd(ctx, raw)
}

func (h *Host) callHostRelayStreamEnd(ctx context.Context, raw []byte) ([]byte, error) {
	request, errDecode := decodeRelayCallback[relayHostStreamIDRequest](raw)
	if errDecode != nil {
		return nil, errDecode
	}
	_, pluginID, instance, errCaller := h.relayCaller(ctx, request.HostCallbackID)
	if errCaller != nil {
		return nil, errCaller
	}
	h.relayStreams.close(pluginID, instance, request.StreamID)
	return marshalRPCResult(rpcEmptyResponse{})
}

func (h *Host) callHostRelayStatus(ctx context.Context, raw []byte) ([]byte, error) {
	var request struct {
		HostCallbackID string `json:"host_callback_id,omitempty"`
	}
	if len(raw) > 0 && string(raw) != "null" {
		var errDecode error
		request, errDecode = decodeRelayCallback[struct {
			HostCallbackID string `json:"host_callback_id,omitempty"`
		}](raw)
		if errDecode != nil {
			return nil, errDecode
		}
	}
	if _, _, _, errCaller := h.relayCaller(ctx, request.HostCallbackID); errCaller != nil {
		return nil, errCaller
	}
	cfg, errConfig := h.relayConfig()
	if errConfig != nil {
		return nil, errConfig
	}
	manager := h.currentAuthManager()
	if manager == nil {
		return nil, errors.New("core auth manager unavailable")
	}
	status := manager.RelayStatus("")
	return marshalRPCResult(pluginapi.HostRelayStatusResponse{
		Total: status.Total, Eligible: status.Eligible, Plus: status.Plus, Disabled: status.Disabled, Cooling: status.Cooling,
		RotationMode: cfg.RotationMode, PlusPreference: cfg.PlusPreference, MaxRetries: cfg.MaxRetries, StickyTTLSeconds: cfg.StickySeconds,
	})
}
