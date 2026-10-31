package keyshop

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	partnerAPIPrefix  = "/plugins/key-shop/partner/v1"
	partnerHeaderName = "X-Key-Shop-Partner"
	partnerClientIP   = "X-Key-Shop-Client-IP"
	bytesPerKB        = 1024
)

// partnerOp returns the exact op for a partner path. Anything else under the
// prefix is not a partner route and must 404.
func partnerOp(method, path string) string {
	switch {
	case method == http.MethodGet && path == partnerAPIPrefix+"/health":
		return "health"
	case method == http.MethodGet && path == partnerAPIPrefix+"/plans":
		return "plans"
	case method == http.MethodPost && path == partnerAPIPrefix+"/mint":
		return "mint"
	case method == http.MethodPost && path == partnerAPIPrefix+"/confirm":
		return "confirm"
	case method == http.MethodPost && path == partnerAPIPrefix+"/void":
		return "void"
	case method == http.MethodPost && path == partnerAPIPrefix+"/status":
		return "status"
	case method == http.MethodPost && path == partnerAPIPrefix+"/refs":
		return "refs"
	}
	return ""
}

// managePartner enforces the alias header before anything else. The header is
// injected by the host alias and stripped from inbound requests, so a direct
// management call cannot use a management key as a partner side door.
func (s *Service) managePartner(req managementRequest, body []byte) map[string]any {
	if headerValue(req.Headers, partnerHeaderName) != "1" {
		return textResponse(http.StatusNotFound, "not found")
	}
	op := partnerOp(req.Method, pluginRelativePath(req.Path))
	if op == "" {
		return textResponse(http.StatusNotFound, "not found")
	}
	started := time.Now()
	ref := partnerRefFromBody(body)
	partnerID, code, response := s.partnerCall(op, req.Headers, body)
	status, _ := response["StatusCode"].(int)
	// The audit write must never turn a working API response into an error.
	_ = s.store.RecordPartnerCall(PartnerCall{
		PartnerID: partnerID, Op: op, HTTPStatus: status, Code: code, Ref: ref,
		LatencyMS: time.Since(started).Milliseconds(), ClientIP: headerValue(req.Headers, partnerClientIP),
	})
	return response
}

// partnerCall runs the check order from the spec: feature flag, auth, body cap.
func (s *Service) partnerCall(op string, headers map[string][]string, body []byte) (string, string, map[string]any) {
	cfg := s.store.cfg
	if !cfg.PartnerAPIEnabled {
		return "", "partner_api_disabled", partnerError(http.StatusServiceUnavailable, "partner_api_disabled", "partner api is disabled")
	}
	token, okToken := partnerBearer(headers)
	if !okToken {
		return "", "unauthorized", partnerUnauthorized()
	}
	partner, err := s.store.AuthenticatePartner(token)
	if err != nil {
		switch {
		case errors.Is(err, ErrPartnerDisabled):
			return "", "partner_disabled", partnerError(http.StatusForbidden, "partner_disabled", "partner is disabled")
		case errors.Is(err, ErrPartnerUnauthorized):
			return "", "unauthorized", partnerUnauthorized()
		default:
			return "", "internal", partnerError(http.StatusInternalServerError, "internal", "partner authentication failed")
		}
	}
	if int64(len(body)) > int64(cfg.PartnerMaxBodyKB)*bytesPerKB {
		return partner.ID, "bad_request", partnerError(http.StatusBadRequest, "bad_request", "request body is too large")
	}
	switch op {
	case "health":
		code, response := s.partnerHealth(partner)
		return partner.ID, code, response
	case "plans":
		code, response := s.partnerPlans(partner, headers)
		return partner.ID, code, response
	case "mint":
		code, response := s.partnerMint(partner, headers, body)
		return partner.ID, code, response
	case "confirm":
		code, response := s.partnerConfirm(partner, body)
		return partner.ID, code, response
	case "void":
		code, response := s.partnerVoid(partner, body)
		return partner.ID, code, response
	case "status":
		code, response := s.partnerStatus(partner, body)
		return partner.ID, code, response
	case "refs":
		code, response := s.partnerRefs(partner, body)
		return partner.ID, code, response
	}
	return partner.ID, "internal", partnerError(http.StatusInternalServerError, "internal", "unknown partner operation")
}

