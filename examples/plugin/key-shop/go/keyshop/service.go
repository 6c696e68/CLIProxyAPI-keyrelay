package keyshop

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Service struct {
	store *Store
	admin []byte
	dash  []byte
}

func NewService(store *Store, admin, dash []byte) *Service {
	return &Service{store: store, admin: admin, dash: dash}
}

func (s *Service) Call(method string, request []byte) ([]byte, error) {
	payload := unwrap(request)
	switch method {
	case "plugin.register", "plugin.reconfigure":
		return ok(map[string]any{
			"schema_version": 6,
			"metadata": map[string]any{
				"Name": "key-shop", "Version": "0.1.2", "Author": "cliproxyapi",
				"GitHubRepository": "https://github.com/router-for-me/CLIProxyAPI",
				"ConfigFields": []map[string]any{
					{"Name": "data_file", "Type": "string", "Description": "SQLite path. Empty uses ~/.cliproxyapi/key-shop/key-shop.db"},
					{"Name": "default_ttl", "Type": "string", "Default": "168h", "Description": "Default key lifetime"},
					{"Name": "default_token_limit", "Type": "integer", "Default": 100000000, "Description": "Default token cap. Key-level 0 means unlimited"},
					{"Name": "reserve_tokens", "Type": "integer", "Default": 32768, "Description": "Tokens held before usage is known"},
					{"Name": "reservation_ttl", "Type": "string", "Default": "15m", "Description": "How long an unfinished reservation is kept"},
					{"Name": "retention_days", "Type": "integer", "Default": 30, "Description": "How long request rows are kept"},
					{"Name": "deleted_retention_days", "Type": "integer", "Default": 90, "Description": "How long deleted key metadata is kept"},
				},
			},
			"capabilities": map[string]any{
				"frontend_auth_provider":   true,
				"request_interceptor":      true,
				"request_lifecycle_plugin": true,
				"usage_plugin":             true,
				"management_api":           true,
			},
		})
	case "frontend_auth.identifier":
		return ok(map[string]string{"identifier": "key-shop"})
	case "frontend_auth.authenticate":
		return ok(s.authenticate(payload))
	case "request.intercept_before":
		return ok(s.intercept(payload))
	case "request.intercept_after":
		return ok(map[string]any{})
	case "usage.handle":
		s.noteUsage(payload)
		return ok(map[string]any{})
	case "request.complete":
		s.complete(payload)
		return ok(map[string]any{})
	case "management.register":
		return ok(s.routes())
	case "management.handle":
		return ok(s.manage(payload))
	default:
		return ok(map[string]any{})
	}
}

func (s *Service) authenticate(payload []byte) map[string]any {
	var req struct {
		Path    string
		Headers map[string][]string
	}
	_ = json.Unmarshal(payload, &req)
	secret, okSecret := ExtractSecret(func(name string) string {
		for key, values := range req.Headers {
			if strings.EqualFold(key, name) && len(values) > 0 {
				return values[0]
			}
		}
		return ""
	})
	if !okSecret || !strings.HasPrefix(secret, "ck_") || unsupportedPath(req.Path) {
		return map[string]any{"Authenticated": false}
	}
	key, err := s.store.Lookup(secret)
	if err != nil || key.Status != "active" {
		return map[string]any{"Authenticated": false}
	}
	return map[string]any{"Authenticated": true, "Principal": key.ID, "Metadata": map[string]string{"key_id": key.ID}}
}

