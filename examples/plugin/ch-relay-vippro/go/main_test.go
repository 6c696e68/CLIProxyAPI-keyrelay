package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	tokenA = "tok-account-a"
	tokenB = "tok-account-b"
)

type hostCall struct {
	method  string
	payload []byte
}

type httpScript struct {
	status   int
	body     []byte
	streamID string
	chunks   []readChunk
}

type readChunk struct {
	payload []byte
	err     string
	done    bool
}

type decodedHTTP struct {
	RPC            string
	HostCallbackID string              `json:"host_callback_id"`
	Method         string              `json:"method"`
	URL            string              `json:"url"`
	Headers        map[string][]string `json:"headers"`
	Body           []byte              `json:"body"`
}

type fakeHost struct {
	mu         sync.Mutex
	calls      []hostCall
	files      []pluginapi.HostAuthFileEntry
	authJSON   map[string]json.RawMessage
	scripts    []httpScript
	scriptUsed int
	pending    []readChunk
	readIndex  int

	holdFirstRead  bool
	readStarted    chan struct{}
	releaseRead    chan struct{}
	streamFinished chan struct{}
	startedOnce    sync.Once
	releaseOnce    sync.Once
	finishOnce     sync.Once
}

func setup(t *testing.T) *fakeHost {
	t.Helper()
	resetRuntime()
	accountCheckAutoStart = false
	host := &fakeHost{authJSON: map[string]json.RawMessage{}}
	hostCallOverride = host.call
	t.Cleanup(func() {
		host.release()
		stopAccountChecker()
		hostCallOverride = nil
	})
	return host
}

func (f *fakeHost) call(method string, payload []byte) (json.RawMessage, error) {
	copied := append([]byte(nil), payload...)
	if method == pluginabi.MethodHostHTTPStreamRead {
		return f.readStream(copied)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, hostCall{method: method, payload: copied})
	switch method {
	case pluginabi.MethodHostAuthList:
		return json.Marshal(map[string]any{"files": f.files})
	case pluginabi.MethodHostAuthGet:
		var req struct {
			AuthIndex string `json:"auth_index"`
		}
		if err := json.Unmarshal(copied, &req); err != nil {
			return nil, err
		}
		raw, ok := f.authJSON[req.AuthIndex]
		if !ok {
			return nil, errString("missing auth " + req.AuthIndex)
		}
		return json.Marshal(map[string]any{"auth_index": req.AuthIndex, "json": json.RawMessage(raw)})
	case pluginabi.MethodHostHTTPDo:
		return f.respondLocked(false)
	case pluginabi.MethodHostHTTPDoStream:
		return f.respondLocked(true)
	case pluginabi.MethodHostHTTPStreamClose:
		return json.RawMessage(`{}`), nil
	case pluginabi.MethodHostStreamClose:
		f.markFinishedLocked()
		return json.RawMessage(`{}`), nil
	case pluginabi.MethodHostStreamEmit:
		return json.RawMessage(`{}`), nil
	default:
		return nil, errString("unexpected host method " + method)
	}
}

func (f *fakeHost) respondLocked(stream bool) (json.RawMessage, error) {
	script := httpScript{status: http.StatusOK, body: []byte(`{"ok":true}`)}
	if f.scriptUsed < len(f.scripts) {
		script = f.scripts[f.scriptUsed]
		f.scriptUsed++
		if script.status == 0 {
			script.status = http.StatusOK
		}
	}
	if stream {
		if script.status < 200 || script.status >= 300 {
			if script.streamID != "" {
				if len(script.chunks) == 0 && len(script.body) > 0 {
					f.pending = []readChunk{{payload: script.body, done: true}}
				} else {
					f.pending = script.chunks
				}
				f.readIndex = 0
				return json.Marshal(map[string]any{"status_code": script.status, "stream_id": script.streamID})
			}
			return json.Marshal(map[string]any{"status_code": script.status, "body": script.body})
		}
		f.pending = script.chunks
		f.readIndex = 0
		id := script.streamID
		if id == "" {
			id = "http-stream"
		}
		return json.Marshal(map[string]any{"status_code": script.status, "stream_id": id})
	}
	return json.Marshal(map[string]any{
		"StatusCode": script.status,
		"Body":       script.body,
		"Headers":    map[string][]string{"Content-Type": {"application/json"}},
	})
}

func (f *fakeHost) readStream(payload []byte) (json.RawMessage, error) {
	f.mu.Lock()
	f.calls = append(f.calls, hostCall{method: pluginabi.MethodHostHTTPStreamRead, payload: payload})
	idx := f.readIndex
	chunk := readChunk{done: true}
	if idx < len(f.pending) {
		chunk = f.pending[idx]
		f.readIndex++
	}
	hold := f.holdFirstRead && idx == 0
	f.mu.Unlock()
	if hold {
		f.startedOnce.Do(func() {
			if f.readStarted != nil {
				close(f.readStarted)
			}
		})
		if f.releaseRead != nil {
			<-f.releaseRead
		}
	}
	done := chunk.done || chunk.err != ""
	return json.Marshal(map[string]any{"payload": chunk.payload, "error": chunk.err, "done": done})
}

func (f *fakeHost) markFinishedLocked() {
	if f.streamFinished == nil {
		return
	}
	f.finishOnce.Do(func() { close(f.streamFinished) })
}

func (f *fakeHost) release() {
	f.releaseOnce.Do(func() {
		if f.releaseRead != nil {
			close(f.releaseRead)
		}
	})
}

func (f *fakeHost) add(entry pluginapi.HostAuthFileEntry, doc any) {
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	if entry.ID == "" {
		entry.ID = entry.AuthIndex
	}
	if entry.Name == "" {
		entry.Name = entry.AuthIndex + ".json"
	}
	f.files = append(f.files, entry)
	f.authJSON[entry.AuthIndex] = raw
}

func unsignedPlanJWT(t *testing.T, plan string) string {
	t.Helper()
	claims, err := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]string{"chatgpt_plan_type": plan},
	})
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
}

func (f *fakeHost) codex(index, token, plan string, edit func(*pluginapi.HostAuthFileEntry)) {
	entry := pluginapi.HostAuthFileEntry{AuthIndex: index, Provider: "codex", Type: "codex"}
	if edit != nil {
		edit(&entry)
	}
	f.add(entry, map[string]any{"access_token": token, "plan_type": plan, "email": "secret-" + index + "@example.com"})
}

func (f *fakeHost) snapshot() []hostCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]hostCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeHost) httpCalls(t *testing.T) []decodedHTTP {
	t.Helper()
	var out []decodedHTTP
	for _, call := range f.snapshot() {
		if call.method != pluginabi.MethodHostHTTPDo && call.method != pluginabi.MethodHostHTTPDoStream {
			continue
		}
		var decoded decodedHTTP
		if err := json.Unmarshal(call.payload, &decoded); err != nil {
			t.Fatalf("decode %s: %v", call.method, err)
		}
		decoded.RPC = call.method
		out = append(out, decoded)
	}
	return out
}

func (f *fakeHost) saw(method string) bool {
	return f.count(method) > 0
}

func (f *fakeHost) count(method string) int {
	n := 0
	for _, call := range f.snapshot() {
		if call.method == method {
			n++
		}
	}
	return n
}

func (f *fakeHost) assertNoHTTP(t *testing.T) {
	t.Helper()
	if calls := f.httpCalls(t); len(calls) != 0 {
		t.Fatalf("host http calls = %d, want 0", len(calls))
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func yamlReq(yml string) []byte {
	raw, err := json.Marshal(map[string]any{"config_yaml": []byte(yml), "schema_version": pluginabi.SchemaVersion})
	if err != nil {
		panic(err)
	}
	return raw
}

func mustConfigure(t *testing.T, yml string) {
	t.Helper()
	if _, err := handleMethod(pluginabi.MethodPluginReconfigure, yamlReq(yml)); err != nil {
		t.Fatalf("configure: %v", err)
	}
}

type rpcEnv struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *envelopeError  `json:"error"`
}

func callEnv(t *testing.T, method string, req any) (rpcEnv, []byte) {
	t.Helper()
	var rawReq []byte
	if req != nil {
		var err error
		rawReq, err = json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal %s: %v", method, err)
		}
	}
	raw, err := handleMethod(method, rawReq)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	var env rpcEnv
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode %s: %v", method, err)
	}
	return env, raw
}

func callOK(t *testing.T, method string, req any) json.RawMessage {
	t.Helper()
	env, raw := callEnv(t, method, req)
	if !env.OK {
		t.Fatalf("%s not ok: %s", method, raw)
	}
	return env.Result
}

func execReq(format, model string, payload []byte) executorCall {
	return executorCall{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:   model,
			Format:  format,
			Payload: payload,
			Headers: http.Header{},
		},
		HostCallbackID: "cb-test",
	}
}

func mustExec(t *testing.T, format, model string, payload []byte) pluginapi.ExecutorResponse {
	t.Helper()
	result := callOK(t, pluginabi.MethodExecutorExecute, execReq(format, model, payload))
	var resp pluginapi.ExecutorResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("decode execute: %v", err)
	}
	return resp
}

func configSignature() string {
	cfg := currentConfig()
	raw, err := json.Marshal(map[string]any{
		"enabled":  cfg.Enabled,
		"base":     cfg.BaseURL,
		"prefix":   cfg.Prefix,
		"retry":    cfg.retry(),
		"fallback": cfg.fallbackRetry(),
		"mode":     cfg.RotationMode,
		"plus":     cfg.PlusPreference,
		"sticky":   cfg.stickySeconds(),
		"stats":    cfg.StatsFile,
		"priority": cfg.Priority,
		"headers":  cfg.Headers,
		"models":   cfg.Models,
	})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func bearer(t *testing.T, call decodedHTTP) string {
	t.Helper()
	got := call.Headers["Authorization"]
	if len(got) != 1 {
		t.Fatalf("Authorization = %#v", call.Headers)
	}
	return got[0]
}

func waitCh(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestDefaults(t *testing.T) {
	setup(t)
	mustConfigure(t, "")
	cfg := currentConfig()
	if cfg.BaseURL != defaultBaseURL {
		t.Fatalf("base-url = %q", cfg.BaseURL)
	}
	if cfg.retry() != 3 {
		t.Fatalf("request-retry = %d", cfg.retry())
	}
	if cfg.fallbackRetry() != 0 {
		t.Fatalf("fallback-retry = %d", cfg.fallbackRetry())
	}
	if cfg.RotationMode != "round-robin" {
		t.Fatalf("rotation-mode = %q", cfg.RotationMode)
	}
	if cfg.PlusPreference != "prefer" {
		t.Fatalf("plus-preference = %q", cfg.PlusPreference)
	}
	if cfg.stickySeconds() != 300 {
		t.Fatalf("sticky-seconds = %d", cfg.stickySeconds())
	}
	if cfg.modelCooldown() != 24*time.Hour {
		t.Fatalf("model cooldown = %s", cfg.modelCooldown())
	}
	if cfg.Prefix != "" {
		t.Fatalf("prefix = %q", cfg.Prefix)
	}
	var listed pluginapi.ModelResponse
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelStatic, map[string]any{}), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 0 {
		t.Fatalf("models = %#v, want none", listed.Models)
	}
}

func TestPrefixAndPlusPreference(t *testing.T) {
	setup(t)
	mustConfigure(t, "prefix: /vippro/\n")
	if got := currentConfig().Prefix; got != "vippro" {
		t.Fatalf("prefix /vippro/ = %q", got)
	}
	mustConfigure(t, "prefix: a/b\nmodels:\n  - name: gpt-5.4\n    alias: gpt-5.4\n")
	if got := currentConfig().Prefix; got != "" {
		t.Fatalf("prefix a/b = %q", got)
	}
	var route pluginapi.ModelRouteResponse
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelRoute, routeCall{ModelRouteRequest: pluginapi.ModelRouteRequest{RequestedModel: "gpt-5.4"}}), &route); err != nil {
		t.Fatal(err)
	}
	if !route.Handled || route.TargetKind != pluginapi.ModelRouteTargetSelf {
		t.Fatalf("empty prefix route = %+v", route)
	}
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelRoute, routeCall{ModelRouteRequest: pluginapi.ModelRouteRequest{RequestedModel: "a/b/gpt-5.4"}}), &route); err != nil {
		t.Fatal(err)
	}
	if route.Handled {
		t.Fatal("a/b prefix still claimed a path")
	}

	mustConfigure(t, "plus-preference: ignore\n")
	if got := currentConfig().PlusPreference; got != "off" {
		t.Fatalf("ignore stored as %q", got)
	}
	mustConfigure(t, "plus-preference: ONLY\n")
	if got := currentConfig().PlusPreference; got != "only" {
		t.Fatalf("ONLY stored as %q", got)
	}
}

