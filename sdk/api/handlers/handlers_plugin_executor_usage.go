package handlers

import (
	"bytes"
	"errors"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

func parsePluginExecutorResponseUsage(protocol string, payload []byte) usage.Detail {
	return helps.ParsePluginExecutorResponseUsage(protocol, payload)
}

// pluginStreamObserver reads a plugin executor stream for accounting only. It
// never changes the bytes sent to the client. Plugin chunks are raw upstream
// SSE, so one event can span two chunks; complete lines are rebuilt before the
// usage parser and the token detector read them.
type pluginStreamObserver struct {
	protocol string
	reporter *helps.UsageReporter
	usage    helps.StreamUsageBuffer
	carry    []byte
	failure  *interfaces.ErrorMessage
	// finished is set by a terminal success event, such as response.completed.
	finished bool
}

func newPluginStreamObserver(protocol string, reporter *helps.UsageReporter) *pluginStreamObserver {
	return &pluginStreamObserver{protocol: strings.ToLower(strings.TrimSpace(protocol)), reporter: reporter}
}

// observe records the first packet, then reads every complete line.
func (o *pluginStreamObserver) observe(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	if o.reporter != nil {
		o.reporter.RecordFirstPacket()
	}
	o.carry = append(o.carry, chunk...)
	for {
		idx := bytes.IndexByte(o.carry, '\n')
		if idx < 0 {
			break
		}
		o.observeLine(o.carry[:idx+1])
		o.carry = o.carry[idx+1:]
	}
	if len(o.carry) == 0 {
		o.carry = nil
	}
}

// flush reads a last line that had no trailing newline.
func (o *pluginStreamObserver) flush() {
	if len(o.carry) > 0 {
		o.observeLine(o.carry)
		o.carry = nil
	}
}

func (o *pluginStreamObserver) observeLine(line []byte) {
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	if o.reporter != nil && !o.reporter.IsTTFTSet() {
		helps.ObservePluginExecutorStreamTTFT(o.protocol, o.reporter, line)
	}
	helps.ObservePluginExecutorStreamUsage(o.protocol, line, &o.usage)
	payload := helps.ExtractStreamJSONPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return
	}
	switch o.protocol {
	case "openai-response", "codex":
		switch gjson.GetBytes(payload, "type").String() {
		case "response.completed", "response.incomplete", "response.done":
			o.finished = true
		case "error":
			node := gjson.GetBytes(payload, "error")
			if !node.IsObject() {
				node = gjson.ParseBytes(payload)
			}
			o.setFailure(node)
		case "response.failed":
			o.setFailure(gjson.GetBytes(payload, "response.error"))
		}
	case "openai":
		if errNode := gjson.GetBytes(payload, "error"); errNode.Exists() && !gjson.GetBytes(payload, "choices").Exists() {
			o.setFailure(errNode)
		}
	}
}

// setFailure keeps the first in-stream error.
func (o *pluginStreamObserver) setFailure(node gjson.Result) {
	if o.failure != nil {
		return
	}
	errType := strings.TrimSpace(node.Get("type").String())
	code := strings.TrimSpace(node.Get("code").String())
	message := strings.TrimSpace(node.Get("message").String())
	if node.Type == gjson.String {
		message = strings.TrimSpace(node.String())
	}
	if message == "" {
		message = strings.TrimSpace(errType + " " + code)
	}
	if message == "" {
		message = "upstream stream error"
	}
	o.failure = &interfaces.ErrorMessage{StatusCode: inStreamErrorStatus(errType, code), Error: errors.New(message)}
}

// inStreamErrorStatus maps an upstream error object to the status recorded for
// the request. The client already received HTTP 200 and the error event.
func inStreamErrorStatus(errType, code string) int {
	key := strings.ToLower(errType + " " + code)
	switch {
	case strings.Contains(key, "usage_limit"), strings.Contains(key, "rate_limit"), strings.Contains(key, "quota"):
		return http.StatusTooManyRequests
	case strings.Contains(key, "invalid_request"), strings.Contains(key, "model_not_found"), strings.Contains(key, "context_length"):
		return http.StatusBadRequest
	case strings.Contains(key, "authentication"), strings.Contains(key, "invalid_api_key"):
		return http.StatusUnauthorized
	default:
		return http.StatusBadGateway
	}
}

// streamFailure reports an in-stream error that ended the turn: no terminal
// success event and no token usage. A stream that produced tokens stays billable.
func (o *pluginStreamObserver) streamFailure() (*interfaces.ErrorMessage, bool) {
	if o.failure == nil || o.finished {
		return nil, false
	}
	if detail, ok := o.usage.Detail(); ok && (detail.InputTokens > 0 || detail.OutputTokens > 0 || detail.TotalTokens > 0) {
		return nil, false
	}
	return o.failure, true
}
