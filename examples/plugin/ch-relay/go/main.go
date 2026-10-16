package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);
typedef struct { uint32_t abi_version; void* host_ctx; cliproxy_host_call_fn call; cliproxy_host_free_fn free_buffer; } cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct { uint32_t abi_version; cliproxy_plugin_call_fn call; cliproxy_plugin_free_fn free_buffer; cliproxy_plugin_shutdown_fn shutdown; } cliproxy_plugin_api;
extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
static const cliproxy_host_api* stored_host;
static void store_host_api(const cliproxy_host_api* host) { stored_host = host; }
static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) return 1;
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}
static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) stored_host->free_buffer(ptr, len);
}
*/
import "C"

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	pluginID       = "ch-relay"
	pluginVersion  = "0.1.0"
	defaultBaseURL = "https://7656b154.linhtd.com/v1"
)

type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}
type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type relayConfig struct {
	Enabled        bool      `yaml:"enabled"`
	BaseURL        string    `yaml:"base_url"`
	Models         []string  `yaml:"models"`
	ClaimMode      string    `yaml:"claim_mode"`
	RotationMode   string    `yaml:"rotation_mode"`
	PlusPreference string    `yaml:"plus_preference"`
	StickySeconds  int       `yaml:"sticky_seconds"`
	RetryOnAuth    bool      `yaml:"retry_on_auth_error"`
	RetryOnRate    bool      `yaml:"retry_on_rate_limit"`
	RetryOn5xx     bool      `yaml:"retry_on_5xx"`
	MaxRetries     int       `yaml:"max_retries"`
	Priority       int       `yaml:"priority"`
	Store          yaml.Node `yaml:"store"`
}

type relayLifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}
type relayExecutorRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type relayStreamRPCResponse struct {
	Headers http.Header `json:"headers,omitempty"`
}
type relayManagementRequest struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type hostCaller func(string, []byte) (json.RawMessage, error)
type activeRelayStream struct {
	callbackID string
	relayID    string
	format     string
	pending    []byte
}
type relayPlugin struct {
	mu       sync.RWMutex
	config   relayConfig
	callback hostCaller
	closed   bool
	streams  map[string]*activeRelayStream
	wg       sync.WaitGroup
}

func defaultRelayConfig() relayConfig {
	return relayConfig{Enabled: true, BaseURL: defaultBaseURL, ClaimMode: "namespace", RotationMode: "round-robin", PlusPreference: "prefer", StickySeconds: 300, RetryOnAuth: true, RetryOnRate: true, RetryOn5xx: true, MaxRetries: 2}
}

func newRelayPlugin(callback hostCaller) *relayPlugin {
	return &relayPlugin{config: defaultRelayConfig(), callback: callback, streams: make(map[string]*activeRelayStream)}
}

