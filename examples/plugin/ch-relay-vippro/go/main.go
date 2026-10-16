package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	oaichat "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/openai/chat-completions"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	pluginID                    = "ch-relay-vippro"
	defaultBaseURL              = "https://7656b154.linhtd.com/v1"
	defaultRetry                = 3
	defaultStickySeconds        = 300
	defaultModelCooldownMinutes = 1440
	maxExtraRetries             = 32
	maxModelCooldownMinutes     = 10080
	maxFallbackRetries          = 3
	maxStickySeconds            = 86400
	redactedToken               = "[REDACTED]"
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type pluginConfig struct {
	Enabled              bool              `yaml:"enabled"`
	Priority             int               `yaml:"priority"`
	BaseURL              string            `yaml:"base-url"`
	Prefix               string            `yaml:"prefix"`
	Headers              map[string]string `yaml:"headers"`
	RequestRetry         *int              `yaml:"request-retry"`
	FallbackRetry        *int              `yaml:"fallback-retry"`
	RotationMode         string            `yaml:"rotation-mode"`
	PlusPreference       string            `yaml:"plus-preference"`
	StickySeconds        *int              `yaml:"sticky-seconds"`
	ModelCooldownMinutes *int              `yaml:"model-cooldown-minutes"`
	StatsFile            string            `yaml:"stats-file"`
	UpstreamCache        bool              `yaml:"upstream-cache"`
	Models               []modelSpec       `yaml:"models"`
}

type modelSpec struct {
	Name                   string         `yaml:"name"`
	Alias                  string         `yaml:"alias,omitempty"`
	DisplayName            string         `yaml:"display-name,omitempty"`
	MaxContextLength       int            `yaml:"max-context-length,omitempty"`
	ForceMapping           bool           `yaml:"force-mapping,omitempty"`
	Image                  bool           `yaml:"image,omitempty"`
	InputModalities        []string       `yaml:"input-modalities,omitempty"`
	OutputModalities       []string       `yaml:"output-modalities,omitempty"`
	IsCompat               bool           `yaml:"is-compat,omitempty"`
	UseMaxCompletionTokens bool           `yaml:"use-max-completion-tokens,omitempty"`
	Thinking               map[string]any `yaml:"thinking,omitempty"`
	Fallback               []string       `yaml:"fallback,omitempty"`
}

type registration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  registrationCaps   `json:"capabilities"`
}

type registrationCaps struct {
	ModelRegistrar        bool     `json:"model_registrar"`
	ModelProvider         bool     `json:"model_provider"`
	ModelRouter           bool     `json:"model_router"`
	Executor              bool     `json:"executor"`
	ExecutorModelScope    string   `json:"executor_model_scope"`
	ExecutorInputFormats  []string `json:"executor_input_formats"`
	ExecutorOutputFormats []string `json:"executor_output_formats"`
	ManagementAPI         bool     `json:"management_api"`
}

type executorCall struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type routeCall struct {
	pluginapi.ModelRouteRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type managementCall struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type modelPool struct {
	alias     string
	clientID  string
	names     []string
	fallbacks [][]string
	display   string
}

type account struct {
	index    string
	id       string
	priority int
	updated  time.Time
	plus     bool
	token    string
	email    string
}

type attempt struct {
	index     string
	token     string
	email     string
	model     string
	fallbacks []string
}

type stickyBind struct {
	authIndex string
	until     time.Time
}

type runtimeState struct {
	cfg           pluginConfig
	configured    bool
	accountCursor int
	pinnedIndex   string
	pinFailed     bool
	stickyCursor  int
	nameCursor    map[string]int
	sticky        map[string]stickyBind
	cooldownIndex map[string]time.Time
	cooldownToken map[string]time.Time
}

type hostHTTPRequest struct {
	HostCallbackID string      `json:"host_callback_id,omitempty"`
	Method         string      `json:"method"`
	URL            string      `json:"url"`
	Headers        http.Header `json:"headers,omitempty"`
	Body           []byte      `json:"body,omitempty"`
}

type hostHTTPResult struct {
	StatusCode    int         `json:"StatusCode"`
	StatusCodeAlt int         `json:"status_code"`
	Headers       http.Header `json:"Headers"`
	HeadersAlt    http.Header `json:"headers"`
	Body          []byte      `json:"Body"`
	BodyAlt       []byte      `json:"body"`
	StreamID      string      `json:"StreamID"`
	StreamIDAlt   string      `json:"stream_id"`
}

func (r hostHTTPResult) status() int {
	if r.StatusCode != 0 {
		return r.StatusCode
	}
	return r.StatusCodeAlt
}

func (r hostHTTPResult) body() []byte {
	if len(r.Body) > 0 {
		return r.Body
	}
	return r.BodyAlt
}

func (r hostHTTPResult) streamID() string {
	if r.StreamID != "" {
		return r.StreamID
	}
	return r.StreamIDAlt
}

func (r hostHTTPResult) headers() http.Header {
	if len(r.Headers) > 0 {
		return r.Headers
	}
	return r.HeadersAlt
}

type hostStreamRef struct {
	HostCallbackID string `json:"host_callback_id,omitempty"`
	StreamID       string `json:"stream_id"`
}

type hostStreamRead struct {
	Payload    []byte `json:"payload"`
	PayloadAlt []byte `json:"Payload"`
	Error      string `json:"error"`
	ErrorAlt   string `json:"Error"`
	Done       bool   `json:"done"`
	DoneAlt    bool   `json:"Done"`
}

func (r hostStreamRead) payload() []byte {
	if len(r.Payload) > 0 {
		return r.Payload
	}
	return r.PayloadAlt
}

func (r hostStreamRead) errText() string {
	if r.Error != "" {
		return r.Error
	}
	return r.ErrorAlt
}

func (r hostStreamRead) done() bool {
	return r.Done || r.DoneAlt || r.errText() != ""
}

type hostEmit struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
}

type hostClose struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

type preparedCall struct {
	callbackID string
	payload    []byte
	spanStart  int
	spanEnd    int
	attempts   []attempt
	path       string
	session    string
	client     http.Header
	protocol   string
}

var (
	runtimeMu        sync.Mutex
	runtime          runtimeState
	nowFn            = time.Now
	hostCallOverride func(method string, payload []byte) (json.RawMessage, error)
)

func init() {
	resetRuntime()
}

func resetRuntime() {
	stopAccountChecker()
	runtimeMu.Lock()
	runtime = runtimeState{
		cfg:           defaultPluginConfig(),
		nameCursor:    map[string]int{},
		sticky:        map[string]stickyBind{},
		cooldownIndex: map[string]time.Time{},
		cooldownToken: map[string]time.Time{},
	}
	nowFn = time.Now
	hostCallOverride = nil
	runtimeMu.Unlock()
	resetStatsMemory()
}

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
		writeResponse(response, errorEnvelope("invalid_method", "method is required", 0))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		status := 0
		if sc, ok := errHandle.(interface{ StatusCode() int }); ok && sc != nil {
			status = sc.StatusCode()
		}
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error(), status))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	stopAccountChecker()
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		configure(request)
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodPluginQuiesce:
		return okEnvelope(map[string]any{})
	case pluginabi.MethodModelRegister, pluginabi.MethodModelStatic:
		return okEnvelope(modelList(currentConfig()))
	case pluginabi.MethodModelForAuth:
		return okEnvelope(pluginapi.ModelResponse{Provider: pluginID, Models: []pluginapi.ModelInfo{}})
	case pluginabi.MethodModelRoute:
		return routeModel(request)
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(map[string]string{"identifier": pluginID})
	case pluginabi.MethodExecutorExecute:
		return execute(request, false)
	case pluginabi.MethodExecutorExecuteStream:
		return execute(request, true)
	case pluginabi.MethodExecutorCountTokens:
		return okEnvelope(pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":0}`)})
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRoutes())
	case pluginabi.MethodManagementHandle:
		return managementStatus(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method, 0), nil
	}
}

