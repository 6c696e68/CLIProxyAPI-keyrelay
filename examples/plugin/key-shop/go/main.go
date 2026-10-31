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
*/
import "C"

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/examples/plugin/key-shop/go/keyshop"
	"gopkg.in/yaml.v3"
)

// partnerYAML receives the partner keys with pointers so an absent key can be
// told apart from an explicit zero.
type partnerYAML struct {
	APIEnabled    *bool   `yaml:"partner_api_enabled"`
	PublicBaseURL *string `yaml:"public_base_url"`
	DashboardPath *string `yaml:"public_dashboard_path"`
	MaxQuantity   *int64  `yaml:"partner_max_quantity"`
	DailyKeyCap   *int64  `yaml:"partner_daily_key_cap"`
	MaxBodyKB     *int64  `yaml:"partner_max_body_kb"`
	AlertMin      *int64  `yaml:"partner_unconfirmed_alert_min"`
	LogRetention  *int64  `yaml:"partner_log_retention_days"`
	LabelTemplate *string `yaml:"partner_key_label_template"`
	NoteTemplate  *string `yaml:"partner_key_note_template"`
}

// partnerSettings is the last valid raw partner config. Zero numbers and empty
// strings mean unset, so keyshop.Open fills the default. An invalid reconfigure
// value keeps the last valid setting instead of resetting to the default.
type partnerSettings struct {
	apiEnabled    bool
	publicBaseURL string
	dashboardPath string
	maxQuantity   int64
	dailyKeyCap   *int64
	maxBodyKB     int64
	alertMin      int64
	logRetention  int64
	labelTemplate string
	noteTemplate  string
}

var (
	runtimeMu     sync.Mutex
	configureMu   sync.Mutex
	service       *keyshop.Service
	store         *keyshop.Store
	tunnels       *keyshop.TunnelManager
	sweepStop     context.CancelFunc
	sweepDone     chan struct{}
	lastPartner   partnerSettings
	configInvalid bool
)

var errConfigDocumentInvalid = errors.New("invalid key-shop config document")

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	plugin.abi_version = 1
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	_ = host
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	var req []byte
	if request != nil && requestLen > 0 {
		req = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, err := dispatch(C.GoString(method), req)
	if err != nil {
		raw, _ = json.Marshal(map[string]any{"ok": false, "error": map[string]string{"code": "plugin_error", "message": err.Error()}})
	}
	if response != nil && len(raw) > 0 {
		response.ptr = C.CBytes(raw)
		response.len = C.size_t(len(raw))
	}
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
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	stopSweeperLocked()
	if store != nil {
		_ = store.Close()
		store = nil
	}
	service = nil
}

func dispatch(method string, request []byte) ([]byte, error) {
	if method == "plugin.register" || method == "plugin.reconfigure" {
		if err := configure(request); err != nil {
			return nil, err
		}
	}
	runtimeMu.Lock()
	svc := service
	invalid := configInvalid
	runtimeMu.Unlock()
	if invalid {
		return nil, errConfigDocumentInvalid
	}
	if svc == nil {
		if err := configure(nil); err != nil {
			return nil, err
		}
		runtimeMu.Lock()
		svc = service
		runtimeMu.Unlock()
		if svc == nil {
			return nil, fmt.Errorf("key-shop service is not initialized")
		}
	}
	return svc.Call(method, request)
}

