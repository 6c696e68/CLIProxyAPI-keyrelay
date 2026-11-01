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
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/codex"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/openai"
	oaichat "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/openai/chat-completions"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"gopkg.in/yaml.v3"
)

const (
	pluginID                          = "ch-relay-vippro"
	defaultBaseURL                    = "https://7656b154.linhtd.com/v1"
	defaultRetry                      = 3
	defaultStickySeconds              = 300
	defaultModelCooldownMinutes       = 1440
	defaultModelOutageCooldownMinutes = 5
	maxExtraRetries                   = 32
	maxModelCooldownMinutes           = 10080
	maxFallbackRetries                = 3
	maxStickySeconds                  = 86400
	redactedToken                     = "[REDACTED]"
	cooldownReasonUnsupported         = "chatgpt_model_unsupported"
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
	Enabled                    bool              `yaml:"enabled"`
	Priority                   int               `yaml:"priority"`
	BaseURL                    string            `yaml:"base-url"`
	Prefix                     string            `yaml:"prefix"`
	Headers                    map[string]string `yaml:"headers"`
	RequestRetry               *int              `yaml:"request-retry"`
	FallbackRetry              *int              `yaml:"fallback-retry"`
	RotationMode               string            `yaml:"rotation-mode"`
	PlusPreference             string            `yaml:"plus-preference"`
	FreeModels                 []string          `yaml:"free-models"`
	StickySeconds              *int              `yaml:"sticky-seconds"`
	ModelCooldownMinutes       *int              `yaml:"model-cooldown-minutes"`
	ModelOutageCooldownMinutes *int              `yaml:"model-outage-cooldown-minutes"`
	StatsFile                  string            `yaml:"stats-file"`
	// UpstreamCache is deprecated and ignored. Production configs still set it.
	UpstreamCache bool `yaml:"upstream-cache"`
	// DisablePromptCache forces a fresh upstream prompt cache entry on every attempt.
	DisablePromptCache bool `yaml:"disable-prompt-cache"`
	// ServiceTier fills service_tier when the client sends none, auto, or default.
	ServiceTier string      `yaml:"service-tier"`
	Models      []modelSpec `yaml:"models"`
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
	ServiceTier            string         `yaml:"service-tier,omitempty"`
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

// penalty is a soft, in-memory demotion. set is when it was last recorded, so a
// success that started earlier cannot clear it.
type penalty struct {
	until time.Time
	set   time.Time
}

type runtimeState struct {
	cfg           pluginConfig
	configured    bool
	pinnedIndex   string
	pinFailed     bool
	stickyCursor  int
	nameCursor    map[string]int
	sticky        map[string]stickyBind
	lastUsed      map[string]time.Time
	cooldownIndex map[string]time.Time // key: auth index + "\x00" + base model
	cooldownToken map[string]time.Time // key: token hash + "\x00" + base model
	penaltyIndex  map[string]penalty
	penaltyToken  map[string]penalty
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
	Model     string    `json:"model,omitempty"`
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
		lastUsed:      map[string]time.Time{},
		cooldownIndex: map[string]time.Time{},
		cooldownToken: map[string]time.Time{},
		penaltyIndex:  map[string]penalty{},
		penaltyToken:  map[string]penalty{},
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
	cfg.FreeModels = normalizeFreeModels(cfg.FreeModels)

	cfg.RequestRetry = clampInt(cfg.RequestRetry, defaultRetry, 0, maxExtraRetries)
	cfg.FallbackRetry = clampInt(cfg.FallbackRetry, 0, 0, maxFallbackRetries)
	cfg.StickySeconds = clampInt(cfg.StickySeconds, defaultStickySeconds, 0, maxStickySeconds)
	cfg.ModelCooldownMinutes = clampInt(cfg.ModelCooldownMinutes, defaultModelCooldownMinutes, 0, maxModelCooldownMinutes)
	cfg.ModelOutageCooldownMinutes = clampInt(cfg.ModelOutageCooldownMinutes, defaultModelOutageCooldownMinutes, 0, maxModelCooldownMinutes)
	cfg.StatsFile = strings.TrimSpace(cfg.StatsFile)
	tier, errTier := normalizeServiceTier(cfg.ServiceTier, false)
	if errTier != nil {
		return pluginConfig{}, errTier
	}
	cfg.ServiceTier = tier

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
		tier, errTier := normalizeServiceTier(model.ServiceTier, true)
		if errTier != nil {
			return nil, fmt.Errorf("model %s: %w", name, errTier)
		}
		model.ServiceTier = tier
		out = append(out, model)
	}
	return out, nil
}