func (s *Service) partnerHealth(partner Partner) (string, map[string]any) {
	return "", jsonResponse(http.StatusOK, map[string]any{
		"ok":          true,
		"partner":     map[string]string{"id": partner.ID, "name": partner.Name},
		"server_time": s.store.cfg.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Service) partnerPlans(partner Partner, headers map[string][]string) (string, map[string]any) {
	plans, err := s.store.ListPlans()
	if err != nil {
		return "internal", partnerError(http.StatusInternalServerError, "internal", "list plans failed")
	}
	visible := make([]Plan, 0, len(plans))
	for _, plan := range plans {
		if plan.Active && partnerPlanAllowed(partner, plan.ID) {
			visible = append(visible, plan)
		}
	}
	baseURL, dashboardURL := s.partnerURLs(headers)
	return "", jsonResponse(http.StatusOK, map[string]any{
		"plans": visible, "base_url": baseURL, "dashboard_url": dashboardURL,
	})
}

func (s *Service) partnerMint(partner Partner, headers map[string][]string, body []byte) (string, map[string]any) {
	var in struct {
		Ref          string   `json:"ref"`
		PlanID       string   `json:"plan_id"`
		PlanRevision int64    `json:"plan_revision"`
		Quantity     int64    `json:"quantity"`
		Secrets      []string `json:"secrets"`
		Customer     string   `json:"customer"`
		Note         string   `json:"note"`
	}
	if !decodePartnerBody(body, &in) {
		return "bad_request", partnerError(http.StatusBadRequest, "bad_request", "bad json")
	}
	result, err := s.store.PartnerMint(partner.ID, PartnerMintInput{
		Ref: in.Ref, PlanID: in.PlanID, PlanRevision: in.PlanRevision,
		Quantity: in.Quantity, Secrets: in.Secrets, Customer: in.Customer, Note: in.Note,
	})
	if err != nil {
		return partnerFailure(err)
	}
	baseURL, dashboardURL := s.partnerURLs(headers)
	return "", jsonResponse(http.StatusOK, map[string]any{
		"ref": result.Ref, "state": result.State, "replayed": result.Replayed,
		"keys": result.Keys, "plan": result.Plan,
		"base_url": baseURL, "dashboard_url": dashboardURL,
	})
}

func (s *Service) partnerConfirm(partner Partner, body []byte) (string, map[string]any) {
	var in struct {
		Ref     string `json:"ref"`
		OrderID string `json:"order_id"`
		Buyer   string `json:"buyer"`
	}
	if !decodePartnerBody(body, &in) || strings.TrimSpace(in.Ref) == "" {
		return "bad_request", partnerError(http.StatusBadRequest, "bad_request", "ref is required")
	}
	if strings.TrimSpace(in.OrderID) == "" {
		return "bad_request", partnerError(http.StatusBadRequest, "bad_request", "order_id is required")
	}
	state, err := s.store.PartnerConfirm(partner.ID, in.Ref, in.OrderID, in.Buyer)
	if err != nil {
		return partnerFailure(err)
	}
	return "", jsonResponse(http.StatusOK, map[string]any{"ref": state.Ref, "state": state.State})
}

func (s *Service) partnerVoid(partner Partner, body []byte) (string, map[string]any) {
	var in struct {
		Ref    string `json:"ref"`
		Reason string `json:"reason"`
	}
	if !decodePartnerBody(body, &in) || strings.TrimSpace(in.Ref) == "" {
		return "bad_request", partnerError(http.StatusBadRequest, "bad_request", "ref is required")
	}
	result, err := s.store.PartnerVoid(partner.ID, in.Ref, in.Reason)
	if err != nil {
		return partnerFailure(err)
	}
	return "", jsonResponse(http.StatusOK, map[string]any{"ref": result.Ref, "state": result.State, "keys_disabled": result.KeysDisabled})
}

func (s *Service) partnerStatus(partner Partner, body []byte) (string, map[string]any) {
	var in struct {
		Ref string `json:"ref"`
	}
	if !decodePartnerBody(body, &in) || strings.TrimSpace(in.Ref) == "" {
		return "bad_request", partnerError(http.StatusBadRequest, "bad_request", "ref is required")
	}
	result, err := s.store.PartnerStatus(partner.ID, in.Ref)
	if err != nil {
		return partnerFailure(err)
	}
	return "", jsonResponse(http.StatusOK, map[string]any{
		"ref": result.Ref, "state": result.State, "order_id": result.OrderID, "keys": result.Keys,
	})
}

func (s *Service) partnerRefs(partner Partner, body []byte) (string, map[string]any) {
	var in struct {
		Since  string `json:"since"`
		Until  string `json:"until"`
		Cursor string `json:"cursor"`
		Limit  int    `json:"limit"`
	}
	if len(bytes.TrimSpace(body)) > 0 && json.Unmarshal(body, &in) != nil {
		return "bad_request", partnerError(http.StatusBadRequest, "bad_request", "bad json")
	}
	since, okSince := partnerTimeQuery(in.Since)
	if !okSince {
		return "bad_request", partnerError(http.StatusBadRequest, "bad_request", "invalid since")
	}
	until, okUntil := partnerTimeQuery(in.Until)
	if !okUntil {
		return "bad_request", partnerError(http.StatusBadRequest, "bad_request", "invalid until")
	}
	result, err := s.store.PartnerRefs(partner.ID, PartnerRefsQuery{
		Since: since, Until: until, Cursor: in.Cursor, Limit: in.Limit,
	})
	if err != nil {
		return partnerFailure(err)
	}
	return "", jsonResponse(http.StatusOK, map[string]any{"refs": result.Refs, "next_cursor": result.NextCursor})
}

// partnerURLs follows the spec: the configured public URL wins, otherwise the
// request Host with https, and http only for local hosts.
func (s *Service) partnerURLs(headers map[string][]string) (string, string) {
	cfg := s.store.cfg
	if base := strings.TrimSpace(cfg.PublicBaseURL); base != "" {
		base = strings.TrimRight(base, "/")
		return base + "/v1", base + cfg.PublicDashboardPath
	}
	host := headerValue(headers, "Host")
	if host == "" {
		host = headerValue(headers, "X-Forwarded-Host")
	}
	if host == "" {
		return "", cfg.PublicDashboardPath
	}
	scheme := "https"
	hostname := host
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		hostname = parsed
	}
	if strings.EqualFold(hostname, "localhost") || hostname == "127.0.0.1" {
		scheme = "http"
	}
	return scheme + "://" + host + "/v1", scheme + "://" + host + cfg.PublicDashboardPath
}

func partnerBearer(headers map[string][]string) (string, bool) {
	value := headerValue(headers, "Authorization")
	parts := strings.SplitN(value, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return "", false
	}
	return token, true
}

func partnerRefFromBody(body []byte) string {
	var in struct {
		Ref string `json:"ref"`
	}
	if json.Unmarshal(body, &in) != nil {
		return ""
	}
	return truncatePartnerRef(strings.TrimSpace(in.Ref))
}

// truncatePartnerRef keeps an unauthenticated audit row bounded by the
// protocol's ref limit. Truncating by rune keeps the stored text valid UTF-8.
func truncatePartnerRef(ref string) string {
	runes := []rune(ref)
	if len(runes) <= partnerRefMaxLength {
		return ref
	}
	return string(runes[:partnerRefMaxLength])
}

func partnerTimeQuery(value string) (time.Time, bool) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, true
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func decodePartnerBody(body []byte, out any) bool {
	return len(bytes.TrimSpace(body)) > 0 && json.Unmarshal(body, out) == nil
}

func partnerError(status int, code, message string) map[string]any {
	return jsonResponse(status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func partnerUnauthorized() map[string]any {
	return partnerError(http.StatusUnauthorized, "unauthorized", "invalid partner token")
}

func partnerSentinelMessage(err error, sentinel error) string {
	message := strings.TrimPrefix(err.Error(), sentinel.Error()+": ")
	if message == "" {
		return sentinel.Error()
	}
	return message
}

// partnerFailure maps every store sentinel to its spec status and code.
func partnerFailure(err error) (string, map[string]any) {
	var changed *PlanChangedError
	switch {
	case errors.As(err, &changed):
		return "plan_changed", jsonResponse(http.StatusConflict, map[string]any{
			"error": map[string]string{"code": "plan_changed", "message": "plan revision changed"},
			"plan":  changed.Plan,
		})
	case errors.Is(err, ErrPartnerBadRequest):
		return "bad_request", partnerError(http.StatusBadRequest, "bad_request", partnerSentinelMessage(err, ErrPartnerBadRequest))
	case errors.Is(err, ErrPartnerUnauthorized):
		return "unauthorized", partnerUnauthorized()
	case errors.Is(err, ErrPartnerDisabled):
		return "partner_disabled", partnerError(http.StatusForbidden, "partner_disabled", "partner is disabled")
	case errors.Is(err, ErrPlanNotFound):
		return "plan_not_found", partnerError(http.StatusNotFound, "plan_not_found", "plan not found")
	case errors.Is(err, ErrPlanNotAllowed):
		return "plan_not_allowed", partnerError(http.StatusForbidden, "plan_not_allowed", "plan is not allowed for this partner")
	case errors.Is(err, ErrPlanInactive):
		return "plan_inactive", partnerError(http.StatusConflict, "plan_inactive", "plan is inactive")
	case errors.Is(err, ErrDailyCapReached):
		return "daily_cap_reached", partnerError(http.StatusTooManyRequests, "daily_cap_reached", "daily key cap reached")
	case errors.Is(err, ErrRefVoided):
		return "ref_voided", partnerError(http.StatusGone, "ref_voided", "ref was voided")
	case errors.Is(err, ErrRefConflict):
		return "ref_conflict", partnerError(http.StatusConflict, "ref_conflict", "ref already exists with a different body")
	case errors.Is(err, ErrSecretConflict):
		return "secret_conflict", partnerError(http.StatusConflict, "secret_conflict", "secret already exists")
	case errors.Is(err, ErrRefNotFound):
		return "ref_not_found", partnerError(http.StatusNotFound, "ref_not_found", "ref not found")
	case errors.Is(err, ErrOrderConflict):
		return "order_conflict", partnerError(http.StatusConflict, "order_conflict", "order id does not match")
	case errors.Is(err, ErrRefConfirmed):
		return "ref_confirmed", partnerError(http.StatusConflict, "ref_confirmed", "ref is confirmed")
	default:
		return "internal", partnerError(http.StatusInternalServerError, "internal", "internal error")
	}
}

type partnerCreateBody struct {
	Name           string   `json:"name"`
	AllowedPlanIDs []string `json:"allowed_plan_ids"`
	DailyKeyCap    *int64   `json:"daily_key_cap"`
	MaxQuantity    *int64   `json:"max_quantity"`
}

// partnerUpdateBody keeps the cap fields raw so a JSON null clears the override
// while an absent field leaves it alone.
type partnerUpdateBody struct {
	ID             string          `json:"id"`
	Name           *string         `json:"name"`
	Status         *string         `json:"status"`
	AllowedPlanIDs *[]string       `json:"allowed_plan_ids"`
	DailyKeyCap    json.RawMessage `json:"daily_key_cap"`
	MaxQuantity    json.RawMessage `json:"max_quantity"`
}

func partnerAdminFailure(err error) (map[string]any, bool) {
	switch {
	case errors.Is(err, ErrPartnerNotFound):
		return jsonError(http.StatusNotFound, "partner not found"), true
	case errors.Is(err, ErrPartnerInvalid):
		return jsonError(http.StatusBadRequest, strings.TrimPrefix(err.Error(), ErrPartnerInvalid.Error()+": ")), true
	case errors.Is(err, ErrPlanNotFound):
		return jsonError(http.StatusBadRequest, "unknown plan"), true
	}
	return nil, false
}

func optionalCap(raw json.RawMessage) (*int64, bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false, nil
	}
	if string(bytes.TrimSpace(raw)) == "null" {
		return nil, true, nil
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, true, err
	}
	return &value, true, nil
}

func (s *Service) listPartners() map[string]any {
	partners, err := s.store.ListPartners()
	if err != nil {
		return jsonError(http.StatusInternalServerError, "list partners failed")
	}
	if partners == nil {
		partners = []Partner{}
	}
	return jsonResponse(http.StatusOK, map[string]any{"partners": partners})
}

func (s *Service) createPartner(body []byte) map[string]any {
	var in partnerCreateBody
	if json.Unmarshal(body, &in) != nil {
		return jsonError(http.StatusBadRequest, "bad json")
	}
	partner, secret, err := s.store.CreatePartner(PartnerInput{
		Name: in.Name, AllowedPlanIDs: in.AllowedPlanIDs,
		DailyKeyCap: in.DailyKeyCap, MaxQuantity: in.MaxQuantity,
	})
	if err != nil {
		if resp, handled := partnerAdminFailure(err); handled {
			return resp
		}
		return jsonError(http.StatusInternalServerError, "create partner failed")
	}
	return jsonResponse(http.StatusOK, map[string]any{"partner": partner, "secret": secret})
}

func (s *Service) updatePartner(body []byte) map[string]any {
	var in partnerUpdateBody
	if json.Unmarshal(body, &in) != nil || strings.TrimSpace(in.ID) == "" {
		return jsonError(http.StatusBadRequest, "bad json")
	}
	update := PartnerUpdate{}
	if in.Name != nil {
		update.Name, update.NameSet = *in.Name, true
	}
	if in.Status != nil {
		update.Status, update.StatusSet = *in.Status, true
	}
	if in.AllowedPlanIDs != nil {
		update.AllowedPlanIDs, update.AllowedPlansSet = *in.AllowedPlanIDs, true
	}
	daily, setDaily, errDaily := optionalCap(in.DailyKeyCap)
	if errDaily != nil {
		return jsonError(http.StatusBadRequest, "invalid daily_key_cap")
	}
	if setDaily {
		update.DailyKeyCap, update.DailyCapSet = daily, true
	}
	max, setMax, errMax := optionalCap(in.MaxQuantity)
	if errMax != nil {
		return jsonError(http.StatusBadRequest, "invalid max_quantity")
	}
	if setMax {
		update.MaxQuantity, update.MaxQuantitySet = max, true
	}
	partner, err := s.store.UpdatePartner(in.ID, update)
	if err != nil {
		if resp, handled := partnerAdminFailure(err); handled {
			return resp
		}
		return jsonError(http.StatusInternalServerError, "update partner failed")
	}
	return jsonResponse(http.StatusOK, map[string]any{"partner": partner})
}

func (s *Service) rotatePartner(body []byte) map[string]any {
	id := idFrom(body)
	if strings.TrimSpace(id) == "" {
		return jsonError(http.StatusBadRequest, "id is required")
	}
	partner, secret, err := s.store.RotatePartner(id)
	if err != nil {
		if resp, handled := partnerAdminFailure(err); handled {
			return resp
		}
		return jsonError(http.StatusInternalServerError, "rotate partner failed")
	}
	return jsonResponse(http.StatusOK, map[string]any{"partner": partner, "secret": secret})
}

func (s *Service) deletePartner(body []byte) map[string]any {
	id := idFrom(body)
	if strings.TrimSpace(id) == "" {
		return jsonError(http.StatusBadRequest, "id is required")
	}
	if err := s.store.DeletePartner(id); err != nil {
		if resp, handled := partnerAdminFailure(err); handled {
			return resp
		}
		return jsonError(http.StatusInternalServerError, "delete partner failed")
	}
	return jsonResponse(http.StatusOK, map[string]any{"deleted": true})
}

func (s *Service) partnerCalls(q map[string][]string) map[string]any {
	calls, next, err := s.store.ListPartnerCalls(PartnerCallQuery{
		PartnerID: queryOne(q, "partner"), Op: queryOne(q, "op"), Status: queryOne(q, "status"),
		From: parseTimeQuery(queryOne(q, "from")), To: parseTimeQuery(queryOne(q, "to")),
		Limit: queryInt(q, "limit", 0), Cursor: queryOne(q, "cursor"),
	})
	if err != nil {
		if errors.Is(err, ErrPartnerBadRequest) {
			return jsonError(http.StatusBadRequest, "invalid cursor")
		}
		return jsonError(http.StatusInternalServerError, "list calls failed")
	}
	return jsonResponse(http.StatusOK, map[string]any{"calls": calls, "next_cursor": next})
}

func (s *Service) partnerOrders(q map[string][]string) map[string]any {
	orders, next, err := s.store.ListPartnerOrders(PartnerOrderQuery{
		PartnerID: queryOne(q, "partner"), State: queryOne(q, "state"), Ref: queryOne(q, "ref"),
		From: parseTimeQuery(queryOne(q, "from")), To: parseTimeQuery(queryOne(q, "to")),
		Limit: queryInt(q, "limit", 0), Cursor: queryOne(q, "cursor"),
	})
	if err != nil {
		if errors.Is(err, ErrPartnerBadRequest) {
			return jsonError(http.StatusBadRequest, "invalid cursor")
		}
		return jsonError(http.StatusInternalServerError, "list orders failed")
	}
	return jsonResponse(http.StatusOK, map[string]any{"orders": orders, "next_cursor": next})
}
