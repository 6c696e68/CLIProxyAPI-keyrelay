package keyshop

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var partnerAPINow = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

type partnerAPIClock struct{ now time.Time }

func (c *partnerAPIClock) Now() time.Time      { return c.now }
func (c *partnerAPIClock) Add(d time.Duration) { c.now = c.now.Add(d) }

type partnerAPI struct {
	service *Service
	store   *Store
	clock   *partnerAPIClock
	group   Group
	plan    Plan
	partner Partner
	secret  string
}

func newPartnerAPI(t *testing.T, tweak func(*Config)) *partnerAPI {
	t.Helper()
	clock := &partnerAPIClock{now: partnerAPINow}
	cfg := Config{
		DataFile:          filepath.Join(t.TempDir(), "keys.db"),
		Now:               clock.Now,
		PartnerAPIEnabled: true,
	}
	if tweak != nil {
		tweak(&cfg)
	}
	store, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	a := &partnerAPI{service: NewService(store, []byte("<admin>"), []byte("<dash>")), store: store, clock: clock}
	a.group = mustGroup(t, store, "g", "m1", "m2")
	a.plan = mustPlan(t, store, planInput("plan", a.group.ID))
	a.partner, a.secret = mustPartner(t, store, PartnerInput{Name: "shop"})
	return a
}

func (a *partnerAPI) authed() map[string][]string {
	return map[string][]string{
		partnerHeaderName: {"1"},
		"Authorization":   {"Bearer " + a.secret},
		"Host":            {"api.example.com"},
		partnerClientIP:   {"198.51.100.7"},
	}
}

func (a *partnerAPI) call(t *testing.T, method, op string, headers map[string][]string, body any) commandResult {
	t.Helper()
	return callManagementAPI(t, a.service, method, partnerAPIPrefix+"/"+op, headers, body)
}

func (a *partnerAPI) mintBody(ref string, secrets []string) map[string]any {
	return map[string]any{
		"ref": ref, "plan_id": a.plan.ID, "plan_revision": a.plan.Revision,
		"quantity": len(secrets), "secrets": secrets,
	}
}

func (a *partnerAPI) mintOK(t *testing.T, ref string, secrets []string) commandResult {
	t.Helper()
	res := a.call(t, http.MethodPost, "mint", a.authed(), a.mintBody(ref, secrets))
	if res.Status != http.StatusOK {
		t.Fatalf("mint %s status=%d body=%s", ref, res.Status, res.Body)
	}
	return res
}

type commandResult struct {
	Status  int
	Headers map[string][]string
	Body    []byte
	JSON    map[string]any
}

func callManagementAPI(t *testing.T, svc *Service, method, path string, headers map[string][]string, body any) commandResult {
	t.Helper()
	return callManagementQuery(t, svc, method, path, headers, nil, body)
}

func callManagementQuery(t *testing.T, svc *Service, method, path string, headers, query map[string][]string, body any) commandResult {
	t.Helper()
	payload := map[string]any{"Method": method, "Path": path}
	if headers != nil {
		payload["Headers"] = headers
	}
	if query != nil {
		payload["Query"] = query
	}
	if body != nil {
		payload["Body"] = body
	}
	raw, err := svc.Call("management.handle", mustJSON(t, payload))
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Result struct {
			StatusCode int
			Headers    map[string][]string
			Body       []byte
		}
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("envelope: %v raw=%s", err, raw)
	}
	result := commandResult{Status: envelope.Result.StatusCode, Headers: envelope.Result.Headers, Body: envelope.Result.Body}
	if len(result.Body) > 0 {
		_ = json.Unmarshal(result.Body, &result.JSON)
	}
	return result
}

func decodeInto(t *testing.T, res commandResult, out any) {
	t.Helper()
	if err := json.Unmarshal(res.Body, out); err != nil {
		t.Fatalf("decode: %v body=%s", err, res.Body)
	}
}

func assertAPIError(t *testing.T, res commandResult, status int, code string) {
	t.Helper()
	if res.Status != status {
		t.Fatalf("status=%d want %d body=%s", res.Status, status, res.Body)
	}
	errorValue, ok := res.JSON["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error envelope: %s", res.Body)
	}
	if errorValue["code"] != code {
		t.Fatalf("code=%v want %q body=%s", errorValue["code"], code, res.Body)
	}
	if message, _ := errorValue["message"].(string); strings.TrimSpace(message) == "" {
		t.Fatalf("empty error message: %s", res.Body)
	}
}