func configure(request []byte) error {
	configureMu.Lock()
	defer configureMu.Unlock()
	cfg := keyshop.Config{Now: time.Now}
	partner := partnerSettings{}
	var tunnelKeepAlive *bool
	var tunnelWatchInterval time.Duration
	var tunnelLogMaxKB int64
	var parsed struct {
		ConfigYAML  []byte `json:"config_yaml"`
		HostBaseURL string `json:"host_base_url"`
	}
	envelope := len(bytes.TrimSpace(request)) > 0
	if err := json.Unmarshal(request, &parsed); err != nil && envelope {
		return failClosedOnConfigDocument()
	}
	hasDocument := len(parsed.ConfigYAML) > 0
	if hasDocument {
		var doc struct {
			DataFile             string                     `yaml:"data_file"`
			DefaultTTL           string                     `yaml:"default_ttl"`
			DefaultLimit         int64                      `yaml:"default_token_limit"`
			DefaultRequestLimit  int64                      `yaml:"default_request_limit"`
			ReserveTokens        int64                      `yaml:"reserve_tokens"`
			ReservationTTL       string                     `yaml:"reservation_ttl"`
			RetentionDays        int                        `yaml:"retention_days"`
			DeletedRetentionDays int                        `yaml:"deleted_retention_days"`
			ModelAccessMode      string                     `yaml:"model_access_mode"`
			Models               []keyshop.ModelAccessModel `yaml:"models"`
			CloudflaredPath      string                     `yaml:"cloudflared_path"`
			TunnelsEnabled       *bool                      `yaml:"tunnels_enabled"`
			TunnelTargetURL      string                     `yaml:"tunnel_target_url"`
			TunnelKeepAlive      *bool                      `yaml:"tunnel_keep_alive"`
			TunnelWatchInterval  string                     `yaml:"tunnel_watch_interval"`
			TunnelLogMaxKB       int64                      `yaml:"tunnel_log_max_kb"`
			Partner              partnerYAML                `yaml:",inline"`
		}
		if err := yaml.Unmarshal(parsed.ConfigYAML, &doc); err != nil {
			return failClosedOnConfigDocument()
		}
		cfg.DataFile = doc.DataFile
		cfg.DefaultLimit = doc.DefaultLimit
		cfg.DefaultRequestLimit = doc.DefaultRequestLimit
		cfg.ReserveTokens = doc.ReserveTokens
		cfg.RetentionDays = doc.RetentionDays
		cfg.DeletedRetentionDays = doc.DeletedRetentionDays
		cfg.ModelAccessMode = doc.ModelAccessMode
		cfg.Models = doc.Models
		cfg.CloudflaredPath = doc.CloudflaredPath
		cfg.TunnelsEnabled = doc.TunnelsEnabled
		cfg.TunnelTargetURL = doc.TunnelTargetURL
		tunnelKeepAlive = doc.TunnelKeepAlive
		tunnelLogMaxKB = doc.TunnelLogMaxKB
		if doc.TunnelWatchInterval != "" {
			if parsedInterval, err := time.ParseDuration(doc.TunnelWatchInterval); err == nil && parsedInterval > 0 {
				tunnelWatchInterval = parsedInterval
			}
		}
		partner = partnerSettingsFromYAML(parsed.ConfigYAML, lastPartner)
		if doc.DefaultTTL != "" {
			if parsedTTL, err := time.ParseDuration(doc.DefaultTTL); err == nil {
				cfg.DefaultTTL = parsedTTL
			}
		}
		if doc.ReservationTTL != "" {
			if parsedTTL, err := time.ParseDuration(doc.ReservationTTL); err == nil {
				cfg.ReservationTTL = parsedTTL
			}
		}
	}
	cfg.PartnerAPIEnabled = partner.apiEnabled
	cfg.PublicBaseURL = partner.publicBaseURL
	cfg.PublicDashboardPath = partner.dashboardPath
	cfg.PartnerMaxQuantity = partner.maxQuantity
	cfg.PartnerDailyKeyCap = partner.dailyKeyCap
	cfg.PartnerMaxBodyKB = partner.maxBodyKB
	cfg.PartnerUnconfirmedAlertMin = partner.alertMin
	cfg.PartnerLogRetentionDays = partner.logRetention
	cfg.PartnerKeyLabelTemplate = partner.labelTemplate
	cfg.PartnerKeyNoteTemplate = partner.noteTemplate
	opened, err := keyshop.Open(cfg)
	if err != nil {
		return failConfigureError(err, hasDocument)
	}
	admin, errAdmin := adminPage()
	if errAdmin != nil {
		_ = opened.Close()
		return failConfigureError(errAdmin, hasDocument)
	}
	dash, errDash := webFS.ReadFile("web/dashboard.html")
	if errDash != nil {
		_ = opened.Close()
		return failConfigureError(errDash, hasDocument)
	}
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	stopSweeperLocked()
	if store != nil {
		_ = store.Close()
	}
	store = opened
	lastPartner = partner
	configInvalid = false
	svc := keyshop.NewService(opened, admin, dash)
	mgr := keyshop.NewTunnelManager(opened, keyshop.TunnelManagerConfig{
		CloudflaredPath: cfg.CloudflaredPath,
		Enabled:         cfg.TunnelsEnabled,
		DataDir:         opened.DataDir(),
		TargetURL:       cfg.TunnelTargetURL,
		HostBaseURL:     strings.TrimSpace(parsed.HostBaseURL),
		KeepAlive:       tunnelKeepAlive,
		WatchInterval:   tunnelWatchInterval,
		LogMaxKB:        tunnelLogMaxKB,
		SystemdRun:      keyshop.SystemdRunPath(),
	})
	svc.SetTunnelManager(mgr)
	tunnels = mgr
	service = svc
	sweepCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	sweepStop = cancel
	sweepDone = done
	go func() {
		defer close(done)
		opened.RunSweeper(sweepCtx)
	}()
	mgr.Reconcile()
	return nil
}

// failClosedOnConfigDocument keeps the running plugin untouched when a
// reconfigure document cannot be parsed. A first register with a broken
// document opens nothing and marks the config invalid, so dispatch never falls
// back to the default data file until a valid document arrives.
func failClosedOnConfigDocument() error {
	runtimeMu.Lock()
	hasStore := store != nil
	if !hasStore {
		configInvalid = true
	}
	runtimeMu.Unlock()
	if hasStore {
		log.Printf("key-shop: warning: ignoring invalid config document; keeping the current store")
		return nil
	}
	log.Printf("key-shop: warning: invalid config document; key-shop is not initialized")
	return errConfigDocumentInvalid
}

