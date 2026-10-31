package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/examples/plugin/key-shop/go/keyshop"
)

func partnerBoolPtr(value bool) *bool       { return &value }
func partnerInt64Ptr(value int64) *int64    { return &value }
func partnerStringPtr(value string) *string { return &value }

func TestPartnerSettingsFromDefaultsWhenAbsent(t *testing.T) {
	cap := int64(9)
	previous := partnerSettings{
		apiEnabled: true, publicBaseURL: "https://shop.example.com", dashboardPath: "/client",
		maxQuantity: 20, dailyKeyCap: &cap, maxBodyKB: 128, alertMin: 15, logRetention: 45,
		labelTemplate: "{plan}", noteTemplate: "note",
	}
	got := partnerSettingsFrom(partnerYAML{}, previous)
	if got != (partnerSettings{}) {
		t.Fatalf("absent keys must fall back to defaults, got %+v", got)
	}
}

func TestPartnerSettingsFromKeepsPreviousOnInvalidValues(t *testing.T) {
	cap := int64(12)
	previous := partnerSettings{
		apiEnabled: true, publicBaseURL: "https://shop.example.com", dashboardPath: "/client",
		maxQuantity: 20, dailyKeyCap: &cap, maxBodyKB: 128, alertMin: 15, logRetention: 45,
		labelTemplate: "{plan}", noteTemplate: "note",
	}
	doc := partnerYAML{
		APIEnabled:    partnerBoolPtr(true),
		PublicBaseURL: partnerStringPtr("https://shop.example.com/"),
		DashboardPath: partnerStringPtr("client"),
		MaxQuantity:   partnerInt64Ptr(0),
		DailyKeyCap:   partnerInt64Ptr(-1),
		MaxBodyKB:     partnerInt64Ptr(0),
		AlertMin:      partnerInt64Ptr(0),
		LogRetention:  partnerInt64Ptr(0),
		LabelTemplate: partnerStringPtr(""),
		NoteTemplate:  partnerStringPtr(strings.Repeat("n", 201)),
	}
	got := partnerSettingsFrom(doc, previous)
	if !reflect.DeepEqual(got, previous) {
		t.Fatalf("invalid values must keep the previous settings:\ngot  %+v\nwant %+v", got, previous)
	}
}

func TestPartnerSettingsFromExplicitZeroDailyCapStaysZero(t *testing.T) {
	cap := int64(5)
	previous := partnerSettings{dailyKeyCap: &cap}
	got := partnerSettingsFrom(partnerYAML{DailyKeyCap: partnerInt64Ptr(0)}, previous)
	if got.dailyKeyCap == nil || *got.dailyKeyCap != 0 {
		t.Fatalf("an explicit zero daily cap must stay unlimited, got %+v", got.dailyKeyCap)
	}
}

func TestPartnerSettingsFromYAMLKeepsPreviousOnDocumentError(t *testing.T) {
	cap := int64(300)
	previous := partnerSettings{
		apiEnabled: true, publicBaseURL: "https://shop.example.com", dashboardPath: "/client",
		maxQuantity: 50, dailyKeyCap: &cap, maxBodyKB: 64, alertMin: 10, logRetention: 30,
		labelTemplate: "{plan}-{ref}-{i}", noteTemplate: "partner:{partner} ref:{ref}",
	}
	broken := []byte("partner_api_enabled: true\npartner_max_quantity: abc\n")
	if got := partnerSettingsFromYAML(broken, previous); !reflect.DeepEqual(got, previous) {
		t.Fatalf("a failed document must keep the previous settings:\ngot  %+v\nwant %+v", got, previous)
	}

	withoutPartner := []byte("data_file: /tmp/key-shop.db\n")
	if got := partnerSettingsFromYAML(withoutPartner, previous); got != (partnerSettings{}) {
		t.Fatalf("absent partner keys must fall back to defaults, got %+v", got)
	}

	valid := []byte("partner_api_enabled: true\npartner_max_quantity: 7\npartner_daily_key_cap: 0\npublic_dashboard_path: /client\n")
	got := partnerSettingsFromYAML(valid, previous)
	if !got.apiEnabled || got.maxQuantity != 7 || got.dailyKeyCap == nil || *got.dailyKeyCap != 0 || got.dashboardPath != "/client" {
		t.Fatalf("valid partner settings were not applied: %+v", got)
	}
}

// resetKeyShopRuntime gives a test its own HOME and clears the package state
// before and after the test.
func resetKeyShopRuntime(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	teardownKeyShopRuntime()
	t.Cleanup(teardownKeyShopRuntime)
}