func (p *relayPlugin) loadConfig(raw []byte) (relayConfig, error) {
	cfg := defaultRelayConfig()
	if len(raw) > 0 && string(raw) != "null" {
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		decoder.KnownFields(true)
		if err := decoder.Decode(&cfg); err != nil {
			return relayConfig{}, fmt.Errorf("decode ch-relay configuration: %w", err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return relayConfig{}, errors.New("configuration must contain one YAML document")
		}
	}
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base == nil || !strings.EqualFold(base.Scheme, "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.ForceQuery || strings.Contains(cfg.BaseURL, "#") || base.Opaque != "" {
		return relayConfig{}, errors.New("invalid ch-relay HTTPS base_url")
	}
	if cfg.ClaimMode == "" {
		cfg.ClaimMode = "namespace"
	}
	if cfg.ClaimMode != "configured-only" && cfg.ClaimMode != "namespace" {
		return relayConfig{}, errors.New("invalid ch-relay claim_mode")
	}
	if cfg.RotationMode == "" {
		cfg.RotationMode = "round-robin"
	}
	if cfg.RotationMode != "round-robin" && cfg.RotationMode != "fill-first" && cfg.RotationMode != "least-recent" && cfg.RotationMode != "sticky" {
		return relayConfig{}, errors.New("invalid ch-relay rotation_mode")
	}
	if cfg.PlusPreference == "" {
		cfg.PlusPreference = "prefer"
	}
	if cfg.PlusPreference != "ignore" && cfg.PlusPreference != "prefer" && cfg.PlusPreference != "only" {
		return relayConfig{}, errors.New("invalid ch-relay plus_preference")
	}
	if cfg.StickySeconds < 0 || cfg.StickySeconds > 86400 || cfg.MaxRetries < 0 || cfg.MaxRetries > 8 {
		return relayConfig{}, errors.New("invalid ch-relay retry or sticky setting")
	}
	seen := make(map[string]struct{}, len(cfg.Models))
	for i, model := range cfg.Models {
		model = strings.TrimSpace(model)
		if model == "" || strings.HasPrefix(model, pluginID+"/") {
			return relayConfig{}, fmt.Errorf("invalid ch-relay models[%d]", i)
		}
		if _, ok := seen[model]; ok {
			return relayConfig{}, fmt.Errorf("duplicate ch-relay model %q", model)
		}
		seen[model] = struct{}{}
		cfg.Models[i] = model
	}
	return cfg, nil
}
func (p *relayPlugin) configure(raw []byte) error {
	var req relayLifecycleRequest
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &req); err != nil {
			return fmt.Errorf("decode lifecycle request: %w", err)
		}
	}
	cfg, err := p.loadConfig(req.ConfigYAML)
	if err != nil {
		return err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("plugin is shut down")
	}
	p.config = cfg
	p.mu.Unlock()
	return nil
}

func (p *relayPlugin) snapshot() relayConfig { p.mu.RLock(); defer p.mu.RUnlock(); return p.config }
func (p *relayPlugin) modelID(model string) string {
	if p.snapshot().ClaimMode == "namespace" {
		return pluginID + "/" + model
	}
	return model
}
func (p *relayPlugin) nativeModel(requested string) (string, bool) {
	requested = strings.TrimSpace(requested)
	cfg := p.snapshot()
	for _, model := range cfg.Models {
		if cfg.ClaimMode == "configured-only" && requested == model {
			return model, true
		}
		if cfg.ClaimMode == "namespace" && requested == pluginID+"/"+model {
			return model, true
		}
	}
	return "", false
}
func (p *relayPlugin) models() []pluginapi.ModelInfo {
	cfg := p.snapshot()
	if !cfg.Enabled || len(cfg.Models) == 0 {
		return nil
	}
	models := make([]pluginapi.ModelInfo, 0, len(cfg.Models))
	for _, name := range cfg.Models {
		models = append(models, pluginapi.ModelInfo{ID: p.modelID(name), Object: "model", OwnedBy: pluginID, Name: name, DisplayName: name, Description: "Codex relay model", SupportedGenerationMethods: []string{"chat", "responses"}, SupportedInputModalities: []string{"text"}, SupportedOutputModalities: []string{"text"}, UserDefined: true})
	}
	return models
}