func configure(raw []byte) {
	var req lifecycleRequest
	if len(bytes.TrimSpace(raw)) > 0 && json.Unmarshal(raw, &req) != nil {
		return
	}
	cfg, errDecode := decodeConfig(req.ConfigYAML)
	runtimeMu.Lock()
	if errDecode != nil {
		if !runtime.configured {
			runtime.cfg = defaultPluginConfig()
			runtime.configured = true
		}
		runtimeMu.Unlock()
		return
	}
	runtime.cfg = cfg
	runtime.configured = true
	runtimeMu.Unlock()
	setStatsFile(cfg.StatsFile)
	if accountCheckAutoStart {
		startAccountChecker()
	}
}

func defaultPluginConfig() pluginConfig {
	retry := defaultRetry
	fallback := 0
	sticky := defaultStickySeconds
	return pluginConfig{
		Enabled:        true,
		BaseURL:        defaultBaseURL,
		RequestRetry:   &retry,
		FallbackRetry:  &fallback,
		RotationMode:   "round-robin",
		PlusPreference: "prefer",
		StickySeconds:  &sticky,
	}
}

func decodeConfig(raw []byte) (pluginConfig, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return defaultPluginConfig(), nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	cfg := defaultPluginConfig()
	if errDecode := dec.Decode(&cfg); errDecode != nil {
		if errDecode == io.EOF {
			return defaultPluginConfig(), nil
		}
		return pluginConfig{}, errDecode
	}
	var extra yaml.Node
	if errExtra := dec.Decode(&extra); errExtra != io.EOF {
		if errExtra == nil {
			return pluginConfig{}, fmt.Errorf("config must be one yaml document")
		}
		return pluginConfig{}, errExtra
	}
	return normalizeConfig(cfg)
}

func normalizeConfig(cfg pluginConfig) (pluginConfig, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	parsed, errURL := validateBaseURL(base)
	if errURL != nil {
		return pluginConfig{}, errURL
	}
	cfg.BaseURL = parsed
	cfg.Prefix = normalizePrefix(cfg.Prefix)

	mode := strings.ToLower(strings.TrimSpace(cfg.RotationMode))
	if mode == "" {
		mode = "round-robin"
	}
	switch mode {
	case "round-robin", "fill-first", "least-recent", "sticky":
	default:
		return pluginConfig{}, fmt.Errorf("invalid rotation-mode %q", cfg.RotationMode)
	}
	cfg.RotationMode = mode

	plus := strings.ToLower(strings.TrimSpace(cfg.PlusPreference))
	if plus == "" {
		plus = "prefer"
	}
	if plus == "ignore" {
		plus = "off"
	}
	switch plus {
	case "off", "prefer", "only":
	default:
		return pluginConfig{}, fmt.Errorf("invalid plus-preference %q", cfg.PlusPreference)
	}
	cfg.PlusPreference = plus

	cfg.RequestRetry = clampInt(cfg.RequestRetry, defaultRetry, 0, maxExtraRetries)
	cfg.FallbackRetry = clampInt(cfg.FallbackRetry, 0, 0, maxFallbackRetries)
	cfg.StickySeconds = clampInt(cfg.StickySeconds, defaultStickySeconds, 0, maxStickySeconds)
	cfg.ModelCooldownMinutes = clampInt(cfg.ModelCooldownMinutes, defaultModelCooldownMinutes, 0, maxModelCooldownMinutes)
	cfg.StatsFile = strings.TrimSpace(cfg.StatsFile)

	headers, errHeaders := normalizeHeaders(cfg.Headers)
	if errHeaders != nil {
		return pluginConfig{}, errHeaders
	}
	cfg.Headers = headers
	models, errModels := normalizeModels(cfg.Models)
	if errModels != nil {
		return pluginConfig{}, errModels
	}
	cfg.Models = models
	return cfg, nil
}

func clampInt(value *int, fallback, minValue, maxValue int) *int {
	if value == nil {
		next := fallback
		return &next
	}
	next := *value
	if next < minValue {
		next = minValue
	}
	if next > maxValue {
		next = maxValue
	}
	return &next
}

func validateBaseURL(raw string) (string, error) {
	parsed, errParse := url.Parse(raw)
	if errParse != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid base-url")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
	case "http":
		if !privateBaseHost(parsed.Hostname()) {
			return "", fmt.Errorf("invalid base-url")
		}
	default:
		return "", fmt.Errorf("invalid base-url")
	}
	return strings.TrimRight(raw, "/"), nil
}

func privateBaseHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// normalizePrefix matches normalizeModelPrefix: trim spaces and slashes, and drop values that still contain a slash.
func normalizePrefix(prefix string) string {
	trimmed := strings.TrimSpace(prefix)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" || strings.Contains(trimmed, "/") {
		return ""
	}
	return trimmed
}

func normalizeHeaders(headers map[string]string) (map[string]string, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	clean := make(map[string]string, len(headers))
	for key, value := range headers {
		name := strings.TrimSpace(key)
		val := strings.TrimSpace(value)
		if name == "" || val == "" {
			continue
		}
		switch http.CanonicalHeaderKey(name) {
		case "Authorization", "Host":
			return nil, fmt.Errorf("header %s is not allowed", name)
		}
		clean[name] = val
	}
	if len(clean) == 0 {
		return nil, nil
	}
	return clean, nil
}

func normalizeModels(models []modelSpec) ([]modelSpec, error) {
	if len(models) == 0 {
		return nil, nil
	}
	out := make([]modelSpec, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		name := strings.TrimSpace(model.Name)
		alias := strings.TrimSpace(model.Alias)
		if name == "" {
			if alias != "" {
				return nil, fmt.Errorf("model name is required")
			}
			continue
		}
		key := name + "\x00" + alias
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		model.Name = name
		model.Alias = alias
		model.DisplayName = strings.TrimSpace(model.DisplayName)
		model.Fallback = normalizeFallback(model.Fallback)
		out = append(out, model)
	}
	return out, nil
}

func normalizeFallback(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		name := strings.TrimSpace(item)
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (c pluginConfig) retry() int {
	if c.RequestRetry == nil {
		return defaultRetry
	}
	return *c.RequestRetry
}

func (c pluginConfig) fallbackRetry() int {
	if c.FallbackRetry == nil {
		return 0
	}
	return *c.FallbackRetry
}

func (c pluginConfig) stickySeconds() int {
	if c.StickySeconds == nil {
		return defaultStickySeconds
	}
	return *c.StickySeconds
}

func (c pluginConfig) modelCooldown() time.Duration {
	minutes := defaultModelCooldownMinutes
	if c.ModelCooldownMinutes != nil {
		minutes = *c.ModelCooldownMinutes
	}
	if minutes <= 0 {
		return 0
	}
	return time.Duration(minutes) * time.Minute
}

func currentConfig() pluginConfig {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	return runtime.cfg
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginID,
			Version:          "0.1.0",
			Author:           "router-for-me",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			ConfigFields:     configFields(),
		},
		Capabilities: registrationCaps{
			ModelRegistrar:        true,
			ModelProvider:         true,
			ModelRouter:           true,
			Executor:              true,
			ExecutorModelScope:    string(pluginapi.ExecutorModelScopeStatic),
			ExecutorInputFormats:  []string{"chat-completions", "openai-responses"},
			ExecutorOutputFormats: []string{"chat-completions", "openai-responses"},
			ManagementAPI:         true,
		},
	}
}

