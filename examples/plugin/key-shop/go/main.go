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
	"context"
	"encoding/json"
	"sync"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/examples/plugin/key-shop/go/keyshop"
	"gopkg.in/yaml.v3"
)

var (
	runtimeMu sync.Mutex
	service   *keyshop.Service
	store     *keyshop.Store
	sweepStop context.CancelFunc
	sweepDone chan struct{}
)

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
	runtimeMu.Unlock()
	if svc == nil {
		if err := configure(nil); err != nil {
			return nil, err
		}
		runtimeMu.Lock()
		svc = service
		runtimeMu.Unlock()
	}
	return svc.Call(method, request)
}

func configure(request []byte) error {
	cfg := keyshop.Config{Now: time.Now}
	var parsed struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	_ = json.Unmarshal(request, &parsed)
	if len(parsed.ConfigYAML) > 0 {
		var doc struct {
			DataFile             string `yaml:"data_file"`
			DefaultTTL           string `yaml:"default_ttl"`
			DefaultLimit         int64  `yaml:"default_token_limit"`
			ReserveTokens        int64  `yaml:"reserve_tokens"`
			ReservationTTL       string `yaml:"reservation_ttl"`
			RetentionDays        int    `yaml:"retention_days"`
			DeletedRetentionDays int    `yaml:"deleted_retention_days"`
		}
		if err := yaml.Unmarshal(parsed.ConfigYAML, &doc); err == nil {
			cfg.DataFile = doc.DataFile
			cfg.DefaultLimit = doc.DefaultLimit
			cfg.ReserveTokens = doc.ReserveTokens
			cfg.RetentionDays = doc.RetentionDays
			cfg.DeletedRetentionDays = doc.DeletedRetentionDays
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
	}
	opened, err := keyshop.Open(cfg)
	if err != nil {
		return err
	}
	admin, _ := webFS.ReadFile("web/admin.html")
	dash, _ := webFS.ReadFile("web/dashboard.html")
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	stopSweeperLocked()
	if store != nil {
		_ = store.Close()
	}
	store = opened
	service = keyshop.NewService(opened, admin, dash)
	sweepCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	sweepStop = cancel
	sweepDone = done
	go func() {
		defer close(done)
		opened.RunSweeper(sweepCtx)
	}()
	return nil
}

func stopSweeperLocked() {
	if sweepStop != nil {
		sweepStop()
		sweepStop = nil
	}
	if sweepDone != nil {
		<-sweepDone
		sweepDone = nil
	}
}