func pluginMetadata() pluginapi.Metadata {
	return pluginapi.Metadata{Name: pluginID, Version: pluginVersion, Author: "router-for-me", GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI", ConfigFields: []pluginapi.ConfigField{
		{Name: "base_url", Type: pluginapi.ConfigFieldTypeString, Description: "HTTPS destination (default https://7656b154.linhtd.com/v1). WARNING: core sends borrowed Codex access tokens to this destination; configure only a relay you trust. No API key is needed."},
		{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "Configured upstream model names this plugin may claim; empty claims nothing."},
		{Name: "claim_mode", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"configured-only", "namespace"}, Description: "Use configured names directly, or expose them as ch-relay/<model> (default)."},
		{Name: "rotation_mode", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"round-robin", "fill-first", "least-recent", "sticky"}, Description: "Host-side Codex account rotation policy."},
		{Name: "plus_preference", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"ignore", "prefer", "only"}, Description: "Host-side Codex Plus account preference."},
		{Name: "sticky_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Host-side sticky account TTL, 0–86400 seconds; default 300. Affinity comes from the caller session."},
		{Name: "retry_on_auth_error", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Allow host-side retries on 401 for replay-safe GET /models only. POST chat/responses are never replayed once sent."},
		{Name: "retry_on_rate_limit", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Allow host-side retries on 429 for replay-safe GET /models only. POST chat/responses are never replayed once sent."},
		{Name: "retry_on_5xx", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Allow host-side retries on 5xx for replay-safe GET /models only. POST chat/responses are never replayed once sent."},
		{Name: "max_retries", Type: pluginapi.ConfigFieldTypeInteger, Description: "Maximum host-side retries for replay-safe GET /models, 0–8; default 2. Does not enable POST replay; no retry state is stored in the plugin."},
	}}
}

type registrationCapabilities struct {
	ModelRegistrar        bool                         `json:"model_registrar"`
	ModelProvider         bool                         `json:"model_provider"`
	ModelRouter           bool                         `json:"model_router"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats"`
	ManagementAPI         bool                         `json:"management_api"`
}
type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

func (p *relayPlugin) registration() registration {
	return registration{SchemaVersion: pluginabi.SchemaVersion, Metadata: pluginMetadata(), Capabilities: registrationCapabilities{ModelRegistrar: true, ModelProvider: true, ModelRouter: true, Executor: true, ExecutorModelScope: pluginapi.ExecutorModelScopeStatic, ExecutorInputFormats: []string{"chat-completions", "openai-response"}, ExecutorOutputFormats: []string{"chat-completions", "openai-response"}, ManagementAPI: true}}
}

func (p *relayPlugin) call(method string, raw []byte) []byte {
	var result any
	var err error
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		err = p.configure(raw)
		if err == nil {
			result = p.registration()
		}
	case pluginabi.MethodModelRegister:
		result = pluginapi.ModelRegistrationResponse{Provider: pluginID, Models: p.models()}
	case pluginabi.MethodModelStatic:
		result = pluginapi.ModelResponse{Provider: pluginID, Models: p.models()}
	case pluginabi.MethodModelForAuth:
		result = pluginapi.ModelResponse{Provider: pluginID}
	case pluginabi.MethodModelRoute:
		var req pluginapi.ModelRouteRequest
		err = json.Unmarshal(raw, &req)
		if err == nil {
			result = p.route(req)
		}
	case pluginabi.MethodExecutorIdentifier:
		result = map[string]string{"identifier": pluginID}
	case pluginabi.MethodExecutorExecute:
		var req relayExecutorRequest
		err = json.Unmarshal(raw, &req)
		if err == nil {
			result, err = p.execute(req)
		}
	case pluginabi.MethodExecutorExecuteStream:
		var req relayExecutorRequest
		err = json.Unmarshal(raw, &req)
		if err == nil {
			result, err = p.executeStream(req)
		}
	case pluginabi.MethodManagementRegister:
		result = pluginapi.ManagementRegistrationResponse{Resources: []pluginapi.ResourceRoute{{Path: "/status", Menu: "Codex Relay", Description: "Aggregate Codex relay status."}}}
	case pluginabi.MethodManagementHandle:
		var req relayManagementRequest
		err = json.Unmarshal(raw, &req)
		if err == nil {
			result, err = p.management(req)
		}
	case pluginabi.MethodPluginQuiesce:
		result = struct{}{}
	default:
		return marshalError("unknown_method", "unknown method: "+method, 0)
	}
	if err != nil {
		return marshalPluginError(err)
	}
	return marshalOK(result)
}