func copyHeaders(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func TestPartnerAliasGuardAndDisabledState(t *testing.T) {
	disabled := newPartnerAPI(t, func(cfg *Config) { cfg.PartnerAPIEnabled = false })
	aliasOnly := map[string][]string{partnerHeaderName: {"1"}}
	if res := disabled.call(t, http.MethodGet, "health", nil, nil); res.Status != http.StatusNotFound {
		t.Fatalf("no header status=%d want 404", res.Status)
	}
	res := disabled.call(t, http.MethodGet, "health", aliasOnly, nil)
	assertAPIError(t, res, http.StatusServiceUnavailable, "partner_api_disabled")
	bogus := copyHeaders(aliasOnly)
	bogus["Authorization"] = []string{"Bearer ksp_" + strings.Repeat("A", 43)}
	res = disabled.call(t, http.MethodGet, "health", bogus, nil)
	assertAPIError(t, res, http.StatusServiceUnavailable, "partner_api_disabled")

	a := newPartnerAPI(t, nil)
	if res = a.call(t, http.MethodGet, "health", nil, nil); res.Status != http.StatusNotFound {
		t.Fatalf("missing alias header status=%d", res.Status)
	}
	if res = a.call(t, http.MethodGet, "health", map[string][]string{partnerHeaderName: {"0"}}, nil); res.Status != http.StatusNotFound {
		t.Fatalf("wrong alias header status=%d", res.Status)
	}
	// A partner prefix path must never fall through to an admin handler.
	if res = callManagementAPI(t, a.service, http.MethodGet, partnerAPIPrefix+"/keys", a.authed(), nil); res.Status != http.StatusNotFound {
		t.Fatalf("partner keys path status=%d body=%s", res.Status, res.Body)
	}
	res = a.call(t, http.MethodGet, "plans", nil, nil)
	if res.Status != http.StatusNotFound || strings.Contains(string(res.Body), `"plans"`) {
		t.Fatalf("partner plans without alias header reached a handler: status=%d body=%s", res.Status, res.Body)
	}
	if res = a.call(t, http.MethodPost, "mint/extra", a.authed(), map[string]any{}); res.Status != http.StatusNotFound {
		t.Fatalf("unknown partner op status=%d", res.Status)
	}
	// The management-prefixed path dispatches the same partner op.
	res = callManagementAPI(t, a.service, http.MethodGet, "/v0/management/plugins/key-shop/partner/v1/health", a.authed(), nil)
	if res.Status != http.StatusOK || res.JSON["ok"] != true {
		t.Fatalf("prefixed partner path status=%d body=%s", res.Status, res.Body)
	}
	// Admin routes still work without the partner header.
	res = callManagementAPI(t, a.service, http.MethodGet, "/plugins/key-shop/plans", nil, nil)
	if res.Status != http.StatusOK {
		t.Fatalf("admin plans status=%d body=%s", res.Status, res.Body)
	}
}

func TestPartnerAuthAndCallLog(t *testing.T) {
	a := newPartnerAPI(t, nil)
	alias := map[string][]string{partnerHeaderName: {"1"}, partnerClientIP: {"198.51.100.7"}}
	res := a.call(t, http.MethodGet, "health", alias, nil)
	assertAPIError(t, res, http.StatusUnauthorized, "unauthorized")
	unknown := copyHeaders(alias)
	unknown["Authorization"] = []string{"Bearer ksp_" + strings.Repeat("A", 43)}
	res = a.call(t, http.MethodGet, "health", unknown, nil)
	assertAPIError(t, res, http.StatusUnauthorized, "unauthorized")
	otherKind := copyHeaders(alias)
	otherKind["Authorization"] = []string{"Bearer ck_" + strings.Repeat("B", 43)}
	res = a.call(t, http.MethodGet, "health", otherKind, nil)
	assertAPIError(t, res, http.StatusUnauthorized, "unauthorized")

	res = a.call(t, http.MethodGet, "health", a.authed(), nil)
	if res.Status != http.StatusOK || res.JSON["ok"] != true {
		t.Fatalf("health status=%d body=%s", res.Status, res.Body)
	}
	partnerInfo, ok := res.JSON["partner"].(map[string]any)
	if !ok || partnerInfo["id"] != a.partner.ID || partnerInfo["name"] != "shop" {
		t.Fatalf("health partner=%v", res.JSON["partner"])
	}
	if res.JSON["server_time"] != partnerAPINow.Format(time.RFC3339) {
		t.Fatalf("server_time=%v", res.JSON["server_time"])
	}
	partners, err := a.store.ListPartners()
	if err != nil || len(partners) != 1 || partners[0].LastUsedAt == nil {
		t.Fatalf("last_used_at not stamped: %+v err=%v", partners, err)
	}

	if _, err = a.store.UpdatePartner(a.partner.ID, PartnerUpdate{Status: "disabled", StatusSet: true}); err != nil {
		t.Fatal(err)
	}
	res = a.call(t, http.MethodGet, "health", a.authed(), nil)
	assertAPIError(t, res, http.StatusForbidden, "partner_disabled")
	if _, err = a.store.UpdatePartner(a.partner.ID, PartnerUpdate{Status: "active", StatusSet: true}); err != nil {
		t.Fatal(err)
	}
	if err = a.store.DeletePartner(a.partner.ID); err != nil {
		t.Fatal(err)
	}
	res = a.call(t, http.MethodGet, "health", a.authed(), nil)
	assertAPIError(t, res, http.StatusUnauthorized, "unauthorized")

	calls, next, err := a.store.ListPartnerCalls(PartnerCallQuery{})
	if err != nil || next != "" || len(calls) != 6 {
		t.Fatalf("calls=%d next=%q err=%v", len(calls), next, err)
	}
	want := []struct {
		status int
		code   string
		known  bool
	}{
		{http.StatusUnauthorized, "unauthorized", false},
		{http.StatusForbidden, "partner_disabled", false},
		{http.StatusOK, "", true},
		{http.StatusUnauthorized, "unauthorized", false},
		{http.StatusUnauthorized, "unauthorized", false},
		{http.StatusUnauthorized, "unauthorized", false},
	}
	for i, item := range want {
		call := calls[i]
		if call.Op != "health" || call.HTTPStatus != item.status || call.Code != item.code {
			t.Fatalf("call[%d]=%+v", i, call)
		}
		if (call.PartnerID == a.partner.ID) != item.known {
			t.Fatalf("call[%d] partner=%q known=%v", i, call.PartnerID, item.known)
		}
		if call.ClientIP != "198.51.100.7" || call.LatencyMS < 0 {
			t.Fatalf("call[%d]=%+v", i, call)
		}
	}

	rows, err := a.store.db.Query(`SELECT partner_id, op, code, ref, client_ip FROM partner_calls`)
	if err != nil {
		t.Fatal(err)
	}
	var dump strings.Builder
	for rows.Next() {
		var partnerID, op, code, ref, clientIP string
		if err = rows.Scan(&partnerID, &op, &code, &ref, &clientIP); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		dump.WriteString(partnerID + op + code + ref + clientIP)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dump.String(), "ksp_") || strings.Contains(dump.String(), "ck_") || strings.Contains(dump.String(), strings.TrimPrefix(a.secret, "ksp_")) {
		t.Fatalf("audit rows leaked a token: %s", dump.String())
	}
}

func TestPartnerBodyCapAndAuthOrder(t *testing.T) {
	a := newPartnerAPI(t, func(cfg *Config) { cfg.PartnerMaxBodyKB = 1 })
	big := map[string]any{"ref": "ref-0001", "pad": strings.Repeat("x", 2048)}
	res := a.call(t, http.MethodPost, "status", a.authed(), big)
	assertAPIError(t, res, http.StatusBadRequest, "bad_request")
	badAuth := a.authed()
	badAuth["Authorization"] = []string{"Bearer ksp_" + strings.Repeat("Z", 43)}
	res = a.call(t, http.MethodPost, "status", badAuth, big)
	assertAPIError(t, res, http.StatusUnauthorized, "unauthorized")
	res = a.call(t, http.MethodPost, "status", a.authed(), map[string]any{"ref": "ref-0001"})
	assertAPIError(t, res, http.StatusNotFound, "ref_not_found")
}

func TestPartnerHealthPlansAndURLs(t *testing.T) {
	a := newPartnerAPI(t, func(cfg *Config) {
		cfg.PublicBaseURL = "https://shop.example.com"
		cfg.PublicDashboardPath = "/client"
	})
	mustPlan(t, a.store, planInput("other", a.group.ID))
	inactiveInput := planInput("inactive", a.group.ID)
	inactiveInput.Active = false
	inactive := mustPlan(t, a.store, inactiveInput)
	if _, err := a.store.UpdatePartner(a.partner.ID, PartnerUpdate{AllowedPlanIDs: []string{a.plan.ID, inactive.ID}, AllowedPlansSet: true}); err != nil {
		t.Fatal(err)
	}
	res := a.call(t, http.MethodGet, "plans", a.authed(), nil)
	var plans struct {
		Plans        []Plan `json:"plans"`
		BaseURL      string `json:"base_url"`
		DashboardURL string `json:"dashboard_url"`
	}
	decodeInto(t, res, &plans)
	if res.Status != http.StatusOK || len(plans.Plans) != 1 || plans.Plans[0].ID != a.plan.ID {
		t.Fatalf("plans status=%d plans=%+v", res.Status, plans.Plans)
	}
	if plans.BaseURL != "https://shop.example.com/v1" || plans.DashboardURL != "https://shop.example.com/client" {
		t.Fatalf("urls base=%q dashboard=%q", plans.BaseURL, plans.DashboardURL)
	}

	for _, tc := range []struct{ host, base, dashboard string }{
		{"api.example.com", "https://api.example.com/v1", "https://api.example.com/client"},
		{"api.example.com:8443", "https://api.example.com:8443/v1", "https://api.example.com:8443/client"},
		{"localhost:8319", "http://localhost:8319/v1", "http://localhost:8319/client"},
		{"127.0.0.1:8319", "http://127.0.0.1:8319/v1", "http://127.0.0.1:8319/client"},
	} {
		b := newPartnerAPI(t, nil)
		headers := b.authed()
		headers["Host"] = []string{tc.host}
		res := b.call(t, http.MethodGet, "plans", headers, nil)
		var got struct {
			BaseURL      string `json:"base_url"`
			DashboardURL string `json:"dashboard_url"`
		}
		decodeInto(t, res, &got)
		if got.BaseURL != tc.base || got.DashboardURL != tc.dashboard {
			t.Fatalf("host %s base=%q dashboard=%q", tc.host, got.BaseURL, got.DashboardURL)
		}
	}
}

func TestPartnerMintHTTP(t *testing.T) {
	a := newPartnerAPI(t, nil)
	secrets := []string{newMintSecret(t), newMintSecret(t)}
	res := a.mintOK(t, "order-0001", secrets)
	var minted struct {
		Ref          string       `json:"ref"`
		State        string       `json:"state"`
		Replayed     bool         `json:"replayed"`
		Keys         []PartnerKey `json:"keys"`
		Plan         Plan         `json:"plan"`
		BaseURL      string       `json:"base_url"`
		DashboardURL string       `json:"dashboard_url"`
	}
	decodeInto(t, res, &minted)
	if minted.Ref != "order-0001" || minted.State != "minted" || minted.Replayed || len(minted.Keys) != 2 || minted.Plan.ID != a.plan.ID {
		t.Fatalf("minted=%+v", minted)
	}
	for i, secret := range secrets {
		body := strings.TrimPrefix(secret, "ck_")
		if minted.Keys[i].Display != Display(body[:4], body[len(body)-4:]) {
			t.Fatalf("key %d display=%q does not match the request secret order", i, minted.Keys[i].Display)
		}
	}
	if minted.BaseURL != "https://api.example.com/v1" || minted.DashboardURL != "https://api.example.com/client" {
		t.Fatalf("urls base=%q dashboard=%q", minted.BaseURL, minted.DashboardURL)
	}
	replay := a.mintOK(t, "order-0001", secrets)
	var replayed struct {
		Replayed bool `json:"replayed"`
	}
	decodeInto(t, replay, &replayed)
	if !replayed.Replayed {
		t.Fatal("a replay must set replayed")
	}

	if _, _, err := a.store.Create(CreateInput{Label: "plain"}); err != nil {
		t.Fatal(err)
	}
	res = callManagementQuery(t, a.service, http.MethodGet, "/plugins/key-shop/keys", nil,
		map[string][]string{"partner": {a.partner.ID}, "ref": {"order-0001"}}, nil)
	var list struct {
		Keys  []Key `json:"keys"`
		Total int   `json:"total"`
	}
	decodeInto(t, res, &list)
	if res.Status != http.StatusOK || list.Total != 2 || len(list.Keys) != 2 {
		t.Fatalf("key list total=%d keys=%d status=%d", list.Total, len(list.Keys), res.Status)
	}
	for _, key := range list.Keys {
		if key.PartnerID != a.partner.ID || key.PartnerRef != "order-0001" {
			t.Fatalf("key list row=%+v", key)
		}
	}
	if !strings.Contains(string(res.Body), `"partner_id":"`) || !strings.Contains(string(res.Body), `"partner_ref":"order-0001"`) {
		t.Fatalf("key list rows must expose partner_id and partner_ref: %s", res.Body)
	}
	res = callManagementQuery(t, a.service, http.MethodGet, "/plugins/key-shop/keys", nil, map[string][]string{"partner": {"ghost"}}, nil)
	var empty struct {
		Total int `json:"total"`
	}
	decodeInto(t, res, &empty)
	if empty.Total != 0 {
		t.Fatalf("unknown partner filter total=%d", empty.Total)
	}
}

func TestPartnerMintHTTPErrors(t *testing.T) {
	t.Run("bad request", func(t *testing.T) {
		a := newPartnerAPI(t, nil)
		res := a.call(t, http.MethodPost, "mint", a.authed(), a.mintBody("order-0001", []string{"ck_short"}))
		assertAPIError(t, res, http.StatusBadRequest, "bad_request")
	})
	t.Run("plan not found", func(t *testing.T) {
		a := newPartnerAPI(t, nil)
		body := a.mintBody("order-0001", []string{newMintSecret(t)})
		body["plan_id"] = "ghost"
		assertAPIError(t, a.call(t, http.MethodPost, "mint", a.authed(), body), http.StatusNotFound, "plan_not_found")
	})
	t.Run("plan not allowed", func(t *testing.T) {
		a := newPartnerAPI(t, nil)
		other := mustPlan(t, a.store, planInput("other", a.group.ID))
		if _, err := a.store.UpdatePartner(a.partner.ID, PartnerUpdate{AllowedPlanIDs: []string{other.ID}, AllowedPlansSet: true}); err != nil {
			t.Fatal(err)
		}
		res := a.call(t, http.MethodPost, "mint", a.authed(), a.mintBody("order-0001", []string{newMintSecret(t)}))
		assertAPIError(t, res, http.StatusForbidden, "plan_not_allowed")
	})
	t.Run("plan inactive", func(t *testing.T) {
		a := newPartnerAPI(t, nil)
		inactive := planInput("plan", a.group.ID)
		inactive.Active = false
		if _, err := a.store.UpdatePlan(a.plan.ID, inactive); err != nil {
			t.Fatal(err)
		}
		res := a.call(t, http.MethodPost, "mint", a.authed(), a.mintBody("order-0001", []string{newMintSecret(t)}))
		assertAPIError(t, res, http.StatusConflict, "plan_inactive")
	})
	t.Run("plan changed", func(t *testing.T) {
		a := newPartnerAPI(t, nil)
		next := planInput("plan", a.group.ID)
		next.TTLSeconds = 7200
		current, err := a.store.UpdatePlan(a.plan.ID, next)
		if err != nil {
			t.Fatal(err)
		}
		res := a.call(t, http.MethodPost, "mint", a.authed(), a.mintBody("order-0001", []string{newMintSecret(t)}))
		assertAPIError(t, res, http.StatusConflict, "plan_changed")
		planValue, ok := res.JSON["plan"].(map[string]any)
		if !ok || planValue["revision"] != float64(current.Revision) {
			t.Fatalf("plan_changed body=%s", res.Body)
		}
	})
	t.Run("daily cap", func(t *testing.T) {
		a := newPartnerAPI(t, nil)
		cap := int64(1)
		if _, err := a.store.UpdatePartner(a.partner.ID, PartnerUpdate{DailyKeyCap: &cap, DailyCapSet: true}); err != nil {
			t.Fatal(err)
		}
		res := a.call(t, http.MethodPost, "mint", a.authed(), a.mintBody("order-0001", []string{newMintSecret(t), newMintSecret(t)}))
		assertAPIError(t, res, http.StatusTooManyRequests, "daily_cap_reached")
	})
	t.Run("ref conflict", func(t *testing.T) {
		a := newPartnerAPI(t, nil)
		a.mintOK(t, "order-0001", []string{newMintSecret(t)})
		res := a.call(t, http.MethodPost, "mint", a.authed(), a.mintBody("order-0001", []string{newMintSecret(t)}))
		assertAPIError(t, res, http.StatusConflict, "ref_conflict")
	})
	t.Run("ref voided", func(t *testing.T) {
		a := newPartnerAPI(t, nil)
		a.mintOK(t, "order-0001", []string{newMintSecret(t)})
		if res := a.call(t, http.MethodPost, "void", a.authed(), map[string]any{"ref": "order-0001"}); res.Status != http.StatusOK {
			t.Fatalf("void status=%d body=%s", res.Status, res.Body)
		}
		res := a.call(t, http.MethodPost, "mint", a.authed(), a.mintBody("order-0001", []string{newMintSecret(t)}))
		assertAPIError(t, res, http.StatusGone, "ref_voided")
	})
	t.Run("secret conflict", func(t *testing.T) {
		a := newPartnerAPI(t, nil)
		_, existing, err := a.store.Create(CreateInput{Label: "existing"})
		if err != nil {
			t.Fatal(err)
		}
		res := a.call(t, http.MethodPost, "mint", a.authed(), a.mintBody("order-0001", []string{existing}))
		assertAPIError(t, res, http.StatusConflict, "secret_conflict")
	})
}

func TestPartnerCallRefIsBounded(t *testing.T) {
	a := newPartnerAPI(t, nil)
	longRef := strings.Repeat("r", partnerRefMaxLength*4)
	res := a.call(t, http.MethodPost, "mint", map[string][]string{partnerHeaderName: {"1"}}, map[string]any{"ref": longRef})
	assertAPIError(t, res, http.StatusUnauthorized, "unauthorized")
	calls, _, err := a.store.ListPartnerCalls(PartnerCallQuery{})
	if err != nil || len(calls) != 1 {
		t.Fatalf("calls=%+v err=%v", calls, err)
	}
	if calls[0].Ref != longRef[:partnerRefMaxLength] {
		t.Fatalf("unauthenticated ref was not bounded: len=%d value=%q", len(calls[0].Ref), calls[0].Ref)
	}

	// Truncation is by rune, so multi-byte text stays valid UTF-8 and bounded.
	unicodeRef := strings.Repeat("あ", partnerRefMaxLength+5)
	got := partnerRefFromBody(mustJSON(t, map[string]any{"ref": "  " + unicodeRef + "  "}))
	if len([]rune(got)) != partnerRefMaxLength || !utf8.ValidString(got) {
		t.Fatalf("unicode ref len=%d valid=%v", len([]rune(got)), utf8.ValidString(got))
	}
}

func TestPartnerConfirmVoidStatusHTTP(t *testing.T) {
	a := newPartnerAPI(t, nil)
	secrets := []string{newMintSecret(t)}
	a.mintOK(t, "order-0001", secrets)
	assertAPIError(t, a.call(t, http.MethodPost, "confirm", a.authed(), map[string]any{"ref": "order-0001"}),
		http.StatusBadRequest, "bad_request")
	assertAPIError(t, a.call(t, http.MethodPost, "confirm", a.authed(), map[string]any{"ref": "order-0001", "order_id": "   "}),
		http.StatusBadRequest, "bad_request")
	res := a.call(t, http.MethodPost, "confirm", a.authed(), map[string]any{"ref": "order-0001", "order_id": "shop-1", "buyer": "buyer"})
	if res.Status != http.StatusOK || res.JSON["state"] != "confirmed" {
		t.Fatalf("confirm status=%d body=%s", res.Status, res.Body)
	}
	res = a.call(t, http.MethodPost, "confirm", a.authed(), map[string]any{"ref": "order-0001", "order_id": "shop-1", "buyer": "buyer"})
	if res.Status != http.StatusOK {
		t.Fatalf("idempotent confirm status=%d body=%s", res.Status, res.Body)
	}
	assertAPIError(t, a.call(t, http.MethodPost, "confirm", a.authed(), map[string]any{"ref": "order-0001", "order_id": "shop-2"}),
		http.StatusConflict, "order_conflict")
	assertAPIError(t, a.call(t, http.MethodPost, "confirm", a.authed(), map[string]any{"ref": "missing-ref", "order_id": "shop-1"}),
		http.StatusNotFound, "ref_not_found")
	assertAPIError(t, a.call(t, http.MethodPost, "void", a.authed(), map[string]any{"ref": "order-0001", "reason": "x"}),
		http.StatusConflict, "ref_confirmed")

	var status PartnerStatusResult
	res = a.call(t, http.MethodPost, "status", a.authed(), map[string]any{"ref": "order-0001"})
	decodeInto(t, res, &status)
	if res.Status != http.StatusOK || status.State != "confirmed" || status.OrderID != "shop-1" || len(status.Keys) != 1 || status.Keys[0].Status != "active" {
		t.Fatalf("status=%+v body=%s", status, res.Body)
	}

	a.mintOK(t, "order-0002", []string{newMintSecret(t)})
	var voided PartnerVoidResult
	res = a.call(t, http.MethodPost, "void", a.authed(), map[string]any{"ref": "order-0002", "reason": "cancel"})
	decodeInto(t, res, &voided)
	if res.Status != http.StatusOK || voided.State != "voided" || voided.KeysDisabled != 1 {
		t.Fatalf("void=%+v body=%s", voided, res.Body)
	}
	res = a.call(t, http.MethodPost, "void", a.authed(), map[string]any{"ref": "order-0002", "reason": "cancel"})
	decodeInto(t, res, &voided)
	if res.Status != http.StatusOK || voided.KeysDisabled != 0 {
		t.Fatalf("idempotent void=%+v body=%s", voided, res.Body)
	}
	assertAPIError(t, a.call(t, http.MethodPost, "confirm", a.authed(), map[string]any{"ref": "order-0002", "order_id": "shop-3"}),
		http.StatusGone, "ref_voided")
	assertAPIError(t, a.call(t, http.MethodPost, "mint", a.authed(), a.mintBody("order-0002", []string{newMintSecret(t)})),
		http.StatusGone, "ref_voided")
	res = a.call(t, http.MethodPost, "status", a.authed(), map[string]any{"ref": "order-0002"})
	decodeInto(t, res, &status)
	if status.State != "voided" || len(status.Keys) != 1 || status.Keys[0].Status != "disabled" {
		t.Fatalf("voided status=%+v", status)
	}

	var tombstone PartnerVoidResult
	res = a.call(t, http.MethodPost, "void", a.authed(), map[string]any{"ref": "never-minted"})
	decodeInto(t, res, &tombstone)
	if res.Status != http.StatusOK || tombstone.State != "voided" || tombstone.KeysDisabled != 0 {
		t.Fatalf("tombstone=%+v body=%s", tombstone, res.Body)
	}
	res = a.call(t, http.MethodPost, "status", a.authed(), map[string]any{"ref": "never-minted"})
	decodeInto(t, res, &status)
	if status.State != "voided" || len(status.Keys) != 0 {
		t.Fatalf("tombstone status=%+v", status)
	}
	assertAPIError(t, a.call(t, http.MethodPost, "status", a.authed(), map[string]any{"ref": "missing-ref"}),
		http.StatusNotFound, "ref_not_found")
	assertAPIError(t, a.call(t, http.MethodPost, "confirm", a.authed(), map[string]any{"ref": ""}),
		http.StatusBadRequest, "bad_request")
}

func TestPartnerRefsHTTP(t *testing.T) {
	a := newPartnerAPI(t, nil)
	for i := range 3 {
		a.clock.Add(time.Minute)
		a.mintOK(t, fmt.Sprintf("ref-%04d", i), []string{newMintSecret(t)})
	}
	res := a.call(t, http.MethodPost, "refs", a.authed(), nil)
	var first struct {
		Refs       []PartnerRef `json:"refs"`
		NextCursor string       `json:"next_cursor"`
	}
	decodeInto(t, res, &first)
	if res.Status != http.StatusOK || len(first.Refs) != 3 || first.NextCursor != "" {
		t.Fatalf("refs status=%d refs=%d next=%q", res.Status, len(first.Refs), first.NextCursor)
	}
	for i, item := range first.Refs {
		if item.Ref != fmt.Sprintf("ref-%04d", i) {
			t.Fatalf("refs order=%+v", first.Refs)
		}
	}
	page := a.call(t, http.MethodPost, "refs", a.authed(), map[string]any{"limit": 2})
	var paged struct {
		Refs       []PartnerRef `json:"refs"`
		NextCursor string       `json:"next_cursor"`
	}
	decodeInto(t, page, &paged)
	if len(paged.Refs) != 2 || paged.NextCursor == "" {
		t.Fatalf("page=%+v", paged)
	}
	second := a.call(t, http.MethodPost, "refs", a.authed(), map[string]any{"limit": 2, "cursor": paged.NextCursor})
	decodeInto(t, second, &paged)
	if len(paged.Refs) != 1 || paged.NextCursor != "" {
		t.Fatalf("second page=%+v", paged)
	}
	since := first.Refs[1].CreatedAt.Format(time.RFC3339)
	windowed := a.call(t, http.MethodPost, "refs", a.authed(), map[string]any{"since": since})
	decodeInto(t, windowed, &paged)
	if len(paged.Refs) != 2 {
		t.Fatalf("since window=%+v", paged)
	}
	until := first.Refs[0].CreatedAt.Format(time.RFC3339)
	early := a.call(t, http.MethodPost, "refs", a.authed(), map[string]any{"until": until})
	decodeInto(t, early, &paged)
	if len(paged.Refs) != 1 {
		t.Fatalf("until window=%+v", paged)
	}
	assertAPIError(t, a.call(t, http.MethodPost, "refs", a.authed(), map[string]any{"cursor": "garbage"}),
		http.StatusBadRequest, "bad_request")
	assertAPIError(t, a.call(t, http.MethodPost, "refs", a.authed(), map[string]any{"since": "not-a-time"}),
		http.StatusBadRequest, "bad_request")
}

func TestAdminPartnerRoutes(t *testing.T) {
	a := newPartnerAPI(t, nil)
	res := callManagementAPI(t, a.service, http.MethodGet, "/plugins/key-shop/partners", nil, nil)
	if res.Status != http.StatusOK || strings.Contains(string(res.Body), a.secret) {
		t.Fatalf("partner list status=%d body=%s", res.Status, res.Body)
	}
	var list struct {
		Partners []Partner `json:"partners"`
	}
	decodeInto(t, res, &list)
	if len(list.Partners) != 1 || list.Partners[0].Stats == nil {
		t.Fatalf("partner list=%+v", list.Partners)
	}

	res = callManagementAPI(t, a.service, http.MethodPost, "/plugins/key-shop/partners", nil, map[string]any{
		"name": "second", "allowed_plan_ids": []string{a.plan.ID}, "daily_key_cap": 3, "max_quantity": 2,
	})
	if res.Status != http.StatusOK {
		t.Fatalf("create partner status=%d body=%s", res.Status, res.Body)
	}
	var created struct {
		Partner Partner `json:"partner"`
		Secret  string  `json:"secret"`
	}
	decodeInto(t, res, &created)
	if !strings.HasPrefix(created.Secret, "ksp_") || created.Partner.DailyKeyCap == nil || *created.Partner.DailyKeyCap != 3 ||
		created.Partner.MaxQuantity == nil || *created.Partner.MaxQuantity != 2 || len(created.Partner.AllowedPlanIDs) != 1 {
		t.Fatalf("created=%+v", created)
	}
	secondID, secondSecret := created.Partner.ID, created.Secret
	res = callManagementAPI(t, a.service, http.MethodGet, "/plugins/key-shop/partners", nil, nil)
	if strings.Contains(string(res.Body), secondSecret) {
		t.Fatal("partner list must not expose the secret")
	}

	res = callManagementAPI(t, a.service, http.MethodPost, "/plugins/key-shop/partners/update", nil, map[string]any{
		"id": secondID, "name": "renamed", "status": "disabled", "allowed_plan_ids": []string{},
		"daily_key_cap": nil, "max_quantity": nil,
	})
	var updated struct {
		Partner Partner `json:"partner"`
	}
	decodeInto(t, res, &updated)
	if res.Status != http.StatusOK || updated.Partner.Name != "renamed" || updated.Partner.Status != "disabled" ||
		updated.Partner.DailyKeyCap != nil || updated.Partner.MaxQuantity != nil || len(updated.Partner.AllowedPlanIDs) != 0 {
		t.Fatalf("updated=%+v body=%s", updated, res.Body)
	}
	res = callManagementAPI(t, a.service, http.MethodPost, "/plugins/key-shop/partners/update", nil, map[string]any{"id": secondID, "daily_key_cap": -1})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("invalid cap status=%d body=%s", res.Status, res.Body)
	}
	res = callManagementAPI(t, a.service, http.MethodPost, "/plugins/key-shop/partners/update", nil, map[string]any{
		"id": secondID, "name": "renamed", "status": "active", "daily_key_cap": 3, "max_quantity": 4,
	})
	if res.Status != http.StatusOK {
		t.Fatalf("re-enable status=%d body=%s", res.Status, res.Body)
	}

	res = callManagementAPI(t, a.service, http.MethodPost, "/plugins/key-shop/partners/rotate", nil, map[string]any{"id": secondID})
	var rotated struct {
		Partner Partner `json:"partner"`
		Secret  string  `json:"secret"`
	}
	decodeInto(t, res, &rotated)
	if res.Status != http.StatusOK || rotated.Secret == secondSecret || !strings.HasPrefix(rotated.Secret, "ksp_") {
		t.Fatalf("rotate=%+v body=%s", rotated, res.Body)
	}
	oldHeaders := map[string][]string{partnerHeaderName: {"1"}, "Authorization": {"Bearer " + secondSecret}}
	assertAPIError(t, callManagementAPI(t, a.service, http.MethodGet, partnerAPIPrefix+"/health", oldHeaders, nil),
		http.StatusUnauthorized, "unauthorized")
	newHeaders := map[string][]string{partnerHeaderName: {"1"}, "Authorization": {"Bearer " + rotated.Secret}}
	if res = callManagementAPI(t, a.service, http.MethodGet, partnerAPIPrefix+"/health", newHeaders, nil); res.Status != http.StatusOK {
		t.Fatalf("rotated secret status=%d body=%s", res.Status, res.Body)
	}

	if res = callManagementAPI(t, a.service, http.MethodPost, "/plugins/key-shop/partners/delete", nil, map[string]any{"id": secondID}); res.Status != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", res.Status, res.Body)
	}
	assertAPIError(t, callManagementAPI(t, a.service, http.MethodGet, partnerAPIPrefix+"/health", newHeaders, nil),
		http.StatusUnauthorized, "unauthorized")
	if res = callManagementAPI(t, a.service, http.MethodPost, "/plugins/key-shop/partners/delete", nil, map[string]any{"id": secondID}); res.Status != http.StatusNotFound {
		t.Fatalf("delete again status=%d body=%s", res.Status, res.Body)
	}
	res = callManagementAPI(t, a.service, http.MethodPost, "/plugins/key-shop/partners", nil, map[string]any{"name": "third", "allowed_plan_ids": []string{"ghost"}})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("unknown allowed plan status=%d body=%s", res.Status, res.Body)
	}
}

