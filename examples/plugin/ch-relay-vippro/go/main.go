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
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
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
	cooldownReasonUnsupported   = "chatgpt_model_unsupported"
)

var modelDoesNotExist = regexp.MustCompile(`\bmodel\b[\w\s'".-]{0,80}does not exist`)

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
	// UpstreamCache is deprecated and ignored. Production configs still set it.
	UpstreamCache bool        `yaml:"upstream-cache"`
	Models        []modelSpec `yaml:"models"`
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
	alias            string
	clientID         string
	names            []string
	fallbacks        [][]string
	display          string
	contextLength    int64
	inputModalities  []string
	outputModalities []string
	thinkingSet      bool
	thinkingSupport  *pluginapi.ThinkingSupport
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
	index    string
	token    string
	email    string
	model    string
	fallback bool
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
	fallbacks  []attempt
	path       string
	session    string
	client     http.Header
	protocol   string
}

type cooldownRecord struct {
	AuthIndex string    `json:"auth_index,omitempty"`
	TokenHash string    `json:"token_hash,omitempty"`
	Until     time.Time `json:"until"`
	Reason    string    `json:"reason,omitempty"`
}

var (
	runtimeMu        sync.Mutex
	cooldownFileMu   sync.Mutex
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
	loadCooldowns(cooldownFilePath(cfg.StatsFile))
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
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || sharedAddress(ip))
}

// sharedAddress reports the RFC 6598 carrier-grade NAT range 100.64.0.0/10.
// Tailscale assigns addresses from this range, and Go's IsPrivate does not include it.
func sharedAddress(ip net.IP) bool {
	ip4 := ip.To4()
	return ip4 != nil && ip4[0] == 100 && ip4[1]&0xc0 == 0x40
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
		model.Fallback = normalizeFallback(name, model.Fallback)
		out = append(out, model)
	}
	return out, nil
}

// normalizeFallback trims names, drops duplicates, and drops the row's own name.
func normalizeFallback(self string, raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := map[string]struct{}{self: {}}
	for _, item := range raw {
		name := strings.TrimSpace(item)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
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
		{Name: "request-retry", Type: pluginapi.ConfigFieldTypeInteger, Description: "Extra attempts on the next account with the same upstream model. 0 sends once. Default 3. Maximum 32. The management panel rejects values outside 0 to 32. The plugin still clamps them so it stays registered."},
		{Name: "fallback-retry", Type: pluginapi.ConfigFieldTypeInteger, Description: "Extra attempts with the model fallback names after every primary attempt failed because the upstream refused the model. Only the request model changes; response bytes are not touched. 0 disables fallback. Maximum 3."},
		{Name: "rotation-mode", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"round-robin", "fill-first", "least-recent", "sticky"}, Description: "Codex account rotation. round-robin keeps a successful access token and moves to the next account after one upstream error."},
		{Name: "plus-preference", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"off", "prefer", "only"}, Description: "plus is the ChatGPT plan inside the account token. A stale plan_type of plus is ignored when the token says free. prefer stays on Plus while any enabled Plus account exists. ignore is stored as off."},
		{Name: "sticky-seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Sticky session lifetime in seconds. Default 300."},
		{Name: "model-cooldown-minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "Minutes to skip an account and its access token after the upstream says the model is not supported for a ChatGPT account. 0 disables. Default 1440. Maximum 10080. The skip is saved beside stats-file and survives restart."},
		{Name: "stats-file", Type: pluginapi.ConfigFieldTypeString, Description: "JSON file for hourly request and token buckets kept for 90 days. Empty disables persistence."},
		{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "Upstream model name plus optional client alias. The same alias with different names is one pool. An exact duplicate name and alias is skipped. fallback lists upstream names tried in order when fallback-retry is above 0."},
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
		if pool.contextLength == 0 && model.MaxContextLength > 0 {
			pool.contextLength = int64(model.MaxContextLength)
		}
		if len(pool.inputModalities) == 0 {
			pool.inputModalities = append([]string(nil), model.InputModalities...)
		}
		if len(pool.outputModalities) == 0 {
			pool.outputModalities = append([]string(nil), model.OutputModalities...)
		}
		if !pool.thinkingSet {
			pool.thinkingSupport = thinkingFromSpec(model)
			pool.thinkingSet = model.Thinking != nil
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
				ID:                        pool.clientID,
				Object:                    "model",
				OwnedBy:                   pluginID,
				Name:                      pool.advertisedName(),
				Description:               pool.describe(),
				DisplayName:               pool.display,
				ContextLength:             pool.contextLength,
				SupportedInputModalities:  append([]string(nil), pool.inputModalities...),
				SupportedOutputModalities: append([]string(nil), pool.outputModalities...),
				UserDefined:               true,
				Thinking:                  pool.thinking(),
			})
		}
	}
	return pluginapi.ModelResponse{Provider: pluginID, Models: models}
}