func (p *relayPlugin) route(req pluginapi.ModelRouteRequest) pluginapi.ModelRouteResponse {
	if !p.snapshot().Enabled {
		return pluginapi.ModelRouteResponse{}
	}
	if _, ok := p.nativeModel(req.RequestedModel); !ok {
		return pluginapi.ModelRouteResponse{}
	}
	return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf, Reason: "configured ch-relay model"}
}

func normalizeFormat(req relayExecutorRequest) string {
	format := strings.ToLower(strings.TrimSpace(req.SourceFormat))
	if format == "" {
		format = strings.ToLower(strings.TrimSpace(req.Format))
	}
	if format == "responses" || format == "openai-response" || format == "openai_responses" || format == "openai-responses" {
		return "responses"
	}
	return "chat-completions"
}
func topLevelObject(raw []byte) (map[string]json.RawMessage, int, int, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, 0, 0, errors.New("request payload must be a JSON object")
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, 0, 0, errors.New("request payload must be a JSON object")
	}
	out := make(map[string]json.RawMessage)
	modelStart, modelEnd := 0, 0
	for dec.More() {
		tok, err = dec.Token()
		if err != nil {
			return nil, 0, 0, errors.New("invalid request payload")
		}
		key, ok := tok.(string)
		if !ok {
			return nil, 0, 0, errors.New("invalid request payload key")
		}
		if strings.EqualFold(key, "model") && key != "model" {
			return nil, 0, 0, errors.New("request payload model key must be lowercase")
		}
		if _, exists := out[key]; exists {
			return nil, 0, 0, fmt.Errorf("duplicate request payload key %q", key)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, 0, 0, errors.New("invalid request payload")
		}
		out[key] = value
		if key == "model" {
			modelEnd = int(dec.InputOffset())
			modelStart = modelEnd - len(value)
		}
	}
	if _, err = dec.Token(); err != nil {
		return nil, 0, 0, errors.New("invalid request payload")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, 0, 0, errors.New("trailing request payload")
	}
	return out, modelStart, modelEnd, nil
}
func (p *relayPlugin) requestBody(req relayExecutorRequest) ([]byte, string, error) {
	if !p.snapshot().Enabled {
		return nil, "", errors.New("ch-relay is disabled")
	}
	requested := strings.TrimSpace(req.Model)
	model, ok := p.nativeModel(requested)
	if !ok {
		return nil, "", fmt.Errorf("model %q is not configured for ch-relay", requested)
	}
	fields, modelStart, modelEnd, err := topLevelObject(req.Payload)
	if err != nil {
		return nil, "", err
	}
	rawModel, ok := fields["model"]
	if !ok {
		return nil, "", errors.New("request payload model is required")
	}
	var payloadModel string
	if err := json.Unmarshal(rawModel, &payloadModel); err != nil || strings.TrimSpace(payloadModel) == "" {
		return nil, "", errors.New("request payload model must be a string")
	}
	if payloadModel != requested && payloadModel != model {
		return nil, "", errors.New("request payload model does not match configured model")
	}
	if payloadModel == model {
		return req.Payload, model, nil
	}
	replacement, err := json.Marshal(model)
	if err != nil {
		return nil, "", err
	}
	body := make([]byte, 0, len(req.Payload)-(modelEnd-modelStart)+len(replacement))
	body = append(body, req.Payload[:modelStart]...)
	body = append(body, replacement...)
	body = append(body, req.Payload[modelEnd:]...)
	return body, model, nil
}
func safeHeaders(in http.Header) http.Header {
	out := make(http.Header)
	if value := in.Get("Content-Type"); value != "" {
		out.Set("Content-Type", value)
	} else {
		out.Set("Content-Type", "application/json")
	}
	return out
}
func (p *relayPlugin) relay(req relayExecutorRequest, path string, body []byte) (pluginapi.HostRelayResponse, error) {
	if strings.TrimSpace(req.HostCallbackID) == "" {
		return pluginapi.HostRelayResponse{}, errors.New("host_callback_id is required")
	}
	request := pluginapi.HostRelayExecuteRequest{HostCallbackID: req.HostCallbackID, Provider: "codex", Path: path, Method: http.MethodPost, Headers: safeHeaders(req.Headers), Body: body}
	result, err := p.hostCall(pluginabi.MethodHostRelayExecute, request)
	if err != nil {
		return pluginapi.HostRelayResponse{}, err
	}
	var response pluginapi.HostRelayResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return pluginapi.HostRelayResponse{}, fmt.Errorf("decode relay response: %w", err)
	}
	if response.StatusCode >= 400 {
		return response, &relayHTTPError{status: response.StatusCode, body: string(response.Body)}
	}
	return response, nil
}
func (p *relayPlugin) execute(req relayExecutorRequest) (pluginapi.ExecutorResponse, error) {
	body, _, err := p.requestBody(req)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	path := "/chat/completions"
	if normalizeFormat(req) == "responses" {
		path = "/responses"
	}
	response, err := p.relay(req, path, body)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	return pluginapi.ExecutorResponse{Payload: response.Body, Headers: response.Headers}, nil
}