func (s *Service) intercept(payload []byte) map[string]any {
	var req struct {
		RequestID string
		Headers   map[string][]string
		Body      []byte
		Metadata  map[string]any
	}
	_ = json.Unmarshal(payload, &req)
	secret, okSecret := ExtractSecret(func(name string) string {
		for key, values := range req.Headers {
			if strings.EqualFold(key, name) && len(values) > 0 {
				return values[0]
			}
		}
		return ""
	})
	if !okSecret || !strings.HasPrefix(secret, "ck_") {
		return map[string]any{}
	}
	path, _ := req.Metadata["request_path"].(string)
	generate := true
	if raw, ok := req.Metadata["generate"].(bool); ok {
		generate = raw
	}
	admission, err := s.store.Admit(AdmitInput{
		Secret: secret, RequestID: req.RequestID, Path: path,
		MaxTokens: maxTokens(req.Body), Generate: generate,
	})
	if err != nil {
		return terminate(http.StatusServiceUnavailable, "key_store_unavailable", "key store unavailable", "server_error")
	}
	if admission.Allowed || admission.Ignored {
		return map[string]any{}
	}
	status, errType := http.StatusUnauthorized, "invalid_request_error"
	switch admission.Code {
	case "token_limit_exceeded":
		status, errType = http.StatusPaymentRequired, "insufficient_quota"
	case "key_expired", "key_disabled":
		status = http.StatusForbidden
	}
	_ = s.store.RecordRejection(req.RequestID, admission.KeyID, status)
	return terminate(status, admission.Code, denialMessage(admission.Code), errType)
}

func (s *Service) noteUsage(payload []byte) {
	var rec struct {
		RequestID string
		Failed    bool
		Detail    struct {
			InputTokens, OutputTokens, ReasoningTokens, CachedTokens int64
		}
	}
	_ = json.Unmarshal(payload, &rec)
	if rec.RequestID == "" {
		return
	}
	_ = s.store.NoteUsage(UsageNote{RequestID: rec.RequestID, Input: rec.Detail.InputTokens, Output: rec.Detail.OutputTokens, Reasoning: rec.Detail.ReasoningTokens, Cached: rec.Detail.CachedTokens, Failed: rec.Failed})
}

func (s *Service) complete(payload []byte) {
	var rec struct {
		RequestID, Model, SourceFormat, Outcome string
		StatusCode                              int
		Stream                                  bool
		StartedAt                               time.Time
		Latency                                 time.Duration
	}
	_ = json.Unmarshal(payload, &rec)
	transport := "http"
	if rec.Stream {
		transport = "websocket"
	}
	_ = s.store.Finalize(FinalizeInput{
		RequestID: rec.RequestID, Model: rec.Model, Transport: transport, Protocol: rec.SourceFormat,
		Outcome: rec.Outcome, HTTPStatus: rec.StatusCode, RequestedAt: rec.StartedAt, Latency: rec.Latency,
	})
}

func (s *Service) routes() map[string]any {
	return map[string]any{
		"routes": []map[string]string{
			{"Method": "POST", "Path": "/plugins/key-shop/keys"},
			{"Method": "POST", "Path": "/plugins/key-shop/keys/bulk"},
			{"Method": "GET", "Path": "/plugins/key-shop/keys"},
			{"Method": "POST", "Path": "/plugins/key-shop/keys/update"},
			{"Method": "POST", "Path": "/plugins/key-shop/keys/rotate"},
			{"Method": "POST", "Path": "/plugins/key-shop/keys/reset-usage"},
			{"Method": "POST", "Path": "/plugins/key-shop/keys/delete"},
			{"Method": "GET", "Path": "/plugins/key-shop/requests"},
			{"Method": "GET", "Path": "/plugins/key-shop/stats"},
		},
		"resources": []map[string]string{
			{"Path": "/admin", "Menu": "API Keys", "Description": "Create and manage client API keys"},
			{"Path": "/dashboard"},
			{"Path": "/api/session"},
		},
	}
}