func (p modelPool) advertisedName() string {
	if len(p.names) > 1 {
		return p.alias
	}
	if len(p.names) == 1 {
		return p.names[0]
	}
	return p.alias
}

func (p modelPool) describe() string {
	if len(p.names) < 2 {
		return ""
	}
	return "upstream names: " + strings.Join(p.names, ", ")
}

func (p modelPool) thinking() *pluginapi.ThinkingSupport {
	if p.thinkingSet {
		return p.thinkingSupport
	}
	if len(p.names) == 0 {
		return nil
	}
	return thinkingFromCanonical(p.names[0])
}

func thinkingFromSpec(model modelSpec) *pluginapi.ThinkingSupport {
	if model.Thinking == nil {
		return nil
	}
	support := &pluginapi.ThinkingSupport{}
	if raw, ok := model.Thinking["levels"].([]any); ok {
		for _, item := range raw {
			level, ok := item.(string)
			if !ok {
				continue
			}
			level = strings.TrimSpace(level)
			if level == "" {
				continue
			}
			support.Levels = append(support.Levels, level)
		}
	}
	if value, ok := intFromMap(model.Thinking["min"]); ok {
		support.Min = value
	}
	if value, ok := intFromMap(model.Thinking["max"]); ok {
		support.Max = value
	}
	if value, ok := model.Thinking["zero-allowed"].(bool); ok {
		support.ZeroAllowed = value
	} else if len(support.Levels) > 0 {
		support.ZeroAllowed = true
	}
	if value, ok := model.Thinking["dynamic-allowed"].(bool); ok {
		support.DynamicAllowed = value
	} else if len(support.Levels) > 0 {
		support.DynamicAllowed = true
	}
	if value, ok := model.Thinking["disable-type"].(string); ok {
		support.DisableType = strings.TrimSpace(value)
	}
	return support
}