func configFields() []pluginapi.ConfigField {
	return []pluginapi.ConfigField{
		{Name: "enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "When false, the plugin claims no models and the executor rejects requests."},
		{Name: "base-url", Type: pluginapi.ConfigFieldTypeString, Description: "HTTPS OpenAI-compatible base URL. Empty uses the built-in default."},
		{Name: "prefix", Type: pluginapi.ConfigFieldTypeString, Description: "Client model prefix. A value that still contains a slash claims nothing."},
		{Name: "headers", Type: pluginapi.ConfigFieldTypeObject, Description: "Extra upstream headers. $NAME copies a client header. $CPA-SESSION-ID uses the executor session."},
		{Name: "request-retry", Type: pluginapi.ConfigFieldTypeInteger, Description: "Extra attempts on the next account with the same upstream model. 0 sends once. Default 3. Maximum 32. Values outside 0 to 32 are clamped so the plugin stays registered."},
		{Name: "fallback-retry", Type: pluginapi.ConfigFieldTypeInteger, Description: "Stored so existing config loads. Requests are not switched to fallback model names. Maximum 3."},
		{Name: "rotation-mode", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"round-robin", "fill-first", "least-recent", "sticky"}, Description: "Codex account rotation. round-robin keeps a successful access token and moves to the next account after one upstream error."},
		{Name: "plus-preference", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"off", "prefer", "only"}, Description: "plus is the ChatGPT plan inside the account token. A stale plan_type of plus is ignored when the token says free. prefer stays on Plus while any enabled Plus account exists. ignore is stored as off."},
		{Name: "sticky-seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Sticky session lifetime in seconds. Default 300."},
		{Name: "model-cooldown-minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "Minutes to skip an account and its access token after the upstream says the model is not supported for a ChatGPT account. 0 disables. Default 1440. Maximum 10080."},
		{Name: "stats-file", Type: pluginapi.ConfigFieldTypeString, Description: "JSON file for hourly request and token buckets kept for 90 days. Empty disables persistence."},
		{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "Upstream model name plus optional client alias. The same alias with different names is one pool. An exact duplicate name and alias is skipped. A fallback list is stored and is not sent."},
	}
}

func (c pluginConfig) pools() []modelPool {
	order := make([]string, 0)
	byAlias := make(map[string]*modelPool)
	for _, model := range c.Models {
		publicName := model.publicName()
		pool := byAlias[publicName]
		if pool == nil {
			pool = &modelPool{alias: publicName, clientID: clientID(c.Prefix, publicName)}
			byAlias[publicName] = pool
			order = append(order, publicName)
		}
		pool.names = append(pool.names, model.Name)
		pool.fallbacks = append(pool.fallbacks, append([]string(nil), model.Fallback...))
		if pool.display == "" {
			pool.display = model.DisplayName
		}
	}
	out := make([]modelPool, 0, len(order))
	for _, alias := range order {
		out = append(out, *byAlias[alias])
	}
	return out
}

func (m modelSpec) publicName() string {
	if alias := strings.TrimSpace(m.Alias); alias != "" {
		return alias
	}
	return m.Name
}

func clientID(prefix, alias string) string {
	if prefix == "" {
		return alias
	}
	return prefix + "/" + alias
}

func modelList(cfg pluginConfig) pluginapi.ModelResponse {
	models := make([]pluginapi.ModelInfo, 0)
	if cfg.Enabled {
		for _, pool := range cfg.pools() {
			models = append(models, pluginapi.ModelInfo{
				ID:          pool.clientID,
				Object:      "model",
				OwnedBy:     pluginID,
				Name:        pool.names[0],
				DisplayName: pool.display,
				UserDefined: true,
			})
		}
	}
	return pluginapi.ModelResponse{Provider: pluginID, Models: models}
}

func routeModel(raw []byte) ([]byte, error) {
	var req routeCall
	if len(bytes.TrimSpace(raw)) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}
	cfg := currentConfig()
	if !cfg.Enabled || !claimed(cfg, req.RequestedModel) {
		return okEnvelope(pluginapi.ModelRouteResponse{})
	}
	return okEnvelope(pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf})
}

func claimed(cfg pluginConfig, model string) bool {
	base, _ := splitSuffix(model)
	for _, pool := range cfg.pools() {
		if base == pool.clientID {
			return true
		}
	}
	return false
}

func splitSuffix(model string) (string, string) {
	parsed := thinking.ParseSuffix(strings.TrimSpace(model))
	if !parsed.HasSuffix {
		return parsed.ModelName, ""
	}
	return parsed.ModelName, "(" + parsed.RawSuffix + ")"
}

func resolveModel(cfg pluginConfig, model string) (alias string, names []string, fallbacks [][]string, suffix string, explicit bool, ok bool) {
	base, suffix := splitSuffix(model)
	for _, pool := range cfg.pools() {
		if base == pool.clientID {
			return pool.alias, append([]string(nil), pool.names...), cloneFallbacks(pool.fallbacks), suffix, false, true
		}
	}
	for _, pool := range cfg.pools() {
		for i, name := range pool.names {
			if base == name {
				var row []string
				if i < len(pool.fallbacks) {
					row = append([]string(nil), pool.fallbacks[i]...)
				}
				return pool.alias, []string{name}, [][]string{row}, suffix, true, true
			}
		}
	}
	return "", nil, nil, "", false, false
}

func execute(raw []byte, stream bool) ([]byte, error) {
	var req executorCall
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	cfg := currentConfig()
	if !cfg.Enabled {
		return errorEnvelope("disabled", "ch-relay-vippro is disabled", http.StatusServiceUnavailable), nil
	}
	model, start, end, errModel := extractModel(req.Payload)
	if errModel != nil {
		return errorEnvelope("invalid_payload", errModel.Error(), http.StatusBadRequest), nil
	}
	alias, names, fallbacks, suffix, explicit, ok := resolveModel(cfg, model)
	if !ok {
		return errorEnvelope("unknown_model", "model is not claimed by ch-relay-vippro", http.StatusBadRequest), nil
	}
	path, errPath := upstreamPath(req.Format, req.SourceFormat)
	if errPath != nil {
		return errorEnvelope("unsupported_format", errPath.Error(), http.StatusBadRequest), nil
	}
	now := nowFn()
	accounts, errAccounts := loadEligible(req.HostCallbackID, now, cfg.PlusPreference)
	if errors.Is(errAccounts, errNoEligiblePlus) {
		return errorEnvelope("no_plus_account", "no eligible plus codex account is available", http.StatusServiceUnavailable), nil
	}
	if errAccounts != nil {
		return errorEnvelope("auth_unavailable", "codex auth list failed", http.StatusBadGateway), nil
	}
	accounts = highestPriority(accounts)
	if cfg.PlusPreference == "only" {
		accounts = onlyPlusBearers(accounts)
		if len(accounts) == 0 {
			return errorEnvelope("no_plus_account", "no plus codex account is available", http.StatusServiceUnavailable), nil
		}
	}
	if len(accounts) == 0 {
		return errorEnvelope("no_account", "no eligible codex account is available", http.StatusServiceUnavailable), nil
	}
	beforeCooldown := len(accounts)
	accounts = withoutCooldown(accounts, now)
	if len(accounts) == 0 {
		if beforeCooldown > 0 {
			return errorEnvelope("accounts_cooling", "codex accounts are in model cooldown", http.StatusServiceUnavailable), nil
		}
		return errorEnvelope("no_account", "no eligible codex account is available", http.StatusServiceUnavailable), nil
	}
	session := sessionID(req.Metadata, req.Headers)
	runtimeMu.Lock()
	ordered := planAccountsLocked(cfg, accounts, session, now)
	plannedNames, plannedFallbacks := planNamesLocked(alias, names, fallbacks, explicit)
	runtimeMu.Unlock()
	attempts := buildAttempts(ordered, plannedNames, plannedFallbacks, 1+cfg.retry(), suffix)
	if len(attempts) == 0 {
		return errorEnvelope("no_account", "no eligible codex account is available", http.StatusServiceUnavailable), nil
	}
	prepared := preparedCall{
		callbackID: req.HostCallbackID,
		payload:    req.Payload,
		spanStart:  start,
		spanEnd:    end,
		attempts:   attempts,
		path:       path,
		session:    session,
		client:     req.Headers,
		protocol:   usageProtocol(req.Format, req.SourceFormat),
	}
	if stream {
		return openStream(req.StreamID, cfg, prepared)
	}
	return sendSerial(cfg, prepared)
}