func teardownKeyShopRuntime() {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	stopSweeperLocked()
	if store != nil {
		_ = store.Close()
	}
	store = nil
	service = nil
	configInvalid = false
	lastPartner = partnerSettings{}
}

func configRequest(t *testing.T, document string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"config_yaml": []byte(document)})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func defaultKeyShopDB(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, ".cliproxyapi", "key-shop", "key-shop.db")
}

func TestConfigureInvalidDocumentKeepsOpenStore(t *testing.T) {
	resetKeyShopRuntime(t)
	dataFile := filepath.Join(t.TempDir(), "a.db")
	valid := fmt.Sprintf("data_file: %q\npartner_api_enabled: true\npartner_max_quantity: 20\npartner_daily_key_cap: 0\n", dataFile)
	if err := configure(configRequest(t, valid)); err != nil {
		t.Fatal(err)
	}
	runtimeMu.Lock()
	storeA, serviceA, partnerA := store, service, lastPartner
	runtimeMu.Unlock()
	if storeA == nil || serviceA == nil || partnerA.maxQuantity != 20 || !partnerA.apiEnabled {
		t.Fatalf("store=%p service=%p partner=%+v", storeA, serviceA, partnerA)
	}
	if _, err := os.Stat(dataFile); err != nil {
		t.Fatalf("data file A was not created: %v", err)
	}

	if err := configure(configRequest(t, "data_file: [broken\n")); err != nil {
		t.Fatalf("a broken reconfigure must keep the current store and return nil: %v", err)
	}
	runtimeMu.Lock()
	storeB, serviceB, partnerB := store, service, lastPartner
	runtimeMu.Unlock()
	if storeB != storeA || serviceB != serviceA {
		t.Fatalf("store or service was replaced: store %p -> %p, service %p -> %p", storeA, storeB, serviceA, serviceB)
	}
	if !reflect.DeepEqual(partnerB, partnerA) {
		t.Fatalf("partner settings changed: %+v -> %+v", partnerA, partnerB)
	}
	if _, err := storeB.List(""); err != nil {
		t.Fatalf("the current store was closed: %v", err)
	}
	if _, err := os.Stat(dataFile); err != nil {
		t.Fatalf("data file A disappeared: %v", err)
	}
	if _, err := os.Stat(defaultKeyShopDB(t)); !os.IsNotExist(err) {
		t.Fatalf("the default data file must not be opened after a broken document: %v", err)
	}
}

func TestRegisterInvalidDocumentFailsClosed(t *testing.T) {
	resetKeyShopRuntime(t)
	if _, err := dispatch("plugin.register", configRequest(t, "data_file: [broken\n")); !errors.Is(err, errConfigDocumentInvalid) {
		t.Fatalf("a broken first register must fail with the config error, got %v", err)
	}
	runtimeMu.Lock()
	gotStore, gotService, invalid := store, service, configInvalid
	runtimeMu.Unlock()
	if gotStore != nil || gotService != nil {
		t.Fatalf("a broken first register opened store=%p service=%p", gotStore, gotService)
	}
	if !invalid {
		t.Fatal("the config failure was not remembered")
	}
	if _, err := dispatch("frontend_auth.authenticate", []byte(`{}`)); !errors.Is(err, errConfigDocumentInvalid) {
		t.Fatalf("a later call must fail closed, got %v", err)
	}
	runtimeMu.Lock()
	gotStore = store
	runtimeMu.Unlock()
	if gotStore != nil {
		t.Fatalf("a later call opened a store after the failed register: %p", gotStore)
	}
	if _, err := os.Stat(defaultKeyShopDB(t)); !os.IsNotExist(err) {
		t.Fatalf("the default data file must not be created after a broken register: %v", err)
	}
}

func TestConfigureRecoversAfterInvalidDocument(t *testing.T) {
	resetKeyShopRuntime(t)
	if err := configure(configRequest(t, "data_file: [broken\n")); !errors.Is(err, errConfigDocumentInvalid) {
		t.Fatalf("broken document err=%v", err)
	}
	runtimeMu.Lock()
	if store != nil || service != nil || !configInvalid {
		t.Fatalf("state after broken document: store=%p service=%p invalid=%v", store, service, configInvalid)
	}
	runtimeMu.Unlock()

	dataFile := filepath.Join(t.TempDir(), "recovered.db")
	if err := configure(configRequest(t, fmt.Sprintf("data_file: %q\n", dataFile))); err != nil {
		t.Fatalf("a valid document must recover: %v", err)
	}
	runtimeMu.Lock()
	gotStore, gotService, invalid := store, service, configInvalid
	runtimeMu.Unlock()
	if gotStore == nil || gotService == nil || invalid {
		t.Fatalf("state after recovery: store=%p service=%p invalid=%v", gotStore, gotService, invalid)
	}
	if _, err := os.Stat(dataFile); err != nil {
		t.Fatalf("the valid data file was not opened: %v", err)
	}
	if _, err := dispatch("frontend_auth.authenticate", []byte(`{}`)); err != nil {
		t.Fatalf("dispatch must work again after a valid document: %v", err)
	}
	if _, err := os.Stat(defaultKeyShopDB(t)); !os.IsNotExist(err) {
		t.Fatalf("the default data file was opened during recovery: %v", err)
	}
}

