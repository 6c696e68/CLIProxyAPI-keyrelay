package keyshop

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// manage calls a management route and returns the HTTP status and body.
func manage(t *testing.T, svc *Service, method, path string, body any) (int, []byte) {
	t.Helper()
	payload := map[string]any{"Method": method, "Path": "/v0/management/plugins/key-shop" + path}
	if q, ok := body.(map[string][]string); ok {
		payload["Query"] = q
	} else if body != nil {
		payload["Body"] = body
	}
	raw := call(t, svc, "management.handle", string(mustJSON(t, payload)))
	var res struct {
		StatusCode int
		Body       []byte
	}
	decodeResult(t, raw, &res)
	return res.StatusCode, res.Body
}

func createGroupVia(t *testing.T, svc *Service, name string, models ...string) string {
	t.Helper()
	status, body := manage(t, svc, http.MethodPost, "/groups", map[string]any{"name": name, "models": models})
	var out struct{ Group Group }
	if status != 200 || json.Unmarshal(body, &out) != nil || out.Group.ID == "" {
		t.Fatalf("create group: %d %s", status, body)
	}
	return out.Group.ID
}

func createKeyIn(t *testing.T, svc *Service, group string) (string, string) {
	t.Helper()
	status, body := manage(t, svc, http.MethodPost, "/keys", map[string]any{"group_id": group, "ttl_seconds": 3600, "token_limit": 100000})
	var out struct {
		Key    Key
		Secret string
	}
	if status != 200 || json.Unmarshal(body, &out) != nil || out.Secret == "" {
		t.Fatalf("create key: %d %s", status, body)
	}
	return out.Key.ID, out.Secret
}

func denied(t *testing.T, svc *Service, secret, id, model string) bool {
	t.Helper()
	var res struct {
		Terminate    bool
		StatusCode   int
		ResponseBody []byte
	}
	decodeResult(t, call(t, svc, "request.intercept_before", interceptModelBody(secret, id, model, "/v1/chat/completions", true)), &res)
	if res.Terminate && (res.StatusCode != 403 || !bytes.Contains(res.ResponseBody, []byte(`"model_not_allowed"`))) {
		t.Fatalf("unexpected denial: %+v", res)
	}
	return res.Terminate
}

func TestInterceptEnforcesTheKeysGroup(t *testing.T) {
	svc := testService(t)
	group := createGroupVia(t, svc, "two", "vippro/gpt-6-sol", "vippro/gpt-6-luna")
	keyID, secret := createKeyIn(t, svc, group)
	for i, model := range []string{"vippro/gpt-6-sol", "vippro/gpt-6-luna(high)", "VIPPRO/GPT-6-SOL"} {
		if denied(t, svc, secret, "ok-"+string(rune('a'+i)), model) {
			t.Fatalf("%q should be allowed by the group", model)
		}
	}
	for i, model := range []string{"vippro/gpt-6-astra", "gpt-image-2", "gpt-6-sol"} {
		if !denied(t, svc, secret, "no-"+string(rune('a'+i)), model) {
			t.Fatalf("%q should be denied by the group", model)
		}
	}
	rows, _, summary, err := svc.store.AdminRequests(AdminRequestQuery{KeyID: keyID, Status: "rejected"})
	if err != nil || len(rows) != 3 || summary.Rejected != 3 || rows[0].RejectionCode != "model_not_allowed" {
		t.Fatalf("denials must appear in the log for the key: rows=%d err=%v", len(rows), err)
	}
	key, _ := svc.store.keyByID(keyID)
	if key.RequestQuotaUsed != 6 {
		t.Fatalf("every attempt counts toward the request quota, got %d", key.RequestQuotaUsed)
	}
}

func TestDeniedModelDoesNotReserveTokens(t *testing.T) {
	svc := testService(t)
	group := createGroupVia(t, svc, "one", "m1")
	keyID, secret := createKeyIn(t, svc, group)
	if !denied(t, svc, secret, "blocked", "m2") {
		t.Fatal("m2 should be denied")
	}
	if reserved, err := svc.store.reserved(keyID); err != nil || reserved != 0 {
		t.Fatalf("reserved=%d err=%v", reserved, err)
	}
}