func upstreamPath(format, source string) (string, error) {
	for _, candidate := range []string{format, source} {
		switch strings.ToLower(strings.TrimSpace(candidate)) {
		case "openai", "chat-completions", "chat_completions", "openai-chat-completions", "openai_chat_completions":
			return "/chat/completions", nil
		case "openai-response", "openai-responses", "openai_responses", "responses":
			return "/responses", nil
		}
	}
	return "", fmt.Errorf("unsupported format")
}

type attemptResult struct {
	success bool
	rewrite error
	resp    hostHTTPResult
	status  int
	body    []byte
	tokens  []string
	index   string
	email   string
	noted   bool
}

func postModel(cfg pluginConfig, prepared preparedCall, token, model string, stream bool) (hostHTTPResult, int, []byte, error) {
	body, errBody := rewriteModel(prepared.payload, prepared.spanStart, prepared.spanEnd, model)
	if errBody != nil {
		return hostHTTPResult{}, 0, nil, errBody
	}
	base := strings.TrimSuffix(cfg.BaseURL, "/")
	resp, errDo := hostDo(prepared.callbackID, base+prepared.path, upstreamHeaders(cfg, prepared.client, prepared.session, token, stream), body, stream)
	if errDo != nil {
		return hostHTTPResult{}, http.StatusBadGateway, []byte(errDo.Error()), nil
	}
	return resp, resp.status(), resp.body(), nil
}

// performAttempts sends the configured upstream model with the selected access token.
// A 2xx response is returned unchanged. A non-2xx response tries the next account.
// The ChatGPT model-not-supported error cools that account and access token.
func performAttempts(cfg pluginConfig, prepared preparedCall, stream bool) attemptResult {
	tokens := make([]string, 0, len(prepared.attempts))
	lastStatus := http.StatusBadGateway
	var lastBody []byte
	var lastResp hostHTTPResult
	for _, at := range prepared.attempts {
		if cooled(at.index, at.token, nowFn()) {
			continue
		}
		tokens = append(tokens, at.token)
		resp, status, body, errPost := postModel(cfg, prepared, at.token, at.model, stream)
		if errPost != nil {
			return attemptResult{rewrite: errPost}
		}
		if status >= 200 && status < 300 {
			if stream && resp.streamID() == "" {
				lastStatus = http.StatusBadGateway
				lastBody = []byte("missing upstream stream id")
				lastResp = resp
				noteRequest(false, 0, 0, 0, at.email)
				continue
			}
			if cfg.RotationMode == "sticky" {
				bindSticky(prepared.session, at.index, cfg.stickySeconds())
			}
			if cfg.RotationMode == "round-robin" {
				pinRoundRobin(at.index, false)
			}
			return attemptResult{
				success: true,
				resp:    resp,
				status:  status,
				body:    body,
				tokens:  append([]string(nil), tokens...),
				index:   at.index,
				email:   at.email,
			}
		}
		if stream {
			if id := resp.streamID(); id != "" {
				if len(body) == 0 {
					body = readStreamBody(prepared.callbackID, id)
				} else {
					_ = closeHostStream(pluginabi.MethodHostHTTPStreamClose, prepared.callbackID, id, "")
				}
			}
		}
		if status != 0 {
			lastStatus = status
		}
		lastResp = resp
		lastBody = body
		noteRequest(false, 0, 0, 0, at.email)
		if clientAborted(status, body) {
			return attemptResult{resp: lastResp, status: lastStatus, body: lastBody, tokens: tokens, email: at.email, noted: true}
		}
		if chatgptModelUnsupported(body) {
			markModelCooldown(at.index, at.token, cfg.modelCooldown())
		}
		if cfg.RotationMode == "round-robin" {
			pinRoundRobin(at.index, true)
		}
	}
	return attemptResult{resp: lastResp, status: lastStatus, body: lastBody, tokens: tokens, noted: len(tokens) > 0}
}

func stickRoundRobinLocked(primary []account) []account {
	if len(primary) == 0 {
		return nil
	}
	start := 0
	if i := accountIndex(primary, runtime.pinnedIndex); i >= 0 {
		start = i
		if runtime.pinFailed {
			start = (i + 1) % len(primary)
			runtime.pinFailed = false
			runtime.pinnedIndex = primary[start].index
		}
	}
	return rotateAccounts(primary, start)
}

func pinRoundRobin(index string, failed bool) {
	if index == "" {
		return
	}
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	runtime.pinnedIndex = index
	runtime.pinFailed = failed
}

func clientAborted(status int, body []byte) bool {
	if status == 499 {
		return true
	}
	text := strings.ToLower(string(body))
	return strings.Contains(text, "context canceled") || strings.Contains(text, "context cancelled") || strings.Contains(text, "client cancelled") || strings.Contains(text, "client canceled")
}

func chatgptModelUnsupported(body []byte) bool {
	text := strings.ToLower(string(body))
	return strings.Contains(text, "model is not supported") && strings.Contains(text, "chatgpt account")
}

// readStreamBody copies upstream bytes as-is, then closes the host stream.
func readStreamBody(callbackID, streamID string) []byte {
	var body []byte
	for {
		chunk, errRead := readHostStream(callbackID, streamID)
		if errRead != nil {
			break
		}
		body = append(body, chunk.payload()...)
		if chunk.done() {
			break
		}
	}
	_ = closeHostStream(pluginabi.MethodHostHTTPStreamClose, callbackID, streamID, "")
	return body
}