func TestRejectInvalidConfigKeepsPrevious(t *testing.T) {
	const seed = `
enabled: true
base-url: https://good.example/v1
prefix: vip
request-retry: 1
rotation-mode: round-robin
plus-preference: prefer
sticky-seconds: 120
models:
  - name: gpt-5.4
    alias: gpt-5.4
`
	cases := []string{
		"base-url: http://bad.example/v1\n",
		"base-url: https://user:pass@bad.example/v1\n",
		"base-url: https://bad.example/v1?q=1\n",
		"base-url: https://bad.example/v1#frag\n",
		"base-url: https://bad.example/v1\nrotation-mode: spin\n",
		"base-url: https://bad.example/v1\nmodels:\n  - alias: gpt-5.4\n",
		"base-url: https://bad.example/v1\nheaders:\n  Authorization: Bearer nope\n",
		"base-url: https://bad.example/v1\napi-key: nope\n",
		"base-url: https://bad.example/v1\nkeys:\n  - nope\n",
		"base-url: https://bad.example/v1\n---\nprefix: other\n",
	}
	for _, bad := range cases {
		t.Run(bad, func(t *testing.T) {
			f := setup(t)
			f.codex("1", tokenA, "plus", nil)
			mustConfigure(t, seed)
			before := configSignature()
			if _, err := handleMethod(pluginabi.MethodPluginReconfigure, yamlReq(bad)); err != nil {
				t.Fatalf("invalid config unregistered the plugin: %v", err)
			}
			if got := configSignature(); got != before {
				t.Fatalf("config changed\nbefore %s\nafter  %s", before, got)
			}
			mustExec(t, "openai", "vip/gpt-5.4", []byte(`{"model":"vip/gpt-5.4"}`))
			calls := f.httpCalls(t)
			if len(calls) != 1 || calls[0].URL != "https://good.example/v1/chat/completions" {
				t.Fatalf("live upstream = %#v", calls)
			}
		})
	}

	t.Run("out of range retries are clamped", func(t *testing.T) {
		setup(t)
		mustConfigure(t, "enabled: true\nrequest-retry: 10\nfallback-retry: 9\nsticky-seconds: -1\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		cfg := currentConfig()
		if cfg.retry() != 10 || cfg.fallbackRetry() != maxFallbackRetries || cfg.stickySeconds() != 0 {
			t.Fatalf("clamped retry=%d fallback=%d sticky=%d", cfg.retry(), cfg.fallbackRetry(), cfg.stickySeconds())
		}
	})

	t.Run("private http base url is accepted", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		mustConfigure(t, "enabled: true\nbase-url: http://192.168.8.1:11500/v1\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 1 || calls[0].URL != "http://192.168.8.1:11500/v1/chat/completions" {
			t.Fatalf("private http = %#v", calls)
		}
	})
}

func TestDuplicateModelPairKeepsFirst(t *testing.T) {
	setup(t)
	mustConfigure(t, `
enabled: true
models:
  - {name: gpt-5.4, alias: gpt-5.4, display-name: first}
  - {name: gpt-5.4, alias: gpt-5.4, display-name: second}
  - {name: " gpt-5.4 ", alias: " gpt-5.4 "}
  - {name: gpt-5.4-mini, alias: gpt-5.4}
`)
	models := currentConfig().Models
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}
	if models[0].Name != "gpt-5.4" || models[0].Alias != "gpt-5.4" || models[0].DisplayName != "first" {
		t.Fatalf("first = %+v", models[0])
	}
	if models[1].Name != "gpt-5.4-mini" || models[1].Alias != "gpt-5.4" {
		t.Fatalf("pool mate = %+v", models[1])
	}
	pools := currentConfig().pools()
	if len(pools) != 1 || len(pools[0].names) != 2 || pools[0].names[0] != "gpt-5.4" || pools[0].names[1] != "gpt-5.4-mini" {
		t.Fatalf("pools = %+v", pools)
	}
}

func TestDisabled(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	mustConfigure(t, `
enabled: false
prefix: vip
models:
  - name: gpt-5.4
    alias: gpt-5.4
`)
	var listed pluginapi.ModelResponse
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelStatic, map[string]any{}), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 0 {
		t.Fatalf("disabled models = %#v", listed.Models)
	}
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelForAuth, map[string]any{}), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 0 {
		t.Fatalf("for_auth models = %#v", listed.Models)
	}
	var route pluginapi.ModelRouteResponse
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelRoute, routeCall{ModelRouteRequest: pluginapi.ModelRouteRequest{RequestedModel: "vip/gpt-5.4"}}), &route); err != nil {
		t.Fatal(err)
	}
	if route.Handled {
		t.Fatal("disabled plugin handled the route")
	}
	env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "vip/gpt-5.4", []byte(`{"model":"vip/gpt-5.4"}`)))
	if env.OK || env.Error == nil || env.Error.Code != "disabled" {
		t.Fatalf("execute = %+v", env.Error)
	}
	f.assertNoHTTP(t)
}

func TestRouteModelsAndIdentifier(t *testing.T) {
	setup(t)
	mustConfigure(t, `
enabled: true
prefix: vippro
models:
  - name: gpt-5.4
    alias: gpt-5.4
    display-name: GPT-5.4
  - name: gpt-5.4-mini
    alias: gpt-5.4
`)
	var listed pluginapi.ModelResponse
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelStatic, map[string]any{}), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 1 {
		t.Fatalf("catalog len = %d, want 1", len(listed.Models))
	}
	model := listed.Models[0]
	if model.ID != "vippro/gpt-5.4" || model.Name != "gpt-5.4" || model.OwnedBy != pluginID || !model.UserDefined || model.DisplayName != "GPT-5.4" {
		t.Fatalf("model = %+v", model)
	}
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelForAuth, map[string]any{"AuthID": "codex-1"}), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 0 {
		t.Fatalf("for_auth = %#v", listed.Models)
	}
	if err := json.Unmarshal(callOK(t, pluginabi.MethodModelRegister, map[string]any{}), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 1 || listed.Models[0].ID != "vippro/gpt-5.4" {
		t.Fatalf("register = %#v", listed.Models)
	}

	assertRoute := func(requested string, handled bool) {
		t.Helper()
		var route pluginapi.ModelRouteResponse
		if err := json.Unmarshal(callOK(t, pluginabi.MethodModelRoute, routeCall{ModelRouteRequest: pluginapi.ModelRouteRequest{RequestedModel: requested}}), &route); err != nil {
			t.Fatal(err)
		}
		if route.Handled != handled {
			t.Fatalf("route %s handled = %v", requested, route.Handled)
		}
		if handled {
			if route.TargetKind != pluginapi.ModelRouteTargetSelf || route.Target != "" {
				t.Fatalf("route %s = %+v", requested, route)
			}
		}
	}
	assertRoute("vippro/gpt-5.4", true)
	assertRoute("vippro/gpt-5.4(high)", true)
	assertRoute("gpt-5.4", false)
	assertRoute("codex/gpt-5.4", false)

	var ident struct {
		Identifier string `json:"identifier"`
	}
	if err := json.Unmarshal(callOK(t, pluginabi.MethodExecutorIdentifier, map[string]any{}), &ident); err != nil {
		t.Fatal(err)
	}
	if ident.Identifier != pluginID {
		t.Fatalf("identifier = %q", ident.Identifier)
	}

	mustConfigure(t, `
enabled: true
models:
  - name: gpt-5.4
    alias: gpt-5.4
`)
	assertRoute("gpt-5.4", true)
	assertRoute("gpt-5.4(high)", true)
	assertRoute("vippro/gpt-5.4", false)

	mustConfigure(t, `
enabled: true
prefix: vippro
models:
  - name: gpt-5.6
    display-name: GPT-5.6
    image: true
    thinking:
      levels: ["low", "high"]
`)
	assertRoute("vippro/gpt-5.6", true)
	assertRoute("gpt-5.6", false)
}

func TestPayloadRewriteAndReject(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	mustConfigure(t, `
enabled: true
prefix: vippro
request-retry: 0
models:
  - name: gpt-5.4
    alias: gpt-5.4
`)
	const original = `{"temperature":0.2,"model":"vippro/gpt-5.4(high)","messages":[{"content":"model"}]}`
	mustExec(t, "openai", "vippro/gpt-5.4(high)", []byte(original))
	calls := f.httpCalls(t)
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	const want = `{"temperature":0.2,"model":"gpt-5.4(high)","messages":[{"content":"model"}]}`
	if string(calls[0].Body) != want {
		t.Fatalf("body = %s", calls[0].Body)
	}

	f.calls = nil
	rejected := []string{
		`{"Model":"vippro/gpt-5.4"}`,
		`{"model":"vippro/gpt-5.4","model":"other"}`,
		`{"model":null}`,
		`{"model":["vippro/gpt-5.4"]}`,
		`{"model":1}`,
	}
	for _, payload := range rejected {
		t.Run(payload, func(t *testing.T) {
			before := len(f.snapshot())
			env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "vippro/gpt-5.4", []byte(payload)))
			if env.OK {
				t.Fatal("invalid payload succeeded")
			}
			if got := len(f.httpCalls(t)); got != 0 {
				t.Fatalf("http calls = %d", got)
			}
			if len(f.snapshot()) != before {
				t.Fatal("host was called for an invalid payload")
			}
		})
	}
}