func (s *Service) manage(payload []byte) map[string]any {
	var req struct {
		Method  string
		Path    string
		Headers map[string][]string
		Query   map[string][]string
		Body    json.RawMessage
	}
	_ = json.Unmarshal(payload, &req)
	body := bodyBytes(req.Body)
	path := req.Path
	switch {
	case strings.HasSuffix(path, "/dashboard"):
		return htmlResponse(s.dash)
	case strings.HasSuffix(path, "/admin"):
		return htmlResponse(s.admin)
	case strings.HasSuffix(path, "/api/session"):
		return s.session(req.Headers, req.Query)
	case strings.HasSuffix(path, "/keys") && req.Method == http.MethodPost:
		return s.create(body)
	case strings.HasSuffix(path, "/keys/bulk") && req.Method == http.MethodPost:
		return s.createBulk(body)
	case strings.HasSuffix(path, "/keys") && req.Method == http.MethodGet:
		return s.list(queryOne(req.Query, "q"), queryOne(req.Query, "status"), queryInt(req.Query, "page", 1), queryInt(req.Query, "page_size", 25))
	case strings.HasSuffix(path, "/keys/update"):
		return s.update(body)
	case strings.HasSuffix(path, "/keys/rotate"):
		return s.rotate(body)
	case strings.HasSuffix(path, "/keys/reset-usage"):
		return s.reset(body)
	case strings.HasSuffix(path, "/keys/delete"):
		return s.delete(body)
	case strings.HasSuffix(path, "/requests"):
		return s.requests(req.Query)
	case strings.HasSuffix(path, "/stats"):
		return s.stats()
	default:
		return textResponse(http.StatusNotFound, "not found")
	}
}

func (s *Service) create(body []byte) map[string]any {
	var in struct {
		Label, Customer, Note string
		TTLSeconds            int64 `json:"ttl_seconds"`
		NeverExpires          bool  `json:"never_expires"`
		TokenLimit            int64 `json:"token_limit"`
		UnlimitedTokens       bool  `json:"unlimited_tokens"`
	}
	if len(bytes.TrimSpace(body)) > 0 && json.Unmarshal(body, &in) != nil {
		return textResponse(http.StatusBadRequest, "bad json")
	}
	limitSet := bytes.Contains(body, []byte(`"token_limit"`))
	key, secret, err := s.store.Create(CreateInput{
		Label: in.Label, Customer: in.Customer, Note: in.Note,
		TTLSeconds: in.TTLSeconds, NeverExpires: in.NeverExpires,
		TokenLimit: in.TokenLimit, TokenLimitSet: limitSet, UnlimitedTokens: in.UnlimitedTokens,
	})
	if err != nil {
		return textResponse(http.StatusInternalServerError, "create failed")
	}
	return jsonResponse(http.StatusOK, map[string]any{"key": key, "secret": secret})
}

func (s *Service) createBulk(body []byte) map[string]any {
	var in struct {
		Label, Customer, Note, LabelTemplate string
		Count                                int   `json:"count"`
		TTLSeconds                           int64 `json:"ttl_seconds"`
		NeverExpires                         bool  `json:"never_expires"`
		TokenLimit                           int64 `json:"token_limit"`
		UnlimitedTokens                      bool  `json:"unlimited_tokens"`
	}
	if json.Unmarshal(body, &in) != nil || in.Count < 1 || in.Count > 500 {
		return textResponse(http.StatusBadRequest, "count must be between 1 and 500")
	}
	label := in.Label
	if label == "" {
		label = in.LabelTemplate
	}
	created, err := s.store.CreateBatch(CreateInput{
		Label: label, Customer: in.Customer, Note: in.Note, TTLSeconds: in.TTLSeconds,
		NeverExpires: in.NeverExpires, TokenLimit: in.TokenLimit,
		TokenLimitSet: bytes.Contains(body, []byte(`"token_limit"`)), UnlimitedTokens: in.UnlimitedTokens,
	}, in.Count)
	if err != nil {
		return textResponse(http.StatusBadRequest, err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"keys": created})
}

func (s *Service) list(q, status string, page, pageSize int) map[string]any {
	keys, total, err := s.store.ListPageFiltered(q, status, page, pageSize)
	if err != nil {
		return textResponse(http.StatusInternalServerError, "list failed")
	}
	if keys == nil {
		keys = []Key{}
	}
	return jsonResponse(http.StatusOK, map[string]any{"keys": keys, "total": total, "page": page, "page_size": pageSize})
}