func TestAdminPartnerLogs(t *testing.T) {
	a := newPartnerAPI(t, nil)
	a.call(t, http.MethodGet, "health", a.authed(), nil)
	bad := a.authed()
	bad["Authorization"] = []string{"Bearer ksp_" + strings.Repeat("A", 43)}
	a.call(t, http.MethodGet, "health", bad, nil)
	a.clock.Add(time.Minute)
	a.mintOK(t, "order-0001", []string{newMintSecret(t)})
	a.clock.Add(time.Minute)
	a.call(t, http.MethodPost, "confirm", a.authed(), map[string]any{"ref": "missing-ref", "order_id": "shop-1"})

	res := callManagementAPI(t, a.service, http.MethodGet, "/plugins/key-shop/partners/calls", nil, nil)
	var calls struct {
		Calls      []PartnerCall `json:"calls"`
		NextCursor string        `json:"next_cursor"`
	}
	decodeInto(t, res, &calls)
	if res.Status != http.StatusOK || len(calls.Calls) != 4 || calls.NextCursor != "" {
		t.Fatalf("calls status=%d calls=%d next=%q", res.Status, len(calls.Calls), calls.NextCursor)
	}
	if calls.Calls[0].Op != "confirm" || calls.Calls[0].HTTPStatus != http.StatusNotFound || calls.Calls[0].Code != "ref_not_found" || calls.Calls[0].Ref != "missing-ref" {
		t.Fatalf("newest call=%+v", calls.Calls[0])
	}
	if calls.Calls[1].Op != "mint" || calls.Calls[1].HTTPStatus != http.StatusOK || calls.Calls[1].Ref != "order-0001" {
		t.Fatalf("mint call=%+v", calls.Calls[1])
	}

	filter := func(query map[string][]string, want int) {
		t.Helper()
		res := callManagementQuery(t, a.service, http.MethodGet, "/plugins/key-shop/partners/calls", nil, query, nil)
		var page struct {
			Calls []PartnerCall `json:"calls"`
		}
		decodeInto(t, res, &page)
		if res.Status != http.StatusOK || len(page.Calls) != want {
			t.Fatalf("query %v status=%d calls=%d body=%s", query, res.Status, len(page.Calls), res.Body)
		}
	}
	filter(map[string][]string{"partner": {a.partner.ID}}, 3)
	filter(map[string][]string{"partner": {"ghost"}}, 0)
	filter(map[string][]string{"op": {"mint"}}, 1)
	filter(map[string][]string{"status": {"ok"}}, 2)
	filter(map[string][]string{"status": {"error"}}, 2)
	filter(map[string][]string{"from": {partnerAPINow.Add(90 * time.Second).Format(time.RFC3339)}}, 1)
	filter(map[string][]string{"to": {partnerAPINow.Add(30 * time.Second).Format(time.RFC3339)}}, 2)

	res = callManagementQuery(t, a.service, http.MethodGet, "/plugins/key-shop/partners/calls", nil, map[string][]string{"limit": {"2"}}, nil)
	var paged struct {
		Calls      []PartnerCall `json:"calls"`
		NextCursor string        `json:"next_cursor"`
	}
	decodeInto(t, res, &paged)
	if len(paged.Calls) != 2 || paged.NextCursor == "" {
		t.Fatalf("call page=%+v", paged)
	}
	res = callManagementQuery(t, a.service, http.MethodGet, "/plugins/key-shop/partners/calls", nil,
		map[string][]string{"limit": {"2"}, "cursor": {paged.NextCursor}}, nil)
	decodeInto(t, res, &paged)
	if len(paged.Calls) != 2 || paged.NextCursor != "" {
		t.Fatalf("call page2=%+v", paged)
	}
	if res = callManagementQuery(t, a.service, http.MethodGet, "/plugins/key-shop/partners/calls", nil,
		map[string][]string{"cursor": {"garbage"}}, nil); res.Status != http.StatusBadRequest {
		t.Fatalf("invalid call cursor status=%d body=%s", res.Status, res.Body)
	}

	res = callManagementAPI(t, a.service, http.MethodGet, "/plugins/key-shop/partners/orders", nil, nil)
	var orders struct {
		Orders     []PartnerOrder `json:"orders"`
		NextCursor string         `json:"next_cursor"`
	}
	decodeInto(t, res, &orders)
	if res.Status != http.StatusOK || len(orders.Orders) != 1 || orders.Orders[0].State != "minted" || orders.Orders[0].Ref != "order-0001" {
		t.Fatalf("orders status=%d orders=%+v", res.Status, orders.Orders)
	}
	orderFilter := func(query map[string][]string, want int) {
		t.Helper()
		res := callManagementQuery(t, a.service, http.MethodGet, "/plugins/key-shop/partners/orders", nil, query, nil)
		var page struct {
			Orders []PartnerOrder `json:"orders"`
		}
		decodeInto(t, res, &page)
		if res.Status != http.StatusOK || len(page.Orders) != want {
			t.Fatalf("order query %v status=%d orders=%d body=%s", query, res.Status, len(page.Orders), res.Body)
		}
	}
	orderFilter(map[string][]string{"partner": {a.partner.ID}}, 1)
	orderFilter(map[string][]string{"partner": {"ghost"}}, 0)
	orderFilter(map[string][]string{"state": {"minted"}}, 1)
	orderFilter(map[string][]string{"state": {"confirmed"}}, 0)
	orderFilter(map[string][]string{"ref": {"order-0001"}}, 1)
	orderFilter(map[string][]string{"from": {partnerAPINow.Format(time.RFC3339)}}, 1)
	orderFilter(map[string][]string{"to": {partnerAPINow.Format(time.RFC3339)}}, 0)

	a.clock.Add(time.Minute)
	a.mintOK(t, "order-0002", []string{newMintSecret(t)})
	res = callManagementQuery(t, a.service, http.MethodGet, "/plugins/key-shop/partners/orders", nil, map[string][]string{"limit": {"1"}}, nil)
	decodeInto(t, res, &orders)
	if len(orders.Orders) != 1 || orders.Orders[0].Ref != "order-0002" || orders.NextCursor == "" {
		t.Fatalf("order page=%+v", orders)
	}
	res = callManagementQuery(t, a.service, http.MethodGet, "/plugins/key-shop/partners/orders", nil,
		map[string][]string{"limit": {"1"}, "cursor": {orders.NextCursor}}, nil)
	decodeInto(t, res, &orders)
	if len(orders.Orders) != 1 || orders.Orders[0].Ref != "order-0001" || orders.NextCursor != "" {
		t.Fatalf("order page2=%+v", orders)
	}
	if res = callManagementQuery(t, a.service, http.MethodGet, "/plugins/key-shop/partners/orders", nil,
		map[string][]string{"cursor": {"garbage"}}, nil); res.Status != http.StatusBadRequest {
		t.Fatalf("invalid order cursor status=%d body=%s", res.Status, res.Body)
	}
}

