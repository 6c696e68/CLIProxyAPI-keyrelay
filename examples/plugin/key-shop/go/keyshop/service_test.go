package keyshop

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testService(t *testing.T) *Service {
	t.Helper()
	store, err := Open(Config{DataFile: filepath.Join(t.TempDir(), "keys.db"), Now: func() time.Time {
		return time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewService(store, []byte("<admin>"), []byte("<dash>"))
}

func call(t *testing.T, svc *Service, method, body string) []byte {
	t.Helper()
	raw, err := svc.Call(method, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func createSecret(t *testing.T, svc *Service, ttl, limit int64) string {
	t.Helper()
	raw := call(t, svc, "management.handle", `{"Method":"POST","Path":"/v0/management/plugins/key-shop/keys","Body":`+string(mustJSON(t, map[string]any{"ttl_seconds": ttl, "token_limit": limit}))+`}`)
	var env struct {
		Result struct {
			Body []byte
		}
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var created struct {
		Secret string
	}
	if err := json.Unmarshal(env.Result.Body, &created); err != nil || created.Secret == "" {
		t.Fatal("create did not return a secret")
	}
	return created.Secret
}

func TestRegisterDeclaresAdmissionCapabilities(t *testing.T) {
	raw := call(t, testService(t), "plugin.register", `{}`)
	if !strings.Contains(string(raw), `"schema_version":6`) || !strings.Contains(string(raw), `"request_interceptor":true`) {
		t.Fatalf("register missing capabilities")
	}
}

func TestBulkCreateReturnsEachSecretAndPersistsTheBatch(t *testing.T) {
	svc := testService(t)
	raw := call(t, svc, "management.handle", `{"Method":"POST","Path":"/plugins/key-shop/keys/bulk","Body":{"count":3,"label":"November","customer":"Acme","token_limit":5000}}`)
	var env struct {
		Result struct {
			Body []byte
		}
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var created struct {
		Keys []struct {
			Secret string
			Key    Key
		}
	}
	if err := json.Unmarshal(env.Result.Body, &created); err != nil {
		t.Fatal(err)
	}
	if len(created.Keys) != 3 {
		t.Fatalf("got %d created keys, want 3", len(created.Keys))
	}
	seen := map[string]bool{}
	for _, item := range created.Keys {
		if item.Secret == "" || seen[item.Secret] {
			t.Fatalf("bulk create returned missing or duplicate secret")
		}
		seen[item.Secret] = true
		if !strings.HasPrefix(item.Key.Label, "November-") || item.Key.Customer != "Acme" || item.Key.TokenLimit != 5000 {
			t.Fatalf("bulk create returned incorrect key metadata: %+v", item.Key)
		}
	}
	if created.Keys[0].Key.Label == created.Keys[1].Key.Label {
		t.Fatalf("bulk labels were not made unique: %+v", created.Keys)
	}
	listed := call(t, svc, "management.handle", `{"Method":"GET","Path":"/plugins/key-shop/keys"}`)
	var listedEnv struct {
		Result struct {
			Body []byte
		}
	}
	if err := json.Unmarshal(listed, &listedEnv); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(listedEnv.Result.Body), `"Label":"November-`) {
		t.Fatal("bulk-created keys were not persisted")
	}
}

func TestCreateWithEmptyLabelGeneratesUniqueLabels(t *testing.T) {
	svc := testService(t)
	raw := call(t, svc, "management.handle", `{"Method":"POST","Path":"/plugins/key-shop/keys/bulk","Body":{"count":2}}`)
	var env struct {
		Result struct {
			Body []byte
		}
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var created struct {
		Keys []struct {
			Key Key
		}
	}
	if err := json.Unmarshal(env.Result.Body, &created); err != nil {
		t.Fatal(err)
	}
	if len(created.Keys) != 2 {
		t.Fatalf("got %d keys, want 2", len(created.Keys))
	}
	if created.Keys[0].Key.Label == "" || created.Keys[0].Key.Label == created.Keys[1].Key.Label {
		t.Fatalf("generated labels are not unique: %+v", created.Keys)
	}
	for _, item := range created.Keys {
		if !strings.HasPrefix(item.Key.Label, "key-") || !strings.HasPrefix(item.Key.ID, item.Key.Label[len("key-"):]) {
			t.Fatalf("generated label does not identify its key: %+v", item.Key)
		}
	}
}

func TestAuthenticateRejectsExpiredAndUnsupportedPaths(t *testing.T) {
	svc := testService(t)
	expired := createSecret(t, svc, -60, 100)
	raw := call(t, svc, "frontend_auth.authenticate", authBody(expired, "/v1/chat/completions"))
	if strings.Contains(string(raw), `"Authenticated":true`) {
		t.Fatal("expired key authenticated")
	}
	secret := createSecret(t, svc, 3600, 100)
	for _, path := range []string{"/v1/realtime", "/v1/live", "/v1/live/abc", "/v1/alpha/search", "/backend-api/codex/alpha/search"} {
		raw = call(t, svc, "frontend_auth.authenticate", authBody(secret, path))
		if strings.Contains(string(raw), `"Authenticated":true`) {
			t.Fatalf("unsupported path authenticated: %s", path)
		}
	}
	raw = call(t, svc, "frontend_auth.authenticate", authBody(secret, "/v1/chat/completions"))
	if !strings.Contains(string(raw), `"Authenticated":true`) {
		t.Fatal("active key was rejected")
	}
}

func TestInterceptBeforeDeniesExhaustedAndExpired(t *testing.T) {
	svc := testService(t)
	exhausted := createSecret(t, svc, 3600, 1)
	raw := call(t, svc, "request.intercept_before", interceptBody(exhausted, "one", "/v1/chat/completions", true))
	if strings.Contains(string(raw), `"Terminate":true`) {
		t.Fatal("first admit terminated")
	}
	_ = call(t, svc, "usage.handle", `{"RequestID":"one","Failed":false,"Detail":{"InputTokens":1}}`)
	_ = call(t, svc, "request.complete", `{"RequestID":"one","Outcome":"succeeded"}`)
	raw = call(t, svc, "request.intercept_before", interceptBody(exhausted, "two", "/v1/messages/count_tokens", false))
	if !strings.Contains(string(raw), `"Terminate":true`) || !strings.Contains(string(raw), `"StatusCode":402`) {
		t.Fatal("exhausted count was not denied")
	}
	expired := createSecret(t, svc, -60, 100)
	raw = call(t, svc, "request.intercept_before", interceptBody(expired, "old", "/v1/messages/count_tokens", false))
	if !strings.Contains(string(raw), `"StatusCode":403`) {
		t.Fatal("expired count was not denied")
	}
}

func TestModelAccessPolicyUsesAliasAndPreservesThinkingSuffix(t *testing.T) {
	svc := testService(t)
	svc.store.cfg.ModelAccessMode = "custom"
	svc.store.cfg.Models = []ModelAccessModel{
		{Name: "gpt-5-codex", Alias: "codex-latest"},
		{Name: "claude-sonnet-4"},
	}
	secret := createSecret(t, svc, 3600, 100000)
	for _, model := range []string{"codex-latest", "codex-latest(high)", "claude-sonnet-4", "claude-sonnet-4(low)"} {
		raw := call(t, svc, "request.intercept_before", interceptModelBody(secret, "allow-"+model, model, "/v1/chat/completions", true))
		if strings.Contains(string(raw), `"Terminate":true`) {
			t.Fatalf("allowed model %q was denied: %s", model, raw)
		}
	}
	for _, model := range []string{"gpt-5-codex", "other-model", "codex-latest-extra"} {
		var denied struct {
			Terminate    bool
			StatusCode   int
			ResponseBody []byte
		}
		decodeResult(t, call(t, svc, "request.intercept_before", interceptModelBody(secret, "deny-"+model, model, "/v1/chat/completions", true)), &denied)
		if !denied.Terminate || denied.StatusCode != 403 || !bytes.Contains(denied.ResponseBody, []byte(`"model_not_allowed"`)) {
			t.Fatalf("model %q was not denied by policy: %+v", model, denied)
		}
	}
}

func TestModelAccessPolicyDoesNotReserveBeforeAdmission(t *testing.T) {
	svc := testService(t)
	svc.store.cfg.ModelAccessMode = "custom"
	svc.store.cfg.Models = []ModelAccessModel{{Name: "allowed"}}
	secret := createSecret(t, svc, 3600, 1)
	denied := call(t, svc, "request.intercept_before", interceptModelBody(secret, "blocked", "blocked", "/v1/chat/completions", true))
	if !strings.Contains(string(denied), `"StatusCode":403`) {
		t.Fatalf("unexpected model policy response: %s", denied)
	}
	allowed := call(t, svc, "request.intercept_before", interceptModelBody(secret, "allowed", "allowed", "/v1/chat/completions", true))
	if strings.Contains(string(allowed), `"Terminate":true`) {
		t.Fatalf("allowed request was denied after blocked request: %s", allowed)
	}
}

func TestModelAccessPolicyIgnoresExternalKeys(t *testing.T) {
	svc := testService(t)
	svc.store.cfg.ModelAccessMode = "custom"
	svc.store.cfg.Models = []ModelAccessModel{{Name: "only-model"}}
	raw := call(t, svc, "request.intercept_before", interceptModelBody("operator-key", "external", "anything", "/v1/chat/completions", true))
	if strings.Contains(string(raw), `"Terminate":true`) {
		t.Fatalf("external key was affected by key-shop model policy: %s", raw)
	}
}

func TestModelAccessPolicyDefaultsToFull(t *testing.T) {
	svc := testService(t)
	secret := createSecret(t, svc, 3600, 100)
	raw := call(t, svc, "request.intercept_before", interceptModelBody(secret, "default-full", "any-model", "/v1/chat/completions", true))
	if strings.Contains(string(raw), `"Terminate":true`) {
		t.Fatalf("default full policy denied model: %s", raw)
	}
}

func TestModelAccessPolicyReadsBodyWhenModelFieldsAreEmpty(t *testing.T) {
	svc := testService(t)
	svc.store.cfg.ModelAccessMode = "custom"
	svc.store.cfg.Models = []ModelAccessModel{{Name: "gpt-5-codex", Alias: "codex-latest"}}
	secret := createSecret(t, svc, 3600, 100000)
	allowed := call(t, svc, "request.intercept_before", interceptBodyOnly(secret, "body-allow", "codex-latest(high)", "/v1/responses"))
	if strings.Contains(string(allowed), `"Terminate":true`) {
		t.Fatalf("body model was denied: %s", allowed)
	}
	assertModelDenied(t, call(t, svc, "request.intercept_before", interceptBodyOnly(secret, "body-deny", "gpt-5-codex", "/v1/messages")))
}

func TestModelAccessPolicyMatchesCaseAndIgnoresEmptyNames(t *testing.T) {
	svc := testService(t)
	svc.store.cfg.ModelAccessMode = "custom"
	svc.store.cfg.Models = []ModelAccessModel{
		{Name: "  ", Alias: "hidden-alias"},
		{Name: "Claude-Sonnet-4"},
		{Name: "GPT-5-Codex", Alias: "Codex-Latest"},
	}
	secret := createSecret(t, svc, 3600, 100000)
	for _, model := range []string{"CODEX-LATEST", "codex-latest(HIGH)", "claude-sonnet-4"} {
		raw := call(t, svc, "request.intercept_before", interceptModelBody(secret, "case-"+model, model, "/v1/chat/completions", true))
		if strings.Contains(string(raw), `"Terminate":true`) {
			t.Fatalf("model %q was denied: %s", model, raw)
		}
	}
	for _, model := range []string{"hidden-alias", "gpt-5-codex", "GPT-5-CODEX"} {
		assertModelDenied(t, call(t, svc, "request.intercept_before", interceptModelBody(secret, "case-deny-"+model, model, "/v1/chat/completions", true)))
	}
}

func TestModelAccessPolicyEmptyCustomListDeniesEveryModel(t *testing.T) {
	svc := testService(t)
	svc.store.cfg.ModelAccessMode = "custom"
	secret := createSecret(t, svc, 3600, 100)
	assertModelDenied(t, call(t, svc, "request.intercept_before", interceptModelBody(secret, "empty-list", "any-model", "/v1/chat/completions", true)))
}

func TestModelAccessPolicyUnrecognizedModeIsFull(t *testing.T) {
	svc := testService(t)
	svc.store.cfg.ModelAccessMode = "weird"
	svc.store.cfg.Models = []ModelAccessModel{{Name: "only-model"}}
	secret := createSecret(t, svc, 3600, 100)
	raw := call(t, svc, "request.intercept_before", interceptModelBody(secret, "unknown-mode", "anything", "/v1/chat/completions", true))
	if strings.Contains(string(raw), `"Terminate":true`) {
		t.Fatalf("unrecognized mode denied a model: %s", raw)
	}
}

func TestPagesShowRequestLimitAccounting(t *testing.T) {
	admin, err := os.ReadFile(filepath.Join("..", "web", "admin.html"))
	if err != nil {
		t.Fatal(err)
	}
	dash, err := os.ReadFile(filepath.Join("..", "web", "dashboard.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, snippet := range []string{
		"request_limit",
		"unlimited_requests",
		"RequestQuotaUsed",
		"request_limit_exceeded",
		"keys_request_limited",
		"/v0/management/plugins/key-shop/keys",
		"/v0/management/plugins/key-shop/stats",
		"page_size=25",
		"cli-proxy-auth",
	} {
		if !bytes.Contains(admin, []byte(snippet)) {
			t.Fatalf("admin page missing %s", snippet)
		}
	}
	for _, snippet := range []string{
		"Request limit",
		"Request limit reached. Ask the administrator to raise the limit.",
		"RequestQuotaUsed",
		"RequestSuccessCount",
		"RequestFailureCount",
		"RequestCanceledCount",
		"RequestRejectedCount",
		"/client/api/session?limit=25",
		"keyshop.client",
	} {
		if !bytes.Contains(dash, []byte(snippet)) {
			t.Fatalf("client page missing %s", snippet)
		}
	}
}

func TestAdminModelPolicySavePreservesLoadedConfig(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "web", "admin.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(page)
	for _, snippet := range []string{
		"/v8/management/config/plugins/configs/key-shop",
		"model_access_mode",
		`"gemini-interactions"`,
	} {
		if !strings.Contains(source, snippet) {
			t.Fatalf("admin model policy missing %s", snippet)
		}
	}
}

func TestEmbeddedPagesAreSelfContainedVue(t *testing.T) {
	admin, err := os.ReadFile(filepath.Join("..", "web", "admin.html"))
	if err != nil {
		t.Fatal(err)
	}
	dash, err := os.ReadFile(filepath.Join("..", "web", "dashboard.html"))
	if err != nil {
		t.Fatal(err)
	}
	for name, page := range map[string][]byte{"admin": admin, "dashboard": dash} {
		if bytes.Contains(page, []byte("React.createElement")) {
			t.Fatalf("%s still uses React UMD", name)
		}
		if bytes.Contains(page, []byte("FLAT_PICKR")) || bytes.Contains(page, []byte("flatpickr")) {
			t.Fatalf("%s still depends on Flatpickr", name)
		}
		if bytes.Contains(page, []byte("datetime-local")) {
			t.Fatalf("%s still falls back to datetime-local", name)
		}
		if bytes.Contains(page, []byte(`src="http`)) || bytes.Contains(page, []byte(`href="http`)) {
			t.Fatalf("%s still loads a remote asset", name)
		}
		if !bytes.Contains(page, []byte(`id="app"`)) {
			t.Fatalf("%s is missing the Vue mount node", name)
		}
		for _, snippet := range []string{"This preview", "Preview scenario", "keyshop.prototype", "createAdminService", "createClientService"} {
			if bytes.Contains(page, []byte(snippet)) {
				t.Fatalf("%s still ships mock preview UI %q", name, snippet)
			}
		}
		if !bytes.Contains(page, []byte(".only-desktop")) || !bytes.Contains(page, []byte(".only-mobile")) {
			t.Fatalf("%s is missing explicit desktop/mobile layout rules", name)
		}
	}
}

func TestModelAccessPolicyLogsRejectionWithoutReserving(t *testing.T) {
	svc := testService(t)
	svc.store.cfg.ModelAccessMode = "custom"
	svc.store.cfg.Models = []ModelAccessModel{{Name: "allowed"}}
	secret := createSecret(t, svc, 3600, 1)
	var denied struct {
		StatusCode   int
		ResponseBody []byte
	}
	decodeResult(t, call(t, svc, "request.intercept_before", interceptModelBody(secret, "blocked-row", "blocked", "/v1/chat/completions", true)), &denied)
	if denied.StatusCode != 403 || !bytes.Contains(denied.ResponseBody, []byte(`"model_not_allowed"`)) {
		t.Fatalf("unexpected denial: %+v body=%s", denied, denied.ResponseBody)
	}
	for _, table := range []string{"reservations", "usage_notes"} {
		var n int
		if err := svc.store.db.QueryRow(`SELECT COUNT(1) FROM `+table+` WHERE request_id=?`, "blocked-row").Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s stored denied request n=%d err=%v", table, n, err)
		}
	}
	var status, code string
	if err := svc.store.db.QueryRow(`SELECT status, rejection_code FROM requests WHERE request_id=?`, "blocked-row").Scan(&status, &code); err != nil || status != "rejected" || code != "model_not_allowed" {
		t.Fatalf("status=%s code=%s err=%v", status, code, err)
	}
	key, err := svc.store.Lookup(secret)
	if err != nil || key.RequestQuotaUsed != 1 || key.RequestRejectedCount != 1 {
		t.Fatalf("model denial must consume request quota: %+v err=%v", key, err)
	}
}

func TestHostGateRejectionIsRecordedWithoutQuota(t *testing.T) {
	svc := testService(t)
	secret := createSecret(t, svc, 3600, 10)
	raw := call(t, svc, "keyshop.note_rejection", `{"Secret":"`+secret+`","RequestID":"gate-1","Code":"model_not_allowed","Status":403}`)
	if !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("note rejection failed: %s", raw)
	}
	key, err := svc.store.Lookup(secret)
	if err != nil || key.RequestQuotaUsed != 1 || key.RequestRejectedCount != 1 || key.RequestCount != 1 {
		t.Fatalf("gate rejection accounting=%+v err=%v", key, err)
	}
	var code string
	if err := svc.store.db.QueryRow(`SELECT rejection_code FROM requests WHERE request_id=?`, "gate-1").Scan(&code); err != nil || code != "model_not_allowed" {
		t.Fatalf("code=%s err=%v", code, err)
	}
}

func TestRequestLimitReturns429AndRemainsReachable(t *testing.T) {
	svc := testService(t)
	secret := createSecret(t, svc, 3600, 100)
	created := call(t, svc, "management.handle", `{"Method":"POST","Path":"/plugins/key-shop/keys","Body":{"ttl_seconds":3600,"token_limit":100,"request_limit":1}}`)
	var env struct {
		Result struct{ Body []byte }
	}
	if err := json.Unmarshal(created, &env); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Secret string
		Key    Key
	}
	if err := json.Unmarshal(env.Result.Body, &payload); err != nil || payload.Key.RequestLimit != 1 {
		t.Fatalf("create did not persist request limit: %s", env.Result.Body)
	}
	secret = payload.Secret
	raw := call(t, svc, "frontend_auth.authenticate", authBody(secret, "/v1/chat/completions"))
	if !strings.Contains(string(raw), `"Authenticated":true`) {
		t.Fatal("fresh limited key was not authenticated")
	}
	first := call(t, svc, "request.intercept_before", interceptBody(secret, "rq-1", "/v1/chat/completions", true))
	if strings.Contains(string(first), `"Terminate":true`) {
		t.Fatal("first request was denied")
	}
	auth := call(t, svc, "frontend_auth.authenticate", authBody(secret, "/v1/chat/completions"))
	if !strings.Contains(string(auth), `"Authenticated":true`) {
		t.Fatal("request-limit exhaustion was rejected before the interceptor")
	}
	var denied struct {
		Terminate    bool
		StatusCode   int
		ResponseBody []byte
	}
	decodeResult(t, call(t, svc, "request.intercept_before", interceptBody(secret, "rq-2", "/v1/chat/completions", true)), &denied)
	if !denied.Terminate || denied.StatusCode != 429 || !bytes.Contains(denied.ResponseBody, []byte(`"request_limit_exceeded"`)) || !bytes.Contains(denied.ResponseBody, []byte(`"insufficient_quota"`)) {
		t.Fatalf("request limit denial=%+v body=%s", denied, denied.ResponseBody)
	}
	var listedEnv struct {
		Result struct{ Body []byte }
	}
	if err := json.Unmarshal(call(t, svc, "management.handle", `{"Method":"GET","Path":"/plugins/key-shop/requests","Query":{"code":["request_limit_exceeded"]}}`), &listedEnv); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(listedEnv.Result.Body, []byte(`"request_limit_exceeded"`)) {
		t.Fatalf("request limit rejection was not listed: %s", listedEnv.Result.Body)
	}
}

func TestCreateWithoutNameKeepsNameOnLaterUpdate(t *testing.T) {
	svc := testService(t)
	raw := call(t, svc, "management.handle", `{"Method":"POST","Path":"/plugins/key-shop/keys","Body":{"label":"","ttl_seconds":3600,"token_limit":10}}`)
	var created struct {
		Result struct {
			Body []byte
		}
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Secret string
		Key    struct {
			ID string
		}
	}
	if err := json.Unmarshal(created.Result.Body, &payload); err != nil || payload.Secret == "" || payload.Key.ID == "" {
		t.Fatal("empty name create failed")
	}
	named := call(t, svc, "management.handle", `{"Method":"POST","Path":"/plugins/key-shop/keys","Body":{"label":"shop","ttl_seconds":3600,"token_limit":10}}`)
	if err := json.Unmarshal(named, &created); err != nil {
		t.Fatal(err)
	}
	var namedPayload struct {
		Key struct {
			ID, Label string
		}
	}
	if err := json.Unmarshal(created.Result.Body, &namedPayload); err != nil || namedPayload.Key.Label != "shop" {
		t.Fatal("name was not saved")
	}
	updated := call(t, svc, "management.handle", `{"Method":"POST","Path":"/plugins/key-shop/keys/update","Body":{"id":"`+namedPayload.Key.ID+`","status":"disabled"}}`)
	if err := json.Unmarshal(updated, &created); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(created.Result.Body, []byte(`"Label":"shop"`)) {
		t.Fatal("status update cleared the name")
	}
}

func TestManagementCreateAndPublicSession(t *testing.T) {
	svc := testService(t)
	raw := call(t, svc, "management.handle", `{"Method":"POST","Path":"/plugins/key-shop/keys","Body":{"label":"buyer","token_limit":50}}`)
	if strings.Contains(string(raw), "secret_hash") {
		t.Fatal("create leaked hash")
	}
	secret := createSecret(t, svc, 3600, 50)
	listed := call(t, svc, "management.handle", `{"Method":"GET","Path":"/plugins/key-shop/keys"}`)
	if strings.Contains(string(listed), secret) || strings.Contains(string(listed), "secret_hash") {
		t.Fatal("list leaked secret material")
	}
	session := call(t, svc, "management.handle", `{"Method":"GET","Path":"/api/session","Headers":{"Authorization":["Bearer `+secret+`"]}}`)
	if !strings.Contains(string(session), `"StatusCode":200`) {
		t.Fatal("session was not authorized")
	}
	missing := call(t, svc, "management.handle", `{"Method":"GET","Path":"/api/session","Query":{"key":["ck_query"]}}`)
	if !strings.Contains(string(missing), `"StatusCode":401`) {
		t.Fatal("query key was accepted")
	}
}

func TestManagementPaginationMetadata(t *testing.T) {
	svc := testService(t)
	for i := 0; i < 2; i++ {
		createSecret(t, svc, 3600, 10)
	}
	raw := call(t, svc, "management.handle", `{"Method":"GET","Path":"/plugins/key-shop/keys","Query":{"page":["2"],"page_size":["1"]}}`)
	var envelope struct {
		Result struct{ Body []byte }
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || !bytes.Contains(envelope.Result.Body, []byte(`"total":2`)) || !bytes.Contains(envelope.Result.Body, []byte(`"page":2`)) {
		t.Fatalf("pagination metadata missing: %s", raw)
	}
}

func TestHostABIDecodesAdmission(t *testing.T) {
	svc := testService(t)
	var register struct {
		SchemaVersion uint32 `json:"schema_version"`
		Capabilities  struct {
			FrontendAuth       bool `json:"frontend_auth_provider"`
			RequestInterceptor bool `json:"request_interceptor"`
			Lifecycle          bool `json:"request_lifecycle_plugin"`
			Usage              bool `json:"usage_plugin"`
			Management         bool `json:"management_api"`
		} `json:"capabilities"`
	}
	decodeResult(t, call(t, svc, "plugin.register", `{}`), &register)
	if register.SchemaVersion != 6 || !register.Capabilities.RequestInterceptor || !register.Capabilities.Management {
		t.Fatalf("register = %+v", register)
	}
	var ident struct {
		Identifier string `json:"identifier"`
	}
	decodeResult(t, call(t, svc, "frontend_auth.identifier", `{}`), &ident)
	if ident.Identifier != "key-shop" {
		t.Fatalf("identifier = %q", ident.Identifier)
	}
	secret := createSecret(t, svc, 3600, 1)
	_ = call(t, svc, "request.intercept_before", interceptBody(secret, "abi", "/v1/chat/completions", true))
	_ = call(t, svc, "usage.handle", `{"RequestID":"abi","Failed":false,"Detail":{"InputTokens":1}}`)
	_ = call(t, svc, "request.complete", `{"RequestID":"abi","Outcome":"succeeded"}`)
	var denied struct {
		Terminate  bool
		StatusCode int
	}
	decodeResult(t, call(t, svc, "request.intercept_before", interceptBody(secret, "abi2", "/v1/chat/completions", false)), &denied)
	if !denied.Terminate || denied.StatusCode != 402 {
		t.Fatalf("denied = %+v", denied)
	}
}

func TestQuotaDenialUsesInsufficientQuotaAndLogsRejection(t *testing.T) {
	svc := testService(t)
	secret := createSecret(t, svc, 3600, 1)
	_ = call(t, svc, "request.intercept_before", interceptBody(secret, "bill", "/v1/chat/completions", true))
	_ = call(t, svc, "usage.handle", `{"RequestID":"bill","Failed":false,"Detail":{"InputTokens":1}}`)
	_ = call(t, svc, "request.complete", `{"RequestID":"bill","Outcome":"succeeded"}`)
	raw := call(t, svc, "request.intercept_before", interceptBody(secret, "over", "/v1/chat/completions", true))
	var denied struct {
		StatusCode   int
		ResponseBody []byte
	}
	decodeResult(t, raw, &denied)
	if denied.StatusCode != 402 || !bytes.Contains(denied.ResponseBody, []byte(`"type":"insufficient_quota"`)) {
		t.Fatalf("denial body missing quota type")
	}
	var listedEnv struct {
		Result struct {
			Body []byte
		}
	}
	if err := json.Unmarshal(call(t, svc, "management.handle", `{"Method":"GET","Path":"/plugins/key-shop/requests","Query":{"status":["rejected"]}}`), &listedEnv); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(listedEnv.Result.Body, []byte(`"rejected"`)) {
		t.Fatal("rejected request was not logged")
	}
}

func TestSessionReturnsBaseURL(t *testing.T) {
	svc := testService(t)
	secret := createSecret(t, svc, 3600, 10)
	raw := call(t, svc, "management.handle", `{"Method":"GET","Path":"/api/session","Headers":{"Authorization":["Bearer `+secret+`"],"Host":["192.168.8.1:8330"],"X-Forwarded-Proto":["http"]}}`)
	var env struct {
		Result struct {
			Body []byte
		}
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(env.Result.Body, []byte(`"base_url":"http://192.168.8.1:8330/v1"`)) {
		t.Fatal("session missing base_url")
	}
}

func TestRequestsAndSessionIncludeFilteredTotal(t *testing.T) {
	svc := testService(t)
	secret := createSecret(t, svc, 3600, 10)
	_ = call(t, svc, "request.intercept_before", interceptBody(secret, "tot", "/v1/chat/completions", true))
	_ = call(t, svc, "usage.handle", `{"RequestID":"tot","Failed":false,"Detail":{"InputTokens":1}}`)
	_ = call(t, svc, "request.complete", `{"RequestID":"tot","Outcome":"succeeded"}`)
	raw := call(t, svc, "management.handle", `{"Method":"GET","Path":"/plugins/key-shop/requests","Query":{"limit":["1"]}}`)
	var listed struct {
		Result struct{ Body []byte }
	}
	if err := json.Unmarshal(raw, &listed); err != nil || !bytes.Contains(listed.Result.Body, []byte(`"total":1`)) {
		t.Fatalf("requests total missing: %s", raw)
	}
	session := call(t, svc, "management.handle", `{"Method":"GET","Path":"/api/session","Headers":{"Authorization":["Bearer `+secret+`"]}}`)
	var env struct {
		Result struct{ Body []byte }
	}
	if err := json.Unmarshal(session, &env); err != nil || !bytes.Contains(env.Result.Body, []byte(`"total":1`)) {
		t.Fatalf("session total missing: %s", session)
	}
}

func TestSecondInterceptDoesNotFailTheStore(t *testing.T) {
	svc := testService(t)
	secret := createSecret(t, svc, 3600, 100)
	body := interceptBody(secret, "same-request", "/v1/chat/completions", true)
	first := call(t, svc, "request.intercept_before", body)
	if strings.Contains(string(first), `"Terminate":true`) {
		t.Fatal("first intercept terminated")
	}
	after := call(t, svc, "request.intercept_after", body)
	if strings.Contains(string(after), "key_store_unavailable") || strings.Contains(string(after), `"Terminate":true`) {
		t.Fatal("after intercept failed the store")
	}
}

func TestClosedStoreTerminatesInsteadOfPassing(t *testing.T) {
	svc := testService(t)
	secret := createSecret(t, svc, 3600, 100)
	if err := svc.store.Close(); err != nil {
		t.Fatal(err)
	}
	var denied struct {
		Terminate  bool
		StatusCode int
	}
	decodeResult(t, call(t, svc, "request.intercept_before", interceptBody(secret, "closed", "/v1/chat/completions", true)), &denied)
	if !denied.Terminate || denied.StatusCode != 503 {
		t.Fatalf("closed store = %+v", denied)
	}
}

func assertModelDenied(t *testing.T, raw []byte) {
	t.Helper()
	var denied struct {
		Terminate    bool
		StatusCode   int
		ResponseBody []byte
	}
	decodeResult(t, raw, &denied)
	if !denied.Terminate || denied.StatusCode != 403 || !bytes.Contains(denied.ResponseBody, []byte(`"model_not_allowed"`)) {
		t.Fatalf("model was not denied by policy: %+v body=%s", denied, denied.ResponseBody)
	}
}

func decodeResult(t *testing.T, raw []byte, dest any) {
	t.Helper()
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		t.Fatalf("envelope = %s", raw)
	}
	if err := json.Unmarshal(env.Result, dest); err != nil {
		t.Fatal(err)
	}
}

func authBody(secret, path string) string {
	body, _ := json.Marshal(map[string]any{"Path": path, "Headers": map[string][]string{"Authorization": {"Bearer " + secret}}})
	return string(body)
}

func interceptBody(secret, id, path string, generate bool) string {
	return interceptModelBody(secret, id, "", path, generate)
}

func interceptModelBody(secret, id, model, path string, generate bool) string {
	body, _ := json.Marshal(map[string]any{
		"RequestID":      id,
		"RequestedModel": model,
		"Model":          model,
		"Headers":        map[string][]string{"Authorization": {"Bearer " + secret}},
		"Metadata":       map[string]any{"request_path": path, "generate": generate},
	})
	return string(body)
}

func interceptBodyOnly(secret, id, model, path string) string {
	payload, _ := json.Marshal(map[string]any{"model": model})
	body, _ := json.Marshal(map[string]any{
		"RequestID": id,
		"Headers":   map[string][]string{"Authorization": {"Bearer " + secret}},
		"Body":      payload,
		"Metadata":  map[string]any{"request_path": path, "generate": true},
	})
	return string(body)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
