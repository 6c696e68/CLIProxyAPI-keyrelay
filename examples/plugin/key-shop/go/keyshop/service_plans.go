package keyshop

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// planBody is the admin plan payload. Active is a pointer so create can default
// to an enabled plan while update still replaces every field.
type planBody struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	GroupID           string `json:"group_id"`
	TTLSeconds        int64  `json:"ttl_seconds"`
	NeverExpires      bool   `json:"never_expires"`
	TokenLimit        int64  `json:"token_limit"`
	UnlimitedTokens   bool   `json:"unlimited_tokens"`
	RequestLimit      int64  `json:"request_limit"`
	UnlimitedRequests bool   `json:"unlimited_requests"`
	Active            *bool  `json:"active"`
}

func (b planBody) input(active bool) PlanInput {
	return PlanInput{
		Name: b.Name, Description: b.Description, GroupID: b.GroupID,
		TTLSeconds: b.TTLSeconds, NeverExpires: b.NeverExpires,
		TokenLimit: b.TokenLimit, UnlimitedTokens: b.UnlimitedTokens,
		RequestLimit: b.RequestLimit, UnlimitedRequests: b.UnlimitedRequests,
		Active: active,
	}
}

// planFailure maps plan errors to admin responses. Group errors fall through to groupFailure.
func planFailure(err error) (map[string]any, bool) {
	var inUse *PlanInUseError
	switch {
	case errors.As(err, &inUse):
		return jsonResponse(http.StatusConflict, map[string]any{"error": err.Error(), "order_count": inUse.OrderCount}), true
	case errors.Is(err, ErrPlanNotFound):
		return jsonError(http.StatusNotFound, "plan not found"), true
	case errors.Is(err, ErrPlanInvalid):
		return jsonError(http.StatusBadRequest, strings.TrimPrefix(err.Error(), ErrPlanInvalid.Error()+": ")), true
	}
	return groupFailure(err)
}

func (s *Service) listPlans() map[string]any {
	plans, err := s.store.ListPlans()
	if err != nil {
		return jsonError(http.StatusInternalServerError, "list plans failed")
	}
	return jsonResponse(http.StatusOK, map[string]any{"plans": plans})
}

func (s *Service) createPlan(body []byte) map[string]any {
	var in planBody
	if json.Unmarshal(body, &in) != nil {
		return jsonError(http.StatusBadRequest, "bad json")
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	plan, err := s.store.CreatePlan(in.input(active))
	if err != nil {
		if resp, handled := planFailure(err); handled {
			return resp
		}
		return jsonError(http.StatusInternalServerError, "create plan failed")
	}
	return jsonResponse(http.StatusOK, map[string]any{"plan": plan})
}

func (s *Service) updatePlan(body []byte) map[string]any {
	var in planBody
	if json.Unmarshal(body, &in) != nil || strings.TrimSpace(in.ID) == "" {
		return jsonError(http.StatusBadRequest, "bad json")
	}
	active := false
	if in.Active != nil {
		active = *in.Active
	}
	plan, err := s.store.UpdatePlan(in.ID, in.input(active))
	if err != nil {
		if resp, handled := planFailure(err); handled {
			return resp
		}
		return jsonError(http.StatusInternalServerError, "update plan failed")
	}
	return jsonResponse(http.StatusOK, map[string]any{"plan": plan})
}

func (s *Service) deletePlan(body []byte) map[string]any {
	id := idFrom(body)
	if strings.TrimSpace(id) == "" {
		return jsonError(http.StatusBadRequest, "id is required")
	}
	if err := s.store.DeletePlan(id); err != nil {
		if resp, handled := planFailure(err); handled {
			return resp
		}
		return jsonError(http.StatusInternalServerError, "delete plan failed")
	}
	return jsonResponse(http.StatusOK, map[string]any{"deleted": true})
}