func TestAliasPool(t *testing.T) {
	t.Run("retry switches name then next request starts there", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "free", nil)
		f.scripts = []httpScript{{status: http.StatusInternalServerError}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, `
enabled: true
request-retry: 1
plus-preference: off
models:
  - name: alpha
    alias: gpt
  - name: beta
    alias: gpt
`)
		var listed pluginapi.ModelResponse
		if err := json.Unmarshal(callOK(t, pluginabi.MethodModelStatic, map[string]any{}), &listed); err != nil {
			t.Fatal(err)
		}
		if len(listed.Models) != 1 || listed.Models[0].ID != "gpt" || listed.Models[0].Name != "alpha" {
			t.Fatalf("catalog = %+v", listed.Models)
		}
		mustExec(t, "openai", "gpt", []byte(`{"model":"gpt","n":1}`))
		calls := f.httpCalls(t)
		if len(calls) != 2 {
			t.Fatalf("calls = %d", len(calls))
		}
		if !strings.Contains(string(calls[0].Body), `"model":"alpha"`) || !strings.Contains(string(calls[1].Body), `"model":"beta"`) {
			t.Fatalf("bodies = %s | %s", calls[0].Body, calls[1].Body)
		}
		if !strings.Contains(string(calls[0].Body), `"n":1`) {
			t.Fatalf("other bytes changed: %s", calls[0].Body)
		}
		mustExec(t, "openai", "gpt", []byte(`{"model":"gpt"}`))
		calls = f.httpCalls(t)
		if len(calls) != 3 || !strings.Contains(string(calls[2].Body), `"model":"beta"`) {
			t.Fatalf("next start = %s", calls[len(calls)-1].Body)
		}
	})

	t.Run("retry 0 stops after the first name", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "free", nil)
		f.scripts = []httpScript{{status: http.StatusInternalServerError}}
		mustConfigure(t, `
enabled: true
request-retry: 0
plus-preference: off
models:
  - name: alpha
    alias: gpt
  - name: beta
    alias: gpt
`)
		env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt", []byte(`{"model":"gpt"}`)))
		if env.OK || env.Error == nil || env.Error.HTTPStatus != http.StatusInternalServerError {
			t.Fatalf("execute = %+v", env.Error)
		}
		calls := f.httpCalls(t)
		if len(calls) != 1 || !strings.Contains(string(calls[0].Body), `"model":"alpha"`) || strings.Contains(string(calls[0].Body), `"model":"beta"`) {
			t.Fatalf("calls = %#v", calls)
		}
	})
}

func TestPickerFiltersPriorityRotation(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	t.Run("skips ineligible accounts", func(t *testing.T) {
		f := setup(t)
		nowFn = func() time.Time { return now }
		f.codex("0", "tok-gemini", "plus", func(e *pluginapi.HostAuthFileEntry) { e.Provider = "gemini" })
		f.codex("1", "tok-disabled", "plus", func(e *pluginapi.HostAuthFileEntry) { e.Disabled = true })
		f.codex("2", "tok-unavailable", "plus", func(e *pluginapi.HostAuthFileEntry) { e.Unavailable = true })
		f.codex("3", "tok-future", "plus", func(e *pluginapi.HostAuthFileEntry) { e.NextRetryAfter = now.Add(time.Minute) })
		f.codex("4", "", "plus", nil)
		f.codex("5", "tok-good", "free", func(e *pluginapi.HostAuthFileEntry) { e.NextRetryAfter = now.Add(-time.Minute) })
		mustConfigure(t, "enabled: true\nplus-preference: off\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 1 || bearer(t, calls[0]) != "Bearer tok-good" {
			t.Fatalf("calls = %#v", calls)
		}
		blob := string(calls[0].Body) + bearer(t, calls[0])
		for _, secret := range []string{"tok-gemini", "tok-disabled", "tok-unavailable", "tok-future"} {
			if strings.Contains(blob, secret) {
				t.Fatalf("used ineligible token %s", secret)
			}
		}
	})

	t.Run("highest priority only", func(t *testing.T) {
		f := setup(t)
		f.codex("1", "tok-low", "free", func(e *pluginapi.HostAuthFileEntry) { e.Priority = 0 })
		f.codex("2", "tok-high", "free", func(e *pluginapi.HostAuthFileEntry) { e.Priority = 2 })
		mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: round-robin\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[0]) != "Bearer tok-high" || bearer(t, calls[1]) != "Bearer tok-high" {
			t.Fatalf("bearers = %s %s", bearer(t, calls[0]), bearer(t, calls[1]))
		}
	})

	t.Run("round-robin keeps a successful account", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "free", nil)
		f.codex("2", tokenB, "free", nil)
		mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: round-robin\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		var got []string
		for i := 0; i < 3; i++ {
			mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
			calls := f.httpCalls(t)
			got = append(got, bearer(t, calls[len(calls)-1]))
		}
		want := []string{"Bearer " + tokenA, "Bearer " + tokenA, "Bearer " + tokenA}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("round-robin = %v", got)
		}
	})

	t.Run("round-robin switches after one upstream error and stays", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "free", nil)
		f.codex("2", tokenB, "free", nil)
		f.scripts = []httpScript{
			{status: http.StatusBadRequest, body: []byte(`{"error":"bad"}`)},
			{status: http.StatusOK, body: []byte(`{"ok":true}`)},
			{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		}
		mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: round-robin\nrequest-retry: 1\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 3 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB || bearer(t, calls[2]) != "Bearer "+tokenB {
			t.Fatalf("switch bearers, calls=%d", len(calls))
		}
	})

	t.Run("client cancel does not switch account", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "free", nil)
		f.codex("2", tokenB, "free", nil)
		f.scripts = []httpScript{
			{status: http.StatusBadGateway, body: []byte("execute host http request: context canceled")},
			{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		}
		mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: round-robin\nrequest-retry: 1\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`)))
		if env.OK || len(f.httpCalls(t)) != 1 || bearer(t, f.httpCalls(t)[0]) != "Bearer "+tokenA {
			t.Fatalf("cancel switched, status=%v calls=%d", env.Error, len(f.httpCalls(t)))
		}
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[1]) != "Bearer "+tokenA {
			t.Fatalf("next bearer = %s", bearer(t, calls[1]))
		}
	})

	t.Run("fill-first keeps the first account", func(t *testing.T) {
		f := setup(t)
		early := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		late := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		f.codex("1", tokenA, "free", func(e *pluginapi.HostAuthFileEntry) { e.UpdatedAt = late })
		f.codex("2", tokenB, "free", func(e *pluginapi.HostAuthFileEntry) { e.UpdatedAt = early })
		mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: fill-first\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenA {
			t.Fatalf("fill-first bearers changed")
		}
	})

	t.Run("least-recent uses the earliest UpdatedAt", func(t *testing.T) {
		f := setup(t)
		early := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		late := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		f.codex("1", tokenA, "free", func(e *pluginapi.HostAuthFileEntry) { e.UpdatedAt = late })
		f.codex("2", tokenB, "free", func(e *pluginapi.HostAuthFileEntry) { e.UpdatedAt = early })
		mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: least-recent\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 1 || bearer(t, calls[0]) != "Bearer "+tokenB {
			t.Fatalf("least-recent bearer = %s", bearer(t, calls[0]))
		}
	})
}

func TestSticky(t *testing.T) {
	const yml = `
enabled: true
plus-preference: off
rotation-mode: sticky
sticky-seconds: 300
request-retry: 0
models:
  - name: gpt-5.4
    alias: gpt-5.4
`
	start := time.Date(2026, 1, 2, 15, 0, 0, 0, time.UTC)

	t.Run("same session sticks until the injected clock expires", func(t *testing.T) {
		f := setup(t)
		current := start
		nowFn = func() time.Time { return current }
		f.codex("1", tokenA, "free", nil)
		f.codex("2", tokenB, "free", nil)
		mustConfigure(t, yml)
		send := func() string {
			t.Helper()
			req := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
			req.Metadata = map[string]any{"execution_session_id": "sess-a"}
			callOK(t, pluginabi.MethodExecutorExecute, req)
			calls := f.httpCalls(t)
			return bearer(t, calls[len(calls)-1])
		}
		if got := send(); got != "Bearer "+tokenA {
			t.Fatalf("first = %s", got)
		}
		current = start.Add(299 * time.Second)
		if got := send(); got != "Bearer "+tokenA {
			t.Fatalf("within ttl = %s", got)
		}
		current = start.Add(301 * time.Second)
		if got := send(); got != "Bearer "+tokenB {
			t.Fatalf("after ttl = %s", got)
		}
	})

	t.Run("different sessions can use different accounts", func(t *testing.T) {
		f := setup(t)
		nowFn = func() time.Time { return start }
		f.codex("1", tokenA, "free", nil)
		f.codex("2", tokenB, "free", nil)
		mustConfigure(t, yml)
		send := func(session string) string {
			t.Helper()
			req := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
			req.Metadata = map[string]any{"execution_session_id": session}
			callOK(t, pluginabi.MethodExecutorExecute, req)
			calls := f.httpCalls(t)
			return bearer(t, calls[len(calls)-1])
		}
		if send("sess-a") != "Bearer "+tokenA || send("sess-b") != "Bearer "+tokenB {
			t.Fatal("sessions did not diverge")
		}
	})

	t.Run("unavailable sticky account is replaced without sleeping", func(t *testing.T) {
		f := setup(t)
		nowFn = func() time.Time { return start }
		f.codex("1", tokenA, "free", nil)
		f.codex("2", tokenB, "free", nil)
		mustConfigure(t, yml)
		req := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		req.Metadata = map[string]any{"execution_session_id": "sess-a"}
		callOK(t, pluginabi.MethodExecutorExecute, req)
		for i := range f.files {
			if f.files[i].AuthIndex == "1" {
				f.files[i].Unavailable = true
			}
		}
		callOK(t, pluginabi.MethodExecutorExecute, req)
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB {
			t.Fatalf("replacement bearers = %s %s", bearer(t, calls[0]), bearer(t, calls[1]))
		}
	})
}

func TestPlusPreference(t *testing.T) {
	const models = "models:\n  - {name: gpt-5.4, alias: gpt-5.4}\n"
	t.Run("off can select plus and non-plus", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "pro", nil)
		f.scripts = []httpScript{{status: http.StatusBadGateway, body: []byte(`{"error":"upstream"}`)}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: round-robin\nrequest-retry: 1\n"+models)
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB {
			t.Fatal("off mode did not use the pro account after an upstream error")
		}
	})

	t.Run("prefer does not use non-plus while a plus account exists", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "team", nil)
		f.scripts = []httpScript{{status: http.StatusTooManyRequests, body: []byte(`{"error":"rate_limit"}`)}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, "enabled: true\nplus-preference: prefer\nrequest-retry: 1\n"+models)
		env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`)))
		if env.OK || env.Error == nil || env.Error.HTTPStatus != http.StatusTooManyRequests {
			t.Fatalf("prefer 429 = %+v", env.Error)
		}
		calls := f.httpCalls(t)
		if len(calls) != 1 || bearer(t, calls[0]) != "Bearer "+tokenA {
			t.Fatalf("prefer stayed on plus = %#v", calls)
		}
	})

	t.Run("prefer fails while enabled plus accounts are cooling", func(t *testing.T) {
		f := setup(t)
		now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
		nowFn = func() time.Time { return now }
		f.codex("1", tokenA, "plus", func(e *pluginapi.HostAuthFileEntry) { e.NextRetryAfter = now.Add(time.Hour) })
		f.codex("2", tokenB, "free", nil)
		mustConfigure(t, "enabled: true\nplus-preference: prefer\nrequest-retry: 3\n"+models)
		env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`)))
		if env.OK || env.Error == nil || env.Error.Code != "no_plus_account" {
			t.Fatalf("cooling plus = %+v", env.Error)
		}
		f.assertNoHTTP(t)
	})

	t.Run("prefer uses free only when no enabled plus exists", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", func(e *pluginapi.HostAuthFileEntry) { e.Disabled = true })
		f.codex("2", tokenB, "free", nil)
		mustConfigure(t, "enabled: true\nplus-preference: prefer\nrequest-retry: 0\n"+models)
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 1 || bearer(t, calls[0]) != "Bearer "+tokenB {
			t.Fatalf("prefer without plus = %#v", calls)
		}
	})

	t.Run("prefer skips a free bearer even when plan_type says plus", func(t *testing.T) {
		f := setup(t)
		dir := t.TempDir()
		plusPath := filepath.Join(dir, "plus.json")
		freePath := filepath.Join(dir, "free.json")
		if err := os.WriteFile(plusPath, []byte(`{"plan_type":"plus","access_token":"`+tokenA+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(freePath, []byte(`{"plan_type":"plus","access_token":"`+unsignedPlanJWT(t, "free")+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		f.codex("1", tokenA, "plus", func(e *pluginapi.HostAuthFileEntry) { e.Path = plusPath })
		f.codex("2", tokenB, "free", func(e *pluginapi.HostAuthFileEntry) { e.Path = freePath })
		mustConfigure(t, "enabled: true\nplus-preference: prefer\nrequest-retry: 3\n"+models)
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 1 || bearer(t, calls[0]) != "Bearer "+tokenA {
			t.Fatalf("file plan prefer = %#v", calls)
		}
		for _, call := range f.snapshot() {
			if call.method == pluginabi.MethodHostAuthGet {
				t.Fatalf("path already had the token: %s", call.payload)
			}
		}
	})

	t.Run("only uses the access token plan and ignores id_token and plan_type", func(t *testing.T) {
		f := setup(t)
		freeBearer := unsignedPlanJWT(t, "free")
		plusBearer := unsignedPlanJWT(t, "plus")
		f.add(pluginapi.HostAuthFileEntry{AuthIndex: "1", Provider: "codex", Type: "codex"}, map[string]any{
			"access_token": freeBearer,
			"plan_type":    "plus",
			"id_token":     unsignedPlanJWT(t, "plus"),
		})
		f.add(pluginapi.HostAuthFileEntry{AuthIndex: "2", Provider: "codex", Type: "codex"}, map[string]any{
			"access_token": plusBearer,
			"plan_type":    "free",
			"id_token":     unsignedPlanJWT(t, "free"),
		})
		mustConfigure(t, "enabled: true\nplus-preference: only\nrequest-retry: 3\n"+models)
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 1 || bearer(t, calls[0]) != "Bearer "+plusBearer {
			t.Fatalf("jwt plan = %#v", calls)
		}
	})

	t.Run("prefer retries another plus account", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "plus", nil)
		f.codex("3", "tok-free", "free", nil)
		f.scripts = []httpScript{{status: http.StatusTooManyRequests, body: []byte(`{"error":"rate_limit"}`)}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, "enabled: true\nplus-preference: prefer\nrequest-retry: 1\n"+models)
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB {
			t.Fatalf("prefer plus retry = %#v", calls)
		}
	})

	t.Run("only does not call http without plus and pro is not plus", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "pro", nil)
		mustConfigure(t, "enabled: true\nplus-preference: only\nrequest-retry: 3\n"+models)
		env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`)))
		if env.OK || env.Error == nil || env.Error.Code != "no_plus_account" {
			t.Fatalf("only/pro = %+v", env.Error)
		}
		f.assertNoHTTP(t)
	})
}