func intFromMap(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func thinkingFromCanonical(name string) *pluginapi.ThinkingSupport {
	info := registry.LookupStaticModelInfo(strings.TrimSpace(name))
	if info == nil || info.Thinking == nil || len(info.Thinking.Levels) == 0 {
		return nil
	}
	return &pluginapi.ThinkingSupport{
		Min:            info.Thinking.Min,
		Max:            info.Thinking.Max,
		ZeroAllowed:    info.Thinking.ZeroAllowed,
		DynamicAllowed: info.Thinking.DynamicAllowed,
		Levels:         append([]string(nil), info.Thinking.Levels...),
	}
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
	ready, cooling := splitCooldown(accounts, now)
	fallbackNames := fallbackModels(names, fallbacks)
	canFallback := cfg.fallbackRetry() > 0 && len(fallbackNames) > 0
	if len(ready) == 0 && !canFallback {
		if len(cooling) > 0 {
			return errorEnvelope("accounts_cooling", "codex accounts are in model cooldown", http.StatusServiceUnavailable), nil
		}
		return errorEnvelope("no_account", "no eligible codex account is available", http.StatusServiceUnavailable), nil
	}
	session := sessionID(req.Metadata, req.Headers)
	runtimeMu.Lock()
	ordered := planAccountsLocked(cfg, ready, session, now)
	plannedNames := planNamesLocked(alias, names, explicit)
	runtimeMu.Unlock()
	primaryNames := plannedNames
	if len(primaryNames) > 1 {
		primaryNames = primaryNames[:1]
	}
	attempts := buildAttempts(ordered, primaryNames, 1+cfg.retry(), suffix)
	var fallbackAttempts []attempt
	if canFallback {
		// Cooldown only means the primary model was refused, so cooled accounts may still serve a fallback name.
		pool := append(append([]account(nil), ordered...), sortAccounts(cooling, false)...)
		fallbackAttempts = buildAttempts(pool, fallbackNames, cfg.fallbackRetry(), suffix)
		for i := range fallbackAttempts {
			fallbackAttempts[i].fallback = true
		}
	}
	if len(attempts) == 0 && len(fallbackAttempts) == 0 {
		return errorEnvelope("no_account", "no eligible codex account is available", http.StatusServiceUnavailable), nil
	}
	prepared := preparedCall{
		callbackID: req.HostCallbackID,
		payload:    req.Payload,
		spanStart:  start,
		spanEnd:    end,
		attempts:   attempts,
		fallbacks:  fallbackAttempts,
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
	success      bool
	rewrite      error
	resp         hostHTTPResult
	status       int
	body         []byte
	tokens       []string
	index        string
	email        string
	noted        bool
	modelMissing bool
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

// runAttempts sends the primary model first. Fallback names are tried only after
// every primary attempt failed with a model error. A non-2xx reply has not reached
// the client yet, so only the request model changes; response bytes stay as-is.
func runAttempts(cfg pluginConfig, prepared preparedCall, stream bool) attemptResult {
	result := performAttempts(cfg, prepared, stream)
	if result.success || result.rewrite != nil || len(prepared.fallbacks) == 0 {
		return result
	}
	if !result.modelMissing && len(result.tokens) > 0 {
		return result
	}
	next := prepared
	next.attempts = prepared.fallbacks
	fallback := performAttempts(cfg, next, stream)
	if fallback.rewrite != nil || len(fallback.tokens) == 0 {
		return result
	}
	fallback.tokens = append(append([]string(nil), result.tokens...), fallback.tokens...)
	fallback.noted = fallback.noted || result.noted
	return fallback
}

// performAttempts sends the configured upstream model with the selected access token.
// A 2xx response is returned unchanged. A non-2xx response tries the next account.
// The ChatGPT model-not-supported error cools that account and access token.
func performAttempts(cfg pluginConfig, prepared preparedCall, stream bool) attemptResult {
	tokens := make([]string, 0, len(prepared.attempts))
	lastStatus := http.StatusBadGateway
	var lastBody []byte
	var lastResp hostHTTPResult
	modelMissing := false
	for _, at := range prepared.attempts {
		if !at.fallback && cooled(at.index, at.token, nowFn()) {
			modelMissing = true
			continue
		}
		tokens = append(tokens, at.token)
		resp, status, body, errPost := postModel(cfg, prepared, at.token, at.model, stream)
		if errPost != nil {
			return attemptResult{rewrite: errPost}
		}
		if status >= 200 && status < 300 {
			refused := chatgptModelUnsupported(body)
			if refused {
				markModelCooldown(at.index, at.token, cfg.modelCooldown(), cfg.StatsFile)
			}
			if stream && resp.streamID() == "" {
				lastStatus = http.StatusBadGateway
				lastBody = []byte("missing upstream stream id")
				lastResp = resp
				noteRequest(false, 0, 0, 0, at.email)
				continue
			}
			if cfg.RotationMode == "sticky" && !refused {
				bindSticky(prepared.session, at.index, cfg.stickySeconds())
			}
			if cfg.RotationMode == "round-robin" {
				pinRoundRobin(at.index, refused)
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
			markModelCooldown(at.index, at.token, cfg.modelCooldown(), cfg.StatsFile)
		}
		if modelUnavailable(body) {
			modelMissing = true
		}
		if cfg.RotationMode == "round-robin" {
			pinRoundRobin(at.index, true)
		}
	}
	return attemptResult{resp: lastResp, status: lastStatus, body: lastBody, tokens: tokens, noted: len(tokens) > 0, modelMissing: modelMissing}
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

// modelUnavailable reports an upstream refusal of the model itself, not of the request.
func modelUnavailable(body []byte) bool {
	if chatgptModelUnsupported(body) {
		return true
	}
	text := strings.ToLower(string(body))
	if strings.Contains(text, "model_not_found") {
		return true
	}
	return modelDoesNotExist.MatchString(text)
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

func splitCooldown(accounts []account, now time.Time) (ready, cooling []account) {
	for _, item := range accounts {
		if cooled(item.index, item.token, now) {
			cooling = append(cooling, item)
			continue
		}
		ready = append(ready, item)
	}
	return ready, cooling
}

func cooled(index, token string, now time.Time) bool {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if until, ok := runtime.cooldownIndex[index]; ok && index != "" && now.Before(until) {
		return true
	}
	key := tokenKey(token)
	if until, ok := runtime.cooldownToken[key]; ok && key != "" && now.Before(until) {
		return true
	}
	return false
}

func tokenKey(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum[:])
}

func markModelCooldown(index, token string, d time.Duration, statsFile string) {
	if d <= 0 || (index == "" && token == "") {
		return
	}
	until := nowFn().Add(d)
	runtimeMu.Lock()
	ensureCooldownMapsLocked()
	if index != "" {
		if prev, ok := runtime.cooldownIndex[index]; !ok || until.After(prev) {
			runtime.cooldownIndex[index] = until
		}
	}
	if key := tokenKey(token); key != "" {
		if prev, ok := runtime.cooldownToken[key]; !ok || until.After(prev) {
			runtime.cooldownToken[key] = until
		}
	}
	runtimeMu.Unlock()
	persistCooldowns(cooldownFilePath(statsFile))
}

func ensureCooldownMapsLocked() {
	if runtime.cooldownIndex == nil {
		runtime.cooldownIndex = map[string]time.Time{}
	}
	if runtime.cooldownToken == nil {
		runtime.cooldownToken = map[string]time.Time{}
	}
}

func cooldownFilePath(statsFile string) string {
	statsFile = strings.TrimSpace(statsFile)
	if statsFile == "" {
		return ""
	}
	return statsFile + ".cooldowns.json"
}

func loadCooldowns(path string) {
	if path == "" {
		runtimeMu.Lock()
		runtime.cooldownIndex = map[string]time.Time{}
		runtime.cooldownToken = map[string]time.Time{}
		runtimeMu.Unlock()
		return
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil || len(bytes.TrimSpace(raw)) == 0 {
		if errRead == nil || os.IsNotExist(errRead) {
			runtimeMu.Lock()
			runtime.cooldownIndex = map[string]time.Time{}
			runtime.cooldownToken = map[string]time.Time{}
			runtimeMu.Unlock()
		}
		return
	}
	var records []cooldownRecord
	if errUnmarshal := json.Unmarshal(raw, &records); errUnmarshal != nil {
		return
	}
	now := nowFn()
	runtimeMu.Lock()
	runtime.cooldownIndex = map[string]time.Time{}
	runtime.cooldownToken = map[string]time.Time{}
	for _, record := range records {
		if !now.Before(record.Until) {
			continue
		}
		if record.AuthIndex != "" {
			if prev, ok := runtime.cooldownIndex[record.AuthIndex]; !ok || record.Until.After(prev) {
				runtime.cooldownIndex[record.AuthIndex] = record.Until
			}
		}
		if record.TokenHash != "" {
			if prev, ok := runtime.cooldownToken[record.TokenHash]; !ok || record.Until.After(prev) {
				runtime.cooldownToken[record.TokenHash] = record.Until
			}
		}
	}
	runtimeMu.Unlock()
	persistCooldowns(path)
}

func persistCooldowns(path string) {
	if path == "" {
		return
	}
	cooldownFileMu.Lock()
	defer cooldownFileMu.Unlock()
	now := nowFn()
	runtimeMu.Lock()
	records := cooldownRecordsLocked(now)
	runtimeMu.Unlock()
	_ = writeCooldownFile(path, records)
}

func cooldownRecordsLocked(now time.Time) []cooldownRecord {
	ensureCooldownMapsLocked()
	out := make([]cooldownRecord, 0, len(runtime.cooldownIndex)+len(runtime.cooldownToken))
	for index, until := range runtime.cooldownIndex {
		if index == "" || !now.Before(until) {
			delete(runtime.cooldownIndex, index)
			continue
		}
		out = append(out, cooldownRecord{AuthIndex: index, Until: until, Reason: cooldownReasonUnsupported})
	}
	for hash, until := range runtime.cooldownToken {
		if hash == "" || !now.Before(until) {
			delete(runtime.cooldownToken, hash)
			continue
		}
		out = append(out, cooldownRecord{TokenHash: hash, Until: until, Reason: cooldownReasonUnsupported})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AuthIndex != out[j].AuthIndex {
			return out[i].AuthIndex < out[j].AuthIndex
		}
		return out[i].TokenHash < out[j].TokenHash
	})
	return out
}

func writeCooldownFile(path string, records []cooldownRecord) error {
	raw, errMarshal := json.Marshal(records)
	if errMarshal != nil {
		return errMarshal
	}
	dir := filepath.Dir(path)
	if errDir := os.MkdirAll(dir, 0o755); errDir != nil {
		return errDir
	}
	tmp, errCreate := os.CreateTemp(dir, ".ch-relay-vippro-cooldown-*")
	if errCreate != nil {
		return errCreate
	}
	tmpName := tmp.Name()
	if _, errWrite := tmp.Write(raw); errWrite != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return errWrite
	}
	if errClose := tmp.Close(); errClose != nil {
		_ = os.Remove(tmpName)
		return errClose
	}
	if errRename := os.Rename(tmpName, path); errRename != nil {
		_ = os.Remove(tmpName)
		return errRename
	}
	return nil
}

func clearSticky(index string) {
	if index == "" {
		return
	}
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	for session, bind := range runtime.sticky {
		if bind.authIndex == index {
			delete(runtime.sticky, session)
		}
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
	result := runAttempts(cfg, prepared, false)
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
	result := runAttempts(cfg, prepared, true)
	if result.rewrite != nil {
		return errorEnvelope("invalid_payload", result.rewrite.Error(), http.StatusBadRequest), nil
	}
	if result.success {
		used := append([]string(nil), result.tokens...)
		callbackID := prepared.callbackID
		protocol := prepared.protocol
		token := ""
		if len(used) > 0 {
			token = used[len(used)-1]
		}
		go forwardStream(callbackID, result.resp.streamID(), executorStreamID, protocol, used, result.email, result.index, token, cfg.modelCooldown(), cfg.StatsFile, cfg.RotationMode)
		return okEnvelope(map[string]any{
			"headers": responseHeaders(result.resp, "text/event-stream"),
		})
	}
	if !result.noted {
		noteRequest(false, 0, 0, 0, result.email)
	}
	return errorEnvelope("upstream_error", failureMessage(result.status, result.body, result.tokens), result.status), nil
}

func forwardStream(callbackID, httpStreamID, executorStreamID, protocol string, tokens []string, email, index, token string, cooldown time.Duration, statsFile, rotation string) {
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
	var scan []byte
	cooledStream := false
	noteUnsupported := func(part []byte) {
		if cooledStream || cooldown <= 0 || len(part) == 0 {
			return
		}
		scan = append(scan, part...)
		if len(scan) > 16384 {
			scan = append([]byte(nil), scan[len(scan)-16384:]...)
		}
		if chatgptModelUnsupported(scan) {
			cooledStream = true
			markModelCooldown(index, token, cooldown, statsFile)
			if rotation == "round-robin" {
				pinRoundRobin(index, true)
			}
			clearSticky(index)
		}
	}
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
		noteUnsupported(chunk.payload())
		if text := chunk.errText(); text != "" {
			noteUnsupported([]byte(text))
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
			return "", cached.plus, false
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
		plusFileCache.Store(cacheKey, cachedPlan{plus: plus})
	}
	return token, plus, true
}

type cachedPlan struct {
	plus bool
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
	return rotateAccounts(primary, start)
}

func accountIndex(accounts []account, index string) int {
	for i, item := range accounts {
		if item.index == index {
			return i
		}
	}
	return -1
}

func planNamesLocked(alias string, names []string, explicit bool) []string {
	names = append([]string(nil), names...)
	if explicit || len(names) < 2 {
		return names
	}
	if runtime.nameCursor == nil {
		runtime.nameCursor = map[string]int{}
	}
	start := runtime.nameCursor[alias] % len(names)
	runtime.nameCursor[alias] = (start + 1) % len(names)
	return rotateStrings(names, start)
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

// fallbackModels joins the fallback rows in config order and skips the primary names.
func fallbackModels(names []string, fallbacks [][]string) []string {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		seen[name] = struct{}{}
	}
	var out []string
	for _, row := range fallbacks {
		for _, name := range row {
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
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
// The caller caps the slice at its retry budget. Each account/name pair is used once.
func buildAttempts(accounts []account, names []string, budget int, suffix string) []attempt {
	accountCount := len(accounts)
	nameCount := len(names)
	if accountCount == 0 || nameCount == 0 || budget < 1 {
		return nil
	}
	used := make(map[string]struct{}, accountCount*nameCount)
	out := make([]attempt, 0, budget)
	add := func(item account, nameIndex int, repeat bool) {
		if len(out) >= budget {
			return
		}
		name := names[nameIndex]
		key := item.index + "\x00" + name
		if !repeat {
			if _, ok := used[key]; ok {
				return
			}
			used[key] = struct{}{}
		}
		out = append(out, attempt{index: item.index, token: item.token, email: item.email, model: name + suffix})
	}
	// The first name spends the budget across accounts. Later names are used only
	// when that budget is not exhausted, which is how fallback lists walk in order.
	for step := 0; step < accountCount && len(out) < budget; step++ {
		add(accounts[step], 0, false)
	}
	if nameCount == 1 {
		for len(out) < budget {
			add(accounts[len(out)%accountCount], 0, true)
		}
	}
	for nameIndex := 1; nameIndex < nameCount && len(out) < budget; nameIndex++ {
		for accountIndex := 0; accountIndex < accountCount && len(out) < budget; accountIndex++ {
			add(accounts[accountIndex], nameIndex, false)
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