type relayHTTPError struct {
	status int
	body   string
}

func (e *relayHTTPError) Error() string   { return e.body }
func (e *relayHTTPError) StatusCode() int { return e.status }

func (p *relayPlugin) executeStream(req relayExecutorRequest) (relayStreamRPCResponse, error) {
	if strings.TrimSpace(req.StreamID) == "" {
		return relayStreamRPCResponse{}, errors.New("stream_id is required")
	}
	body, _, err := p.requestBody(req)
	if err != nil {
		return relayStreamRPCResponse{}, err
	}
	if strings.TrimSpace(req.HostCallbackID) == "" {
		return relayStreamRPCResponse{}, errors.New("host_callback_id is required")
	}
	path := "/chat/completions"
	if normalizeFormat(req) == "responses" {
		path = "/responses"
	}
	openReq := pluginapi.HostRelayStreamRequest{HostCallbackID: req.HostCallbackID, Provider: "codex", Path: path, Method: http.MethodPost, Headers: safeHeaders(req.Headers), Body: body}
	result, err := p.hostCall(pluginabi.MethodHostRelayStreamOpen, openReq)
	if err != nil {
		return relayStreamRPCResponse{}, err
	}
	var opened pluginapi.HostRelayResponse
	if err := json.Unmarshal(result, &opened); err != nil {
		return relayStreamRPCResponse{}, fmt.Errorf("decode relay stream response: %w", err)
	}
	if opened.StatusCode >= 400 {
		return relayStreamRPCResponse{}, &relayHTTPError{status: opened.StatusCode, body: string(opened.Body)}
	}
	if opened.Done || strings.TrimSpace(opened.StreamID) == "" {
		return relayStreamRPCResponse{}, errors.New("relay stream did not return stream_id")
	}
	stream := &activeRelayStream{callbackID: req.HostCallbackID, relayID: opened.StreamID, format: normalizeFormat(req)}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_, _ = p.hostCallWhileClosed(pluginabi.MethodHostRelayCancel, pluginapi.HostRelayCancelRequest{HostCallbackID: req.HostCallbackID, StreamID: opened.StreamID})
		return relayStreamRPCResponse{}, errors.New("plugin is shut down")
	}
	p.streams[req.StreamID] = stream
	p.wg.Add(1)
	p.mu.Unlock()
	go p.forwardStream(req.StreamID, stream)
	return relayStreamRPCResponse{Headers: opened.Headers}, nil
}
func normalizeRelayStreamPayload(payload []byte, format string) [][]byte {
	var pending []byte
	return normalizeRelayStreamPayloadBuffered(&pending, payload, format, true)
}

