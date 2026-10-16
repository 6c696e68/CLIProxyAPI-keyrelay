package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func relayRPC(t *testing.T, p *relayPlugin, method string, request any) pluginabi.Envelope {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var env pluginabi.Envelope
	if err := json.Unmarshal(p.call(method, raw), &env); err != nil {
		t.Fatal(err)
	}
	return env
}

func relayResult[T any](t *testing.T, env pluginabi.Envelope) T {
	t.Helper()
	if !env.OK {
		t.Fatalf("RPC failed: %+v", env.Error)
	}
	var result T
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func relayConfigure(t *testing.T, p *relayPlugin, yaml string) pluginabi.Envelope {
	t.Helper()
	return relayRPC(t, p, pluginabi.MethodPluginReconfigure, map[string]any{
		"config_yaml": []byte(yaml), "schema_version": pluginabi.SchemaVersion,
	})
}

func relayHostResult(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	return json.RawMessage(raw), err
}

func TestConfiguredModelsDoNotClaimCodex(t *testing.T) {
	p := newRelayPlugin(func(method string, _ []byte) (json.RawMessage, error) {
		return nil, fmt.Errorf("unexpected host callback %s", method)
	})
	t.Cleanup(p.shutdown)
	for _, mode := range []string{"namespace", "configured-only"} {
		t.Run(mode, func(t *testing.T) {
			relayResult[json.RawMessage](t, relayConfigure(t, p, "enabled: true\nmodels: [relay-gpt]\nclaim_mode: "+mode+"\n"))
			models := relayResult[pluginapi.ModelRegistrationResponse](t, relayRPC(t, p, pluginabi.MethodModelRegister, pluginapi.ModelRegistrationRequest{}))
			want := "ch-relay/relay-gpt"
			if mode == "configured-only" {
				want = "relay-gpt"
			}
			if models.Provider != "ch-relay" || len(models.Models) != 1 || models.Models[0].ID != want || models.Models[0].Name != "relay-gpt" {
				t.Fatalf("configured model registration = %+v", models)
			}
			for _, model := range []string{"gpt-6-astra", "codex/gpt-6-astra", "relay-gpt", "ch-relay/relay-gpt", "ch-relay/not-configured"} {
				response := relayResult[pluginapi.ModelRouteResponse](t, relayRPC(t, p, pluginabi.MethodModelRoute, pluginapi.ModelRouteRequest{RequestedModel: model, SourceFormat: "openai"}))
				if response.Handled != (model == want) || (response.Handled && response.TargetKind != pluginapi.ModelRouteTargetSelf) {
					t.Fatalf("route(%q) = %+v; only %q may be claimed", model, response, want)
				}
			}
		})
	}
	for _, config := range []string{"enabled: true\nmodels: []\n", "enabled: false\nmodels: [relay-gpt]\n"} {
		relayResult[json.RawMessage](t, relayConfigure(t, p, config))
		models := relayResult[pluginapi.ModelRegistrationResponse](t, relayRPC(t, p, pluginabi.MethodModelRegister, pluginapi.ModelRegistrationRequest{}))
		route := relayResult[pluginapi.ModelRouteResponse](t, relayRPC(t, p, pluginabi.MethodModelRoute, pluginapi.ModelRouteRequest{RequestedModel: "ch-relay/relay-gpt"}))
		if len(models.Models) != 0 || route.Handled {
			t.Fatal("disabled or empty configuration claims a model")
		}
	}
}

func TestConfigRejectsUnsafeDestinationAndPolicy(t *testing.T) {
	p := newRelayPlugin(nil)
	t.Cleanup(p.shutdown)
	relayResult[json.RawMessage](t, relayConfigure(t, p, "enabled: true\nmodels: [relay-gpt]\nclaim_mode: namespace\n"))
	for _, override := range []string{
		"base_url: http://relay.example/v1", "base_url: https://user:pass@relay.example/v1",
		"base_url: https://relay.example/v1?key=secret", "base_url: 'https://relay.example/v1#'",
		"rotation_mode: random", "plus_preference: always", "claim_mode: all",
		"sticky_seconds: -1", "sticky_seconds: 86401", "max_retries: -1", "max_retries: 9",
		"api_key: secret", "models: ['']", "models: [ch-relay/relay-gpt]",
	} {
		t.Run(override, func(t *testing.T) {
			config := "enabled: true\nmodels: [relay-gpt]\n" + override + "\n"
			if strings.HasPrefix(override, "models:") {
				config = "enabled: true\n" + override + "\n"
			}
			env := relayConfigure(t, p, config)
			if env.OK || env.Error == nil {
				t.Fatalf("unsafe configuration accepted: %s", override)
			}
			route := relayResult[pluginapi.ModelRouteResponse](t, relayRPC(t, p, pluginabi.MethodModelRoute, pluginapi.ModelRouteRequest{RequestedModel: "ch-relay/relay-gpt"}))
			if !route.Handled {
				t.Fatal("failed reconfiguration replaced the previous valid config")
			}
		})
	}
	for _, mode := range []string{"round-robin", "fill-first", "least-recent", "sticky"} {
		for _, plus := range []string{"ignore", "prefer", "only"} {
			relayResult[json.RawMessage](t, relayConfigure(t, p, "enabled: true\nmodels: [relay-gpt]\nrotation_mode: "+mode+"\nplus_preference: "+plus+"\n"))
		}
	}
}

func TestExecuteUsesScopedTokenFreeRelayAndPreservesUsage(t *testing.T) {
	for _, format := range []string{"openai", "openai-response"} {
		t.Run(format, func(t *testing.T) {
			upstream := []byte(`{"id":"response-id","usage":{"input_tokens":17,"output_tokens":9},"choices":[]}`)
			calls := 0
			p := newRelayPlugin(func(method string, raw []byte) (json.RawMessage, error) {
				calls++
				if method != pluginabi.MethodHostRelayExecute {
					return nil, fmt.Errorf("unexpected callback %s", method)
				}
				var keys map[string]json.RawMessage
				if err := json.Unmarshal(raw, &keys); err != nil {
					return nil, err
				}
				for key := range keys {
					if !strings.Contains("|host_callback_id|provider|path|method|headers|body|", "|"+key+"|") {
						return nil, fmt.Errorf("credential or destination field %q leaked", key)
					}
				}
				if bytes.Contains(raw, []byte("caller-secret")) || bytes.Contains(raw, []byte("auth-file-id")) {
					return nil, errors.New("credential state leaked into callback")
				}
				var request pluginapi.HostRelayExecuteRequest
				if err := json.Unmarshal(raw, &request); err != nil {
					return nil, err
				}
				wantPath := "/chat/completions"
				if format == "openai-response" {
					wantPath = "/responses"
				}
				if request.HostCallbackID != "scope-execute" || request.Provider != "codex" || request.Method != http.MethodPost || request.Path != wantPath || request.Headers.Get("Authorization") != "" {
					return nil, fmt.Errorf("bad relay operation: %+v", request)
				}
				var body map[string]json.RawMessage
				if err := json.Unmarshal(request.Body, &body); err != nil {
					return nil, err
				}
				if string(body["model"]) != `"relay-gpt"` || string(body["temperature"]) != "0.7" || string(body["messages"]) != `[{"role":"user","content":"hello"}]` {
					return nil, fmt.Errorf("request payload changed: %s", request.Body)
				}
				return relayHostResult(pluginapi.HostRelayResponse{StatusCode: 200, Headers: http.Header{"X-Request-Id": {"upstream-id"}}, Body: upstream})
			})
			t.Cleanup(p.shutdown)
			relayResult[json.RawMessage](t, relayConfigure(t, p, "enabled: true\nmodels: [relay-gpt]\n"))
			request := map[string]any{
				"Model": "ch-relay/relay-gpt", "SourceFormat": format, "Format": format,
				"Payload":          []byte(`{"model":"ch-relay/relay-gpt","messages":[{"role":"user","content":"hello"}],"temperature":0.7}`),
				"host_callback_id": "scope-execute", "AuthID": "auth-file-id",
				"Headers":     http.Header{"Authorization": {"Bearer caller-secret"}, "X-Api-Key": {"caller-secret"}},
				"StorageJSON": []byte(`{"access_token":"caller-secret"}`), "AuthMetadata": map[string]any{"access_token": "caller-secret"},
			}
			response := relayResult[pluginapi.ExecutorResponse](t, relayRPC(t, p, pluginabi.MethodExecutorExecute, request))
			if !bytes.Equal(response.Payload, upstream) || response.Headers.Get("X-Request-Id") != "upstream-id" || calls != 1 {
				t.Fatalf("response or usage changed: %+v; callbacks=%d", response, calls)
			}
			request["host_callback_id"] = ""
			if env := relayRPC(t, p, pluginabi.MethodExecutorExecute, request); env.OK || calls != 1 {
				t.Fatal("unscoped request reached the host")
			}
		})
	}
}

func TestExecuteNamespacePreservesPayloadBytesExceptModelValue(t *testing.T) {
	for _, modelValue := range []string{`"ch-relay/relay-gpt"`, `"ch-relay\/relay-gpt"`, `"ch-relay/relay-\u0067pt"`} {
		t.Run(modelValue, func(t *testing.T) {
			var forwarded []byte
			p := newRelayPlugin(func(method string, raw []byte) (json.RawMessage, error) {
				if method != pluginabi.MethodHostRelayExecute {
					return nil, fmt.Errorf("unexpected callback %s", method)
				}
				var request pluginapi.HostRelayExecuteRequest
				if err := json.Unmarshal(raw, &request); err != nil {
					return nil, err
				}
				forwarded = request.Body
				return relayHostResult(pluginapi.HostRelayResponse{StatusCode: 200, Body: []byte(`{}`)})
			})
			t.Cleanup(p.shutdown)
			relayResult[json.RawMessage](t, relayConfigure(t, p, "models: [relay-gpt]\n"))
			prefix := " \n{\n  \"z\": {\"model\":\"ch-relay/relay-gpt\",\"text\":\"<tag>&\",\"n\":1e+09},\n  \"mo\\u0064el\" : \t"
			suffix := " \r\n ,\"a\":[true, null],\"messages\":[{\"content\":\"雪\\u2028\",\"role\":\"user\"}]\n}\t\n"
			payload := []byte(prefix + modelValue + suffix)
			relayResult[pluginapi.ExecutorResponse](t, relayRPC(t, p, pluginabi.MethodExecutorExecute, map[string]any{
				"Model": "ch-relay/relay-gpt", "SourceFormat": "openai", "Payload": payload, "host_callback_id": "scope",
			}))
			want := []byte(prefix + `"relay-gpt"` + suffix)
			if !bytes.Equal(forwarded, want) {
				t.Fatalf("namespace rewrite changed unrelated payload bytes:\n got %q\nwant %q", forwarded, want)
			}
		})
	}
}

func TestConfigRejectsNoncanonicalEnumsWithoutChangingConfiguration(t *testing.T) {
	p := newRelayPlugin(nil)
	t.Cleanup(p.shutdown)
	relayResult[json.RawMessage](t, relayConfigure(t, p, "models: [relay-gpt]\nrotation_mode: sticky\nplus_preference: only\n"))
	for _, override := range []string{
		"claim_mode: NAMESPACE", "claim_mode: ' namespace '",
		"rotation_mode: STICKY", "rotation_mode: ' sticky '",
		"plus_preference: ONLY", "plus_preference: ' only '",
	} {
		t.Run(override, func(t *testing.T) {
			env := relayConfigure(t, p, "models: [replacement]\n"+override+"\n")
			if env.OK || env.Error == nil {
				t.Fatalf("accepted noncanonical host policy %s", override)
			}
			cfg := p.snapshot()
			if cfg.ClaimMode != "namespace" || cfg.RotationMode != "sticky" || cfg.PlusPreference != "only" || !reflect.DeepEqual(cfg.Models, []string{"relay-gpt"}) {
				t.Fatalf("rejected policy replaced valid configuration: %+v", cfg)
			}
		})
	}
}

func TestNormalizeRelayStreamPayloadPreservesSSEFrames(t *testing.T) {
	for _, tc := range []struct {
		name, format, input, want string
	}{
		{"chat preframed", "chat-completions", "data: {\"id\":1}\n\n", "{\"id\":1}"},
		{"chat done", "chat-completions", "[DONE]", ""},
		{"responses preframed", "responses", "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n", "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(bytes.Join(normalizeRelayStreamPayload([]byte(tc.input), tc.format), nil)); got != tc.want {
				t.Fatalf("normalized frame = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExecuteRejectsPayloadModelSmugglingAndUnclaimedModels(t *testing.T) {
	calls := 0
	p := newRelayPlugin(func(string, []byte) (json.RawMessage, error) {
		calls++
		return relayHostResult(pluginapi.HostRelayResponse{StatusCode: 200, Body: []byte(`{}`)})
	})
	t.Cleanup(p.shutdown)
	relayResult[json.RawMessage](t, relayConfigure(t, p, "enabled: true\nmodels: [relay-gpt]\n"))
	for _, payload := range []string{
		`{"model":"ch-relay/relay-gpt","model":"not-configured"}`,
		`{"model":"not-configured","model":"ch-relay/relay-gpt"}`,
		`{"Model":"not-configured","model":"ch-relay/relay-gpt"}`,
		`{"model":"not-configured"}`, `{"model":null}`, `{"messages":[]}`, `[]`,
	} {
		env := relayRPC(t, p, pluginabi.MethodExecutorExecute, map[string]any{"Model": "ch-relay/relay-gpt", "SourceFormat": "openai", "Payload": []byte(payload), "host_callback_id": "scope"})
		if env.OK || env.Error == nil || calls != 0 {
			t.Fatalf("invalid payload reached the host: %s", payload)
		}
	}
	env := relayRPC(t, p, pluginabi.MethodExecutorExecute, map[string]any{"Model": "gpt-6-astra", "SourceFormat": "openai", "Payload": []byte(`{"model":"gpt-6-astra"}`), "host_callback_id": "scope"})
	if env.OK || calls != 0 {
		t.Fatal("unclaimed Codex model reached relay executor")
	}
}

func TestExecutePreservesUpstreamAndCallbackErrors(t *testing.T) {
	for _, status := range []int{401, 403, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			body := `{"error":{"message":"quota exhausted","type":"insufficient_quota"}}`
			p := newRelayPlugin(func(string, []byte) (json.RawMessage, error) {
				return relayHostResult(pluginapi.HostRelayResponse{StatusCode: status, Body: []byte(body)})
			})
			t.Cleanup(p.shutdown)
			relayResult[json.RawMessage](t, relayConfigure(t, p, "enabled: true\nmodels: [relay-gpt]\n"))
			env := relayRPC(t, p, pluginabi.MethodExecutorExecute, map[string]any{"Model": "ch-relay/relay-gpt", "SourceFormat": "openai", "Payload": []byte(`{"model":"ch-relay/relay-gpt"}`), "host_callback_id": "scope"})
			if env.OK || env.Error == nil || env.Error.HTTPStatus != status || env.Error.Message != body {
				t.Fatalf("upstream error was swallowed: %+v", env)
			}
		})
	}
	p := newRelayPlugin(func(string, []byte) (json.RawMessage, error) {
		return nil, pluginabi.NewError("host_rate_limit", "host callback rate limited", 429)
	})
	t.Cleanup(p.shutdown)
	relayResult[json.RawMessage](t, relayConfigure(t, p, "enabled: true\nmodels: [relay-gpt]\n"))
	env := relayRPC(t, p, pluginabi.MethodExecutorExecute, map[string]any{"Model": "ch-relay/relay-gpt", "SourceFormat": "openai", "Payload": []byte(`{"model":"ch-relay/relay-gpt"}`), "host_callback_id": "scope"})
	if env.OK || env.Error == nil || env.Error.HTTPStatus != 429 || env.Error.Code != "host_rate_limit" {
		t.Fatalf("callback error status was lost: %+v", env)
	}
}

func TestStreamForwardsIncrementallyAndClosesBothSides(t *testing.T) {
	first := []byte(`{"choices":[{"delta":{"content":"hello"}}]}`)
	last := []byte(`{"usage":{"prompt_tokens":4,"completion_tokens":2}}`)
	var mu sync.Mutex
	var methods []string
	var emitted [][]byte
	reads := 0
	closed := make(chan struct{})
	p := newRelayPlugin(func(method string, raw []byte) (json.RawMessage, error) {
		mu.Lock()
		defer mu.Unlock()
		methods = append(methods, method)
		if strings.HasPrefix(method, "host.relay.") {
			var scope struct {
				HostCallbackID string `json:"host_callback_id"`
			}
			if err := json.Unmarshal(raw, &scope); err != nil || scope.HostCallbackID != "scope-stream" {
				return nil, errors.New("stream callback scope was lost")
			}
		}
		switch method {
		case pluginabi.MethodHostRelayStreamOpen:
			return relayHostResult(pluginapi.HostRelayResponse{StatusCode: 200, StreamID: "relay-stream", Headers: http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"stream-upstream"}}})
		case pluginabi.MethodHostRelayStreamRead:
			reads++
			if reads == 1 {
				return relayHostResult(pluginapi.HostRelayResponse{Payload: append(append([]byte("data: "), first...), []byte("\n\n")...)})
			}
			if reads != 2 || len(emitted) != 1 || !bytes.Equal(emitted[0], first) {
				return nil, errors.New("plugin buffered or reframed the stream")
			}
			return relayHostResult(pluginapi.HostRelayResponse{Payload: append(append([]byte("data: "), last...), []byte("\n\ndata: [DONE]\n\n")...), Done: true})
		case pluginabi.MethodHostStreamEmit:
			var event struct {
				StreamID string `json:"stream_id"`
				Payload  []byte `json:"payload"`
			}
			if err := json.Unmarshal(raw, &event); err != nil || event.StreamID != "plugin-stream" {
				return nil, errors.New("wrong downstream stream")
			}
			emitted = append(emitted, event.Payload)
		case pluginabi.MethodHostRelayStreamClose:
		case pluginabi.MethodHostStreamClose:
			var event struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(raw, &event); err != nil || event.Error != "" {
				return nil, fmt.Errorf("unexpected terminal error: %s", raw)
			}
			close(closed)
		default:
			return nil, fmt.Errorf("unexpected callback %s", method)
		}
		return relayHostResult(struct{}{})
	})
	t.Cleanup(p.shutdown)
	relayResult[json.RawMessage](t, relayConfigure(t, p, "enabled: true\nmodels: [relay-gpt]\n"))
	result := relayResult[struct {
		Headers http.Header       `json:"headers"`
		Chunks  []json.RawMessage `json:"chunks"`
	}](t, relayRPC(t, p, pluginabi.MethodExecutorExecuteStream, map[string]any{"Model": "ch-relay/relay-gpt", "SourceFormat": "openai", "Stream": true, "Payload": []byte(`{"model":"ch-relay/relay-gpt","stream":true}`), "host_callback_id": "scope-stream", "stream_id": "plugin-stream"}))
	if result.Headers.Get("X-Request-Id") != "stream-upstream" || len(result.Chunks) != 0 {
		t.Fatal("stream was buffered or upstream headers were lost")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not close")
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{pluginabi.MethodHostRelayStreamOpen, pluginabi.MethodHostRelayStreamRead, pluginabi.MethodHostStreamEmit, pluginabi.MethodHostRelayStreamRead, pluginabi.MethodHostStreamEmit, pluginabi.MethodHostRelayStreamClose, pluginabi.MethodHostStreamClose}
	if !reflect.DeepEqual(methods, want) || !reflect.DeepEqual(emitted, [][]byte{first, last}) {
		t.Fatalf("stream lifecycle = %v; payloads=%q", methods, emitted)
	}
}

func TestResponsesStreamForwardsCompleteRawSSEFrames(t *testing.T) {
	frames := [][]byte{
		[]byte("event: response.created\ndata: {\"type\":\"response.created\"}\n\n"),
		[]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"),
		[]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"),
	}
	var emitted [][]byte
	reads := 0
	closed := make(chan struct{})
	p := newRelayPlugin(func(method string, raw []byte) (json.RawMessage, error) {
		switch method {
		case pluginabi.MethodHostRelayStreamOpen:
			return relayHostResult(pluginapi.HostRelayResponse{StatusCode: 200, StreamID: "responses-stream"})
		case pluginabi.MethodHostRelayStreamRead:
			if reads > 0 {
				return relayHostResult(pluginapi.HostRelayResponse{Done: true})
			}
			reads++
			payload := bytes.Join(frames, nil)
			return relayHostResult(pluginapi.HostRelayResponse{Payload: payload, Done: true})
		case pluginabi.MethodHostStreamEmit:
			var event struct {
				Payload []byte `json:"payload"`
			}
			if err := json.Unmarshal(raw, &event); err != nil {
				return nil, err
			}
			emitted = append(emitted, event.Payload)
		case pluginabi.MethodHostRelayStreamClose, pluginabi.MethodHostStreamClose:
			if method == pluginabi.MethodHostStreamClose {
				close(closed)
			}
		default:
			return nil, fmt.Errorf("unexpected callback %s", method)
		}
		return relayHostResult(struct{}{})
	})
	t.Cleanup(p.shutdown)
	relayResult[json.RawMessage](t, relayConfigure(t, p, "enabled: true\nmodels: [relay-gpt]\n"))
	result := relayResult[struct {
		Headers http.Header       `json:"headers"`
		Chunks  []json.RawMessage `json:"chunks"`
	}](t, relayRPC(t, p, pluginabi.MethodExecutorExecuteStream, map[string]any{"Model": "ch-relay/relay-gpt", "SourceFormat": "openai-response", "Payload": []byte(`{"model":"ch-relay/relay-gpt","stream":true}`), "host_callback_id": "scope-responses", "stream_id": "responses-plugin"}))
	if len(result.Chunks) != 0 {
		t.Fatalf("Responses stream was buffered: %d chunks", len(result.Chunks))
	}
	<-closed
	if !reflect.DeepEqual(emitted, frames) {
		t.Fatalf("raw Responses frames changed: got %q want %q", emitted, frames)
	}
}
func TestResponsesStreamForwardsSplitCompletionFrame(t *testing.T) {
	frame := []byte("event: response.completed\r\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\r\n\r\n")
	chunks := [][]byte{frame[:len(frame)-3], frame[len(frame)-3:]}
	var emitted [][]byte
	reads := 0
	closed := make(chan struct{})
	p := newRelayPlugin(func(method string, raw []byte) (json.RawMessage, error) {
		switch method {
		case pluginabi.MethodHostRelayStreamOpen:
			return relayHostResult(pluginapi.HostRelayResponse{StatusCode: 200, StreamID: "responses-stream"})
		case pluginabi.MethodHostRelayStreamRead:
			payload := chunks[reads]
			reads++
			return relayHostResult(pluginapi.HostRelayResponse{Payload: payload, Done: reads == len(chunks)})
		case pluginabi.MethodHostStreamEmit:
			var event struct {
				Payload []byte `json:"payload"`
			}
			if err := json.Unmarshal(raw, &event); err != nil {
				return nil, err
			}
			emitted = append(emitted, event.Payload)
		case pluginabi.MethodHostRelayCancel, pluginabi.MethodHostRelayStreamClose:
		case pluginabi.MethodHostStreamClose:
			close(closed)
		default:
			return nil, fmt.Errorf("unexpected callback %s", method)
		}
		return relayHostResult(struct{}{})
	})
	t.Cleanup(p.shutdown)
	relayResult[json.RawMessage](t, relayConfigure(t, p, "enabled: true\nmodels: [relay-gpt]\n"))
	relayResult[relayStreamRPCResponse](t, relayRPC(t, p, pluginabi.MethodExecutorExecuteStream, map[string]any{"Model": "ch-relay/relay-gpt", "SourceFormat": "openai-response", "Payload": []byte(`{"model":"ch-relay/relay-gpt","stream":true}`), "host_callback_id": "scope-responses", "stream_id": "responses-plugin"}))
	<-closed
	if !reflect.DeepEqual(emitted, [][]byte{frame}) {
		t.Fatalf("split Responses frame emissions = %q, want exactly %q", emitted, [][]byte{frame})
	}
}

func TestResponsesStreamFlushesPendingFrameOnEmptyDone(t *testing.T) {
	frame := []byte("event: response.completed\r\ndata: {\"type\":\"response.completed\"}\r\n\r\n")
	reads := 0
	var emitted [][]byte
	closed := make(chan struct{})
	p := newRelayPlugin(func(method string, raw []byte) (json.RawMessage, error) {
		switch method {
		case pluginabi.MethodHostRelayStreamOpen:
			return relayHostResult(pluginapi.HostRelayResponse{StatusCode: 200, StreamID: "responses-stream"})
		case pluginabi.MethodHostRelayStreamRead:
			reads++
			if reads == 1 {
				return relayHostResult(pluginapi.HostRelayResponse{Payload: frame[:len(frame)-4]})
			}
			if len(emitted) != 0 {
				return nil, errors.New("incomplete Responses frame was emitted before Done")
			}
			return relayHostResult(pluginapi.HostRelayResponse{Done: true})
		case pluginabi.MethodHostStreamEmit:
			var event struct {
				Payload []byte `json:"payload"`
			}
			if err := json.Unmarshal(raw, &event); err != nil {
				return nil, err
			}
			emitted = append(emitted, event.Payload)
		case pluginabi.MethodHostRelayStreamClose:
		case pluginabi.MethodHostStreamClose:
			close(closed)
		default:
			return nil, fmt.Errorf("unexpected callback %s", method)
		}
		return relayHostResult(struct{}{})
	})
	t.Cleanup(p.shutdown)
	relayResult[json.RawMessage](t, relayConfigure(t, p, "enabled: true\nmodels: [relay-gpt]\n"))
	relayResult[relayStreamRPCResponse](t, relayRPC(t, p, pluginabi.MethodExecutorExecuteStream, map[string]any{"Model": "ch-relay/relay-gpt", "SourceFormat": "openai-response", "Payload": []byte(`{"model":"ch-relay/relay-gpt","stream":true}`), "host_callback_id": "scope-responses", "stream_id": "responses-plugin"}))
	<-closed
	want := frame[:len(frame)-4]
	if !reflect.DeepEqual(emitted, [][]byte{want}) {
		t.Fatalf("empty Done lost pending Responses tail: got %q want %q", emitted, [][]byte{want})
	}
}

func TestChatStreamForwardsCoalescedAndSplitSSEFrames(t *testing.T) {
	first := []byte(`{"choices":[{"delta":{"content":"prefix data: hello"}}]}`)
	last := []byte(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2}}`)
	wire := append(append(append([]byte("event: message\r\ndata: "), first...), []byte("\r\n\r\n: heartbeat\r\n\r\ndata: ")...), last...)
	wire = append(wire, []byte("\r\n\r\ndata: [DONE]\r\n\r\n")...)
	contentSplit := bytes.Index(wire, []byte("data: hello"))
	for _, chunks := range [][][]byte{{wire}, {wire[:19], wire[19:55], wire[55:]}, {wire[:contentSplit], wire[contentSplit:]}} {
		t.Run(fmt.Sprintf("reads-%d", len(chunks)), func(t *testing.T) {
			var emitted [][]byte
			reads := 0
			terminal := ""
			closed := make(chan struct{})
			p := newRelayPlugin(func(method string, raw []byte) (json.RawMessage, error) {
				switch method {
				case pluginabi.MethodHostRelayStreamOpen:
					return relayHostResult(pluginapi.HostRelayResponse{StatusCode: 200, StreamID: "relay-stream"})
				case pluginabi.MethodHostRelayStreamRead:
					payload := chunks[reads]
					reads++
					return relayHostResult(pluginapi.HostRelayResponse{Payload: payload, Done: reads == len(chunks)})
				case pluginabi.MethodHostStreamEmit:
					var event struct {
						Payload []byte `json:"payload"`
					}
					if err := json.Unmarshal(raw, &event); err != nil {
						return nil, err
					}
					emitted = append(emitted, event.Payload)
				case pluginabi.MethodHostRelayCancel, pluginabi.MethodHostRelayStreamClose:
				case pluginabi.MethodHostStreamClose:
					var event struct {
						Error string `json:"error"`
					}
					if err := json.Unmarshal(raw, &event); err != nil {
						return nil, err
					}
					terminal = event.Error
					close(closed)
				default:
					return nil, fmt.Errorf("unexpected callback %s", method)
				}
				return relayHostResult(struct{}{})
			})
			t.Cleanup(p.shutdown)
			relayResult[json.RawMessage](t, relayConfigure(t, p, "enabled: true\nmodels: [relay-gpt]\n"))
			relayResult[relayStreamRPCResponse](t, relayRPC(t, p, pluginabi.MethodExecutorExecuteStream, map[string]any{"Model": "ch-relay/relay-gpt", "SourceFormat": "openai", "Payload": []byte(`{"model":"ch-relay/relay-gpt","stream":true}`), "host_callback_id": "scope-stream", "stream_id": "plugin-stream"}))
			<-closed
			if terminal != "" || !reflect.DeepEqual(emitted, [][]byte{first, last}) {
				t.Fatalf("chat stream emitted %q; terminal=%q, want ordered JSON chunks without SSE/DONE", emitted, terminal)
			}
		})
	}
}

func TestStreamErrorAndDownstreamCancellationReleaseRelay(t *testing.T) {
	for _, failing := range []string{pluginabi.MethodHostRelayStreamRead, pluginabi.MethodHostStreamEmit} {
		t.Run(failing, func(t *testing.T) {
			closed := make(chan struct{})
			var mu sync.Mutex
			var calls []string
			terminal := ""
			p := newRelayPlugin(func(method string, raw []byte) (json.RawMessage, error) {
				mu.Lock()
				defer mu.Unlock()
				calls = append(calls, method)
				if method == failing {
					return nil, errors.New("fixture stream failure")
				}
				switch method {
				case pluginabi.MethodHostRelayStreamOpen:
					return relayHostResult(pluginapi.HostRelayResponse{StatusCode: 200, StreamID: "relay-stream", Headers: http.Header{"Content-Type": {"text/event-stream"}}})
				case pluginabi.MethodHostRelayStreamRead:
					return relayHostResult(pluginapi.HostRelayResponse{Payload: []byte("data: hello\n\n")})
				case pluginabi.MethodHostRelayCancel, pluginabi.MethodHostRelayStreamClose:
					var request pluginapi.HostRelayStreamCloseRequest
					if err := json.Unmarshal(raw, &request); err != nil || request.HostCallbackID != "scope-stream" || request.StreamID != "relay-stream" {
						return nil, errors.New("stream cleanup lost ownership")
					}
				case pluginabi.MethodHostStreamClose:
					var event struct {
						Error string `json:"error"`
					}
					if err := json.Unmarshal(raw, &event); err != nil {
						return nil, err
					}
					terminal = event.Error
					close(closed)
				default:
					return nil, fmt.Errorf("unexpected callback %s", method)
				}
				return relayHostResult(struct{}{})
			})
			t.Cleanup(p.shutdown)
			relayResult[json.RawMessage](t, relayConfigure(t, p, "enabled: true\nmodels: [relay-gpt]\n"))
			relayResult[json.RawMessage](t, relayRPC(t, p, pluginabi.MethodExecutorExecuteStream, map[string]any{"Model": "ch-relay/relay-gpt", "SourceFormat": "openai", "Payload": []byte(`{"model":"ch-relay/relay-gpt","stream":true}`), "host_callback_id": "scope-stream", "stream_id": "plugin-stream"}))
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatal("failed stream did not close")
			}
			mu.Lock()
			defer mu.Unlock()
			if !strings.Contains(terminal, "fixture stream failure") || len(calls) < 3 || !reflect.DeepEqual(calls[len(calls)-3:], []string{pluginabi.MethodHostRelayCancel, pluginabi.MethodHostRelayStreamClose, pluginabi.MethodHostStreamClose}) {
				t.Fatalf("stream failure lost or leaked upstream: terminal=%q calls=%v", terminal, calls)
			}
		})
	}
}

func TestStreamOpenHTTPErrorIsNotSuccessfulSSE(t *testing.T) {
	p := newRelayPlugin(func(method string, _ []byte) (json.RawMessage, error) {
		if method != pluginabi.MethodHostRelayStreamOpen {
			return nil, fmt.Errorf("unexpected callback after failed open: %s", method)
		}
		return relayHostResult(pluginapi.HostRelayResponse{StatusCode: 429, Body: []byte(`{"error":{"message":"rate limited"}}`), Done: true})
	})
	t.Cleanup(p.shutdown)
	relayResult[json.RawMessage](t, relayConfigure(t, p, "enabled: true\nmodels: [relay-gpt]\n"))
	env := relayRPC(t, p, pluginabi.MethodExecutorExecuteStream, map[string]any{"Model": "ch-relay/relay-gpt", "SourceFormat": "openai", "Payload": []byte(`{"model":"ch-relay/relay-gpt","stream":true}`), "host_callback_id": "scope", "stream_id": "plugin-stream"})
	if env.OK || env.Error == nil || env.Error.HTTPStatus != 429 || env.Error.Message != `{"error":{"message":"rate limited"}}` {
		t.Fatalf("failed upstream became successful SSE: %+v", env)
	}
}

func TestShutdownCancelsAnActiveIncrementalStream(t *testing.T) {
	reading := make(chan struct{})
	cancelled := make(chan struct{})
	closed := make(chan struct{})
	var cancelOnce sync.Once
	p := newRelayPlugin(func(method string, _ []byte) (json.RawMessage, error) {
		switch method {
		case pluginabi.MethodHostRelayStreamOpen:
			return relayHostResult(pluginapi.HostRelayResponse{StatusCode: 200, StreamID: "relay-stream", Headers: http.Header{"Content-Type": {"text/event-stream"}}})
		case pluginabi.MethodHostRelayStreamRead:
			close(reading)
			<-cancelled
			return nil, errors.New("relay canceled")
		case pluginabi.MethodHostRelayCancel:
			cancelOnce.Do(func() { close(cancelled) })
		case pluginabi.MethodHostRelayStreamClose:
		case pluginabi.MethodHostStreamClose:
			close(closed)
		default:
			return nil, fmt.Errorf("unexpected callback %s", method)
		}
		return relayHostResult(struct{}{})
	})
	t.Cleanup(func() { cancelOnce.Do(func() { close(cancelled) }); p.shutdown() })
	relayResult[json.RawMessage](t, relayConfigure(t, p, "enabled: true\nmodels: [relay-gpt]\n"))
	relayResult[json.RawMessage](t, relayRPC(t, p, pluginabi.MethodExecutorExecuteStream, map[string]any{"Model": "ch-relay/relay-gpt", "SourceFormat": "openai", "Payload": []byte(`{"model":"ch-relay/relay-gpt","stream":true}`), "host_callback_id": "scope-stream", "stream_id": "plugin-stream"}))
	select {
	case <-reading:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not start reading")
	}
	stopped := make(chan struct{})
	go func() { p.shutdown(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not cancel and drain stream goroutine")
	}
	select {
	case <-closed:
	default:
		t.Fatal("shutdown returned before stream cleanup")
	}
}

func TestStatusResourceRendersOnlyAggregateHostData(t *testing.T) {
	p := newRelayPlugin(func(method string, raw []byte) (json.RawMessage, error) {
		if method != pluginabi.MethodHostRelayStatus || string(raw) != `{"host_callback_id":"scope-status"}` {
			return nil, fmt.Errorf("unsafe status operation %s %s", method, raw)
		}
		return json.RawMessage(`{"total":6,"eligible":4,"plus":3,"disabled":1,"cooling":1,"rotation_mode":"sticky","plus_preference":"only","sticky_ttl_seconds":300,"max_retries":2,"access_token":"credential-sentinel","auth_id":"private-auth-id","email":"private@example.com","path":"/private/auth.json"}`), nil
	})
	t.Cleanup(p.shutdown)
	response := relayResult[pluginapi.ManagementResponse](t, relayRPC(t, p, pluginabi.MethodManagementHandle, map[string]any{"Method": "GET", "Path": "/status", "host_callback_id": "scope-status"}))
	if response.StatusCode != 200 || !strings.Contains(response.Headers.Get("Content-Type"), "text/html") || !bytes.Contains(response.Body, []byte("sticky")) {
		t.Fatalf("status resource is not usable HTML: %+v", response)
	}
	for _, private := range []string{"credential-sentinel", "private-auth-id", "private@example.com", "/private/auth.json", "scope-status"} {
		if bytes.Contains(response.Body, []byte(private)) {
			t.Fatalf("status resource leaked %s", private)
		}
	}
}