func TestHTTPHeadersRetryAndURLs(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	mustConfigure(t, `
enabled: true
request-retry: 0
headers:
  X-Claude-Code-Session-Id: "$ABC"
  X-Missing: "$NONEXISTENT"
  X-Static: static-value
  X-Session: "$CPA-SESSION-ID"
models:
  - name: gpt-5.4
    alias: gpt-5.4
`)
	req := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	req.Headers = http.Header{
		"Abc":           []string{"session-abc-value"},
		"Authorization": []string{"Bearer client-secret"},
	}
	req.Metadata = map[string]any{"execution_session_id": "sess-1"}
	callOK(t, pluginabi.MethodExecutorExecute, req)
	calls := f.httpCalls(t)
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	call := calls[0]
	if call.Method != http.MethodPost || call.URL != defaultBaseURL+"/chat/completions" {
		t.Fatalf("request = %s %s", call.Method, call.URL)
	}
	if call.HostCallbackID != "cb-test" {
		t.Fatalf("host_callback_id = %q", call.HostCallbackID)
	}
	if bearer(t, call) != "Bearer "+tokenA {
		t.Fatalf("authorization = %s", bearer(t, call))
	}
	if strings.Contains(bearer(t, call), "client-secret") {
		t.Fatal("forwarded client authorization")
	}
	if call.Headers["X-Claude-Code-Session-Id"][0] != "session-abc-value" {
		t.Fatalf("$ABC = %#v", call.Headers["X-Claude-Code-Session-Id"])
	}
	if _, ok := call.Headers["X-Missing"]; ok {
		t.Fatalf("missing header was sent: %#v", call.Headers["X-Missing"])
	}
	if call.Headers["X-Static"][0] != "static-value" {
		t.Fatalf("static = %#v", call.Headers["X-Static"])
	}
	if call.Headers["X-Session"][0] != "sess-1" {
		t.Fatalf("session = %#v", call.Headers["X-Session"])
	}
	if call.Headers["Content-Type"][0] != "application/json" {
		t.Fatalf("content-type = %#v", call.Headers["Content-Type"])
	}
	streamReq := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	streamReq.StreamID = "exec-headers"
	f.scriptUsed = 0
	f.scripts = []httpScript{{status: http.StatusOK, streamID: "http-headers", chunks: []readChunk{{payload: []byte("data: {\"ok\":true}\n"), done: true}}}}
	f.streamFinished = make(chan struct{})
	callOK(t, pluginabi.MethodExecutorExecuteStream, streamReq)
	waitCh(t, f.streamFinished, "header stream")
	streamCall := f.httpCalls(t)[len(f.httpCalls(t))-1]
	if streamCall.Headers["Accept"][0] != "text/event-stream" || streamCall.Headers["Cache-Control"][0] != "no-cache" {
		t.Fatalf("stream headers = %#v", streamCall.Headers)
	}
	if bearer(t, streamCall) != "Bearer "+tokenA {
		t.Fatalf("stream authorization = %s", bearer(t, streamCall))
	}

	t.Run("session header and spoofed CPA-SESSION-ID", func(t *testing.T) {
		req := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		req.Headers = http.Header{"Session-Id": []string{"from-header"}, "CPA-SESSION-ID": []string{"spoof"}}
		callOK(t, pluginabi.MethodExecutorExecute, req)
		got := f.httpCalls(t)
		last := got[len(got)-1]
		if last.Headers["X-Session"][0] != "from-header" {
			t.Fatalf("header session = %#v", last.Headers["X-Session"])
		}

		req = execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		req.Headers = http.Header{"CPA-SESSION-ID": []string{"spoof"}}
		callOK(t, pluginabi.MethodExecutorExecute, req)
		got = f.httpCalls(t)
		last = got[len(got)-1]
		if _, ok := last.Headers["X-Session"]; ok {
			t.Fatalf("spoofed CPA-SESSION-ID was copied: %#v", last.Headers["X-Session"])
		}
	})

	t.Run("responses url", func(t *testing.T) {
		before := len(f.httpCalls(t))
		mustExec(t, "openai-response", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		got := f.httpCalls(t)
		if got[len(got)-1].URL != defaultBaseURL+"/responses" || len(got) != before+1 {
			t.Fatalf("responses url = %s", got[len(got)-1].URL)
		}
	})

	t.Run("retry 0 stops after one 429", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusTooManyRequests, body: []byte(`{"error":"rate_limit"}`)}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, "enabled: true\nrequest-retry: 0\nplus-preference: off\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`)))
		if env.OK || env.Error == nil || env.Error.HTTPStatus != http.StatusTooManyRequests {
			t.Fatalf("429 = %+v", env.Error)
		}
		if len(f.httpCalls(t)) != 1 {
			t.Fatalf("http calls = %d", len(f.httpCalls(t)))
		}
	})

	t.Run("retry 1 moves to the next account after 429", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "free", nil)
		f.codex("2", tokenB, "free", nil)
		f.scripts = []httpScript{{status: http.StatusTooManyRequests, body: []byte(`{"error":"rate_limit"}`)}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, "enabled: true\nrequest-retry: 1\nplus-preference: off\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB {
			t.Fatalf("429 failover bearers mismatch, calls=%d", len(calls))
		}
	})

	t.Run("401 switches account and does not refresh", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "free", nil)
		f.codex("2", tokenB, "free", nil)
		f.scripts = []httpScript{{status: http.StatusUnauthorized, body: []byte(`{"error":"unauthorized"}`)}, {status: http.StatusOK, body: []byte(`{"ok":true}`)}}
		mustConfigure(t, "enabled: true\nrequest-retry: 1\nplus-preference: off\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB {
			t.Fatal("401 did not switch accounts")
		}
		if f.saw(pluginabi.MethodHostAuthSave) || f.saw("auth.refresh") || f.saw(pluginabi.MethodAuthRefresh) {
			t.Fatal("401 refreshed or saved auth")
		}
	})
}