func normalizeRelayStreamPayloadBuffered(pending *[]byte, payload []byte, format string, final bool) [][]byte {
	if format == "responses" {
		buffer := append([]byte(nil), payload...)
		if pending != nil {
			buffer = append(append([]byte(nil), (*pending)...), payload...)
		}
		frames, remainder := splitCompleteRawSSEFrames(buffer)
		if pending != nil {
			if final {
				if len(remainder) > 0 {
					frames = append(frames, remainder)
				}
				*pending = (*pending)[:0]
			} else {
				*pending = append((*pending)[:0], remainder...)
			}
		}
		return frames
	}
	if pending == nil {
		return normalizeChatRelayStreamPayload(payload)
	}
	buffer := append(append([]byte(nil), (*pending)...), payload...)
	buffer = bytes.ReplaceAll(buffer, []byte("\r\n"), []byte("\n"))
	frames := make([][]byte, 0)
	for {
		index := bytes.Index(buffer, []byte("\n\n"))
		if index < 0 {
			break
		}
		frames = append(frames, normalizeChatRelayFrame(buffer[:index]))
		buffer = buffer[index+2:]
	}
	if final && len(bytes.TrimSpace(buffer)) > 0 {
		frames = append(frames, normalizeChatRelayFrame(buffer))
		buffer = nil
	}
	*pending = append((*pending)[:0], buffer...)
	return compactRelayFrames(frames)
}

func splitCompleteRawSSEFrames(payload []byte) ([][]byte, []byte) {
	frames := make([][]byte, 0)
	for {
		lf := bytes.Index(payload, []byte("\n\n"))
		crlf := bytes.Index(payload, []byte("\r\n\r\n"))
		index, separator := lf, 2
		if crlf >= 0 && (index < 0 || crlf < index) {
			index, separator = crlf, 4
		}
		if index < 0 {
			return frames, bytes.Clone(payload)
		}
		end := index + separator
		frames = append(frames, bytes.Clone(payload[:end]))
		payload = payload[end:]
	}
}

func splitRawSSEFrames(payload []byte) [][]byte {
	frames, remainder := splitCompleteRawSSEFrames(payload)
	if len(remainder) > 0 {
		frames = append(frames, remainder)
	}
	return frames
}

func normalizeChatRelayStreamPayload(payload []byte) [][]byte {
	frames := make([][]byte, 0)
	for _, raw := range splitRawSSEFrames(payload) {
		frames = append(frames, normalizeChatRelayFrame(raw))
	}
	return compactRelayFrames(frames)
}

func normalizeChatRelayFrame(raw []byte) []byte {
	var data []byte
	for _, line := range bytes.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == '\r' }) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || bytes.HasPrefix(line, []byte(":")) || bytes.HasPrefix(line, []byte("event:")) {
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		}
		data = append(data, line...)
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return nil
	}
	return bytes.Clone(data)
}

func compactRelayFrames(frames [][]byte) [][]byte {
	out := frames[:0]
	for _, frame := range frames {
		if len(frame) > 0 {
			out = append(out, frame)
		}
	}
	return out
}

