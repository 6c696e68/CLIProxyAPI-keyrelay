package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type keyShopListHost struct {
	keyShopGateHost
	result KeyShopModelList
	err    error
	secret string
	calls  int
}

func (h *keyShopListHost) KeyShopListModels(_ context.Context, secret string) (KeyShopModelList, error) {
	h.calls++
	h.secret = secret
	return h.result, h.err
}

func keyShopListContext(t *testing.T, secret string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	if secret != "" {
		c.Request.Header.Set("Authorization", "Bearer "+secret)
	}
	return c
}

func keyShopListIDs(models []map[string]any) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		id, _ := model["id"].(string)
		ids = append(ids, id)
	}
	return ids
}

func keyShopListServerModels() []map[string]any {
	return []map[string]any{
		{"id": "gpt-6-sol", "object": "model"},
		{"id": "gpt-6-luna", "object": "model"},
	}
}

func TestKeyShopModelListOperatorKeyUnchanged(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	host := &keyShopListHost{
		result: KeyShopModelList{Loaded: true, Mode: "group", Models: []KeyShopModelEntry{{Name: "gpt-6-luna", Alias: "luna"}}},
		err:    context.DeadlineExceeded,
	}
	handler.SetPluginHost(host)
	models := keyShopListServerModels()

	got, errMsg := handler.FilterKeyShopModels(keyShopListContext(t, "operator-smoke-key"), models)
	if errMsg != nil {
		t.Fatalf("operator key got an error: %+v", errMsg)
	}
	if host.calls != 0 {
		t.Fatalf("operator key called list_models %d times", host.calls)
	}
	if ids := keyShopListIDs(got); len(ids) != 2 || ids[0] != "gpt-6-sol" || ids[1] != "gpt-6-luna" {
		t.Fatalf("operator key list = %v, want both server models", ids)
	}
}

func TestKeyShopModelListGroupAlias(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	handler.SetPluginHost(&keyShopListHost{
		result: KeyShopModelList{Loaded: true, Mode: "group", Models: []KeyShopModelEntry{{Name: "gpt-6-luna", Alias: "luna"}}},
	})
	models := keyShopListServerModels()

	got, errMsg := handler.FilterKeyShopModels(keyShopListContext(t, "ck_customer"), models)
	if errMsg != nil {
		t.Fatalf("group alias list failed: %+v", errMsg)
	}
	if ids := keyShopListIDs(got); len(ids) != 1 || ids[0] != "luna" {
		t.Fatalf("group alias list = %v, want [luna]", ids)
	}
	if id, _ := models[1]["id"].(string); id != "gpt-6-luna" {
		t.Fatalf("filter mutated the server model: id = %q", id)
	}
}

func TestKeyShopModelListGroupWithoutAlias(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	handler.SetPluginHost(&keyShopListHost{
		result: KeyShopModelList{Loaded: true, Mode: "group", Models: []KeyShopModelEntry{{Name: "gpt-6-luna"}}},
	})

	got, errMsg := handler.FilterKeyShopModels(keyShopListContext(t, "ck_customer"), keyShopListServerModels())
	if errMsg != nil {
		t.Fatalf("group list failed: %+v", errMsg)
	}
	if ids := keyShopListIDs(got); len(ids) != 1 || ids[0] != "gpt-6-luna" {
		t.Fatalf("group list = %v, want [gpt-6-luna]", ids)
	}
}

func TestKeyShopModelListAllModelsUnchanged(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	handler.SetPluginHost(&keyShopListHost{result: KeyShopModelList{Loaded: true, Mode: "group", AllModels: true}})
	models := keyShopListServerModels()

	got, errMsg := handler.FilterKeyShopModels(keyShopListContext(t, "ck_customer"), models)
	if errMsg != nil {
		t.Fatalf("all-models list failed: %+v", errMsg)
	}
	if ids := keyShopListIDs(got); len(ids) != 2 || ids[0] != "gpt-6-sol" || ids[1] != "gpt-6-luna" {
		t.Fatalf("all-models list = %v, want the full list", ids)
	}
}