func TestChatGPTModelCooldown(t *testing.T) {
	f := setup(t)
	start := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	now := start
	nowFn = func() time.Time { return now }
	f.codex("1", tokenA, "plus", nil)
	f.codex("2", tokenB, "plus", nil)
	const unsupported = `{"detail":"The 'gpt-6.1-sol' model is not supported when using Codex with a ChatGPT account."}`
	f.scripts = []httpScript{
		{status: http.StatusBadRequest, body: []byte(unsupported)},
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		{status: http.StatusOK, body: []byte(`{"ok":true}`)},
	}
	mustConfigure(t, `
enabled: true
plus-preference: off
rotation-mode: fill-first
request-retry: 1
model-cooldown-minutes: 60
models:
  - name: primary-model
    alias: primary-model
`)
	mustExec(t, "openai", "primary-model", []byte(`{"model":"primary-model"}`))
	mustExec(t, "openai", "primary-model", []byte(`{"model":"primary-model"}`))
	calls := f.httpCalls(t)
	if len(calls) != 3 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB || bearer(t, calls[2]) != "Bearer "+tokenB {
		t.Fatalf("cooled bearers, calls=%d", len(calls))
	}
	now = start.Add(61 * time.Minute)
	mustExec(t, "openai", "primary-model", []byte(`{"model":"primary-model"}`))
	calls = f.httpCalls(t)
	if len(calls) != 4 || bearer(t, calls[3]) != "Bearer "+tokenA {
		t.Fatalf("expired cooldown bearer, calls=%d", len(calls))
	}
}

func TestUpstreamCacheKeepsStatsAndModels(t *testing.T) {
	setup(t)
	path := filepath.Join(t.TempDir(), "stats.json")
	raw := `{"requests":9,"success":8,"failed":1,"since":"2026-10-09T00:00:00Z","buckets":[{"start":"2026-10-16T16:00:00Z","requests":9,"success":8,"failed":1}],"usage_by_email":[{"email":"a@b.com","success":8,"failed":1}]}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	mustConfigure(t, `
enabled: true
base-url: http://127.0.0.1:11500/v1
prefix: vippro
stats-file: `+strconv.Quote(path)+`
upstream-cache: false
models:
  - name: gpt-6-astra
    alias: gpt-6-astra
`)
	cfg := currentConfig()
	if cfg.Prefix != "vippro" || cfg.BaseURL != "http://127.0.0.1:11500/v1" || len(cfg.Models) != 1 {
		t.Fatalf("live config dropped: prefix=%q base=%q models=%d", cfg.Prefix, cfg.BaseURL, len(cfg.Models))
	}
	snap := statsSnapshot()
	if snap.Requests != 9 || len(snap.UsageByEmail) != 1 {
		t.Fatalf("stats not loaded requests=%d emails=%d", snap.Requests, len(snap.UsageByEmail))
	}
}

func TestChatGPTModelCooldownStreamKeepsRawBody(t *testing.T) {
	const unsupported = `{"detail":"The 'gpt-6-astra' model is not supported when using Codex with a ChatGPT account."}`
	f := setup(t)
	streamClosed := make(chan struct{}, 2)
	previousHost := hostCallOverride
	hostCallOverride = func(method string, payload []byte) (json.RawMessage, error) {
		raw, errCall := previousHost(method, payload)
		if method == pluginabi.MethodHostStreamClose {
			streamClosed <- struct{}{}
		}
		return raw, errCall
	}
	start := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return start }
	f.codex("1", tokenA, "plus", nil)
	f.codex("2", tokenB, "plus", nil)
	f.scripts = []httpScript{
		{status: http.StatusBadRequest, streamID: "http-err-1", chunks: []readChunk{{payload: []byte(unsupported), done: true}}},
		{status: http.StatusOK, streamID: "http-ok-1", chunks: []readChunk{{payload: []byte("data: {\"ok\":true}\n\n"), done: true}}},
		{status: http.StatusOK, streamID: "http-ok-2", chunks: []readChunk{{payload: []byte("data: {\"ok\":true}\n\n"), done: true}}},
	}
	mustConfigure(t, `
enabled: true
plus-preference: off
rotation-mode: fill-first
request-retry: 1
model-cooldown-minutes: 1440
models:
  - name: gpt-6-astra
    alias: gpt-6-astra
`)
	req := execReq("openai", "gpt-6-astra", []byte(`{"model":"gpt-6-astra"}`))
	req.StreamID = "exec-1"
	callOK(t, pluginabi.MethodExecutorExecuteStream, req)
	req2 := execReq("openai", "gpt-6-astra", []byte(`{"model":"gpt-6-astra"}`))
	req2.StreamID = "exec-2"
	callOK(t, pluginabi.MethodExecutorExecuteStream, req2)
	calls := f.httpCalls(t)
	if len(calls) != 3 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB || bearer(t, calls[2]) != "Bearer "+tokenB {
		t.Fatalf("stream cooldown bearers, calls=%d", len(calls))
	}
	waitCh(t, streamClosed, "first success stream close")
	waitCh(t, streamClosed, "second success stream close")

	f2 := setup(t)
	nowFn = func() time.Time { return start }
	f2.codex("1", tokenA, "plus", nil)
	f2.scripts = []httpScript{
		{status: http.StatusBadRequest, streamID: "http-err-raw", chunks: []readChunk{{payload: []byte(unsupported), done: true}}},
	}
	mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
model-cooldown-minutes: 1440
models:
  - name: gpt-6-astra
    alias: gpt-6-astra
`)
	fail := execReq("openai", "gpt-6-astra", []byte(`{"model":"gpt-6-astra"}`))
	fail.StreamID = "exec-fail"
	env, raw := callEnv(t, pluginabi.MethodExecutorExecuteStream, fail)
	if env.OK || env.Error == nil || env.Error.HTTPStatus != http.StatusBadRequest || env.Error.Message != unsupported {
		t.Fatalf("raw stream error = %s", raw)
	}
}

func TestOpenAICompatPayloads(t *testing.T) {
	var pending []byte
	var param any
	raw := "data: {\"a\":1}\ndata: {\"b\":2}\ndata: [DONE]\n"
	got := openAICompatPayloads(&pending, []byte(raw), true, &param)
	if len(got) != 2 || string(got[0]) != `{"a":1}` || string(got[1]) != `{"b":2}` || len(pending) != 0 {
		t.Fatalf("frames = %d pending=%d", len(got), len(pending))
	}
	pending = nil
	param = nil
	if parts := openAICompatPayloads(&pending, []byte(`data: {"a":`), false, &param); len(parts) != 0 {
		t.Fatalf("partial = %#v", parts)
	}
	got = openAICompatPayloads(&pending, []byte("1}\n"), false, &param)
	if len(got) != 1 || string(got[0]) != `{"a":1}` || len(pending) != 0 {
		t.Fatalf("joined = %#v pending=%q", got, pending)
	}
	pending = nil
	param = nil
	got = openAICompatPayloads(&pending, []byte(": keep-alive\n\ndata: {\"c\":3}\n"), true, &param)
	if len(got) != 1 || string(got[0]) != `{"c":3}` {
		t.Fatalf("keepalive = %#v", got)
	}
}

func TestStream(t *testing.T) {
	t.Run("429 at open is an rpc error", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusTooManyRequests, body: []byte(`{"error":"rate_limit"}`)}}
		mustConfigure(t, "enabled: true\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		req := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		req.StreamID = "exec-stream-1"
		env, raw := callEnv(t, pluginabi.MethodExecutorExecuteStream, req)
		if env.OK || env.Error == nil || env.Error.HTTPStatus != http.StatusTooManyRequests {
			t.Fatalf("stream 429 = %+v", env.Error)
		}
		if strings.Contains(string(raw), "text/event-stream") {
			t.Fatalf("429 returned sse: %s", raw)
		}
		calls := f.httpCalls(t)
		if len(calls) != 1 || calls[0].RPC != pluginabi.MethodHostHTTPDoStream {
			t.Fatalf("stream open calls = %#v", calls)
		}
		if f.saw(pluginabi.MethodHostStreamEmit) || f.saw(pluginabi.MethodHostHTTPStreamRead) {
			t.Fatal("failed open emitted or read a stream")
		}
	})

	t.Run("headers return before emit and a mid-stream error closes both ids", func(t *testing.T) {
		f := setup(t)
		f.codex("1", "tok-secret-visible", "plus", nil)
		f.holdFirstRead = true
		f.readStarted = make(chan struct{})
		f.releaseRead = make(chan struct{})
		f.streamFinished = make(chan struct{})
		f.scripts = []httpScript{
			{status: http.StatusOK, streamID: "http-stream-1", chunks: []readChunk{
				{payload: []byte("data: tok-secret-visible\n\n")},
				{err: "boom", done: true},
			}},
			{status: http.StatusOK, streamID: "http-stream-2", chunks: []readChunk{{payload: []byte("data: next\n\n"), done: true}}},
		}
		mustConfigure(t, "enabled: true\nrequest-retry: 3\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		req := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		req.StreamID = "exec-stream-1"
		req.HostCallbackID = "cb-stream"
		type asyncResult struct {
			env rpcEnv
			raw []byte
			err error
		}
		done := make(chan asyncResult, 1)
		go func() {
			rawReq, errMarshal := json.Marshal(req)
			if errMarshal != nil {
				done <- asyncResult{err: errMarshal}
				return
			}
			raw, errCall := handleMethod(pluginabi.MethodExecutorExecuteStream, rawReq)
			result := asyncResult{raw: raw, err: errCall}
			if errCall == nil {
				_ = json.Unmarshal(raw, &result.env)
			}
			done <- result
		}()
		waitCh(t, f.readStarted, "upstream read")
		for _, call := range f.snapshot() {
			if call.method != pluginabi.MethodHostStreamEmit {
				continue
			}
			var ref struct {
				Payload []byte `json:"payload"`
			}
			_ = json.Unmarshal(call.payload, &ref)
			if string(ref.Payload) != string(chatStreamKeepAlive) {
				t.Fatalf("emit before upstream data = %s", ref.Payload)
			}
		}
		f.release()
		var got asyncResult
		select {
		case got = <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("execute_stream did not return headers after the first chunk")
		}
		if got.err != nil {
			t.Fatalf("execute_stream: %v", got.err)
		}
		if !got.env.OK || !strings.Contains(string(got.raw), "text/event-stream") {
			t.Fatalf("stream headers = %s", got.raw)
		}
		waitCh(t, f.streamFinished, "stream close")

		var readID, emitID, httpCloseID, execCloseID string
		var emitPayload []byte
		for _, call := range f.snapshot() {
			var ref struct {
				StreamID       string `json:"stream_id"`
				HostCallbackID string `json:"host_callback_id"`
				Payload        []byte `json:"payload"`
			}
			_ = json.Unmarshal(call.payload, &ref)
			switch call.method {
			case pluginabi.MethodHostHTTPStreamRead:
				readID = ref.StreamID
				if ref.HostCallbackID != "cb-stream" {
					t.Fatalf("read callback = %q", ref.HostCallbackID)
				}
			case pluginabi.MethodHostStreamEmit:
				emitID = ref.StreamID
				emitPayload = ref.Payload
			case pluginabi.MethodHostHTTPStreamClose:
				httpCloseID = ref.StreamID
			case pluginabi.MethodHostStreamClose:
				execCloseID = ref.StreamID
			}
		}
		if readID != "http-stream-1" || emitID != "exec-stream-1" || httpCloseID != "http-stream-1" || execCloseID != "exec-stream-1" {
			t.Fatalf("ids read=%s emit=%s httpClose=%s execClose=%s", readID, emitID, httpCloseID, execCloseID)
		}
		if !strings.Contains(string(emitPayload), "tok-secret-visible") || strings.Contains(string(emitPayload), "data:") {
			t.Fatalf("emit payload = %s", emitPayload)
		}
		opens := 0
		for _, call := range f.httpCalls(t) {
			if call.RPC == pluginabi.MethodHostHTTPDoStream {
				opens++
				if call.HostCallbackID != "cb-stream" {
					t.Fatalf("do_stream callback = %q", call.HostCallbackID)
				}
			}
		}
		if opens != 1 {
			t.Fatalf("do_stream calls = %d", opens)
		}
	})
}