func TestConfigureValidDocumentUnchanged(t *testing.T) {
	resetKeyShopRuntime(t)
	dataFile := filepath.Join(t.TempDir(), "explicit.db")
	document := fmt.Sprintf("data_file: %q\npartner_api_enabled: true\npartner_max_quantity: 7\n", dataFile)
	if err := configure(configRequest(t, document)); err != nil {
		t.Fatal(err)
	}
	runtimeMu.Lock()
	gotStore, gotPartner := store, lastPartner
	runtimeMu.Unlock()
	if gotStore == nil || !gotPartner.apiEnabled || gotPartner.maxQuantity != 7 {
		t.Fatalf("store=%p partner=%+v", gotStore, gotPartner)
	}
	if _, err := os.Stat(dataFile); err != nil {
		t.Fatalf("the explicit data file was not opened: %v", err)
	}
	if _, err := os.Stat(defaultKeyShopDB(t)); !os.IsNotExist(err) {
		t.Fatalf("the default data file must not be used when data_file is explicit: %v", err)
	}

	// An explicitly empty data_file is a valid operator choice: use the default.
	teardownKeyShopRuntime()
	if err := configure(configRequest(t, "data_file: \"\"\n")); err != nil {
		t.Fatalf("an empty data_file is valid: %v", err)
	}
	runtimeMu.Lock()
	gotStore = store
	runtimeMu.Unlock()
	if gotStore == nil {
		t.Fatal("an empty data_file must still open the default store")
	}
	if _, err := os.Stat(defaultKeyShopDB(t)); err != nil {
		t.Fatalf("an empty data_file must open the default path: %v", err)
	}
}

func TestRegisterOpenFailureFailsClosed(t *testing.T) {
	resetKeyShopRuntime(t)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	dataFile := filepath.Join(blocker, "key-shop.db")
	if _, err := dispatch("plugin.register", configRequest(t, fmt.Sprintf("data_file: %q\n", dataFile))); err == nil {
		t.Fatal("a register whose data file cannot be opened must fail")
	}
	runtimeMu.Lock()
	gotStore, gotService, invalid := store, service, configInvalid
	runtimeMu.Unlock()
	if gotStore != nil || gotService != nil {
		t.Fatalf("a failed register opened store=%p service=%p", gotStore, gotService)
	}
	if !invalid {
		t.Fatal("a document-bearing configure failure while no store is open must remember the failure")
	}
	if _, err := dispatch("frontend_auth.authenticate", []byte(`{}`)); !errors.Is(err, errConfigDocumentInvalid) {
		t.Fatalf("a later call must fail closed, got %v", err)
	}
	if _, err := os.Stat(defaultKeyShopDB(t)); !os.IsNotExist(err) {
		t.Fatalf("the default data file must not be created after a failed register: %v", err)
	}
}

func TestConfigureMalformedEnvelopeKeepsStore(t *testing.T) {
	resetKeyShopRuntime(t)
	dataFile := filepath.Join(t.TempDir(), "a.db")
	if err := configure(configRequest(t, fmt.Sprintf("data_file: %q\n", dataFile))); err != nil {
		t.Fatal(err)
	}
	runtimeMu.Lock()
	storeA, serviceA := store, service
	runtimeMu.Unlock()
	if storeA == nil || serviceA == nil {
		t.Fatalf("store=%p service=%p", storeA, serviceA)
	}
	for _, request := range [][]byte{[]byte("not json"), []byte(`{"config_yaml": 123}`)} {
		if err := configure(request); err != nil {
			t.Fatalf("a malformed envelope must keep the current store and return nil: %v", err)
		}
		runtimeMu.Lock()
		storeB, serviceB := store, service
		runtimeMu.Unlock()
		if storeB != storeA || serviceB != serviceA {
			t.Fatalf("a malformed envelope replaced store %p -> %p or service %p -> %p", storeA, storeB, serviceA, serviceB)
		}
	}
	if _, err := storeA.List(""); err != nil {
		t.Fatalf("the current store was closed: %v", err)
	}
	if _, err := os.Stat(defaultKeyShopDB(t)); !os.IsNotExist(err) {
		t.Fatalf("the default data file must not be opened for a malformed envelope: %v", err)
	}
}