// failConfigureError remembers a document-bearing configure failure when no
// store is open, so dispatch cannot fall back to configure(nil) and open the
// default data file.
func failConfigureError(err error, hasDocument bool) error {
	if err == nil || !hasDocument {
		return err
	}
	runtimeMu.Lock()
	if store == nil {
		configInvalid = true
	}
	runtimeMu.Unlock()
	return err
}

// partnerSettingsFromYAML parses only the partner keys. A document that does
// not unmarshal, for example a partner value of the wrong type, keeps the
// previous partner settings instead of resetting every key to its default.
func partnerSettingsFromYAML(configYAML []byte, previous partnerSettings) partnerSettings {
	var doc struct {
		Partner partnerYAML `yaml:",inline"`
	}
	if err := yaml.Unmarshal(configYAML, &doc); err != nil {
		log.Printf("key-shop: warning: ignoring invalid config document; keeping previous partner settings")
		return previous
	}
	return partnerSettingsFrom(doc.Partner, previous)
}

func partnerSettingsFrom(doc partnerYAML, previous partnerSettings) partnerSettings {
	next := partnerSettings{}
	if doc.APIEnabled != nil {
		next.apiEnabled = *doc.APIEnabled
	}
	if doc.PublicBaseURL != nil {
		if validPartnerBaseURL(*doc.PublicBaseURL) {
			next.publicBaseURL = *doc.PublicBaseURL
		} else {
			warnInvalidPartnerSetting("public_base_url")
			next.publicBaseURL = previous.publicBaseURL
		}
	}
	if doc.DashboardPath != nil {
		if strings.HasPrefix(*doc.DashboardPath, "/") {
			next.dashboardPath = *doc.DashboardPath
		} else {
			warnInvalidPartnerSetting("public_dashboard_path")
			next.dashboardPath = previous.dashboardPath
		}
	}
	if doc.MaxQuantity != nil {
		next.maxQuantity = partnerRange(*doc.MaxQuantity, 1, 500, previous.maxQuantity, "partner_max_quantity")
	}
	if doc.DailyKeyCap != nil {
		if *doc.DailyKeyCap >= 0 {
			value := *doc.DailyKeyCap
			next.dailyKeyCap = &value
		} else {
			warnInvalidPartnerSetting("partner_daily_key_cap")
			next.dailyKeyCap = cloneInt64Value(previous.dailyKeyCap)
		}
	}
	if doc.MaxBodyKB != nil {
		next.maxBodyKB = partnerRange(*doc.MaxBodyKB, 1, 1024, previous.maxBodyKB, "partner_max_body_kb")
	}
	if doc.AlertMin != nil {
		if *doc.AlertMin >= 1 {
			next.alertMin = *doc.AlertMin
		} else {
			warnInvalidPartnerSetting("partner_unconfirmed_alert_min")
			next.alertMin = previous.alertMin
		}
	}
	if doc.LogRetention != nil {
		if *doc.LogRetention >= 1 {
			next.logRetention = *doc.LogRetention
		} else {
			warnInvalidPartnerSetting("partner_log_retention_days")
			next.logRetention = previous.logRetention
		}
	}
	if doc.LabelTemplate != nil {
		if strings.TrimSpace(*doc.LabelTemplate) != "" && utf8.RuneCountInString(*doc.LabelTemplate) <= 120 {
			next.labelTemplate = *doc.LabelTemplate
		} else {
			warnInvalidPartnerSetting("partner_key_label_template")
			next.labelTemplate = previous.labelTemplate
		}
	}
	if doc.NoteTemplate != nil {
		if utf8.RuneCountInString(*doc.NoteTemplate) <= 200 {
			next.noteTemplate = *doc.NoteTemplate
		} else {
			warnInvalidPartnerSetting("partner_key_note_template")
			next.noteTemplate = previous.noteTemplate
		}
	}
	return next
}

func partnerRange(value, low, high, fallback int64, name string) int64 {
	if value >= low && value <= high {
		return value
	}
	warnInvalidPartnerSetting(name)
	return fallback
}

func validPartnerBaseURL(value string) bool {
	if value == "" {
		return true
	}
	if strings.HasSuffix(value, "/") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func cloneInt64Value(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func warnInvalidPartnerSetting(name string) {
	log.Printf("key-shop: warning: ignoring invalid %s value", name)
}

func adminPage() ([]byte, error) {
	page, err := webFS.ReadFile("web/admin.html")
	if err != nil {
		return nil, err
	}
	if bytes.Contains(page, []byte("datetime-local")) {
		return nil, fmt.Errorf("admin page still falls back to datetime-local")
	}
	if bytes.Contains(page, []byte("FLAT_PICKR")) || bytes.Contains(page, []byte("flatpickr")) {
		return nil, fmt.Errorf("admin page still depends on Flatpickr")
	}
	if bytes.Contains(page, []byte("React.createElement")) {
		return nil, fmt.Errorf("admin page still uses React UMD")
	}
	return page, nil
}

func stopSweeperLocked() {
	if tunnels != nil {
		tunnels.Shutdown()
		tunnels = nil
	}
	if sweepStop != nil {
		sweepStop()
		sweepStop = nil
	}
	if sweepDone != nil {
		<-sweepDone
		sweepDone = nil
	}
}