func TestCountTokensDoesNotCallHTTP(t *testing.T) {
	f := setup(t)
	called := false
	hostCallOverride = func(method string, payload []byte) (json.RawMessage, error) {
		called = true
		t.Errorf("count_tokens called %s", method)
		return json.RawMessage(`{}`), nil
	}
	_ = f
	result := callOK(t, pluginabi.MethodExecutorCountTokens, execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`)))
	var resp pluginapi.ExecutorResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatal(err)
	}
	if string(resp.Payload) != `{"input_tokens":0}` {
		t.Fatalf("payload = %s", resp.Payload)
	}
	if called {
		t.Fatal("host was called")
	}
}

func TestStatusAndRedaction(t *testing.T) {
	f := setup(t)
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }
	f.codex("0", "tok-gemini", "plus", func(e *pluginapi.HostAuthFileEntry) {
		e.Provider = "gemini"
		e.Email = "gemini@example.com"
		e.Path = "/secret/gemini.json"
	})
	f.add(pluginapi.HostAuthFileEntry{
		AuthIndex: "1", Provider: "codex", Email: "secret@example.com", Path: "/secret/codex/a.json",
	}, map[string]any{"access_token": "tok-secret-visible", "plan_type": "plus", "email": "secret@example.com"})
	f.codex("2", "tok-disabled", "plus", func(e *pluginapi.HostAuthFileEntry) {
		e.Disabled = true
		e.Email = "secret@example.com"
		e.Path = "/secret/codex/a.json"
	})
	f.codex("3", "tok-cooling", "plus", func(e *pluginapi.HostAuthFileEntry) {
		e.NextRetryAfter = now.Add(time.Hour)
		e.Path = "/secret/codex/a.json"
	})
	f.codex("4", "tok-down", "plus", func(e *pluginapi.HostAuthFileEntry) { e.Unavailable = true })
	mustConfigure(t, "enabled: true\nrotation-mode: round-robin\nplus-preference: prefer\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")

	before := len(f.calls)
	var page pluginapi.ManagementResponse
	if err := json.Unmarshal(callOK(t, pluginabi.MethodManagementHandle, managementCall{ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/management/ch-relay-vippro/stats"}}), &page); err != nil {
		t.Fatal(err)
	}
	if page.StatusCode != http.StatusOK {
		t.Fatalf("stats status = %d", page.StatusCode)
	}
	for _, call := range f.calls[before:] {
		if call.method == pluginabi.MethodHostAuthList || call.method == pluginabi.MethodHostAuthGet {
			t.Fatalf("stats scanned auth via %s", call.method)
		}
	}
	for _, secret := range []string{"tok-secret-visible", "secret@example.com", "/secret/codex/a.json"} {
		if strings.Contains(string(page.Body), secret) {
			t.Fatalf("stats leaked %q", secret)
		}
	}

	f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"echo":"tok-secret-visible"}`)}}
	resp := mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	if string(resp.Payload) != `{"echo":"tok-secret-visible"}` {
		t.Fatalf("success payload = %s", resp.Payload)
	}

	f2 := setup(t)
	f2.codex("1", "tok-secret-visible", "plus", nil)
	f2.scripts = []httpScript{{status: http.StatusBadGateway, body: []byte(`{"error":"tok-secret-visible"}`)}}
	mustConfigure(t, "enabled: true\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
	env, raw := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`)))
	if env.OK || env.Error == nil || env.Error.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("upstream error = %+v", env.Error)
	}
	if strings.Contains(string(raw), "tok-secret-visible") || !strings.Contains(env.Error.Message, "[REDACTED]") {
		t.Fatalf("error body leaked token: %s", raw)
	}
}

func TestRegistrationAndQuiesce(t *testing.T) {
	setup(t)
	result := callOK(t, pluginabi.MethodPluginRegister, map[string]any{"schema_version": pluginabi.SchemaVersion})
	var reg registration
	if err := json.Unmarshal(result, &reg); err != nil {
		t.Fatal(err)
	}
	if reg.SchemaVersion != pluginabi.SchemaVersion || reg.Metadata.Name != pluginID {
		t.Fatalf("registration = %+v", reg)
	}
	if !reg.Capabilities.ModelRegistrar || !reg.Capabilities.ModelProvider || !reg.Capabilities.ModelRouter || !reg.Capabilities.Executor || !reg.Capabilities.ManagementAPI {
		t.Fatalf("capabilities = %+v", reg.Capabilities)
	}
	if strings.Join(reg.Capabilities.ExecutorInputFormats, ",") != "chat-completions,openai-responses" || strings.Join(reg.Capabilities.ExecutorOutputFormats, ",") != "chat-completions,openai-responses" {
		t.Fatalf("formats = %#v %#v", reg.Capabilities.ExecutorInputFormats, reg.Capabilities.ExecutorOutputFormats)
	}
	if reg.Capabilities.ExecutorModelScope != string(pluginapi.ExecutorModelScopeStatic) {
		t.Fatalf("scope = %s", reg.Capabilities.ExecutorModelScope)
	}
	quiesce := callOK(t, pluginabi.MethodPluginQuiesce, map[string]any{})
	var obj map[string]any
	if err := json.Unmarshal(quiesce, &obj); err != nil {
		t.Fatal(err)
	}
	if len(obj) != 0 {
		t.Fatalf("quiesce = %s", quiesce)
	}
}

func TestStatsCountCompletedUsage(t *testing.T) {
	statsMu.Lock()
	stats = usageCounters{}
	statsMu.Unlock()
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)}}
	mustConfigure(t, "enabled: true\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	got := statsSnapshot()
	if got.Requests != 1 || got.Success != 1 || got.Failed != 0 || got.InputTokens != 3 || got.OutputTokens != 4 || got.TotalTokens != 7 {
		t.Fatalf("stats = %+v", got)
	}
}

func TestEmailUsageReport(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", func(e *pluginapi.HostAuthFileEntry) { e.Email = "a@gmail.com" })
	f.codex("2", tokenB, "plus", func(e *pluginapi.HostAuthFileEntry) { e.Email = "b@gmail.com" })
	f.scripts = []httpScript{
		{status: http.StatusOK, body: []byte(`{"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)},
		{status: http.StatusBadRequest, body: []byte(`{"error":"bad"}`)},
		{status: http.StatusOK, body: []byte(`{"usage":{"prompt_tokens":5,"completion_tokens":6,"total_tokens":11}}`)},
		{status: http.StatusOK, body: []byte(`{"usage":{"prompt_tokens":5,"completion_tokens":6,"total_tokens":11}}`)},
	}
	mustConfigure(t, "enabled: true\nplus-preference: off\nrotation-mode: round-robin\nrequest-retry: 1\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	rows := statsSnapshot().UsageByEmail
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Email != "b@gmail.com" || rows[0].Success != 2 || rows[0].Failed != 0 || rows[0].TotalTokens != 22 {
		t.Fatalf("first = %+v", rows[0])
	}
	if rows[1].Email != "a@gmail.com" || rows[1].Success != 1 || rows[1].Failed != 1 || rows[1].TotalTokens != 7 {
		t.Fatalf("second = %+v", rows[1])
	}
}

func TestUsagePageMasksEmailAndCopiesFullAddress(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", func(e *pluginapi.HostAuthFileEntry) { e.Email = "asd@gmail.com" })
	f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"usage":{"total_tokens":7}}`)}}
	mustConfigure(t, "enabled: true\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))

	registered := callOK(t, pluginabi.MethodManagementRegister, map[string]any{})
	if !strings.Contains(string(registered), `"/dashboard"`) || !strings.Contains(string(registered), "Dashboard") {
		t.Fatalf("dashboard menu = %s", registered)
	}
	if strings.Contains(string(registered), "/v0/resource/plugins/ch-relay-vippro/status") {
		t.Fatalf("registered a status resource: %s", registered)
	}

	var page pluginapi.ManagementResponse
	if err := json.Unmarshal(callOK(t, pluginabi.MethodManagementHandle, managementCall{
		ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/resource/plugins/ch-relay-vippro/dashboard"},
	}), &page); err != nil {
		t.Fatal(err)
	}
	if page.StatusCode != http.StatusOK || !strings.Contains(page.Headers.Get("Content-Type"), "text/html") {
		t.Fatalf("page = %#v", page.Headers)
	}
	body := string(page.Body)
	if !strings.Contains(body, `id="root"`) || strings.Contains(body, "asd@gmail.com") {
		t.Fatalf("shell = %s", body[:min(200, len(body))])
	}
	var data struct {
		Emails []struct {
			Email  string `json:"email"`
			Masked string `json:"masked"`
		} `json:"emails"`
	}
	raw := callOK(t, pluginabi.MethodManagementHandle, managementCall{
		ManagementRequest: pluginapi.ManagementRequest{
			Method: http.MethodGet,
			Path:   "/v0/resource/plugins/ch-relay-vippro/dashboard",
			Query:  url.Values{"format": []string{"json"}},
		},
	})
	var jsonPage pluginapi.ManagementResponse
	if err := json.Unmarshal(raw, &jsonPage); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(jsonPage.Body, &data); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(data.Emails) != 1 || data.Emails[0].Email != "asd@gmail.com" || data.Emails[0].Masked != "asd***@****.com" {
		t.Fatalf("emails = %+v", data.Emails)
	}
}

func managementBody(t *testing.T, method, path string) (pluginapi.ManagementResponse, statsPayload) {
	t.Helper()
	result := callOK(t, pluginabi.MethodManagementHandle, managementCall{
		ManagementRequest: pluginapi.ManagementRequest{Method: method, Path: path},
		HostCallbackID:    "cb-mgmt",
	})
	var page pluginapi.ManagementResponse
	if err := json.Unmarshal(result, &page); err != nil {
		t.Fatal(err)
	}
	var body statsPayload
	if len(page.Body) > 0 {
		if err := json.Unmarshal(page.Body, &body); err != nil {
			t.Fatalf("stats json: %v body=%s", err, page.Body)
		}
	}
	return page, body
}

func waitAccountCheck(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		checkWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for account check")
	}
}

func TestResponsesUsageAndSSE(t *testing.T) {
	const models = "enabled: true\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n"
	t.Run("response.usage", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusOK, body: []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":5,"output_tokens":6,"total_tokens":11}}}`)}}
		mustConfigure(t, models)
		mustExec(t, "openai-response", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		got := statsSnapshot()
		if got.Requests != 1 || got.Success != 1 || got.Failed != 0 || got.InputTokens != 5 || got.OutputTokens != 6 || got.TotalTokens != 11 {
			t.Fatalf("stats = %+v", got)
		}
		if len(got.Buckets) != 1 || got.Buckets[0].InputTokens != 5 || got.Buckets[0].TotalTokens != 11 {
			t.Fatalf("buckets = %+v", got.Buckets)
		}
	})

	t.Run("sse data lines", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.streamFinished = make(chan struct{})
		f.scripts = []httpScript{{
			status:   http.StatusOK,
			streamID: "http-usage",
			chunks: []readChunk{
				{payload: []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")},
				{payload: []byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":8,\"output_tokens\":9,\"total_tokens\":17}}}\n\n"), done: true},
			},
		}}
		mustConfigure(t, models)
		req := execReq("openai-response", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		req.StreamID = "exec-usage"
		env, raw := callEnv(t, pluginabi.MethodExecutorExecuteStream, req)
		if !env.OK {
			t.Fatalf("stream = %s", raw)
		}
		waitCh(t, f.streamFinished, "usage stream")
		got := statsSnapshot()
		if got.Requests != 1 || got.Success != 1 || got.InputTokens != 8 || got.OutputTokens != 9 || got.TotalTokens != 17 {
			t.Fatalf("stats = %+v", got)
		}
	})
}