func withoutCooldown(accounts []account, now time.Time) []account {
	out := make([]account, 0, len(accounts))
	for _, item := range accounts {
		if cooled(item.index, item.token, now) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func cooled(index, token string, now time.Time) bool {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if until, ok := runtime.cooldownIndex[index]; ok && index != "" && now.Before(until) {
		return true
	}
	if until, ok := runtime.cooldownToken[token]; ok && token != "" && now.Before(until) {
		return true
	}
	return false
}

func markModelCooldown(index, token string, d time.Duration) {
	if d <= 0 {
		return
	}
	until := nowFn().Add(d)
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if runtime.cooldownIndex == nil {
		runtime.cooldownIndex = map[string]time.Time{}
	}
	if runtime.cooldownToken == nil {
		runtime.cooldownToken = map[string]time.Time{}
	}
	if index != "" {
		runtime.cooldownIndex[index] = until
	}
	if token != "" {
		runtime.cooldownToken[token] = until
	}
}

func bindSticky(session, index string, seconds int) {
	if session == "" || index == "" || seconds <= 0 {
		return
	}
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if runtime.sticky == nil {
		runtime.sticky = map[string]stickyBind{}
	}
	if current, ok := runtime.sticky[session]; ok && current.authIndex == index && nowFn().Before(current.until) {
		return
	}
	runtime.sticky[session] = stickyBind{
		authIndex: index,
		until:     nowFn().Add(time.Duration(seconds) * time.Second),
	}
}

func sendSerial(cfg pluginConfig, prepared preparedCall) ([]byte, error) {
	result := performAttempts(cfg, prepared, false)
	if result.rewrite != nil {
		return errorEnvelope("invalid_payload", result.rewrite.Error(), http.StatusBadRequest), nil
	}
	if result.success {
		in, out, total := responseUsage(prepared.protocol, result.body)
		noteRequest(true, in, out, total, result.email)
		return okEnvelope(pluginapi.ExecutorResponse{
			Payload: append([]byte(nil), result.body...),
			Headers: responseHeaders(result.resp, "application/json"),
		})
	}
	if !result.noted {
		noteRequest(false, 0, 0, 0, result.email)
	}
	return errorEnvelope("upstream_error", failureMessage(result.status, result.body, result.tokens), result.status), nil
}

// openAICompatPayloads feeds upstream lines through the CPA OpenAI passthrough translator.
func openAICompatPayloads(pending *[]byte, raw []byte, final bool, param *any) [][]byte {
	buf := append(*pending, raw...)
	var out [][]byte
	emitLine := func(line []byte) {
		frames := oaichat.ConvertOpenAIResponseToOpenAI(context.Background(), "", nil, nil, line, param)
		for _, frame := range frames {
			frame = bytes.TrimSpace(frame)
			if len(frame) == 0 || frame[0] == ':' {
				continue
			}
			out = append(out, frame)
		}
	}
	for {
		index := bytes.IndexByte(buf, '\n')
		if index < 0 {
			break
		}
		line := buf[:index]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		buf = buf[index+1:]
		emitLine(line)
	}
	if final && len(buf) > 0 {
		emitLine(buf)
		buf = nil
	}
	*pending = append((*pending)[:0], buf...)
	return out
}

func openStream(executorStreamID string, cfg pluginConfig, prepared preparedCall) ([]byte, error) {
	if strings.TrimSpace(executorStreamID) == "" {
		return errorEnvelope("executor_error", "stream_id is required", http.StatusBadRequest), nil
	}
	result := performAttempts(cfg, prepared, true)
	if result.rewrite != nil {
		return errorEnvelope("invalid_payload", result.rewrite.Error(), http.StatusBadRequest), nil
	}
	if result.success {
		used := append([]string(nil), result.tokens...)
		callbackID := prepared.callbackID
		protocol := prepared.protocol
		go forwardStream(callbackID, result.resp.streamID(), executorStreamID, protocol, used, result.email)
		return okEnvelope(map[string]any{
			"headers": responseHeaders(result.resp, "text/event-stream"),
		})
	}
	if !result.noted {
		noteRequest(false, 0, 0, 0, result.email)
	}
	return errorEnvelope("upstream_error", failureMessage(result.status, result.body, result.tokens), result.status), nil
}

func forwardStream(callbackID, httpStreamID, executorStreamID, protocol string, tokens []string, email string) {
	var buffer helps.StreamUsageBuffer
	failed := false
	errText := ""
	var once sync.Once
	finish := func(msg string) {
		once.Do(func() {
			in, out, total := int64(0), int64(0), int64(0)
			if detail, ok := buffer.Detail(); ok {
				in, out, total = countsFromDetail(detail)
			}
			noteRequest(!failed, in, out, total, email)
			_ = closeHostStream(pluginabi.MethodHostHTTPStreamClose, callbackID, httpStreamID, "")
			_ = closeHostStream(pluginabi.MethodHostStreamClose, "", executorStreamID, redact(msg, tokens))
		})
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			failed = true
			finish("stream failed")
		}
	}()
	var pending []byte
	var compatParam any
	// One empty delta opens the client response immediately. Without it the
	// client sits on a silent socket and aborts at about 15s, which ch-relay
	// records as client cancelled.
	if protocol != "openai-response" {
		if errEmit := emitHostStream(executorStreamID, chatStreamKeepAlive); errEmit != nil {
			failed = true
			finish(errEmit.Error())
			return
		}
	}
	emitChunk := func(payload []byte, final bool) bool {
		pieces := [][]byte{payload}
		if protocol != "openai-response" {
			pieces = openAICompatPayloads(&pending, payload, final, &compatParam)
		} else if len(payload) == 0 {
			pieces = nil
		}
		for _, piece := range pieces {
			if len(piece) == 0 {
				continue
			}
			helps.ObservePluginExecutorStreamUsage(protocol, piece, &buffer)
			if errEmit := emitHostStream(executorStreamID, piece); errEmit != nil {
				failed = true
				finish(errEmit.Error())
				return false
			}
		}
		return true
	}
	for {
		chunk, errRead := readHostStream(callbackID, httpStreamID)
		if errRead != nil {
			failed = true
			finish(errRead.Error())
			return
		}
		if !emitChunk(chunk.payload(), chunk.done()) {
			return
		}
		if chunk.done() {
			if chunk.errText() != "" {
				failed = true
				errText = chunk.errText()
			}
			finish(errText)
			return
		}
	}
}

// chatStreamKeepAlive is a valid chat chunk with no text and no finish.
var chatStreamKeepAlive = []byte(`{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":null}]}`)

func failureMessage(status int, body []byte, tokens []string) string {
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		if status == 0 {
			status = http.StatusBadGateway
		}
		msg = fmt.Sprintf("upstream status %d", status)
	}
	return redact(msg, tokens)
}

func responseHeaders(resp hostHTTPResult, fallback string) http.Header {
	headers := resp.headers()
	if headers == nil {
		headers = make(http.Header)
	} else {
		headers = headers.Clone()
	}
	if strings.TrimSpace(headers.Get("Content-Type")) == "" && fallback != "" {
		headers.Set("Content-Type", fallback)
	}
	return headers
}

func upstreamHeaders(cfg pluginConfig, client http.Header, session, token string, stream bool) http.Header {
	out := make(http.Header)
	out.Set("Content-Type", "application/json")
	keys := make([]string, 0, len(cfg.Headers))
	for key := range cfg.Headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if canonical := http.CanonicalHeaderKey(key); canonical == "Authorization" || canonical == "Host" {
			continue
		}
		value, ok := resolveHeader(cfg.Headers[key], client, session)
		if !ok {
			continue
		}
		out.Set(key, value)
	}
	out.Set("Authorization", "Bearer "+token)
	if stream {
		out.Set("Accept", "text/event-stream")
		out.Set("Cache-Control", "no-cache")
	}
	return out
}

func resolveHeader(value string, client http.Header, session string) (string, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "$") {
		if value == "" {
			return "", false
		}
		return value, true
	}
	name := strings.TrimSpace(strings.TrimPrefix(value, "$"))
	if name == "" {
		return "", false
	}
	if strings.EqualFold(name, "CPA-SESSION-ID") {
		if session == "" {
			return "", false
		}
		return session, true
	}
	if client == nil {
		return "", false
	}
	got := strings.TrimSpace(client.Get(name))
	if got == "" {
		return "", false
	}
	return got, true
}