func (p *relayPlugin) forwardStream(pluginStreamID string, stream *activeRelayStream) {
	defer p.wg.Done()
	defer func() { p.mu.Lock(); delete(p.streams, pluginStreamID); p.mu.Unlock() }()
	for {
		request := pluginapi.HostRelayStreamReadRequest{HostCallbackID: stream.callbackID, StreamID: stream.relayID}
		result, err := p.hostCallWhileClosed(pluginabi.MethodHostRelayStreamRead, request)
		if err != nil {
			p.failStream(pluginStreamID, stream, err.Error())
			return
		}
		var chunk pluginapi.HostRelayResponse
		if err := json.Unmarshal(result, &chunk); err != nil {
			p.failStream(pluginStreamID, stream, err.Error())
			return
		}
		if chunk.Error != "" {
			p.failStream(pluginStreamID, stream, chunk.Error)
			return
		}
		if len(chunk.Payload) > 0 || chunk.Done {
			payloads := normalizeRelayStreamPayloadBuffered(&stream.pending, chunk.Payload, stream.format, chunk.Done)
			for _, payload := range payloads {
				event := map[string]any{"stream_id": pluginStreamID, "payload": payload}
				if _, err := p.hostCallWhileClosed(pluginabi.MethodHostStreamEmit, event); err != nil {
					p.failStream(pluginStreamID, stream, err.Error())
					return
				}
			}
		}
		if chunk.Done {
			p.finishStream(pluginStreamID, stream, "")
			return
		}
	}
}
func (p *relayPlugin) finishStream(pluginStreamID string, stream *activeRelayStream, message string) {
	_, _ = p.hostCallWhileClosed(pluginabi.MethodHostRelayStreamClose, pluginapi.HostRelayStreamCloseRequest{HostCallbackID: stream.callbackID, StreamID: stream.relayID})
	_, _ = p.hostCallWhileClosed(pluginabi.MethodHostStreamClose, map[string]any{"stream_id": pluginStreamID, "error": message})
}
func (p *relayPlugin) failStream(pluginStreamID string, stream *activeRelayStream, message string) {
	_, _ = p.hostCallWhileClosed(pluginabi.MethodHostRelayCancel, pluginapi.HostRelayCancelRequest{HostCallbackID: stream.callbackID, StreamID: stream.relayID})
	_, _ = p.hostCallWhileClosed(pluginabi.MethodHostRelayStreamClose, pluginapi.HostRelayStreamCloseRequest{HostCallbackID: stream.callbackID, StreamID: stream.relayID})
	_, _ = p.hostCallWhileClosed(pluginabi.MethodHostStreamClose, map[string]any{"stream_id": pluginStreamID, "error": message})
}

func (p *relayPlugin) management(req relayManagementRequest) (pluginapi.ManagementResponse, error) {
	if req.Path != "/status" && req.Path != "/v0/resource/plugins/ch-relay/status" {
		return pluginapi.ManagementResponse{StatusCode: http.StatusNotFound, Headers: http.Header{"Content-Type": {"text/html; charset=utf-8"}}, Body: []byte("<!doctype html><title>Not found</title>")}, nil
	}
	if strings.TrimSpace(req.HostCallbackID) == "" {
		return pluginapi.ManagementResponse{}, errors.New("host_callback_id is required")
	}
	result, err := p.hostCall(pluginabi.MethodHostRelayStatus, struct {
		HostCallbackID string `json:"host_callback_id"`
	}{req.HostCallbackID})
	if err != nil {
		return pluginapi.ManagementResponse{}, err
	}
	var status pluginapi.HostRelayStatusResponse
	if err := json.Unmarshal(result, &status); err != nil {
		return pluginapi.ManagementResponse{}, err
	}
	body := fmt.Sprintf("<!doctype html><html><head><meta charset=\"utf-8\"><title>Codex Relay</title><style>body{font:16px system-ui;max-width:52rem;margin:2rem auto;padding:0 1rem;background:#10131a;color:#e6e8ef}main{background:#1a1f2b;border:1px solid #30384a;border-radius:12px;padding:1.5rem}dt{color:#9da9c4}dd{margin:0 0 .8rem;font-weight:600}</style></head><body><main><h1>Codex Relay</h1><dl><dt>Total</dt><dd>%d</dd><dt>Eligible</dt><dd>%d</dd><dt>Plus</dt><dd>%d</dd><dt>Disabled</dt><dd>%d</dd><dt>Cooling</dt><dd>%d</dd><dt>Rotation</dt><dd>%s</dd><dt>Plus preference</dt><dd>%s</dd><dt>Retries</dt><dd>%d</dd><dt>Sticky TTL</dt><dd>%d seconds</dd></dl></main></body></html>", status.Total, status.Eligible, status.Plus, status.Disabled, status.Cooling, html.EscapeString(status.RotationMode), html.EscapeString(status.PlusPreference), status.MaxRetries, status.StickyTTLSeconds)
	return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": {"text/html; charset=utf-8"}}, Body: []byte(body)}, nil
}