func TestStatsGetDoesNotListAuth(t *testing.T) {
	f := setup(t)
	f.codex("1", tokenA, "plus", nil)
	f.codex("2", "tok-disabled", "plus", func(e *pluginapi.HostAuthFileEntry) { e.Disabled = true })
	mustConfigure(t, "enabled: true\n")
	before := len(f.snapshot())
	page, body := managementBody(t, http.MethodGet, "/v0/management/ch-relay-vippro/stats")
	if page.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", page.StatusCode)
	}
	for _, call := range f.snapshot()[before:] {
		if call.method == pluginabi.MethodHostAuthList || call.method == pluginabi.MethodHostAuthGet {
			t.Fatalf("GET stats called %s", call.method)
		}
	}
	if body.Accounts != nil {
		t.Fatalf("accounts = %#v, want null", body.Accounts)
	}
	if body.Buckets == nil {
		t.Fatal("buckets is null")
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(page.Body, &keys); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"requests", "success", "failed", "input_tokens", "output_tokens", "total_tokens", "since", "buckets", "accounts"} {
		if _, ok := keys[key]; !ok {
			t.Fatalf("missing %s in %s", key, page.Body)
		}
	}
	if string(keys["accounts"]) != "null" || string(keys["buckets"]) != "[]" {
		t.Fatalf("body = %s", page.Body)
	}
	if _, err := time.Parse(time.RFC3339, body.Since.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	registered := callOK(t, pluginabi.MethodManagementRegister, map[string]any{})
	if strings.Contains(string(registered), "/v0/resource/plugins/ch-relay-vippro/status") {
		t.Fatalf("registered a status resource: %s", registered)
	}
}

func TestAccountCheckSkipsDisabled(t *testing.T) {
	f := setup(t)
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }
	f.codex("gem", "tok-gem", "plus", func(e *pluginapi.HostAuthFileEntry) { e.Provider = "gemini" })
	f.codex("off", "tok-off", "plus", func(e *pluginapi.HostAuthFileEntry) { e.Disabled = true })
	f.codex("plus", "tok-plus", "plus", nil)
	f.codex("free", "tok-free", "free", nil)
	f.codex("cool", "tok-cool", "plus", func(e *pluginapi.HostAuthFileEntry) {
		e.NextRetryAfter = now.Add(time.Minute)
	})
	f.codex("down", "tok-down", "plus", func(e *pluginapi.HostAuthFileEntry) { e.Unavailable = true })
	f.codex("empty", "", "plus", nil)
	mustConfigure(t, "enabled: true\n")

	if _, body := managementBody(t, http.MethodGet, "/v0/management/ch-relay-vippro/stats"); body.Accounts != nil || f.count(pluginabi.MethodHostAuthList) != 0 {
		t.Fatal("GET stats scanned auth before the first check")
	}

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseFn)
	inner := f.call
	entered := make(chan struct{}, 1)
	hostCallOverride = func(method string, payload []byte) (json.RawMessage, error) {
		if method == pluginabi.MethodHostAuthGet {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
		}
		return inner(method, payload)
	}

	page, body := managementBody(t, http.MethodPost, "/v0/management/ch-relay-vippro/accounts/check")
	if page.StatusCode != http.StatusOK || body.Accounts == nil || !body.Accounts.Running {
		t.Fatalf("post check status=%d accounts=%#v", page.StatusCode, body.Accounts)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("account check did not reach auth.get")
	}
	if f.count(pluginabi.MethodHostAuthList) != 1 {
		t.Fatalf("auth.list = %d", f.count(pluginabi.MethodHostAuthList))
	}
	progress := statsSnapshot()
	if progress.Accounts == nil || !progress.Accounts.Running || progress.Accounts.Total != 5 || progress.Accounts.DisabledSkipped != 1 {
		t.Fatalf("progress = %#v", progress.Accounts)
	}
	managementBody(t, http.MethodGet, "/v0/management/ch-relay-vippro/stats")
	managementBody(t, http.MethodPost, "/v0/management/ch-relay-vippro/accounts/check")
	if f.count(pluginabi.MethodHostAuthList) != 1 {
		t.Fatalf("auth.list after GET and overlapping POST = %d", f.count(pluginabi.MethodHostAuthList))
	}

	releaseFn()
	waitAccountCheck(t)
	_, done := managementBody(t, http.MethodGet, "/v0/management/ch-relay-vippro/stats")
	if done.Accounts == nil || done.Accounts.Running {
		t.Fatalf("finished = %#v", done.Accounts)
	}
	acc := done.Accounts
	if acc.Checked != 5 || acc.Total != 5 || acc.Eligible != 2 || acc.Plus != 1 || acc.Cooling != 1 || acc.Unavailable != 2 || acc.DisabledSkipped != 1 {
		t.Fatalf("accounts = %+v", *acc)
	}
	if _, err := time.Parse(time.RFC3339, acc.CheckedAt); err != nil {
		t.Fatalf("checked_at %q: %v", acc.CheckedAt, err)
	}
	gets := map[string]int{}
	for _, call := range f.snapshot() {
		if call.method != pluginabi.MethodHostAuthGet {
			continue
		}
		var req struct {
			AuthIndex string `json:"auth_index"`
		}
		if err := json.Unmarshal(call.payload, &req); err != nil {
			t.Fatal(err)
		}
		gets[req.AuthIndex]++
	}
	for _, index := range []string{"plus", "free", "cool", "down", "empty"} {
		if gets[index] != 1 {
			t.Fatalf("auth.get %s = %d in %#v", index, gets[index], gets)
		}
	}
	if gets["off"] != 0 || gets["gem"] != 0 {
		t.Fatalf("skipped accounts were fetched: %#v", gets)
	}
	if f.count(pluginabi.MethodHostAuthList) != 1 {
		t.Fatalf("auth.list = %d", f.count(pluginabi.MethodHostAuthList))
	}
}

func TestAccountCheckPlusFollowsTokenPlan(t *testing.T) {
	f := setup(t)
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }

	// Two stored-plus files whose tokens say free, so a plan_type counter would report plus=2.
	staleBoth := pluginapi.HostAuthFileEntry{AuthIndex: "stale-both", Provider: "codex", Type: "codex"}
	f.add(staleBoth, map[string]any{
		"access_token":      unsignedPlanJWT(t, "free"),
		"plan_type":         "plus",
		"chatgpt_plan_type": "plus",
		"id_token":          unsignedPlanJWT(t, "free"),
	})
	staleType := pluginapi.HostAuthFileEntry{AuthIndex: "stale-type", Provider: "codex", Type: "codex"}
	f.add(staleType, map[string]any{
		"access_token": unsignedPlanJWT(t, "free"),
		"plan_type":    "plus",
		"id_token":     unsignedPlanJWT(t, "plus"),
	})
	staleTop := pluginapi.HostAuthFileEntry{AuthIndex: "stale-top", Provider: "codex", Type: "codex"}
	f.add(staleTop, map[string]any{
		"access_token":      "tok-top",
		"plan_type":         "free",
		"chatgpt_plan_type": "plus",
		"id_token":          unsignedPlanJWT(t, "free"),
	})
	liveID := pluginapi.HostAuthFileEntry{AuthIndex: "live-id", Provider: "codex", Type: "codex"}
	f.add(liveID, map[string]any{
		"access_token": unsignedPlanJWT(t, "plus"),
		"plan_type":    "free",
		"id_token":     unsignedPlanJWT(t, "free"),
	})
	liveAccess := pluginapi.HostAuthFileEntry{AuthIndex: "live-access", Provider: "codex", Type: "codex"}
	rawAccess, errMarshal := json.Marshal(map[string]any{
		"access_token": unsignedPlanJWT(t, "plus"),
		"plan_type":    "free",
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	f.authJSON[liveAccess.AuthIndex] = rawAccess

	for _, entry := range []pluginapi.HostAuthFileEntry{staleBoth, staleType, staleTop} {
		kind, plus := classifyEnabled(entry, "cb", now)
		if kind != kindEligible || plus {
			t.Fatalf("%s stored plus with free token: kind=%v plus=%v", entry.AuthIndex, kind, plus)
		}
	}
	kind, plus := classifyEnabled(liveID, "cb", now)
	if kind != kindEligible || !plus {
		t.Fatalf("access token plus with free id_token: kind=%v plus=%v", kind, plus)
	}
	kind, plus = classifyEnabled(liveAccess, "cb", now)
	if kind != kindEligible || !plus {
		t.Fatalf("access_token jwt plus with free plan_type: kind=%v plus=%v", kind, plus)
	}

	mustConfigure(t, "enabled: true\n")
	page, _ := managementBody(t, http.MethodPost, "/v0/management/ch-relay-vippro/accounts/check")
	if page.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", page.StatusCode)
	}
	waitAccountCheck(t)
	before := len(f.snapshot())
	_, done := managementBody(t, http.MethodGet, "/v0/management/ch-relay-vippro/stats")
	for _, call := range f.snapshot()[before:] {
		if call.method == pluginabi.MethodHostAuthList || call.method == pluginabi.MethodHostAuthGet {
			t.Fatalf("GET stats called %s", call.method)
		}
	}
	acc := done.Accounts
	if acc == nil || acc.Running || acc.Checked != 4 || acc.Total != 4 || acc.Eligible != 4 || acc.Plus != 1 {
		t.Fatalf("accounts = %#v", acc)
	}
}