func (s *Service) update(body []byte) map[string]any {
	var in struct {
		ID, Label, Customer, Note, Status string
		TTLSeconds                        int64 `json:"ttl_seconds"`
		NeverExpires                      bool  `json:"never_expires"`
		TokenLimit                        int64 `json:"token_limit"`
		UnlimitedTokens                   bool  `json:"unlimited_tokens"`
	}
	if json.Unmarshal(body, &in) != nil || in.ID == "" {
		return textResponse(http.StatusBadRequest, "bad json")
	}
	if bytes.Contains(body, []byte(`"status"`)) && in.Status != "active" && in.Status != "disabled" {
		return textResponse(http.StatusBadRequest, "invalid status")
	}
	key, err := s.store.Update(UpdateInput{
		ID: in.ID, Label: in.Label, Customer: in.Customer, Note: in.Note, Status: in.Status,
		LabelSet: bytes.Contains(body, []byte(`"label"`)), CustomerSet: bytes.Contains(body, []byte(`"customer"`)),
		NoteSet: bytes.Contains(body, []byte(`"note"`)), StatusSet: bytes.Contains(body, []byte(`"status"`)),
		TTLSeconds: in.TTLSeconds, TTLSet: bytes.Contains(body, []byte(`"ttl_seconds"`)), NeverExpires: in.NeverExpires,
		TokenLimit: in.TokenLimit, TokenLimitSet: bytes.Contains(body, []byte(`"token_limit"`)),
		UnlimitedTokens: in.UnlimitedTokens,
	})
	if err != nil {
		return textResponse(http.StatusBadRequest, "update failed")
	}
	return jsonResponse(http.StatusOK, map[string]any{"key": key})
}

func (s *Service) rotate(body []byte) map[string]any {
	id := idFrom(body)
	key, secret, err := s.store.Rotate(id)
	if err != nil {
		return textResponse(http.StatusNotFound, "not found")
	}
	return jsonResponse(http.StatusOK, map[string]any{"key": key, "secret": secret})
}

func (s *Service) reset(body []byte) map[string]any {
	key, err := s.store.ResetUsage(idFrom(body))
	if err != nil {
		return textResponse(http.StatusNotFound, "not found")
	}
	return jsonResponse(http.StatusOK, map[string]any{"key": key})
}

func (s *Service) delete(body []byte) map[string]any {
	if err := s.store.Delete(idFrom(body)); err != nil {
		return textResponse(http.StatusNotFound, "not found")
	}
	return jsonResponse(http.StatusOK, map[string]any{"deleted": true})
}

func (s *Service) requests(q map[string][]string) map[string]any {
	limit := queryInt(q, "limit", 25)
	rows, page, err := s.store.RequestsPage(RequestQuery{KeyID: queryOne(q, "id"), Status: queryOne(q, "status"), Model: queryOne(q, "model"), From: parseTimeQuery(queryOne(q, "from")), To: parseTimeQuery(queryOne(q, "to")), Limit: limit, Cursor: queryOne(q, "cursor")})
	if err != nil {
		return textResponse(http.StatusBadRequest, "requests failed")
	}
	if rows == nil {
		rows = []Request{}
	}
	return jsonResponse(http.StatusOK, map[string]any{"requests": rows, "next_cursor": page.NextCursor, "has_more": page.HasMore})
}

func (s *Service) stats() map[string]any {
	summary, err := s.store.StatsSummary()
	if err != nil {
		return textResponse(http.StatusInternalServerError, "stats failed")
	}
	return jsonResponse(http.StatusOK, summary)
}