func sessionID(meta map[string]any, headers http.Header) string {
	for _, key := range []string{"canonical_session_id", "execution_session_id", "session_id"} {
		if value := metaString(meta, key); value != "" {
			return value
		}
	}
	if headers == nil {
		return ""
	}
	for _, key := range []string{"Session-Id", "X-Session-Id", "X-Claude-Code-Session-Id"} {
		if value := strings.TrimSpace(headers.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func metaString(meta map[string]any, key string) string {
	if len(meta) == 0 {
		return ""
	}
	value, ok := meta[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

var errNoEligiblePlus = errors.New("no eligible plus codex account")

var plusFileCache sync.Map

func loadEligible(callbackID string, now time.Time, preference string) ([]account, error) {
	raw, errList := callHost(pluginabi.MethodHostAuthList, map[string]string{"host_callback_id": callbackID})
	if errList != nil {
		return nil, errList
	}
	var listed struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	if errUnmarshal := json.Unmarshal(raw, &listed); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	type candidate struct {
		file  pluginapi.HostAuthFileEntry
		plus  bool
		token string
	}
	candidates := make([]candidate, 0, len(listed.Files))
	hasEnabledPlus := false
	for _, file := range listed.Files {
		if !strings.EqualFold(strings.TrimSpace(file.Provider), "codex") || file.Disabled {
			continue
		}
		plus, token := accountPlan(callbackID, file)
		if plus {
			hasEnabledPlus = true
		}
		candidates = append(candidates, candidate{file: file, plus: plus, token: token})
	}
	// prefer/only stay on Plus while any enabled Plus account exists, including ones that are cooling.
	if preference == "only" && !hasEnabledPlus {
		return nil, errNoEligiblePlus
	}
	restrictPlus := (preference == "prefer" || preference == "only") && hasEnabledPlus
	out := make([]account, 0, len(candidates))
	for _, item := range candidates {
		if restrictPlus && !item.plus {
			continue
		}
		file := item.file
		if file.Unavailable {
			continue
		}
		if !file.NextRetryAfter.IsZero() && file.NextRetryAfter.After(now) {
			continue
		}
		index := strings.TrimSpace(file.AuthIndex)
		if index == "" {
			continue
		}
		token, errToken := accountToken(callbackID, index, item.token)
		if errToken != nil || token == "" {
			continue
		}
		out = append(out, account{
			index:    index,
			id:       strings.TrimSpace(file.ID),
			priority: file.Priority,
			updated:  file.UpdatedAt,
			plus:     item.plus,
			token:    token,
			email:    strings.TrimSpace(file.Email),
		})
	}
	if restrictPlus && len(out) == 0 {
		return nil, errNoEligiblePlus
	}
	return out, nil
}

func accountPlan(callbackID string, file pluginapi.HostAuthFileEntry) (bool, string) {
	if token, plus, ok := tokenFromPath(file.Path); ok {
		return plus, token
	}
	index := strings.TrimSpace(file.AuthIndex)
	if index == "" {
		return false, ""
	}
	token, plus, errGet := readAuth(callbackID, index)
	if errGet != nil {
		return false, ""
	}
	return plus, strings.TrimSpace(token)
}

func accountToken(callbackID, index, known string) (string, error) {
	if token := strings.TrimSpace(known); token != "" {
		return token, nil
	}
	token, _, errGet := readAuth(callbackID, index)
	if errGet != nil {
		return "", errGet
	}
	return strings.TrimSpace(token), nil
}

func tokenFromPath(path string) (string, bool, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false, false
	}
	info, errStat := os.Stat(path)
	cacheKey := ""
	if errStat == nil {
		cacheKey = path + "\x00" + strconv.FormatInt(info.ModTime().UnixNano(), 10) + "\x00" + strconv.FormatInt(info.Size(), 10)
		if value, ok := plusFileCache.Load(cacheKey); ok {
			cached := value.(cachedPlan)
			return cached.token, cached.plus, true
		}
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil || len(bytes.TrimSpace(data)) == 0 {
		return "", false, false
	}
	token, plus, known := tokenPlanFromAuthJSON(data)
	if !known {
		return "", false, false
	}
	if cacheKey != "" {
		plusFileCache.Store(cacheKey, cachedPlan{token: token, plus: plus})
	}
	return token, plus, true
}

type cachedPlan struct {
	token string
	plus  bool
}

func readAuth(callbackID, index string) (string, bool, error) {
	raw, errGet := callHost(pluginabi.MethodHostAuthGet, map[string]string{
		"host_callback_id": callbackID,
		"auth_index":       index,
	})
	if errGet != nil {
		return "", false, errGet
	}
	var resp struct {
		JSON json.RawMessage `json:"json"`
	}
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		return "", false, errUnmarshal
	}
	var doc struct {
		AccessToken string `json:"access_token"`
	}
	if len(bytes.TrimSpace(resp.JSON)) == 0 {
		return "", false, nil
	}
	if errUnmarshal := json.Unmarshal(resp.JSON, &doc); errUnmarshal != nil {
		return "", false, errUnmarshal
	}
	token, plus, known := tokenPlanFromAuthJSON(resp.JSON)
	return token, known && plus, nil
}

// tokenPlanFromAuthJSON returns the access_token and whether that bearer is Plus.
func tokenPlanFromAuthJSON(data []byte) (string, bool, bool) {
	plan, known := planFromAuthJSON(data)
	var doc struct {
		AccessToken string `json:"access_token"`
	}
	if errUnmarshal := json.Unmarshal(data, &doc); errUnmarshal != nil {
		return "", false, false
	}
	return strings.TrimSpace(doc.AccessToken), known && strings.EqualFold(plan, "plus"), known
}

// planFromAuthJSON reports the ChatGPT plan inside the access_token that will be sent.
// id_token and a stored plan_type cannot override a bearer that is a JWT.
func planFromAuthJSON(data []byte) (string, bool) {
	var doc struct {
		PlanType        string `json:"plan_type"`
		ChatGPTPlanType string `json:"chatgpt_plan_type"`
		AccessToken     string `json:"access_token"`
	}
	if errUnmarshal := json.Unmarshal(data, &doc); errUnmarshal != nil {
		return "", false
	}
	if plan := jwtChatGPTPlan(doc.AccessToken); plan != "" {
		return plan, true
	}
	// A JWT with no plan claim is not plus. Only a non-JWT token may use the file field.
	if looksLikeJWT(doc.AccessToken) {
		return "", false
	}
	if plan := strings.TrimSpace(doc.PlanType); plan != "" {
		return plan, true
	}
	if plan := strings.TrimSpace(doc.ChatGPTPlanType); plan != "" {
		return plan, true
	}
	return "", false
}

func looksLikeJWT(token string) bool {
	parts := strings.Split(strings.TrimSpace(token), ".")
	return len(parts) >= 2 && parts[0] != "" && parts[1] != ""
}

func jwtChatGPTPlan(token string) string {
	token = strings.TrimSpace(token)
	parts := strings.Split(token, ".")
	if len(parts) < 2 || parts[1] == "" {
		return ""
	}
	raw, errDecode := base64.RawURLEncoding.DecodeString(parts[1])
	if errDecode != nil {
		padded := parts[1]
		if m := len(padded) % 4; m != 0 {
			padded += strings.Repeat("=", 4-m)
		}
		raw, errDecode = base64.URLEncoding.DecodeString(padded)
		if errDecode != nil {
			return ""
		}
	}
	var claims struct {
		Auth struct {
			Plan string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	if errUnmarshal := json.Unmarshal(raw, &claims); errUnmarshal != nil {
		return ""
	}
	return strings.TrimSpace(claims.Auth.Plan)
}

func onlyPlusBearers(accounts []account) []account {
	out := make([]account, 0, len(accounts))
	for _, item := range accounts {
		if strings.EqualFold(jwtChatGPTPlan(item.token), "plus") || (!looksLikeJWT(item.token) && item.plus) {
			out = append(out, item)
		}
	}
	return out
}

func anyPlus(accounts []account) bool {
	for _, item := range accounts {
		if item.plus {
			return true
		}
	}
	return false
}

func highestPriority(accounts []account) []account {
	if len(accounts) == 0 {
		return nil
	}
	best := accounts[0].priority
	for _, item := range accounts[1:] {
		if item.priority > best {
			best = item.priority
		}
	}
	out := make([]account, 0, len(accounts))
	for _, item := range accounts {
		if item.priority == best {
			out = append(out, item)
		}
	}
	return out
}

func planAccountsLocked(cfg pluginConfig, eligible []account, session string, now time.Time) []account {
	if len(eligible) == 0 {
		return nil
	}
	plus, rest := splitPlus(sortAccounts(eligible, false))
	var primary, tail []account
	switch cfg.PlusPreference {
	case "only":
		primary = plus
	case "prefer":
		if len(plus) > 0 {
			primary = plus
		} else {
			primary = rest
		}
	default:
		primary = sortAccounts(eligible, false)
	}
	if len(primary) == 0 {
		return nil
	}
	if cfg.RotationMode == "least-recent" {
		primary = sortAccounts(primary, true)
		if len(tail) > 0 {
			tail = sortAccounts(tail, true)
		}
	}
	switch cfg.RotationMode {
	case "round-robin":
		primary = stickRoundRobinLocked(primary)
	case "sticky":
		primary = stickyOrderLocked(cfg, primary, session, now)
	}
	if len(tail) == 0 {
		return primary
	}
	return append(primary, tail...)
}

func splitPlus(in []account) (plus, rest []account) {
	for _, item := range in {
		if item.plus {
			plus = append(plus, item)
		} else {
			rest = append(rest, item)
		}
	}
	return plus, rest
}

func sortAccounts(in []account, byUpdated bool) []account {
	out := append([]account(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		if byUpdated && !out[i].updated.Equal(out[j].updated) {
			return out[i].updated.Before(out[j].updated)
		}
		if out[i].index != out[j].index {
			return out[i].index < out[j].index
		}
		return out[i].id < out[j].id
	})
	return out
}

func rotateAccounts(in []account, start int) []account {
	if len(in) == 0 {
		return nil
	}
	start %= len(in)
	if start == 0 {
		return append([]account(nil), in...)
	}
	out := make([]account, 0, len(in))
	out = append(out, in[start:]...)
	out = append(out, in[:start]...)
	return out
}

func stickyOrderLocked(cfg pluginConfig, primary []account, session string, now time.Time) []account {
	if runtime.sticky == nil {
		runtime.sticky = map[string]stickyBind{}
	}
	if session != "" {
		if bind, ok := runtime.sticky[session]; ok && now.Before(bind.until) {
			if index := accountIndex(primary, bind.authIndex); index >= 0 {
				return rotateAccounts(primary, index)
			}
		}
	}
	start := 0
	if len(primary) > 0 {
		start = runtime.stickyCursor % len(primary)
		runtime.stickyCursor++
	}
	ordered := rotateAccounts(primary, start)
	if session != "" && len(ordered) > 0 {
		runtime.sticky[session] = stickyBind{
			authIndex: ordered[0].index,
			until:     now.Add(time.Duration(cfg.stickySeconds()) * time.Second),
		}
	}
	return ordered
}

func accountIndex(accounts []account, index string) int {
	for i, item := range accounts {
		if item.index == index {
			return i
		}
	}
	return -1
}

func planNamesLocked(alias string, names []string, fallbacks [][]string, explicit bool) ([]string, [][]string) {
	names = append([]string(nil), names...)
	fallbacks = cloneFallbacks(fallbacks)
	if len(fallbacks) < len(names) {
		grown := make([][]string, len(names))
		copy(grown, fallbacks)
		fallbacks = grown
	}
	if explicit || len(names) < 2 {
		return names, fallbacks
	}
	if runtime.nameCursor == nil {
		runtime.nameCursor = map[string]int{}
	}
	start := runtime.nameCursor[alias] % len(names)
	runtime.nameCursor[alias] = (start + 1) % len(names)
	return rotateStrings(names, start), rotateFallbacks(fallbacks, start)
}

func cloneFallbacks(in [][]string) [][]string {
	if len(in) == 0 {
		return nil
	}
	out := make([][]string, len(in))
	for i, row := range in {
		if len(row) == 0 {
			continue
		}
		out[i] = append([]string(nil), row...)
	}
	return out
}

func rotateFallbacks(in [][]string, start int) [][]string {
	if len(in) == 0 {
		return nil
	}
	start %= len(in)
	if start == 0 {
		return cloneFallbacks(in)
	}
	out := make([][]string, 0, len(in))
	out = append(out, cloneFallbacks(in[start:])...)
	out = append(out, cloneFallbacks(in[:start])...)
	return out
}

func rotateStrings(in []string, start int) []string {
	if len(in) == 0 {
		return nil
	}
	start %= len(in)
	if start == 0 {
		return append([]string(nil), in...)
	}
	out := make([]string, 0, len(in))
	out = append(out, in[start:]...)
	out = append(out, in[:start]...)
	return out
}

// buildAttempts walks account and name pairs together, then fills unused pairs.
// The caller caps the slice at 1+request-retry. Each account/name pair is used once.
func buildAttempts(accounts []account, names []string, fallbacks [][]string, budget int, suffix string) []attempt {
	accountCount := len(accounts)
	nameCount := len(names)
	if accountCount == 0 || nameCount == 0 || budget < 1 {
		return nil
	}
	used := make(map[string]struct{}, accountCount*nameCount)
	out := make([]attempt, 0, budget)
	add := func(item account, nameIndex int) {
		if len(out) >= budget {
			return
		}
		name := names[nameIndex]
		key := item.index + "\x00" + name
		if _, ok := used[key]; ok {
			return
		}
		used[key] = struct{}{}
		var alts []string
		if nameIndex < len(fallbacks) {
			alts = make([]string, len(fallbacks[nameIndex]))
			for i, alt := range fallbacks[nameIndex] {
				alts[i] = alt + suffix
			}
		}
		out = append(out, attempt{index: item.index, token: item.token, email: item.email, model: name + suffix, fallbacks: alts})
	}
	limit := accountCount * nameCount
	for step := 0; step < limit && len(out) < budget; step++ {
		add(accounts[step%accountCount], step%nameCount)
	}
	for nameIndex := 0; nameIndex < nameCount && len(out) < budget; nameIndex++ {
		for accountIndex := 0; accountIndex < accountCount && len(out) < budget; accountIndex++ {
			add(accounts[accountIndex], nameIndex)
		}
	}
	return out
}

func rewriteModel(payload []byte, start, end int, model string) ([]byte, error) {
	quoted, errMarshal := json.Marshal(model)
	if errMarshal != nil {
		return nil, errMarshal
	}
	out := make([]byte, 0, len(payload)-(end-start)+len(quoted))
	out = append(out, payload[:start]...)
	out = append(out, quoted...)
	out = append(out, payload[end:]...)
	return out, nil
}

func extractModel(payload []byte) (string, int, int, error) {
	s := &js{b: payload}
	s.skipSpace()
	if s.i >= len(s.b) || s.b[s.i] != '{' {
		return "", 0, 0, fmt.Errorf("payload must be a JSON object")
	}
	s.i++
	seen := false
	var model string
	var start, end int
	s.skipSpace()
	if s.i < len(s.b) && s.b[s.i] == '}' {
		return "", 0, 0, fmt.Errorf("model must be a string")
	}
	for {
		s.skipSpace()
		key, _, _, errKey := s.parseString()
		if errKey != nil {
			return "", 0, 0, errKey
		}
		s.skipSpace()
		if s.i >= len(s.b) || s.b[s.i] != ':' {
			return "", 0, 0, fmt.Errorf("invalid payload")
		}
		s.i++
		s.skipSpace()
		if key == "model" {
			if seen {
				return "", 0, 0, fmt.Errorf("duplicate model key")
			}
			seen = true
			if s.i >= len(s.b) || s.b[s.i] != '"' {
				return "", 0, 0, fmt.Errorf("model must be a string")
			}
			var errVal error
			model, start, end, errVal = s.parseString()
			if errVal != nil {
				return "", 0, 0, errVal
			}
		} else if strings.EqualFold(key, "model") {
			return "", 0, 0, fmt.Errorf("model key must be lowercase")
		} else if errSkip := s.skipValue(); errSkip != nil {
			return "", 0, 0, errSkip
		}
		s.skipSpace()
		if s.i >= len(s.b) {
			return "", 0, 0, fmt.Errorf("invalid payload")
		}
		switch s.b[s.i] {
		case ',':
			s.i++
		case '}':
			s.i++
			s.skipSpace()
			if s.i != len(s.b) {
				return "", 0, 0, fmt.Errorf("invalid payload")
			}
			if !seen {
				return "", 0, 0, fmt.Errorf("model must be a string")
			}
			return model, start, end, nil
		default:
			return "", 0, 0, fmt.Errorf("invalid payload")
		}
	}
}

type js struct {
	b []byte
	i int
}

func (s *js) skipSpace() {
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case ' ', '\n', '\r', '\t':
			s.i++
		default:
			return
		}
	}
}

func (s *js) parseString() (string, int, int, error) {
	if s.i >= len(s.b) || s.b[s.i] != '"' {
		return "", 0, 0, fmt.Errorf("invalid payload")
	}
	start := s.i
	s.i++
	var out strings.Builder
	for s.i < len(s.b) {
		ch := s.b[s.i]
		if ch == '"' {
			s.i++
			return out.String(), start, s.i, nil
		}
		if ch == '\\' {
			s.i++
			if s.i >= len(s.b) {
				return "", 0, 0, fmt.Errorf("invalid payload")
			}
			esc := s.b[s.i]
			s.i++
			switch esc {
			case '"', '\\', '/':
				out.WriteByte(esc)
			case 'b':
				out.WriteByte('\b')
			case 'f':
				out.WriteByte('\f')
			case 'n':
				out.WriteByte('\n')
			case 'r':
				out.WriteByte('\r')
			case 't':
				out.WriteByte('\t')
			case 'u':
				if s.i+4 > len(s.b) {
					return "", 0, 0, fmt.Errorf("invalid payload")
				}
				n, errParse := strconv.ParseUint(string(s.b[s.i:s.i+4]), 16, 32)
				if errParse != nil {
					return "", 0, 0, fmt.Errorf("invalid payload")
				}
				s.i += 4
				out.WriteRune(rune(n))
			default:
				return "", 0, 0, fmt.Errorf("invalid payload")
			}
			continue
		}
		if ch < 0x20 {
			return "", 0, 0, fmt.Errorf("invalid payload")
		}
		out.WriteByte(ch)
		s.i++
	}
	return "", 0, 0, fmt.Errorf("invalid payload")
}

func (s *js) skipValue() error {
	s.skipSpace()
	if s.i >= len(s.b) {
		return fmt.Errorf("invalid payload")
	}
	switch s.b[s.i] {
	case '"':
		_, _, _, errParse := s.parseString()
		return errParse
	case '{':
		return s.skipObject()
	case '[':
		return s.skipArray()
	case 't':
		return s.literal("true")
	case 'f':
		return s.literal("false")
	case 'n':
		return s.literal("null")
	default:
		return s.skipNumber()
	}
}

func (s *js) skipObject() error {
	if s.i >= len(s.b) || s.b[s.i] != '{' {
		return fmt.Errorf("invalid payload")
	}
	s.i++
	s.skipSpace()
	if s.i < len(s.b) && s.b[s.i] == '}' {
		s.i++
		return nil
	}
	for {
		s.skipSpace()
		if _, _, _, errKey := s.parseString(); errKey != nil {
			return errKey
		}
		s.skipSpace()
		if s.i >= len(s.b) || s.b[s.i] != ':' {
			return fmt.Errorf("invalid payload")
		}
		s.i++
		if errValue := s.skipValue(); errValue != nil {
			return errValue
		}
		s.skipSpace()
		if s.i >= len(s.b) {
			return fmt.Errorf("invalid payload")
		}
		switch s.b[s.i] {
		case ',':
			s.i++
		case '}':
			s.i++
			return nil
		default:
			return fmt.Errorf("invalid payload")
		}
	}
}

func (s *js) skipArray() error {
	if s.i >= len(s.b) || s.b[s.i] != '[' {
		return fmt.Errorf("invalid payload")
	}
	s.i++
	s.skipSpace()
	if s.i < len(s.b) && s.b[s.i] == ']' {
		s.i++
		return nil
	}
	for {
		if errValue := s.skipValue(); errValue != nil {
			return errValue
		}
		s.skipSpace()
		if s.i >= len(s.b) {
			return fmt.Errorf("invalid payload")
		}
		switch s.b[s.i] {
		case ',':
			s.i++
		case ']':
			s.i++
			return nil
		default:
			return fmt.Errorf("invalid payload")
		}
	}
}

func (s *js) literal(word string) error {
	if s.i+len(word) > len(s.b) || string(s.b[s.i:s.i+len(word)]) != word {
		return fmt.Errorf("invalid payload")
	}
	s.i += len(word)
	return nil
}

func (s *js) skipNumber() error {
	start := s.i
	if s.i < len(s.b) && (s.b[s.i] == '-' || s.b[s.i] == '+') {
		s.i++
	}
	for s.i < len(s.b) {
		ch := s.b[s.i]
		if (ch >= '0' && ch <= '9') || ch == '.' || ch == 'e' || ch == 'E' || ch == '+' || ch == '-' {
			s.i++
			continue
		}
		break
	}
	if s.i == start {
		return fmt.Errorf("invalid payload")
	}
	return nil
}

func redact(body string, tokens []string) string {
	ordered := append([]string(nil), tokens...)
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, token := range ordered {
		if token == "" {
			continue
		}
		body = strings.ReplaceAll(body, token, redactedToken)
	}
	return body
}

func hostDo(callbackID, target string, headers http.Header, body []byte, stream bool) (hostHTTPResult, error) {
	method := pluginabi.MethodHostHTTPDo
	if stream {
		method = pluginabi.MethodHostHTTPDoStream
	}
	raw, errCall := callHost(method, hostHTTPRequest{
		HostCallbackID: callbackID,
		Method:         http.MethodPost,
		URL:            target,
		Headers:        headers,
		Body:           body,
	})
	if errCall != nil {
		return hostHTTPResult{}, errCall
	}
	var resp hostHTTPResult
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		return hostHTTPResult{}, errUnmarshal
	}
	return resp, nil
}

func readHostStream(callbackID, streamID string) (hostStreamRead, error) {
	raw, errCall := callHost(pluginabi.MethodHostHTTPStreamRead, hostStreamRef{HostCallbackID: callbackID, StreamID: streamID})
	if errCall != nil {
		return hostStreamRead{}, errCall
	}
	var chunk hostStreamRead
	if errUnmarshal := json.Unmarshal(raw, &chunk); errUnmarshal != nil {
		return hostStreamRead{}, errUnmarshal
	}
	return chunk, nil
}

func emitHostStream(streamID string, payload []byte) error {
	_, errCall := callHost(pluginabi.MethodHostStreamEmit, hostEmit{StreamID: streamID, Payload: payload})
	return errCall
}

func closeHostStream(method, callbackID, streamID, errText string) error {
	if method == pluginabi.MethodHostHTTPStreamClose {
		_, errCall := callHost(method, hostStreamRef{HostCallbackID: callbackID, StreamID: streamID})
		return errCall
	}
	_, errCall := callHost(method, hostClose{StreamID: streamID, Error: errText})
	return errCall
}

func callHost(method string, payload any) (json.RawMessage, error) {
	rawPayload, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal host callback %s: %w", method, errMarshal)
	}
	if hostCallOverride != nil {
		return hostCallOverride(method, rawPayload)
	}
	return callHostC(method, rawPayload)
}

func callHostC(method string, rawPayload []byte) (json.RawMessage, error) {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		cPayload := C.CBytes(rawPayload)
		if cPayload == nil {
			return nil, fmt.Errorf("allocate host callback %s", method)
		}
		defer C.free(cPayload)
		requestPtr = (*C.uint8_t)(cPayload)
	}
	callCode := C.call_host_api(cMethod, requestPtr, C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("host callback %s returned no response, code=%d", method, int(callCode))
	}
	var env envelope
	if errUnmarshal := json.Unmarshal(rawResponse, &env); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host envelope %s: %w", method, errUnmarshal)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	if callCode != 0 {
		return nil, fmt.Errorf("host callback %s returned code=%d", method, int(callCode))
	}
	return append(json.RawMessage(nil), env.Result...), nil
}

func okEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string, status int) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message, HTTPStatus: status}})
	return raw
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