func TestGroupEditAndKeyMoveTakeEffectImmediately(t *testing.T) {
	svc := testService(t)
	g1 := createGroupVia(t, svc, "g1", "m1")
	g2 := createGroupVia(t, svc, "g2", "m2")
	keyID, secret := createKeyIn(t, svc, g1)
	if denied(t, svc, secret, "r1", "m1") || !denied(t, svc, secret, "r2", "m2") {
		t.Fatal("initial group not enforced")
	}
	if status, body := manage(t, svc, http.MethodPost, "/groups/update", map[string]any{"id": g1, "name": "g1", "models": []string{"m1", "m2"}}); status != 200 {
		t.Fatalf("update group: %d %s", status, body)
	}
	if denied(t, svc, secret, "r3", "m2") {
		t.Fatal("edited group should now allow m2")
	}
	if status, body := manage(t, svc, http.MethodPost, "/keys/bulk-update", map[string]any{"ids": []string{keyID}, "group_id": g2}); status != 200 {
		t.Fatalf("bulk-update: %d %s", status, body)
	}
	if !denied(t, svc, secret, "r4", "m1") || denied(t, svc, secret, "r5", "m2") {
		t.Fatal("moved key should follow its new group")
	}
}

func TestAllModelsGroupAndLegacyKeysAreUnrestricted(t *testing.T) {
	svc := testService(t)
	_, allSecret := createKeyIn(t, svc, AllModelsGroupID)
	if denied(t, svc, allSecret, "a1", "anything-at-all") {
		t.Fatal("all-models group must allow every model")
	}
	legacy, _, err := svc.store.Create(CreateInput{Label: "legacy"})
	if err != nil || legacy.GroupID != "" {
		t.Fatal(err)
	}
}

func TestGroupLayersOnTopOfGlobalPolicy(t *testing.T) {
	svc := testService(t)
	svc.store.cfg.ModelAccessMode = "custom"
	svc.store.cfg.Models = []ModelAccessModel{{Name: "vippro/gpt-6-sol", Alias: "gpt-6-sol"}, {Name: "vippro/gpt-6-luna", Alias: "gpt-6-luna"}}
	group := createGroupVia(t, svc, "sol-only", "vippro/gpt-6-sol")
	_, secret := createKeyIn(t, svc, group)
	// The host reports the client alias as RequestedModel and the rewritten upstream name as Model.
	intercept := func(id, requested, model string) bool {
		body, _ := json.Marshal(map[string]any{
			"RequestID": id, "RequestedModel": requested, "Model": model,
			"Headers":  map[string][]string{"Authorization": {"Bearer " + secret}},
			"Metadata": map[string]any{"request_path": "/v1/chat/completions", "generate": true},
		})
		var res struct{ Terminate bool }
		decodeResult(t, call(t, svc, "request.intercept_before", string(body)), &res)
		return res.Terminate
	}
	if intercept("x1", "gpt-6-sol", "vippro/gpt-6-sol") {
		t.Fatal("alias that maps into the group should pass")
	}
	if intercept("x2", "gpt-6-sol(high)", "vippro/gpt-6-sol(high)") {
		t.Fatal("thinking suffix should not matter")
	}
	if !intercept("x3", "gpt-6-luna", "vippro/gpt-6-luna") {
		t.Fatal("a model in the global list but outside the group must be denied")
	}
	if !intercept("x4", "vippro/other", "vippro/other") {
		t.Fatal("the global policy must still deny unknown models")
	}
}