func TestRegisterMalformedEnvelopeFailsClosed(t *testing.T) {
	resetKeyShopRuntime(t)
	if _, err := dispatch("plugin.register", []byte("not json")); !errors.Is(err, errConfigDocumentInvalid) {
		t.Fatalf("a malformed first register must fail with the config error, got %v", err)
	}
	runtimeMu.Lock()
	gotStore, invalid := store, configInvalid
	runtimeMu.Unlock()
	if gotStore != nil || !invalid {
		t.Fatalf("state after a malformed register: store=%p invalid=%v", gotStore, invalid)
	}
	if _, err := os.Stat(defaultKeyShopDB(t)); !os.IsNotExist(err) {
		t.Fatalf("the default data file must not be created after a malformed register: %v", err)
	}
}

func activeTunnelManager(t *testing.T) *keyshop.TunnelManager {
	t.Helper()
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if tunnels == nil {
		t.Fatal("configure created no tunnel manager")
	}
	return tunnels
}

func TestConfigureTunnelKeepAliveSettings(t *testing.T) {
	resetKeyShopRuntime(t)
	dataFile := filepath.Join(t.TempDir(), "keepalive.db")
	document := fmt.Sprintf("data_file: %q\ntunnel_keep_alive: false\ntunnel_watch_interval: 5s\n", dataFile)
	if err := configure(configRequest(t, document)); err != nil {
		t.Fatal(err)
	}
	mgr := activeTunnelManager(t)
	if mgr.KeepAliveEnabled() {
		t.Fatal("tunnel_keep_alive: false did not reach the manager")
	}
	if got := mgr.WatchInterval(); got != 5*time.Second {
		t.Fatalf("watch interval %v, want 5s", got)
	}

	// An invalid interval is ignored the way an invalid default_ttl is.
	document = fmt.Sprintf("data_file: %q\ntunnel_watch_interval: -3s\n", dataFile)
	if err := configure(configRequest(t, document)); err != nil {
		t.Fatal(err)
	}
	if got := activeTunnelManager(t).WatchInterval(); got != keyshop.DefaultTunnelWatchInterval {
		t.Fatalf("invalid interval %v, want the default %v", got, keyshop.DefaultTunnelWatchInterval)
	}
}

func TestConfigureTunnelDefaults(t *testing.T) {
	resetKeyShopRuntime(t)
	dataFile := filepath.Join(t.TempDir(), "tunnel-defaults.db")
	if err := configure(configRequest(t, fmt.Sprintf("data_file: %q\n", dataFile))); err != nil {
		t.Fatal(err)
	}
	mgr := activeTunnelManager(t)
	if !mgr.KeepAliveEnabled() {
		t.Fatal("keep-alive must default to on")
	}
	if got := mgr.WatchInterval(); got != keyshop.DefaultTunnelWatchInterval {
		t.Fatalf("watch interval %v, want %v", got, keyshop.DefaultTunnelWatchInterval)
	}
	if got := mgr.LogMaxKB(); got != keyshop.DefaultTunnelLogMaxKB {
		t.Fatalf("log cap %d KiB, want the default %d", got, keyshop.DefaultTunnelLogMaxKB)
	}
}

func TestConfigureTunnelLogMaxKB(t *testing.T) {
	resetKeyShopRuntime(t)
	dataFile := filepath.Join(t.TempDir(), "logcap.db")
	wantKB := int64(keyshop.DefaultTunnelLogMaxKB / 4)
	document := fmt.Sprintf("data_file: %q\ntunnel_log_max_kb: %d\n", dataFile, wantKB)
	if err := configure(configRequest(t, document)); err != nil {
		t.Fatal(err)
	}
	if got := activeTunnelManager(t).LogMaxKB(); got != wantKB {
		t.Fatalf("log cap %d KiB, want %d", got, wantKB)
	}

	// A non-positive value is ignored the way an invalid default_ttl is.
	document = fmt.Sprintf("data_file: %q\ntunnel_log_max_kb: -5\n", dataFile)
	if err := configure(configRequest(t, document)); err != nil {
		t.Fatal(err)
	}
	if got := activeTunnelManager(t).LogMaxKB(); got != keyshop.DefaultTunnelLogMaxKB {
		t.Fatalf("negative log cap %d KiB, want the default %d", got, keyshop.DefaultTunnelLogMaxKB)
	}
}