func TestHourBucketsAggregate(t *testing.T) {
	f := setup(t)
	path := filepath.Join(t.TempDir(), "stats.json")
	current := time.Date(2026, 4, 1, 23, 10, 0, 0, time.UTC)
	nowFn = func() time.Time { return current }
	f.codex("1", tokenA, "plus", nil)
	f.scripts = []httpScript{
		{status: http.StatusOK, body: []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)},
		{status: http.StatusOK, body: []byte(`{"usage":{"prompt_tokens":4,"completion_tokens":5,"total_tokens":9}}`)},
		{status: http.StatusBadGateway, body: []byte(`{"error":"down"}`)},
		{status: http.StatusOK, body: []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}}`)},
	}
	mustConfigure(t, "enabled: true\nrequest-retry: 0\nstats-file: "+strconv.Quote(path)+"\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`)))
	if env.OK || env.Error == nil || env.Error.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("failed request = %+v", env.Error)
	}
	current = time.Date(2026, 4, 2, 0, 5, 0, 0, time.UTC)
	mustExec(t, "openai-response", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))

	got := statsSnapshot()
	if got.Requests != 4 || got.Success != 3 || got.Failed != 1 || got.InputTokens != 15 || got.OutputTokens != 27 || got.TotalTokens != 42 {
		t.Fatalf("totals = %+v", got)
	}
	if len(got.Buckets) != 2 {
		t.Fatalf("buckets = %+v", got.Buckets)
	}
	first := time.Date(2026, 4, 1, 23, 0, 0, 0, time.UTC)
	second := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	if !got.Buckets[0].Start.Equal(first) || got.Buckets[0].Requests != 3 || got.Buckets[0].Success != 2 || got.Buckets[0].Failed != 1 || got.Buckets[0].InputTokens != 5 || got.Buckets[0].OutputTokens != 7 || got.Buckets[0].TotalTokens != 12 {
		t.Fatalf("hour 1 = %+v", got.Buckets[0])
	}
	if !got.Buckets[1].Start.Equal(second) || got.Buckets[1].Requests != 1 || got.Buckets[1].Success != 1 || got.Buckets[1].Failed != 0 || got.Buckets[1].InputTokens != 10 || got.Buckets[1].OutputTokens != 20 || got.Buckets[1].TotalTokens != 30 {
		t.Fatalf("hour 2 = %+v", got.Buckets[1])
	}
	rawFile, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var saved statsPersist
	if err := json.Unmarshal(rawFile, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Requests != 4 || len(saved.Buckets) != 2 || !saved.Buckets[0].Start.Equal(first) || !saved.Buckets[1].Start.Equal(second) {
		t.Fatalf("file = %+v", saved)
	}

	statsMu.Lock()
	stats = usageCounters{}
	hourBuckets = nil
	statsMu.Unlock()
	loadStats(path)
	reloaded := statsSnapshot()
	if reloaded.Requests != got.Requests || reloaded.InputTokens != got.InputTokens || len(reloaded.Buckets) != 2 || !reloaded.Buckets[0].Start.Equal(first) || reloaded.Buckets[1].TotalTokens != 30 {
		t.Fatalf("reloaded = %+v", reloaded)
	}

	f.scripts = append(f.scripts, httpScript{status: http.StatusOK, body: []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)})
	current = second.Add(91 * 24 * time.Hour)
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	pruned := statsSnapshot()
	if len(pruned.Buckets) != 1 || pruned.Requests != 1 || pruned.Success != 1 || pruned.InputTokens != 1 || pruned.OutputTokens != 1 || pruned.TotalTokens != 2 {
		t.Fatalf("pruned = %+v buckets=%+v", pruned, pruned.Buckets)
	}
	if !pruned.Buckets[0].Start.Equal(hourStart(current)) {
		t.Fatalf("pruned start = %s", pruned.Buckets[0].Start)
	}

	kept, errKept := os.ReadFile(path)
	if errKept != nil {
		t.Fatal(errKept)
	}
	mustConfigure(t, "enabled: true\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
	mustExec(t, "openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	after, errAfter := os.ReadFile(path)
	if errAfter != nil {
		t.Fatal(errAfter)
	}
	if string(after) != string(kept) {
		t.Fatal("empty stats-file still rewrote the file")
	}
}

func requestModel(t *testing.T, call decodedHTTP) string {
	t.Helper()
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(call.Body, &payload); err != nil {
		t.Fatalf("model json: %v body=%s", err, call.Body)
	}
	return payload.Model
}

func TestModelFallback(t *testing.T) {
	const unsupported = `{"detail":"The 'requested-model' MODEL IS NOT SUPPORTED when using Codex with a ChatGPT account."}`
	const primaryBody = `{"model":"primary-model"}`

	t.Run("zero ignores the fallback list", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{{status: http.StatusBadRequest, body: []byte(unsupported)}}
		mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 0
fallback-retry: 0
models:
  - name: primary-model
    alias: primary-model
    fallback: ["other-model"]
`)
		env, _ := callEnv(t, pluginabi.MethodExecutorExecute, execReq("openai", "primary-model", []byte(primaryBody)))
		calls := f.httpCalls(t)
		if env.OK || env.Error == nil || env.Error.HTTPStatus != http.StatusBadRequest || len(calls) != 1 || requestModel(t, calls[0]) != "primary-model" {
			t.Fatalf("status=%v calls=%d", env.Error, len(calls))
		}
	})

	t.Run("400 keeps the model and the next account uses it too", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "plus", nil)
		f.scripts = []httpScript{
			{status: http.StatusBadRequest, body: []byte(unsupported)},
			{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		}
		mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 1
fallback-retry: 1
models:
  - name: primary-model
    alias: primary-model
    fallback: ["other-model"]
`)
		mustExec(t, "openai", "primary-model", []byte(primaryBody))
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB {
			t.Fatalf("calls=%d", len(calls))
		}
		if requestModel(t, calls[0]) != "primary-model" || requestModel(t, calls[1]) != "primary-model" {
			t.Fatalf("models = %s %s", calls[0].Body, calls[1].Body)
		}
	})

	t.Run("429 switches account and keeps the model", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "free", nil)
		f.codex("2", tokenB, "free", nil)
		f.scripts = []httpScript{
			{status: http.StatusTooManyRequests, body: []byte(`{"error":"rate_limit"}`)},
			{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		}
		mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 1
fallback-retry: 2
models:
  - name: primary-model
    alias: primary-model
    fallback: ["other-model"]
`)
		mustExec(t, "openai", "primary-model", []byte(primaryBody))
		calls := f.httpCalls(t)
		if len(calls) != 2 || bearer(t, calls[0]) != "Bearer "+tokenA || bearer(t, calls[1]) != "Bearer "+tokenB {
			t.Fatalf("calls=%d", len(calls))
		}
		if requestModel(t, calls[0]) != "primary-model" || requestModel(t, calls[1]) != "primary-model" {
			t.Fatalf("models = %s %s", calls[0].Body, calls[1].Body)
		}
	})

	t.Run("http 200 overload is returned unchanged", func(t *testing.T) {
		f := setup(t)
		f.codex("1", tokenA, "plus", nil)
		f.codex("2", tokenB, "plus", nil)
		const overload = `{"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`
		f.scripts = []httpScript{
			{status: http.StatusOK, body: []byte(overload)},
			{status: http.StatusOK, body: []byte(`{"ok":true}`)},
		}
		mustConfigure(t, `
enabled: true
plus-preference: off
request-retry: 1
fallback-retry: 2
models:
  - name: primary-model
    alias: primary-model
    fallback: ["other-model"]
`)
		resp := mustExec(t, "openai", "primary-model", []byte(primaryBody))
		calls := f.httpCalls(t)
		if len(calls) != 1 || bearer(t, calls[0]) != "Bearer "+tokenA || requestModel(t, calls[0]) != "primary-model" {
			t.Fatalf("overload calls=%d", len(calls))
		}
		if string(resp.Payload) != overload {
			t.Fatalf("payload = %s", resp.Payload)
		}
	})

	t.Run("stream forwards the json inside the sse line", func(t *testing.T) {
		f := setup(t)
		f.streamFinished = make(chan struct{})
		f.codex("1", tokenA, "plus", nil)
		f.scripts = []httpScript{
			{status: http.StatusOK, streamID: "http-over", chunks: []readChunk{{payload: []byte("data: {\"code\":\"server_is_overloaded\"}\n\n"), done: true}}},
		}
		mustConfigure(t, "enabled: true\nplus-preference: off\nrequest-retry: 1\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		req := execReq("openai", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		req.StreamID = "exec-over"
		callOK(t, pluginabi.MethodExecutorExecuteStream, req)
		waitCh(t, f.streamFinished, "overload stream")
		calls := f.httpCalls(t)
		if len(calls) != 1 || bearer(t, calls[0]) != "Bearer "+tokenA {
			t.Fatalf("stream calls=%d", len(calls))
		}
		var emitted string
		for _, call := range f.snapshot() {
			if call.method != pluginabi.MethodHostStreamEmit {
				continue
			}
			var ref struct {
				Payload []byte `json:"payload"`
			}
			_ = json.Unmarshal(call.payload, &ref)
			emitted = string(ref.Payload)
		}
		if emitted != `{"code":"server_is_overloaded"}` {
			t.Fatalf("emit = %s", emitted)
		}
	})

	t.Run("responses stream forwards original bytes", func(t *testing.T) {
		f := setup(t)
		f.streamFinished = make(chan struct{})
		f.codex("1", tokenA, "plus", nil)
		const raw = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"pong\"}\n\n"
		f.scripts = []httpScript{{
			status:   http.StatusOK,
			streamID: "http-responses",
			chunks:   []readChunk{{payload: []byte(raw), done: true}},
		}}
		mustConfigure(t, "enabled: true\nrequest-retry: 0\nmodels:\n  - {name: gpt-5.4, alias: gpt-5.4}\n")
		req := execReq("openai-response", "gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
		req.StreamID = "exec-responses"
		callOK(t, pluginabi.MethodExecutorExecuteStream, req)
		waitCh(t, f.streamFinished, "responses stream")
		var emitted string
		for _, call := range f.snapshot() {
			if call.method != pluginabi.MethodHostStreamEmit {
				continue
			}
			var ref struct {
				Payload []byte `json:"payload"`
			}
			_ = json.Unmarshal(call.payload, &ref)
			emitted = string(ref.Payload)
		}
		if emitted != raw {
			t.Fatalf("emit = %s", emitted)
		}
	})

	t.Run("duplicate name and alias keeps the first fallback", func(t *testing.T) {
		setup(t)
		mustConfigure(t, `
enabled: true
models:
  - {name: primary-model, alias: primary-model, fallback: ["kept-model"]}
  - {name: primary-model, alias: primary-model, fallback: ["dropped-model"]}
`)
		models := currentConfig().Models
		if len(models) != 1 || len(models[0].Fallback) != 1 || models[0].Fallback[0] != "kept-model" {
			t.Fatalf("models = %+v", models)
		}
	})
}