func TestKeyShopModelListUnknownGroupFailsClosed(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	handler.SetPluginHost(&keyShopListHost{result: KeyShopModelList{Loaded: true, Mode: "group", Models: []KeyShopModelEntry{}}})

	got, errMsg := handler.FilterKeyShopModels(keyShopListContext(t, "ck_customer"), keyShopListServerModels())
	if errMsg != nil {
		t.Fatalf("unknown group returned an error: %+v", errMsg)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("unknown group list = %v, want an empty non-nil slice", got)
	}
}

func TestKeyShopModelListListerErrorFailsClosed(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	handler.SetPluginHost(&keyShopListHost{err: context.DeadlineExceeded})

	got, errMsg := handler.FilterKeyShopModels(keyShopListContext(t, "ck_customer"), keyShopListServerModels())
	if got != nil {
		t.Fatalf("failed list returned models: %v", keyShopListIDs(got))
	}
	if errMsg == nil || errMsg.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(errMsg.Body), `"model_policy_unavailable"`) {
		t.Fatalf("lister error was not fail-closed: %+v", errMsg)
	}
}

func TestKeyShopModelListLegacyCustomYAMLFilters(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	host := &keyShopListHost{
		keyShopGateHost: keyShopGateHost{yaml: []byte("model_access_mode: custom\nmodels:\n  - name: gpt-6-luna\n    alias: luna\n")},
		result:          KeyShopModelList{Loaded: false, Mode: "legacy"},
	}
	handler.SetPluginHost(host)

	got, errMsg := handler.FilterKeyShopModels(keyShopListContext(t, "ck_customer"), keyShopListServerModels())
	if errMsg != nil {
		t.Fatalf("legacy custom list failed: %+v", errMsg)
	}
	if ids := keyShopListIDs(got); len(ids) != 1 || ids[0] != "luna" {
		t.Fatalf("legacy custom list = %v, want [luna]", ids)
	}
}

func TestKeyShopModelListLegacyYAMLFullUnchanged(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	host := &keyShopListHost{
		keyShopGateHost: keyShopGateHost{yaml: []byte("model_access_mode: full\n")},
		result:          KeyShopModelList{Loaded: true, Mode: "legacy"},
	}
	handler.SetPluginHost(host)

	got, errMsg := handler.FilterKeyShopModels(keyShopListContext(t, "ck_customer"), keyShopListServerModels())
	if errMsg != nil {
		t.Fatalf("legacy full list failed: %+v", errMsg)
	}
	if ids := keyShopListIDs(got); len(ids) != 2 || ids[0] != "gpt-6-sol" || ids[1] != "gpt-6-luna" {
		t.Fatalf("legacy full list = %v, want the full list", ids)
	}
}

func TestKeyShopModelListGeminiNameAlias(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)
	handler.SetPluginHost(&keyShopListHost{
		result: KeyShopModelList{Loaded: true, Mode: "group", Models: []KeyShopModelEntry{{Name: "gpt-6-luna", Alias: "luna"}}},
	})
	models := []map[string]any{
		{"name": "models/gpt-6-sol"},
		{"name": "models/gpt-6-luna"},
	}

	got, errMsg := handler.FilterKeyShopModels(keyShopListContext(t, "ck_customer"), models)
	if errMsg != nil {
		t.Fatalf("gemini list failed: %+v", errMsg)
	}
	if len(got) != 1 {
		t.Fatalf("gemini list = %v, want one model", keyShopListIDs(got))
	}
	if name, _ := got[0]["name"].(string); name != "models/luna" {
		t.Fatalf("gemini name = %q, want models/luna", name)
	}
	if id, _ := got[0]["id"].(string); id != "luna" {
		t.Fatalf("gemini id = %q, want luna", id)
	}
}