func TestPartnerRegistration(t *testing.T) {
	svc := testService(t)
	raw := call(t, svc, "plugin.register", `{}`)
	var register struct {
		Result struct {
			SchemaVersion int `json:"schema_version"`
			Metadata      struct {
				ConfigFields []struct {
					Name    string `json:"Name"`
					Type    string `json:"Type"`
					Default any    `json:"Default"`
				} `json:"ConfigFields"`
			} `json:"metadata"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &register); err != nil {
		t.Fatal(err)
	}
	if register.Result.SchemaVersion != 6 {
		t.Fatalf("schema_version=%d", register.Result.SchemaVersion)
	}
	fields := make(map[string]struct {
		fieldType string
		def       any
	}, len(register.Result.Metadata.ConfigFields))
	for _, field := range register.Result.Metadata.ConfigFields {
		fields[field.Name] = struct {
			fieldType string
			def       any
		}{field.Type, field.Default}
	}
	wantFields := map[string]struct {
		fieldType string
		def       any
	}{
		"partner_api_enabled":           {"bool", false},
		"public_base_url":               {"string", nil},
		"public_dashboard_path":         {"string", "/client"},
		"partner_max_quantity":          {"integer", float64(50)},
		"partner_daily_key_cap":         {"integer", float64(300)},
		"partner_max_body_kb":           {"integer", float64(64)},
		"partner_unconfirmed_alert_min": {"integer", float64(10)},
		"partner_log_retention_days":    {"integer", float64(30)},
		"partner_key_label_template":    {"string", "{plan}-{ref}-{i}"},
		"partner_key_note_template":     {"string", "partner:{partner} ref:{ref}"},
	}
	for name, want := range wantFields {
		got, ok := fields[name]
		if !ok || got.fieldType != want.fieldType || fmt.Sprint(got.def) != fmt.Sprint(want.def) {
			t.Fatalf("config field %s = %+v want %+v", name, got, want)
		}
	}

	raw = call(t, svc, "management.register", `{}`)
	var routes struct {
		Result struct {
			Routes []struct {
				Method string `json:"Method"`
				Path   string `json:"Path"`
			} `json:"routes"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &routes); err != nil {
		t.Fatal(err)
	}
	registered := make(map[string]bool, len(routes.Result.Routes))
	for _, route := range routes.Result.Routes {
		registered[route.Method+" "+route.Path] = true
	}
	for _, want := range []string{
		"GET /plugins/key-shop/partner/v1/health",
		"GET /plugins/key-shop/partner/v1/plans",
		"POST /plugins/key-shop/partner/v1/mint",
		"POST /plugins/key-shop/partner/v1/confirm",
		"POST /plugins/key-shop/partner/v1/void",
		"POST /plugins/key-shop/partner/v1/status",
		"POST /plugins/key-shop/partner/v1/refs",
		"GET /plugins/key-shop/plans",
		"POST /plugins/key-shop/plans",
		"POST /plugins/key-shop/plans/update",
		"POST /plugins/key-shop/plans/delete",
		"GET /plugins/key-shop/partners",
		"POST /plugins/key-shop/partners",
		"POST /plugins/key-shop/partners/update",
		"POST /plugins/key-shop/partners/rotate",
		"POST /plugins/key-shop/partners/delete",
		"GET /plugins/key-shop/partners/calls",
		"GET /plugins/key-shop/partners/orders",
	} {
		if !registered[want] {
			t.Fatalf("route %q is not registered", want)
		}
	}
}