func TestGroupedAliasSkipsGlobalPolicyAfterHostRewrite(t *testing.T) {
	svc := testService(t)
	svc.store.cfg.ModelAccessMode = "custom"
	svc.store.cfg.Models = []ModelAccessModel{{Name: "vippro/gpt-6-sol", Alias: "global-sol"}}
	status, body := manage(t, svc, http.MethodPost, "/groups", map[string]any{
		"name":   "sol",
		"models": []map[string]string{{"name": "vippro/gpt-6-sol", "alias": "client-sol"}},
	})
	var created struct{ Group Group }
	if status != 200 || json.Unmarshal(body, &created) != nil {
		t.Fatalf("create group: %d %s", status, body)
	}
	_, secret := createKeyIn(t, svc, created.Group.ID)
	_, legacySecret, err := svc.store.Create(CreateInput{Label: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	intercept := func(id, sec, requested, model string) bool {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{
			"RequestID": id, "RequestedModel": requested, "Model": model,
			"Headers":  map[string][]string{"Authorization": {"Bearer " + sec}},
			"Metadata": map[string]any{"request_path": "/v1/chat/completions", "generate": true},
		})
		var res struct{ Terminate bool }
		decodeResult(t, call(t, svc, "request.intercept_before", string(raw)), &res)
		return res.Terminate
	}
	if intercept("grouped", secret, "client-sol", "vippro/gpt-6-sol") {
		t.Fatal("grouped alias was denied by the global list after the host rewrite")
	}
	if !intercept("legacy", legacySecret, "client-sol", "vippro/gpt-6-sol") {
		t.Fatal("ungrouped key must still be denied by the global alias list")
	}
}

func TestGroupRoutesValidationAndInUse(t *testing.T) {
	svc := testService(t)
	if status, _ := manage(t, svc, http.MethodPost, "/groups", map[string]any{"name": "empty"}); status != 400 {
		t.Fatalf("empty selection should be 400, got %d", status)
	}
	group := createGroupVia(t, svc, "vippro", "vippro/a")
	if status, _ := manage(t, svc, http.MethodPost, "/groups", map[string]any{"name": "VIPPRO", "models": []string{"x"}}); status != 400 {
		t.Fatalf("duplicate name should be 400, got %d", status)
	}
	createKeyIn(t, svc, group)
	status, body := manage(t, svc, http.MethodPost, "/groups/delete", map[string]any{"id": group})
	if status != 409 || !strings.Contains(string(body), `"key_count":1`) {
		t.Fatalf("in-use delete: %d %s", status, body)
	}
	if status, _ = manage(t, svc, http.MethodPost, "/groups/delete", map[string]any{"id": AllModelsGroupID}); status != 403 {
		t.Fatalf("system delete should be 403, got %d", status)
	}
	if status, _ = manage(t, svc, http.MethodPost, "/groups/delete", map[string]any{"id": "ghost"}); status != 404 {
		t.Fatalf("unknown delete should be 404, got %d", status)
	}
	status, body = manage(t, svc, http.MethodGet, "/groups", nil)
	var list struct{ Groups []Group }
	if status != 200 || json.Unmarshal(body, &list) != nil || len(list.Groups) != 2 || list.Groups[0].ID != AllModelsGroupID || list.Groups[1].KeyCount != 1 {
		t.Fatalf("list: %d %s", status, body)
	}
}

func TestCreateRequiresGroup(t *testing.T) {
	svc := testService(t)
	for _, path := range []string{"/keys", "/keys/bulk"} {
		body := map[string]any{"label": "x", "count": 2}
		if status, out := manage(t, svc, http.MethodPost, path, body); status != 400 || !strings.Contains(string(out), "group_id is required") {
			t.Fatalf("%s without group: %d %s", path, status, out)
		}
		body["group_id"] = "ghost"
		if status, _ := manage(t, svc, http.MethodPost, path, body); status != 400 {
			t.Fatalf("%s with unknown group: %d", path, status)
		}
	}
	group := createGroupVia(t, svc, "g", "m")
	status, out := manage(t, svc, http.MethodPost, "/keys/bulk", map[string]any{"label": "x", "count": 2, "group_id": group})
	if status != 200 || strings.Count(string(out), `"GroupID":"`+group+`"`) != 2 {
		t.Fatalf("bulk create: %d %s", status, out)
	}
}

func TestBulkRoutesAndKeyGroupFilter(t *testing.T) {
	svc := testService(t)
	g1 := createGroupVia(t, svc, "g1", "m")
	g2 := createGroupVia(t, svc, "g2", "m")
	var ids []string
	for range 3 {
		id, _ := createKeyIn(t, svc, g1)
		ids = append(ids, id)
	}
	status, body := manage(t, svc, http.MethodPost, "/keys/bulk-update", map[string]any{"ids": ids[:2], "group_id": g2, "status": "disabled"})
	if status != 200 || !strings.Contains(string(body), `"updated":2`) {
		t.Fatalf("bulk-update: %d %s", status, body)
	}
	if status, _ = manage(t, svc, http.MethodPost, "/keys/bulk-update", map[string]any{"ids": []string{ids[2], "ghost"}, "status": "disabled"}); status != 404 {
		t.Fatalf("unknown id should be 404, got %d", status)
	}
	if status, _ = manage(t, svc, http.MethodPost, "/keys/bulk-update", map[string]any{"ids": ids}); status != 400 {
		t.Fatalf("no fields should be 400, got %d", status)
	}
	_, body = manage(t, svc, http.MethodGet, "/keys", map[string][]string{"group": {g2}})
	var listed struct {
		Keys  []Key
		Total int
	}
	if json.Unmarshal(body, &listed) != nil || listed.Total != 2 || len(listed.Keys) != 2 {
		t.Fatalf("group filter: %s", body)
	}
	status, body = manage(t, svc, http.MethodPost, "/keys/bulk-delete", map[string]any{"ids": ids})
	if status != 200 || !strings.Contains(string(body), `"deleted":3`) {
		t.Fatalf("bulk-delete: %d %s", status, body)
	}
}

func TestUsageNoteFlowsIntoRequestLogThroughHostCalls(t *testing.T) {
	svc := testService(t)
	group := createGroupVia(t, svc, "g", "m")
	keyID, secret := createKeyIn(t, svc, group)
	call(t, svc, "request.intercept_before", interceptModelBody(secret, "life-1", "m", "/v1/chat/completions", true))
	call(t, svc, "usage.handle", `{"RequestID":"life-1","Provider":"ch-relay-vippro","AuthID":"acct-1","AuthIndex":"i1","AuthType":"codex","ReasoningEffort":"max","ServiceTier":"priority","TTFT":2660000000,"Detail":{"InputTokens":48,"OutputTokens":385,"ReasoningTokens":1200,"CachedTokens":36}}`)
	call(t, svc, "request.complete", `{"RequestID":"life-1","Model":"m","RequestedModel":"alias-m","SourceFormat":"openai","Outcome":"succeeded","StatusCode":200,"Stream":false,"Latency":35900000000,"StartedAt":"2026-10-17T00:00:00Z"}`)
	status, body := manage(t, svc, http.MethodGet, "/requests", map[string][]string{"id": {keyID}})
	var out struct {
		Requests []AdminRequest
		Summary  RequestSummary
	}
	if status != 200 || json.Unmarshal(body, &out) != nil || len(out.Requests) != 1 {
		t.Fatalf("requests: %d %s", status, body)
	}
	r := out.Requests[0]
	if r.TTFTMs != 2660 || r.AuthID != "acct-1" || r.Effort != "max" || r.ServiceTier != "priority" || r.RequestedModel != "alias-m" || r.Output != 385 || r.GroupName != "g" || r.KeyLabel == "" {
		t.Fatalf("row=%+v", r)
	}
	if status, body = manage(t, svc, http.MethodGet, "/stats/usage", map[string][]string{"bucket": {"day"}}); status != 200 || !strings.Contains(string(body), `"top_accounts":[{"id":"acct-1"`) {
		t.Fatalf("usage stats: %d %s", status, body)
	}
}

func TestSessionShowsGroupClientNames(t *testing.T) {
	svc := testService(t)
	svc.store.cfg.ModelAccessMode = "custom"
	svc.store.cfg.Models = []ModelAccessModel{{Name: "vippro/gpt-6-sol", Alias: "global-sol"}, {Name: "vippro/gpt-6-luna", Alias: "gpt-6-luna"}}
	status, body := manage(t, svc, http.MethodPost, "/groups", map[string]any{
		"name": "sol",
		"models": []map[string]string{
			{"name": "vippro/gpt-6-sol", "alias": "gpt-6-sol"},
			{"name": "vippro/not-in-global"},
		},
	})
	var created struct{ Group Group }
	if status != 200 || json.Unmarshal(body, &created) != nil || created.Group.Models[0].Alias != "gpt-6-sol" || created.Group.Models[1].Alias != "" {
		t.Fatalf("create with alias: %d %s", status, body)
	}
	_, secret := createKeyIn(t, svc, created.Group.ID)
	raw := call(t, svc, "management.handle", `{"Method":"GET","Path":"/api/session","Headers":{"Authorization":["Bearer `+secret+`"]}}`)
	var env struct{ Result struct{ Body []byte } }
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(env.Result.Body, []byte(`"models":["gpt-6-sol","vippro/not-in-global"]`)) || !bytes.Contains(env.Result.Body, []byte(`"group":{"all_models":false,"id":"`+created.Group.ID+`","name":"sol"}`)) {
		t.Fatalf("session = %s", env.Result.Body)
	}
	if bytes.Contains(env.Result.Body, []byte(`"global-sol"`)) {
		t.Fatalf("group session must not use the global alias: %s", env.Result.Body)
	}
}

func TestResolveModelMatrix(t *testing.T) {
	svc := testService(t)
	status, body := manage(t, svc, http.MethodPost, "/groups", map[string]any{
		"name": "sol",
		"models": []any{
			map[string]string{"name": "vippro/gpt-6-sol", "alias": "gpt-6-sol"},
			"vippro/plain",
		},
	})
	var created struct{ Group Group }
	if status != 200 || json.Unmarshal(body, &created) != nil {
		t.Fatalf("create: %d %s", status, body)
	}
	keyID, secret := createKeyIn(t, svc, created.Group.ID)
	_, allSecret := createKeyIn(t, svc, AllModelsGroupID)
	legacy, legacySecret, err := svc.store.Create(CreateInput{Label: "legacy"})
	if err != nil || legacy.GroupID != "" {
		t.Fatal(err)
	}
	if _, err = svc.store.db.Exec(`UPDATE keys SET group_id=? WHERE id=?`, "missing-group", keyID); err != nil {
		t.Fatal(err)
	}
	missingSecret := secret
	keyID, secret, err = func() (string, string, error) {
		key, sec, errCreate := svc.store.Create(CreateInput{Label: "grouped", GroupID: created.Group.ID})
		return key.ID, sec, errCreate
	}()
	if err != nil {
		t.Fatal(err)
	}
	_ = keyID
	resolve := func(sec, model, bodyModel string) resolveModelResult {
		t.Helper()
		raw := call(t, svc, "keyshop.resolve_model", string(mustJSON(t, map[string]string{
			"Secret": sec, "Model": model, "BodyModel": bodyModel,
		})))
		var res resolveModelResult
		decodeResult(t, raw, &res)
		return res
	}
	if got := resolve(secret, "gpt-6-sol", "gpt-6-sol"); got.Mode != "group" || !got.Allowed || got.Model != "vippro/gpt-6-sol" {
		t.Fatalf("alias rewrite: %+v", got)
	}
	if got := resolve(secret, "gpt-6-sol(high)", "gpt-6-sol(high)"); got.Mode != "group" || !got.Allowed || got.Model != "vippro/gpt-6-sol(high)" {
		t.Fatalf("suffix kept: %+v", got)
	}
	if got := resolve(secret, "vippro/gpt-6-sol", ""); got.Allowed {
		t.Fatalf("bare upstream name must be rejected when an alias is set: %+v", got)
	}
	if got := resolve(secret, "vippro/plain", "vippro/plain"); !got.Allowed || got.Model != "vippro/plain" {
		t.Fatalf("unaliased upstream name: %+v", got)
	}
	if got := resolve(secret, "vippro/plain(low)", ""); !got.Allowed || got.Model != "vippro/plain(low)" {
		t.Fatalf("unaliased suffix: %+v", got)
	}
	if got := resolve(secret, "gpt-6-luna", ""); got.Mode != "group" || got.Allowed {
		t.Fatalf("unknown model: %+v", got)
	}
	if got := resolve(secret, "gpt-6-sol", "vippro/plain"); got.Allowed {
		t.Fatalf("body mismatch: %+v", got)
	}
	if got := resolve(allSecret, "anything(high)", "other"); got.Mode != "group" || !got.Allowed || got.Model != "anything(high)" {
		t.Fatalf("all-models: %+v", got)
	}
	if got := resolve(missingSecret, "gpt-6-sol", ""); got.Mode != "group" || got.Allowed {
		t.Fatalf("unknown group: %+v", got)
	}
	if got := resolve(legacySecret, "gpt-6-sol", ""); got.Mode != "legacy" {
		t.Fatalf("legacy key: %+v", got)
	}
	if got := resolve("ck_missing", "gpt-6-sol", ""); got.Mode != "legacy" {
		t.Fatalf("unknown key: %+v", got)
	}
	if got := resolve("operator-key", "gpt-6-sol", ""); got.Mode != "legacy" {
		t.Fatalf("operator key: %+v", got)
	}
}