// normalizeFreeModels trims names, drops empty entries, and dedupes case-insensitively
// while keeping the first-seen order.
func normalizeFreeModels(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		name := strings.TrimSpace(item)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

// modelOutageCooldown is the shorter skip used when a refusal was not proven on
// another account. It never exceeds the full model cooldown, so a disabled full
// cooldown disables both.
func (c pluginConfig) modelOutageCooldown() time.Duration {
	full := c.modelCooldown()
	if full <= 0 {
		return 0
	}
	minutes := defaultModelOutageCooldownMinutes
	if c.ModelOutageCooldownMinutes != nil {
		minutes = *c.ModelOutageCooldownMinutes
	}
	outage := time.Duration(minutes) * time.Minute
	if outage > full {
		return full
	}
	return outage
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
		{Name: "free-models", Type: pluginapi.ConfigFieldTypeArray, Description: "Upstream model names, not aliases, that may also use free and other non-Plus Codex accounts. Other models keep plus-preference. No effect when plus-preference is off."},
		{Name: "sticky-seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Sticky session lifetime in seconds. Default 300."},
		{Name: "model-cooldown-minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "Minutes to skip an account and its access token for one model after the upstream says that model is not supported for a ChatGPT account. Other models stay available. 0 disables. Default 1440. Maximum 10080. The skip is saved beside stats-file and survives restart."},
		{Name: "model-outage-cooldown-minutes", Type: pluginapi.ConfigFieldTypeInteger, Description: "Minutes to skip an account and its access token for one model after a refusal that no other account proved, capped by model-cooldown-minutes; 0 remembers nothing and the default is 5."},
		{Name: "stats-file", Type: pluginapi.ConfigFieldTypeString, Description: "JSON file for hourly request and token buckets kept for 90 days. Empty disables persistence."},
		{Name: "service-tier", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"", "priority", "ultrafast"}, Description: "Force a speed tier: set service_tier when the client sends none, auto, or default. priority is Fast; fast is stored as priority. A client tier such as priority, ultrafast, or flex is kept. A model service-tier overrides this; off disables it for that model. Empty disables."},
		{Name: "disable-prompt-cache", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Force an upstream prompt-cache miss: a random prompt_cache_key and a request tag at the end of the system text on every attempt. Default false."},
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
				// The relay forwards service_tier to the Codex backend, so the
				// host keeps the template speed tiers (Fast, Ultrafast) in the
				// Codex client catalog for these models.
				SupportedParameters: []string{"service_tier"},
				Thinking:            pool.thinking(),
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
	name = strings.TrimSpace(name)
	info := registry.LookupStaticModelInfo(name)
	if info == nil || info.Thinking == nil || len(info.Thinking.Levels) == 0 {
		return nil
	}
	return &pluginapi.ThinkingSupport{
		Min:            info.Thinking.Min,
		Max:            info.Thinking.Max,
		ZeroAllowed:    info.Thinking.ZeroAllowed,
		DynamicAllowed: info.Thinking.DynamicAllowed,
		Levels:         unionLevels(info.Thinking.Levels, codexClientLevels(name)),
	}
}

// unionLevels appends the levels in extra that base lacks, keeping base order.
func unionLevels(base, extra []string) []string {
	out := append([]string(nil), base...)
	seen := make(map[string]struct{}, len(base))
	for _, level := range base {
		seen[strings.ToLower(strings.TrimSpace(level))] = struct{}{}
	}
	for _, level := range extra {
		key := strings.ToLower(strings.TrimSpace(level))
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, strings.TrimSpace(level))
	}
	return out
}

var (
	codexClientLevelsOnce sync.Once
	codexClientLevelsByID map[string][]string
)

// codexClientLevels returns the reasoning efforts the embedded Codex client
// catalog lists for a model slug. The static models.json stops at "max" while
// the Codex catalog also carries "ultra"; the relay accepts whatever the Codex
// backend accepts, so the advertised levels follow the Codex catalog.
func codexClientLevels(slug string) []string {
	codexClientLevelsOnce.Do(func() {
		codexClientLevelsByID = map[string][]string{}
		var payload struct {
			Models []struct {
				Slug   string `json:"slug"`
				Levels []struct {
					Effort string `json:"effort"`
				} `json:"supported_reasoning_levels"`
			} `json:"models"`
		}
		if err := json.Unmarshal(registry.GetCodexClientModelsJSON(), &payload); err != nil {
			return
		}
		for _, model := range payload.Models {
			levels := make([]string, 0, len(model.Levels))
			for _, level := range model.Levels {
				if effort := strings.TrimSpace(level.Effort); effort != "" {
					levels = append(levels, effort)
				}
			}
			codexClientLevelsByID[strings.ToLower(strings.TrimSpace(model.Slug))] = levels
		}
	})
	return codexClientLevelsByID[strings.ToLower(strings.TrimSpace(slug))]
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
	accounts, hasEnabledPlus, errAccounts := loadEligible(req.HostCallbackID, now)
	if errAccounts != nil {
		return errorEnvelope("auth_unavailable", "codex auth list failed", http.StatusBadGateway), nil
	}
	allAccounts := accounts
	session := sessionID(req.Metadata, req.Headers)
	primaryNames := []string(nil)
	primaryModel := ""
	planPrimary := func() {
		runtimeMu.Lock()
		planned := planNamesLocked(alias, names, explicit)
		runtimeMu.Unlock()
		if len(planned) > 1 {
			planned = planned[:1]
		}
		primaryNames = planned
		if len(planned) > 0 {
			primaryModel = planned[0]
		}
	}
	planCfg := cfg
	freePrimary := false
	plannedPrimary := false
	if len(cfg.FreeModels) > 0 && cfg.PlusPreference != "off" {
		planPrimary()
		plannedPrimary = true
		freePrimary = cfg.freeModel(primaryModel)
	}
	if freePrimary {
		// A listed primary name may use every eligible account, as plus-preference off would.
		planCfg.PlusPreference = "off"
	}
	accounts, errCode, errMessage := restrictAccounts(cfg, freePrimary, accounts, hasEnabledPlus)
	if errCode != "" {
		return errorEnvelope(errCode, errMessage, http.StatusServiceUnavailable), nil
	}
	if len(accounts) == 0 {
		return errorEnvelope("no_account", "no eligible codex account is available", http.StatusServiceUnavailable), nil
	}
	if !plannedPrimary {
		planPrimary()
	}
	ready, cooling := splitCooldown(accounts, primaryModel, now)
	fallbackNames := fallbackModels(names, fallbacks)
	canFallback := cfg.fallbackRetry() > 0 && len(fallbackNames) > 0
	if len(ready) == 0 && !canFallback {
		if len(cooling) > 0 {
			return errorEnvelope("accounts_cooling", "codex accounts are in model cooldown", http.StatusServiceUnavailable), nil
		}
		return errorEnvelope("no_account", "no eligible codex account is available", http.StatusServiceUnavailable), nil
	}
	runtimeMu.Lock()
	healthy, penalized := splitPenaltyLocked(ready, now)
	healthy = highestPriority(healthy)
	ordered := planAccountsLocked(planCfg, healthy, priorityAtLeast(penalized, healthy), session, now)
	runtimeMu.Unlock()
	attempts := buildAttempts(ordered, primaryNames, 1+cfg.retry(), suffix, func(item account, name string) bool {
		return planCfg.allowsAccount(item, name, hasEnabledPlus)
	})
	var fallbackAttempts []attempt
	if canFallback {
		// Cooldown only means the primary model was refused, so cooled accounts may still serve a fallback name.
		basePool := append(append([]account(nil), ordered...), sortAccounts(priorityAtLeast(cooling, ordered), false)...)
		pool := basePool
		inBase := make(map[string]struct{}, len(basePool))
		for _, item := range basePool {
			inBase[item.index] = struct{}{}
		}
		if cfg.PlusPreference != "off" {
			for _, name := range fallbackNames {
				if !cfg.freeModel(name) {
					continue
				}
				// A listed fallback name may use accounts that the primary pool dropped.
				extra := make([]account, 0, len(allAccounts))
				for _, item := range allAccounts {
					if _, ok := inBase[item.index]; ok {
						continue
					}
					extra = append(extra, item)
				}
				pool = append(pool, sortAccounts(extra, false)...)
				break
			}
		}
		fallbackAttempts = buildAttempts(pool, fallbackNames, cfg.fallbackRetry(), suffix, func(item account, name string) bool {
			if _, ok := inBase[item.index]; !ok && !cfg.freeModel(name) {
				return false
			}
			return cfg.allowsAccount(item, name, hasEnabledPlus)
		})
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
	model        string
	email        string
	noted        bool
	modelMissing bool
}

// modelFormat returns the provider format the host thinking pipeline expects.
func modelFormat(path string) string {
	if path == "/responses" {
		return "codex"
	}
	return "openai"
}

// buildModelBody rewrites the model to its bare name and converts a thinking
// suffix through the host pipeline. Local validation never fails a request: a
// rejected suffix falls back to the user-defined apply, then to the bare body.
func buildModelBody(prepared preparedCall, model string) ([]byte, error) {
	bare, suffix := splitSuffix(model)
	body, errBody := rewriteModel(prepared.payload, prepared.spanStart, prepared.spanEnd, bare)
	if errBody != nil {
		return nil, errBody
	}
	if suffix == "" {
		return body, nil
	}
	format := modelFormat(prepared.path)
	applied, errThinking := thinking.ApplyThinking(body, model, format, format, format)
	if errThinking != nil {
		applied, errThinking = thinking.ApplyThinkingWithModelInfo(body, nil, model, format, format, format, &registry.ModelInfo{ID: bare, UserDefined: true})
		if errThinking != nil {
			return body, nil
		}
	}
	return applied, nil
}

// normalizeServiceTier lowercases a configured tier and stores fast as
// priority. off is only valid on a model, where it disables the plugin tier.
func normalizeServiceTier(raw string, allowOff bool) (string, error) {
	tier := strings.ToLower(strings.TrimSpace(raw))
	switch tier {
	case "", "priority", "ultrafast":
		return tier, nil
	case "fast":
		return "priority", nil
	case "off":
		if allowOff {
			return tier, nil
		}
	}
	return "", fmt.Errorf("invalid service-tier %q", raw)
}

// serviceTierFor returns the forced tier for an upstream model name. The first
// model entry with that name and a tier wins; otherwise the plugin tier applies.
func (c pluginConfig) serviceTierFor(model string) string {
	bare, _ := splitSuffix(model)
	for _, spec := range c.Models {
		if spec.ServiceTier == "" || !strings.EqualFold(spec.Name, bare) {
			continue
		}
		if spec.ServiceTier == "off" {
			return ""
		}
		return spec.ServiceTier
	}
	return c.ServiceTier
}

// applyServiceTier sets service_tier when the body has none, auto, or default.
// Any other client tier is kept, and an empty tier leaves the body unchanged.
func applyServiceTier(body []byte, tier string) ([]byte, error) {
	if tier == "" {
		return body, nil
	}
	current := gjson.GetBytes(body, "service_tier")
	if current.Exists() && current.Type != gjson.Null {
		switch strings.ToLower(strings.TrimSpace(current.String())) {
		case "", "auto", "default":
		default:
			return body, nil
		}
	}
	return sjson.SetBytes(body, "service_tier", tier)
}

// promptCacheTag returns a fresh 32-character lowercase hex tag.
func promptCacheTag() (string, error) {
	var raw [16]byte
	if _, errRead := rand.Read(raw[:]); errRead != nil {
		return "", errRead
	}
	return hex.EncodeToString(raw[:]), nil
}

// applyPromptCacheMiss makes one upstream attempt start a new prompt cache
// entry. The upstream ignores prompt_cache_options, so the same tag goes into
// the top-level prompt_cache_key and at the end of the system text. Each call
// returns a new tag, which is why every attempt gets its own cache entry.
func applyPromptCacheMiss(path string, body []byte) ([]byte, error) {
	tag, errTag := promptCacheTag()
	if errTag != nil {
		return nil, errTag
	}
	tagged, errKey := sjson.SetBytes(body, "prompt_cache_key", tag)
	if errKey != nil {
		return nil, errKey
	}
	tagText := "\n\n[request " + tag + "]"
	if path == "/responses" {
		instructions := gjson.GetBytes(tagged, "instructions")
		switch {
		case instructions.Type == gjson.String && instructions.String() != "":
			tagged, errKey = sjson.SetBytes(tagged, "instructions", instructions.String()+tagText)
		case instructions.Type == gjson.String || !instructions.Exists():
			tagged, errKey = sjson.SetBytes(tagged, "instructions", "[request "+tag+"]")
		}
		if errKey != nil {
			return nil, errKey
		}
		return tagged, nil
	}
	index, text := -1, ""
	messages := gjson.GetBytes(tagged, "messages")
	if !messages.IsArray() {
		return tagged, nil
	}
	messages.ForEach(func(key, value gjson.Result) bool {
		role := value.Get("role")
		if role.Type != gjson.String || (role.String() != "system" && role.String() != "developer") {
			return true
		}
		content := value.Get("content")
		if content.Type != gjson.String {
			return true
		}
		index = int(key.Int())
		text = content.String()
		return false
	})
	if index < 0 {
		return tagged, nil
	}
	tagged, errKey = sjson.SetBytes(tagged, "messages."+strconv.Itoa(index)+".content", text+tagText)
	if errKey != nil {
		return nil, errKey
	}
	return tagged, nil
}

// postUpstream sends one built body and reads the body of a non-2xx stream reply.
func postUpstream(cfg pluginConfig, prepared preparedCall, token string, body []byte, stream bool) (hostHTTPResult, int, []byte) {
	base := strings.TrimSuffix(cfg.BaseURL, "/")
	resp, errDo := hostDo(prepared.callbackID, base+prepared.path, upstreamHeaders(cfg, prepared.client, prepared.session, token, stream), body, stream)
	if errDo != nil {
		return hostHTTPResult{}, http.StatusBadGateway, []byte(errDo.Error())
	}
	status := resp.status()
	respBody := resp.body()
	if stream && (status < 200 || status >= 300) {
		if id := resp.streamID(); id != "" {
			if len(respBody) == 0 {
				respBody = readStreamBody(prepared.callbackID, id)
			} else {
				_ = closeHostStream(pluginabi.MethodHostHTTPStreamClose, prepared.callbackID, id, "")
			}
		}
	}
	return resp, status, respBody
}

// postModel builds one attempt body and sends it. The built body is returned so
// an effort retry can keep every field the first attempt had.
func postModel(cfg pluginConfig, prepared preparedCall, token, model string, stream bool) (hostHTTPResult, int, []byte, []byte, error) {
	body, errBody := buildModelBody(prepared, model)
	if errBody != nil {
		return hostHTTPResult{}, 0, nil, nil, errBody
	}
	tiered, errTier := applyServiceTier(body, cfg.serviceTierFor(model))
	if errTier != nil {
		return hostHTTPResult{}, 0, nil, nil, errTier
	}
	body = tiered
	if cfg.DisablePromptCache {
		tagged, errCache := applyPromptCacheMiss(prepared.path, body)
		if errCache != nil {
			return hostHTTPResult{}, 0, nil, nil, errCache
		}
		body = tagged
	}
	resp, status, respBody := postUpstream(cfg, prepared, token, body, stream)
	return resp, status, respBody, body, nil
}

// effortRetryTarget reads an upstream reasoning-effort rejection and returns the
// level to retry with, using the host clamp rule and the upstream's own list.
func effortRetryTarget(body []byte) (string, bool) {
	var reply struct {
		Error struct {
			Param   string `json:"param"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &reply) != nil {
		return "", false
	}
	if reply.Error.Param != "reasoning.effort" && reply.Error.Param != "reasoning_effort" {
		return "", false
	}
	if reply.Error.Code != "unsupported_value" && reply.Error.Code != "invalid_value" {
		return "", false
	}
	quoted := quotedTokens(reply.Error.Message)
	if len(quoted) == 0 {
		return "", false
	}
	rejected := quoted[0]
	var supported []string
	if idx := strings.Index(strings.ToLower(reply.Error.Message), "supported values are:"); idx >= 0 {
		supported = quotedTokens(reply.Error.Message[idx+len("supported values are:"):])
	}
	target, ok := thinking.ClampLevelToSupported(rejected, supported)
	if !ok {
		if thinking.HasLevel(supported, "medium") {
			target = "medium"
		} else {
			target, ok = thinking.ClampLevelToSupported("medium", supported)
			if !ok {
				return "", false
			}
		}
	}
	if target == "" || strings.EqualFold(target, rejected) {
		return "", false
	}
	return target, true
}

// quotedTokens returns the single- or double-quoted tokens in text.
func quotedTokens(text string) []string {
	var out []string
	for i := 0; i < len(text); i++ {
		quote := text[i]
		if quote != '\'' && quote != '"' {
			continue
		}
		end := strings.IndexByte(text[i+1:], quote)
		if end < 0 {
			break
		}
		out = append(out, text[i+1:i+1+end])
		i += end + 1
	}
	return out
}

// effortRetryBody rebuilds the first attempt body with the clamped effort.
func effortRetryBody(body []byte, model, format, target string) ([]byte, bool) {
	bare, _ := splitSuffix(model)
	applied, errApply := thinking.ApplyThinkingWithModelInfo(body, nil, bare+"("+target+")", format, format, format, &registry.ModelInfo{ID: bare, UserDefined: true})
	if errApply != nil {
		return nil, false
	}
	return applied, true
}

// runAttempts sends the primary model first. Fallback names are tried only after
// every primary attempt failed with a model error. A non-2xx reply has not reached
// the client yet, so only the request model changes; response bytes stay as-is.
func runAttempts(cfg pluginConfig, prepared preparedCall, stream bool) attemptResult {
	spent := spentAttempts{}
	result := performAttempts(cfg, prepared, stream, spent)
	if result.success || result.rewrite != nil || len(prepared.fallbacks) == 0 {
		return result
	}
	if !result.modelMissing && len(result.tokens) > 0 {
		return result
	}
	next := prepared
	next.attempts = prepared.fallbacks
	fallback := performAttempts(cfg, next, stream, spent)
	if fallback.rewrite != nil || len(fallback.tokens) == 0 {
		return result
	}
	fallback.tokens = append(append([]string(nil), result.tokens...), fallback.tokens...)
	fallback.noted = fallback.noted || result.noted
	return fallback
}

// spentAttempts remembers what one request already burned: whole accounts after a
// 429/401/402/403, and (account, model) pairs after a model refusal.
type spentAttempts map[string]struct{}

func (s spentAttempts) skip(at attempt) bool {
	for _, key := range spentKeys(at, true) {
		if _, ok := s[key]; ok {
			return true
		}
	}
	return false
}

func (s spentAttempts) markAccount(at attempt) {
	for _, key := range spentKeys(at, false) {
		s[key] = struct{}{}
	}
}

func (s spentAttempts) markModel(at attempt) {
	base, _ := splitSuffix(at.model)
	s["m\x00"+at.index+"\x00"+base] = struct{}{}
}

func spentKeys(at attempt, withModel bool) []string {
	keys := []string{"a\x00" + at.index}
	if hash := tokenKey(at.token); hash != "" {
		keys = append(keys, "t\x00"+hash)
	}
	if withModel {
		base, _ := splitSuffix(at.model)
		keys = append(keys, "m\x00"+at.index+"\x00"+base)
	}
	return keys
}

// clientRejected reports a 4xx that blames the request, so another account cannot help.
func clientRejected(status int, body []byte) bool {
	switch status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		return !modelUnavailable(body)
	}
	return false
}

// performAttempts sends the configured upstream model with the selected access token.
// A 2xx response is returned unchanged. A non-2xx response tries the next account,
// except client errors, which return at once. The ChatGPT model-not-supported error
// cools that account and access token for that model only. 429, 401, 402 and 403
// demote the account softly and are never retried on it within the request.
func performAttempts(cfg pluginConfig, prepared preparedCall, stream bool, spent spentAttempts) attemptResult {
	tokens := make([]string, 0, len(prepared.attempts))
	lastStatus := http.StatusBadGateway
	var lastBody []byte
	var lastResp hostHTTPResult
	modelMissing := false
	var refusals []attempt
	proven := map[string]struct{}{}
	// A refusal is only proven when the same phase got a non-refused 2xx for the
	// same base model on another attempt. Unproven refusals get the outage skip.
	settleRefusals := func() {
		full := cfg.modelCooldown()
		outage := cfg.modelOutageCooldown()
		for _, item := range refusals {
			d := outage
			if _, ok := proven[baseModel(item.model)]; ok {
				d = full
			}
			markModelCooldown(item.index, item.token, item.model, d, cfg.StatsFile)
		}
	}
	for _, at := range prepared.attempts {
		if spent.skip(at) {
			continue
		}
		if cooled(at.index, at.token, at.model, nowFn()) {
			modelMissing = true
			continue
		}
		tokens = append(tokens, at.token)
		started := nowFn()
		markUsed(at.index, started)
		resp, status, body, sentBody, errPost := postModel(cfg, prepared, at.token, at.model, stream)
		if errPost != nil {
			settleRefusals()
			return attemptResult{rewrite: errPost}
		}
		if status == http.StatusBadRequest {
			if target, ok := effortRetryTarget(body); ok {
				if retryBody, okBuild := effortRetryBody(sentBody, at.model, modelFormat(prepared.path), target); okBuild {
					resp, status, body = postUpstream(cfg, prepared, at.token, retryBody, stream)
				}
			}
		}
		if status >= 200 && status < 300 {
			refused := chatgptModelRefused(body)
			if refused {
				refusals = append(refusals, at)
				spent.markModel(at)
			} else {
				proven[baseModel(at.model)] = struct{}{}
			}
			if stream && resp.streamID() == "" {
				lastStatus = http.StatusBadGateway
				lastBody = []byte("missing upstream stream id")
				lastResp = resp
				noteRequest(false, 0, 0, 0, at.email)
				continue
			}
			demoted := false
			if !refused {
				demoted = settlePenalty(at.index, at.token, started)
			}
			if cfg.RotationMode == "sticky" && !refused && !demoted {
				bindSticky(prepared.session, at.index, cfg.stickySeconds())
			}
			if cfg.RotationMode == "round-robin" && !demoted {
				pinRoundRobin(at.index, refused)
			}
			settleRefusals()
			return attemptResult{
				success: true,
				resp:    resp,
				status:  status,
				body:    body,
				tokens:  append([]string(nil), tokens...),
				index:   at.index,
				model:   at.model,
				email:   at.email,
			}
		}
		if status != 0 {
			lastStatus = status
		}
		lastResp = resp
		lastBody = body
		noteRequest(false, 0, 0, 0, at.email)
		if clientAborted(status, body) || clientRejected(status, body) {
			settleRefusals()
			return attemptResult{resp: lastResp, status: lastStatus, body: lastBody, tokens: tokens, email: at.email, noted: true}
		}
		if chatgptModelUnsupported(body) {
			refusals = append(refusals, at)
		}
		if modelUnavailable(body) {
			modelMissing = true
			spent.markModel(at)
		}
		if penalizeAttempt(at, status, resp.headers(), body, nowFn()) {
			spent.markAccount(at)
		}
		if cfg.RotationMode == "round-robin" {
			pinRoundRobin(at.index, true)
		}
	}
	settleRefusals()
	return attemptResult{resp: lastResp, status: lastStatus, body: lastBody, tokens: tokens, noted: len(tokens) > 0, modelMissing: modelMissing}
}

// stickRoundRobinLocked stays on the pinned account while it succeeds and moves to
// its successor after a failure. A pinned account that left the pool (cooled,
// demoted, unavailable) hands over to the first account sorting after it.
func stickRoundRobinLocked(primary []account) []account {
	if len(primary) == 0 {
		return nil
	}
	start := 0
	if runtime.pinnedIndex != "" {
		i := accountIndex(primary, runtime.pinnedIndex)
		switch {
		case i >= 0 && !runtime.pinFailed:
			start = i
		case i >= 0:
			start = (i + 1) % len(primary)
			runtime.pinFailed = false
			runtime.pinnedIndex = primary[start].index
		default:
			for j, item := range primary {
				if item.index > runtime.pinnedIndex {
					start = j
					break
				}
			}
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

// refusalFieldPaths are the only 2xx JSON fields that may carry a refusal.
// Reply content such as choices[].message.content is never matched.
var refusalFieldPaths = [][]string{
	{"detail"},
	{"message"},
	{"error", "message"},
	{"error", "detail"},
	{"response", "error", "message"},
}

// chatgptModelRefused reports a refusal in a 2xx body. JSON objects and complete
// SSE data lines are checked field by field. Anything else, such as an error
// chunk, keeps the plain substring check.
func chatgptModelRefused(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return false
	}
	if trimmed[0] == '{' {
		obj := map[string]any{}
		if json.Unmarshal(trimmed, &obj) != nil {
			return false
		}
		return refusalInObject(obj)
	}
	if trimmed[0] == '[' || trimmed[0] == '"' {
		return false
	}
	if objects, sawData := sseRefusalObjects(trimmed); sawData {
		for _, obj := range objects {
			if refusalInObject(obj) {
				return true
			}
		}
		return false
	}
	return chatgptModelUnsupported(body)
}

// sseRefusalObjects parses every data: line that holds a JSON object. sawData is
// true for an SSE chunk even when the last line is still incomplete.
func sseRefusalObjects(body []byte) (objects []map[string]any, sawData bool) {
	for len(body) > 0 {
		line := body
		if idx := bytes.IndexByte(body, '\n'); idx >= 0 {
			line = body[:idx]
			body = body[idx+1:]
		} else {
			body = nil
		}
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		sawData = true
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || payload[0] != '{' {
			continue
		}
		var obj map[string]any = map[string]any{}
		if json.Unmarshal(payload, &obj) == nil {
			objects = append(objects, obj)
		}
	}
	return objects, sawData
}

func refusalInObject(obj map[string]any) bool {
	for _, path := range refusalFieldPaths {
		value, ok := nestedField(obj, path)
		if !ok {
			continue
		}
		if text, ok := value.(string); ok && chatgptModelUnsupported([]byte(text)) {
			return true
		}
	}
	return false
}

func nestedField(obj map[string]any, path []string) (any, bool) {
	var current any = obj
	for _, key := range path {
		node, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = node[key]
		if !ok {
			return nil, false
		}
	}
	return current, true
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

const (
	defaultPenalty = 10 * time.Minute
	minPenalty     = 30 * time.Second
	maxPenalty     = 7 * 24 * time.Hour
)

// baseModel drops the thinking suffix so a cooldown covers every effort level.
func baseModel(model string) string {
	base, _ := splitSuffix(model)
	return base
}

func cooldownKey(owner, model string) string {
	return owner + "\x00" + baseModel(model)
}

func splitCooldown(accounts []account, model string, now time.Time) (ready, cooling []account) {
	for _, item := range accounts {
		if cooled(item.index, item.token, model, now) {
			cooling = append(cooling, item)
			continue
		}
		ready = append(ready, item)
	}
	return ready, cooling
}

// cooled reports a model refusal still in force for this account or token and model.
func cooled(index, token, model string, now time.Time) bool {
	if baseModel(model) == "" {
		return false
	}
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if until, ok := runtime.cooldownIndex[cooldownKey(index, model)]; ok && index != "" && now.Before(until) {
		return true
	}
	key := tokenKey(token)
	if until, ok := runtime.cooldownToken[cooldownKey(key, model)]; ok && key != "" && now.Before(until) {
		return true
	}
	return false
}

// cooledAny reports a model refusal in force for any model.
func cooledAny(index, token string, now time.Time) bool {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	anyCooled := func(m map[string]time.Time, owner string) bool {
		if owner == "" {
			return false
		}
		prefix := owner + "\x00"
		for key, until := range m {
			if strings.HasPrefix(key, prefix) && now.Before(until) {
				return true
			}
		}
		return false
	}
	return anyCooled(runtime.cooldownIndex, index) || anyCooled(runtime.cooldownToken, tokenKey(token))
}

func tokenKey(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum[:])
}

func markModelCooldown(index, token, model string, d time.Duration, statsFile string) {
	if d <= 0 || (index == "" && token == "") || baseModel(model) == "" {
		return
	}
	until := nowFn().Add(d)
	runtimeMu.Lock()
	ensureCooldownMapsLocked()
	if index != "" {
		key := cooldownKey(index, model)
		if prev, ok := runtime.cooldownIndex[key]; !ok || until.After(prev) {
			runtime.cooldownIndex[key] = until
		}
	}
	if hash := tokenKey(token); hash != "" {
		key := cooldownKey(hash, model)
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

// penalizeAttempt records a soft penalty for a failed attempt and reports whether
// the account must not be sent to again in this request.
func penalizeAttempt(at attempt, status int, header http.Header, body []byte, now time.Time) bool {
	var d time.Duration
	byToken := false
	switch status {
	case http.StatusTooManyRequests:
		d = rateLimitDelay(header, body, now)
	case http.StatusUnauthorized:
		d, byToken = defaultPenalty, true
	case http.StatusPaymentRequired, http.StatusForbidden:
		d = defaultPenalty
	default:
		return false
	}
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	pruneExpiredLocked(now)
	until := now.Add(d)
	if byToken {
		if hash := tokenKey(at.token); hash != "" {
			runtime.penaltyToken[hash] = widenPenalty(runtime.penaltyToken[hash], until, now)
		}
		return true
	}
	if at.index != "" {
		runtime.penaltyIndex[at.index] = widenPenalty(runtime.penaltyIndex[at.index], until, now)
	}
	return true
}

func widenPenalty(prev penalty, until, now time.Time) penalty {
	if prev.until.After(until) {
		until = prev.until
	}
	return penalty{until: until, set: now}
}

func pruneExpiredLocked(now time.Time) {
	if runtime.penaltyIndex == nil {
		runtime.penaltyIndex = map[string]penalty{}
	}
	if runtime.penaltyToken == nil {
		runtime.penaltyToken = map[string]penalty{}
	}
	for key, p := range runtime.penaltyIndex {
		if !now.Before(p.until) {
			delete(runtime.penaltyIndex, key)
		}
	}
	for key, p := range runtime.penaltyToken {
		if !now.Before(p.until) {
			delete(runtime.penaltyToken, key)
		}
	}
}

// rateLimitDelay reads how long a 429 asks to wait: body resets_in_seconds, then
// body resets_at (unix seconds), then Retry-After, then a default.
func rateLimitDelay(header http.Header, body []byte, now time.Time) time.Duration {
	var parsed struct {
		Error struct {
			ResetsIn float64 `json:"resets_in_seconds"`
			ResetsAt float64 `json:"resets_at"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &parsed)
	d := defaultPenalty
	switch {
	case parsed.Error.ResetsIn > 0:
		d = time.Duration(parsed.Error.ResetsIn * float64(time.Second))
	case parsed.Error.ResetsAt > 0:
		d = time.Unix(int64(parsed.Error.ResetsAt), 0).Sub(now)
	default:
		for name, values := range header {
			if !strings.EqualFold(name, "Retry-After") || len(values) == 0 {
				continue
			}
			value := strings.TrimSpace(values[0])
			if seconds, errParse := strconv.Atoi(value); errParse == nil && seconds > 0 {
				d = time.Duration(seconds) * time.Second
			} else if at, errTime := http.ParseTime(value); errTime == nil {
				d = at.Sub(now)
			}
			break
		}
	}
	return min(max(d, minPenalty), maxPenalty)
}

// penaltyUntilLocked returns when the account's penalty ends, or the zero time.
func penaltyUntilLocked(item account, now time.Time) time.Time {
	var until time.Time
	if p, ok := runtime.penaltyIndex[item.index]; ok && now.Before(p.until) {
		until = p.until
	}
	if hash := tokenKey(item.token); hash != "" {
		if p, ok := runtime.penaltyToken[hash]; ok && now.Before(p.until) && p.until.After(until) {
			until = p.until
		}
	}
	return until
}

// splitPenaltyLocked puts healthy accounts first and returns penalized ones
// sorted by penalty expiry, soonest first.
func splitPenaltyLocked(accounts []account, now time.Time) (healthy, penalized []account) {
	expiry := map[string]time.Time{}
	for _, item := range accounts {
		if until := penaltyUntilLocked(item, now); !until.IsZero() {
			expiry[item.index] = until
			penalized = append(penalized, item)
			continue
		}
		healthy = append(healthy, item)
	}
	sort.SliceStable(penalized, func(i, j int) bool {
		ei, ej := expiry[penalized[i].index], expiry[penalized[j].index]
		if !ei.Equal(ej) {
			return ei.Before(ej)
		}
		return penalized[i].index < penalized[j].index
	})
	return healthy, penalized
}

// settlePenalty clears penalties recorded before started, after a success. It
// reports whether a penalty recorded during the attempt still stands.
func settlePenalty(index, token string, started time.Time) bool {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	demoted := false
	if p, ok := runtime.penaltyIndex[index]; ok {
		if p.set.After(started) {
			demoted = true
		} else {
			delete(runtime.penaltyIndex, index)
		}
	}
	if hash := tokenKey(token); hash != "" {
		if p, ok := runtime.penaltyToken[hash]; ok {
			if p.set.After(started) {
				demoted = true
			} else {
				delete(runtime.penaltyToken, hash)
			}
		}
	}
	return demoted
}

func markUsed(index string, now time.Time) {
	if index == "" {
		return
	}
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if runtime.lastUsed == nil {
		runtime.lastUsed = map[string]time.Time{}
	}
	runtime.lastUsed[index] = now
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
		// Records from before per-model cooldowns carry no model and are dropped.
		if !now.Before(record.Until) || baseModel(record.Model) == "" {
			continue
		}
		if record.AuthIndex != "" {
			key := cooldownKey(record.AuthIndex, record.Model)
			if prev, ok := runtime.cooldownIndex[key]; !ok || record.Until.After(prev) {
				runtime.cooldownIndex[key] = record.Until
			}
		}
		if record.TokenHash != "" {
			key := cooldownKey(record.TokenHash, record.Model)
			if prev, ok := runtime.cooldownToken[key]; !ok || record.Until.After(prev) {
				runtime.cooldownToken[key] = record.Until
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
	for key, until := range runtime.cooldownIndex {
		index, model, _ := strings.Cut(key, "\x00")
		if index == "" || !now.Before(until) {
			delete(runtime.cooldownIndex, key)
			continue
		}
		out = append(out, cooldownRecord{AuthIndex: index, Model: model, Until: until, Reason: cooldownReasonUnsupported})
	}
	for key, until := range runtime.cooldownToken {
		hash, model, _ := strings.Cut(key, "\x00")
		if hash == "" || !now.Before(until) {
			delete(runtime.cooldownToken, key)
			continue
		}
		out = append(out, cooldownRecord{TokenHash: hash, Model: model, Until: until, Reason: cooldownReasonUnsupported})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AuthIndex != out[j].AuthIndex {
			return out[i].AuthIndex < out[j].AuthIndex
		}
		if out[i].TokenHash != out[j].TokenHash {
			return out[i].TokenHash < out[j].TokenHash
		}
		return out[i].Model < out[j].Model
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
	now := nowFn()
	for key, bind := range runtime.sticky {
		if !now.Before(bind.until) {
			delete(runtime.sticky, key)
		}
	}
	if current, ok := runtime.sticky[session]; ok && current.authIndex == index {
		return
	}
	runtime.sticky[session] = stickyBind{
		authIndex: index,
		until:     now.Add(time.Duration(seconds) * time.Second),
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
		go forwardStream(callbackID, result.resp.streamID(), executorStreamID, protocol, used, result.email, result.index, token, result.model, cfg.modelOutageCooldown(), cfg.StatsFile, cfg.RotationMode)
		return okEnvelope(map[string]any{
			"headers": responseHeaders(result.resp, "text/event-stream"),
		})
	}
	if !result.noted {
		noteRequest(false, 0, 0, 0, result.email)
	}
	return errorEnvelope("upstream_error", failureMessage(result.status, result.body, result.tokens), result.status), nil
}

func forwardStream(callbackID, httpStreamID, executorStreamID, protocol string, tokens []string, email, index, token, model string, cooldown time.Duration, statsFile, rotation string) {
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
		if chatgptModelRefused(scan) {
			cooledStream = true
			markModelCooldown(index, token, model, cooldown, statsFile)
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
	// Upstream may keep the Responses stream open after the terminal event
	// and only send keep-alive comments. Codex drops the connection as soon
	// as response.completed arrives, which the host then records as a client
	// cancel. Close the stream ourselves once the terminal frame is complete.
	terminalSeen := false
	var carry []byte
	noteTerminal := func(part []byte) {
		if terminalSeen || protocol != "openai-response" || len(part) == 0 {
			return
		}
		joined := append(append([]byte(nil), carry...), part...)
		if responsesTerminalEvent(joined) {
			terminalSeen = true
		}
		if len(part) > terminalCarryBytes {
			part = part[len(part)-terminalCarryBytes:]
		}
		carry = append(carry[:0], part...)
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
		noteTerminal(chunk.payload())
		if terminalSeen && sseFrameBoundary(chunk.payload()) {
			finish(errText)
			return
		}
	}
}

// terminalCarryBytes is how much of the previous chunk is kept so a terminal
// marker split across two reads is still recognised.
const terminalCarryBytes = 128

// responsesTerminalEvent reports whether an SSE buffer contains a Responses
// terminal event at the start of a line. Only line starts count so the same
// text inside a JSON string of another event is ignored.
func responsesTerminalEvent(buf []byte) bool {
	for _, line := range bytes.Split(buf, []byte("\n")) {
		line = bytes.TrimSpace(line)
		for _, marker := range responsesTerminalMarkers {
			if bytes.HasPrefix(line, marker) {
				return true
			}
		}
	}
	return false
}

var responsesTerminalMarkers = [][]byte{
	[]byte("event: response.completed"),
	[]byte("event: response.failed"),
	[]byte("event: response.incomplete"),
	[]byte(`data: {"type":"response.completed"`),
	[]byte(`data: {"type":"response.failed"`),
	[]byte(`data: {"type":"response.incomplete"`),
}

// sseFrameBoundary reports whether a chunk ends exactly on an SSE frame end.
func sseFrameBoundary(part []byte) bool {
	return bytes.HasSuffix(part, []byte("\n\n")) || bytes.HasSuffix(part, []byte("\r\n\r\n"))
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

// loadEligible returns every enabled Codex account that can serve a request now,
// and whether at least one enabled Plus account exists. Plus-preference is applied
// later per upstream model name. hasEnabledPlus counts cooling and unavailable
// Plus accounts, matching the preference rules.
func loadEligible(callbackID string, now time.Time) ([]account, bool, error) {
	raw, errList := callHost(pluginabi.MethodHostAuthList, map[string]string{"host_callback_id": callbackID})
	if errList != nil {
		return nil, false, errList
	}
	var listed struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	if errUnmarshal := json.Unmarshal(raw, &listed); errUnmarshal != nil {
		return nil, false, errUnmarshal
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
	out := make([]account, 0, len(candidates))
	for _, item := range candidates {
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
	return out, hasEnabledPlus, nil
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

// tokenFromPath reads the access token and plan straight from the auth file on
// every call. Reading the file costs about 25 microseconds; the host auth.get
// fallback costs about 9 ms per account, which made every request wait for all
// auth files. A token is never served from memory, so a rewrite that keeps the
// same mtime and size is still seen.
func tokenFromPath(path string) (string, bool, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false, false
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil || len(bytes.TrimSpace(data)) == 0 {
		return "", false, false
	}
	token, plus, known := tokenPlanFromAuthJSON(data)
	if !known {
		return "", false, false
	}
	return token, plus, true
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
		if plusBearer(item) {
			out = append(out, item)
		}
	}
	return out
}

// plusBearer reports whether the access token that will be sent is a Plus bearer.
func plusBearer(item account) bool {
	return strings.EqualFold(jwtChatGPTPlan(item.token), "plus") || (!looksLikeJWT(item.token) && item.plus)
}

// freeModel reports whether the upstream model name may use accounts that
// plus-preference would skip. Matching is case-insensitive and exact.
func (c pluginConfig) freeModel(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, item := range c.FreeModels {
		if strings.EqualFold(item, name) {
			return true
		}
	}
	return false
}

// allowsAccount reports whether the account may serve the upstream model name
// under plus-preference. A listed free model allows every eligible account.
func (c pluginConfig) allowsAccount(item account, name string, hasEnabledPlus bool) bool {
	if c.PlusPreference == "off" || c.freeModel(name) {
		return true
	}
	if c.PlusPreference == "only" {
		return plusBearer(item)
	}
	if !hasEnabledPlus {
		return true
	}
	return item.plus
}

// restrictAccounts applies plus-preference to the eligible pool for one primary
// upstream name. freePrimary keeps every eligible account, like preference off.
// It returns an error code and message when no account is allowed.
func restrictAccounts(cfg pluginConfig, freePrimary bool, accounts []account, hasEnabledPlus bool) ([]account, string, string) {
	if freePrimary || cfg.PlusPreference == "off" {
		return accounts, "", ""
	}
	if cfg.PlusPreference == "only" && !hasEnabledPlus {
		return nil, "no_plus_account", "no eligible plus codex account is available"
	}
	if hasEnabledPlus {
		plusAccounts := make([]account, 0, len(accounts))
		for _, item := range accounts {
			if item.plus {
				plusAccounts = append(plusAccounts, item)
			}
		}
		accounts = plusAccounts
		if len(accounts) == 0 {
			return nil, "no_plus_account", "no eligible plus codex account is available"
		}
	}
	if cfg.PlusPreference == "only" {
		accounts = onlyPlusBearers(accounts)
		if len(accounts) == 0 {
			return nil, "no_plus_account", "no plus codex account is available"
		}
	}
	return accounts, "", ""
}

// priorityAtLeast keeps the accounts at or above the best priority in anchor, so a
// tail never reaches below the tier already in use. An empty anchor falls back to
// the accounts' own highest tier.
func priorityAtLeast(accounts, anchor []account) []account {
	if len(anchor) == 0 {
		return highestPriority(accounts)
	}
	best := anchor[0].priority
	for _, item := range anchor[1:] {
		if item.priority > best {
			best = item.priority
		}
	}
	var out []account
	for _, item := range accounts {
		if item.priority >= best {
			out = append(out, item)
		}
	}
	return out
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

// planAccountsLocked orders healthy accounts by the rotation mode. Penalized
// accounts follow as a tail, already sorted by penalty expiry, so they are still
// tried when nothing healthy is left.
func planAccountsLocked(cfg pluginConfig, eligible, tail []account, session string, now time.Time) []account {
	if len(eligible) == 0 {
		return tail
	}
	plus, rest := splitPlus(sortAccounts(eligible, false))
	var primary []account
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
		return tail
	}
	switch cfg.RotationMode {
	case "least-recent":
		primary = leastRecentLocked(primary)
		// Stamp the first pick now so concurrent requests spread before any attempt is sent.
		runtime.lastUsed[primary[0].index] = now
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

// leastRecentLocked orders never-used accounts first, then by last use, oldest
// first. Ties fall back to UpdatedAt, then index.
func leastRecentLocked(in []account) []account {
	if runtime.lastUsed == nil {
		runtime.lastUsed = map[string]time.Time{}
	}
	out := sortAccounts(in, true)
	sort.SliceStable(out, func(i, j int) bool {
		ti, usedI := runtime.lastUsed[out[i].index]
		tj, usedJ := runtime.lastUsed[out[j].index]
		if usedI != usedJ {
			return !usedI
		}
		return usedI && ti.Before(tj)
	})
	return out
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

func stickyOrderLocked(_ pluginConfig, primary []account, session string, now time.Time) []account {
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
// allowed filters the pairs when a fallback name may use a different account set.
func buildAttempts(accounts []account, names []string, budget int, suffix string, allowed func(account, string) bool) []attempt {
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
		if allowed != nil && !allowed(item, name) {
			return
		}
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
		// Stop after a full cycle without a new attempt, so a filtered pool ends.
		misses := 0
		for cursor := 0; len(out) < budget && misses < accountCount; cursor++ {
			before := len(out)
			add(accounts[cursor%accountCount], 0, true)
			if len(out) == before {
				misses++
			} else {
				misses = 0
			}
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