func (p *relayPlugin) hostCall(method string, request any) (json.RawMessage, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	p.mu.RLock()
	callback := p.callback
	closed := p.closed
	p.mu.RUnlock()
	if closed {
		return nil, errors.New("plugin is shut down")
	}
	if callback == nil {
		return callCgoHost(method, raw)
	}
	return callback(method, raw)
}
func (p *relayPlugin) shutdown() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		p.wg.Wait()
		return
	}
	p.closed = true
	streams := make([]*activeRelayStream, 0, len(p.streams))
	for _, stream := range p.streams {
		streams = append(streams, stream)
	}
	p.mu.Unlock()
	for _, stream := range streams {
		_, _ = p.hostCallWhileClosed(pluginabi.MethodHostRelayCancel, pluginapi.HostRelayCancelRequest{HostCallbackID: stream.callbackID, StreamID: stream.relayID})
		_, _ = p.hostCallWhileClosed(pluginabi.MethodHostRelayStreamClose, pluginapi.HostRelayStreamCloseRequest{HostCallbackID: stream.callbackID, StreamID: stream.relayID})
	}
	p.wg.Wait()
}
func (p *relayPlugin) hostCallWhileClosed(method string, request any) (json.RawMessage, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	p.mu.RLock()
	callback := p.callback
	p.mu.RUnlock()
	if callback == nil {
		return callCgoHost(method, raw)
	}
	return callback(method, raw)
}

var defaultPlugin = newRelayPlugin(nil)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, marshalError("invalid_method", "method is required", 0))
		return 1
	}
	var raw []byte
	if request != nil && requestLen > 0 {
		raw = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	writeResponse(response, defaultPlugin.call(C.GoString(method), raw))
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() { defaultPlugin.shutdown() }

func callCgoHost(method string, raw []byte) (json.RawMessage, error) {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var request *C.uint8_t
	if len(raw) > 0 {
		ptr := C.CBytes(raw)
		if ptr == nil {
			return nil, errors.New("allocate host callback request")
		}
		defer C.free(ptr)
		request = (*C.uint8_t)(ptr)
	}
	var response C.cliproxy_buffer
	code := C.call_host_api(cMethod, request, C.size_t(len(raw)), &response)
	var result []byte
	if response.ptr != nil && response.len > 0 {
		result = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("host callback %s returned no response, code=%d", method, int(code))
	}
	var env pluginabi.Envelope
	if err := json.Unmarshal(result, &env); err != nil {
		return nil, fmt.Errorf("decode host callback %s: %w", method, err)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, env.Error
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	if code != 0 {
		return nil, fmt.Errorf("host callback %s returned code=%d", method, int(code))
	}
	return append(json.RawMessage(nil), env.Result...), nil
}
func marshalOK(result any) []byte {
	raw, err := json.Marshal(result)
	if err != nil {
		return marshalError("encode_result", err.Error(), 0)
	}
	out, _ := json.Marshal(envelope{OK: true, Result: raw})
	return out
}
func marshalError(code, message string, status int) []byte {
	out, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message, HTTPStatus: status}})
	return out
}
func marshalPluginError(err error) []byte {
	status := 0
	code := "plugin_error"
	var se interface{ StatusCode() int }
	if errors.As(err, &se) && se != nil {
		status = se.StatusCode()
	}
	var pe *pluginabi.Error
	if errors.As(err, &pe) && pe != nil {
		code = pe.Code
		status = pe.HTTPStatus
	}
	message := err.Error()
	return marshalError(code, message, status)
}
func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
