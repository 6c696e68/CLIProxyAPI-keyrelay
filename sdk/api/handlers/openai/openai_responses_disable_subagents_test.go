package openai

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

func newDisableSubagentsTestHandler(t *testing.T, executor *responsesMultiAgentCaptureExecutor) (*OpenAIResponsesAPIHandler, string) {
	t.Helper()

	modelID := "disable-subagents-test-model"
	authID := "disable-subagents-test-auth"
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive, ProxyURL: "direct"}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, auth.Provider, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Client: sdkconfig.ClientConfig{Codex: sdkconfig.CodexClientConfig{DisableSubagents: true}},
	}, manager)
	return NewOpenAIResponsesAPIHandler(base), modelID
}

func TestResponsesDisableSubagentsRejectsCollabSpawn(t *testing.T) {
	executor := &responsesMultiAgentCaptureExecutor{}
	handler, modelID := newDisableSubagentsTestHandler(t, executor)
	router := gin.New()
	router.POST("/v1/responses", handler.Responses)

	payload := fmt.Sprintf(`{"model":%q,"stream":false,"input":[]}`, modelID)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(payload))
	request.Header.Set("X-Openai-Subagent", "collab_spawn")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", recorder.Code, recorder.Body.String())
	}
	if gjson.GetBytes(recorder.Body.Bytes(), "error.code").String() != "subagents_disabled" {
		t.Fatalf("body = %s", recorder.Body.String())
	}
	if len(executor.Payloads()) != 0 {
		t.Fatalf("upstream received %d payloads, want 0", len(executor.Payloads()))
	}
}

func TestResponsesDisableSubagentsStripsSpawnNamespace(t *testing.T) {
	executor := &responsesMultiAgentCaptureExecutor{}
	handler, modelID := newDisableSubagentsTestHandler(t, executor)
	router := gin.New()
	router.POST("/v1/responses", handler.Responses)

	payload := fmt.Sprintf(`{"model":%q,"stream":false,"tools":[{"type":"namespace","name":"multi_agent_v1","tools":[{"type":"function","name":"spawn_agent"}]},{"type":"namespace","name":"mcp__demo","tools":[{"type":"function","name":"ping"}]}]}`, modelID)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(payload))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	payloads := executor.Payloads()
	if len(payloads) != 1 {
		t.Fatalf("captured payload count = %d, want 1", len(payloads))
	}
	captured := payloads[0]
	if gjson.GetBytes(captured, "tools.#").Int() != 1 {
		t.Fatalf("tools = %s", captured)
	}
	if got := gjson.GetBytes(captured, "tools.0.name").String(); got != "mcp__demo" {
		t.Fatalf("kept tool = %q, want mcp__demo", got)
	}
}

func TestResponsesWebsocketDisableSubagentsRejectsHandshake(t *testing.T) {
	executor := &responsesMultiAgentCaptureExecutor{}
	handler, _ := newDisableSubagentsTestHandler(t, executor)
	router := gin.New()
	router.GET("/v1/responses", handler.ResponsesWebsocket)

	request := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-Websocket-Version", "13")
	request.Header.Set("Sec-Websocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	request.Header.Set("X-Openai-Subagent", "collab_spawn")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", recorder.Code, recorder.Body.String())
	}
	if gjson.GetBytes(recorder.Body.Bytes(), "error.code").String() != "subagents_disabled" {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}