func (s *Service) session(headers map[string][]string, query map[string][]string) map[string]any {
	secret, okSecret := ExtractSecret(func(name string) string {
		for key, values := range headers {
			if strings.EqualFold(key, name) && len(values) > 0 {
				return values[0]
			}
		}
		return ""
	})
	if !okSecret {
		return jsonResponse(http.StatusUnauthorized, map[string]string{"error": "api key required"})
	}
	session, page, err := s.store.SessionPage(secret, queryInt(query, "limit", 25), queryOne(query, "cursor"))
	if err == sql.ErrNoRows {
		return jsonResponse(http.StatusUnauthorized, map[string]string{"error": "invalid api key"})
	}
	if err != nil {
		return textResponse(http.StatusInternalServerError, "session failed")
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"Key": session.Key, "Requests": session.Requests, "base_url": requestBaseURL(headers),
		"next_cursor": page.NextCursor, "has_more": page.HasMore,
	})
}

func unsupportedPath(path string) bool {
	switch {
	case path == "/v1/live" || strings.HasPrefix(path, "/v1/live/"):
		return true
	case path == "/v1/realtime" || strings.HasPrefix(path, "/v1/realtime/"):
		return true
	case path == "/v1/alpha/search" || path == "/backend-api/codex/alpha/search":
		return true
	default:
		return false
	}
}

func terminate(status int, code, message, errType string) map[string]any {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "type": errType, "code": code}})
	return map[string]any{"Terminate": true, "StatusCode": status, "ResponseBody": body}
}

func denialMessage(code string) string {
	switch code {
	case "token_limit_exceeded":
		return "token limit exceeded"
	case "key_expired":
		return "api key expired"
	case "key_disabled":
		return "api key disabled"
	default:
		return "invalid api key"
	}
}

func requestBaseURL(headers map[string][]string) string {
	host := headerValue(headers, "Host")
	if host == "" {
		host = headerValue(headers, "X-Forwarded-Host")
	}
	if host == "" {
		return ""
	}
	scheme := headerValue(headers, "X-Forwarded-Proto")
	if scheme == "" {
		scheme = "http"
	}
	return scheme + "://" + host + "/v1"
}

func headerValue(headers map[string][]string, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
	}
	return ""
}

func parseTimeQuery(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func maxTokens(body []byte) int64 {
	var payload struct {
		MaxTokens     int64 `json:"max_tokens"`
		MaxCompletion int64 `json:"max_completion_tokens"`
	}
	_ = json.Unmarshal(body, &payload)
	if payload.MaxCompletion > 0 {
		return payload.MaxCompletion
	}
	return payload.MaxTokens
}

func unwrap(request []byte) []byte {
	var wrapped struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(request, &wrapped) == nil && len(wrapped.Result) > 0 {
		return wrapped.Result
	}
	return request
}

func ok(result any) ([]byte, error) {
	return json.Marshal(map[string]any{"ok": true, "result": result})
}

func jsonResponse(status int, payload any) map[string]any {
	body, _ := json.Marshal(payload)
	return map[string]any{"StatusCode": status, "Headers": map[string][]string{"Content-Type": {"application/json"}}, "Body": body}
}

func htmlResponse(body []byte) map[string]any {
	if len(body) == 0 {
		body = []byte("<!doctype html><title>key-shop</title>")
	}
	return map[string]any{"StatusCode": http.StatusOK, "Headers": map[string][]string{"Content-Type": {"text/html; charset=utf-8"}}, "Body": body}
}

func textResponse(status int, message string) map[string]any {
	return map[string]any{"StatusCode": status, "Headers": map[string][]string{"Content-Type": {"text/plain"}}, "Body": []byte(message)}
}

func queryOne(q map[string][]string, key string) string {
	if q == nil {
		return ""
	}
	values := q[key]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func queryInt(q map[string][]string, key string, fallback int) int {
	value, err := strconv.Atoi(queryOne(q, key))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func bodyBytes(raw json.RawMessage) []byte {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if raw[0] == '"' {
		var decoded []byte
		if json.Unmarshal(raw, &decoded) == nil {
			return decoded
		}
	}
	return raw
}

func idFrom(body []byte) string {
	var in struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &in)
	return in.ID
}
