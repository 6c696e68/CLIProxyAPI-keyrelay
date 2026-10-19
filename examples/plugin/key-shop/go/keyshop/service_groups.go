package keyshop

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

func jsonError(status int, message string) map[string]any {
	return jsonResponse(status, map[string]string{"error": message})
}

// groupFailure maps group and bulk errors to HTTP responses. handled is false for other errors.
func groupFailure(err error) (map[string]any, bool) {
	var inUse *GroupInUseError
	switch {
	case errors.As(err, &inUse):
		return jsonResponse(http.StatusConflict, map[string]any{"error": err.Error(), "key_count": inUse.Count}), true
	case errors.Is(err, ErrGroupNotFound):
		return jsonError(http.StatusBadRequest, "unknown model group"), true
	case errors.Is(err, ErrGroupRequired):
		return jsonError(http.StatusBadRequest, "group_id is required"), true
	case errors.Is(err, ErrGroupInvalid):
		return jsonError(http.StatusBadRequest, strings.TrimPrefix(err.Error(), ErrGroupInvalid.Error()+": ")), true
	case errors.Is(err, ErrGroupSystem):
		return jsonError(http.StatusForbidden, err.Error()), true
	case errors.Is(err, ErrKeysNotFound):
		return jsonError(http.StatusNotFound, err.Error()), true
	}
	return nil, false
}

type groupBody struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	AllModels   bool           `json:"all_models"`
	Models      groupModelList `json:"models"`
}

// groupModelList accepts a legacy string array or [{name, alias}].
type groupModelList []GroupModel

func (m *groupModelList) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return err
	}
	out := make([]GroupModel, 0, len(items))
	for _, item := range items {
		item = bytes.TrimSpace(item)
		if len(item) == 0 {
			continue
		}
		if item[0] == '"' {
			var name string
			if err := json.Unmarshal(item, &name); err != nil {
				return err
			}
			out = append(out, GroupModel{Name: name})
			continue
		}
		var model GroupModel
		if err := json.Unmarshal(item, &model); err != nil {
			return err
		}
		out = append(out, model)
	}
	*m = out
	return nil
}

func (b groupBody) input() GroupInput {
	return GroupInput{Name: b.Name, Description: b.Description, AllModels: b.AllModels, Models: b.Models}
}

func (s *Service) listGroups() map[string]any {
	groups, err := s.store.ListGroups()
	if err != nil {
		return jsonError(http.StatusInternalServerError, "list groups failed")
	}
	return jsonResponse(http.StatusOK, map[string]any{"groups": groups})
}

func (s *Service) createGroup(body []byte) map[string]any {
	var in groupBody
	if json.Unmarshal(body, &in) != nil {
		return jsonError(http.StatusBadRequest, "bad json")
	}
	group, err := s.store.CreateGroup(in.input())
	if err != nil {
		if resp, handled := groupFailure(err); handled {
			return resp
		}
		return jsonError(http.StatusInternalServerError, "create group failed")
	}
	return jsonResponse(http.StatusOK, map[string]any{"group": group})
}

func (s *Service) updateGroup(body []byte) map[string]any {
	var in groupBody
	if json.Unmarshal(body, &in) != nil || strings.TrimSpace(in.ID) == "" {
		return jsonError(http.StatusBadRequest, "bad json")
	}
	group, err := s.store.UpdateGroup(in.ID, in.input())
	if err != nil {
		if resp, handled := groupFailure(err); handled {
			return resp
		}
		return jsonError(http.StatusInternalServerError, "update group failed")
	}
	return jsonResponse(http.StatusOK, map[string]any{"group": group})
}

func (s *Service) deleteGroup(body []byte) map[string]any {
	if err := s.store.DeleteGroup(idFrom(body)); err != nil {
		if resp, handled := groupFailure(err); handled {
			if errors.Is(err, ErrGroupNotFound) {
				return jsonError(http.StatusNotFound, "model group not found")
			}
			return resp
		}
		return jsonError(http.StatusInternalServerError, "delete group failed")
	}
	return jsonResponse(http.StatusOK, map[string]any{"deleted": true})
}

func (s *Service) bulkUpdate(body []byte) map[string]any {
	var in struct {
		IDs               []string `json:"ids"`
		GroupID           string   `json:"group_id"`
		Status            string   `json:"status"`
		Customer          string   `json:"customer"`
		Note              string   `json:"note"`
		TTLSeconds        int64    `json:"ttl_seconds"`
		NeverExpires      bool     `json:"never_expires"`
		TokenLimit        int64    `json:"token_limit"`
		UnlimitedTokens   bool     `json:"unlimited_tokens"`
		RequestLimit      int64    `json:"request_limit"`
		UnlimitedRequests bool     `json:"unlimited_requests"`
	}
	if json.Unmarshal(body, &in) != nil {
		return jsonError(http.StatusBadRequest, "bad json")
	}
	has := func(field string) bool { return strings.Contains(string(body), `"`+field+`"`) }
	updated, err := s.store.BulkUpdate(BulkUpdateInput{
		IDs: in.IDs, GroupID: in.GroupID, GroupSet: has("group_id"),
		Status: in.Status, StatusSet: has("status"),
		Customer: in.Customer, CustomerSet: has("customer"), Note: in.Note, NoteSet: has("note"),
		TTLSeconds: in.TTLSeconds, TTLSet: has("ttl_seconds"), NeverExpires: in.NeverExpires,
		TokenLimit: in.TokenLimit, TokenLimitSet: has("token_limit"), UnlimitedTokens: in.UnlimitedTokens,
		RequestLimit: in.RequestLimit, RequestLimitSet: has("request_limit"), UnlimitedRequests: in.UnlimitedRequests,
	})
	if err != nil {
		if resp, handled := groupFailure(err); handled {
			return resp
		}
		return jsonError(http.StatusBadRequest, err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"updated": updated})
}

func (s *Service) bulkDelete(body []byte) map[string]any {
	var in struct {
		IDs []string `json:"ids"`
	}
	if json.Unmarshal(body, &in) != nil {
		return jsonError(http.StatusBadRequest, "bad json")
	}
	deleted, err := s.store.BulkDelete(in.IDs)
	if err != nil {
		if resp, handled := groupFailure(err); handled {
			return resp
		}
		return jsonError(http.StatusBadRequest, err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"deleted": deleted})
}

func (s *Service) usageStats(q map[string][]string) map[string]any {
	stats, err := s.store.UsageStats(parseTimeQuery(queryOne(q, "from")), parseTimeQuery(queryOne(q, "to")), queryOne(q, "bucket"))
	if err != nil {
		return jsonError(http.StatusInternalServerError, "usage stats failed")
	}
	return jsonResponse(http.StatusOK, stats)
}

// sessionModels returns the model names a customer key may call, as the client sees them.
// A limited group uses its own alias, or the upstream name when no alias is set.
// The all-models group and a key with no group still follow the global policy.
func (s *Service) sessionModels(group *Group) (string, []string) {
	if group != nil && !group.AllModels {
		visible := make([]string, 0, len(group.Models))
		for _, model := range group.Models {
			if shown := model.ClientName(); shown != "" {
				visible = append(visible, shown)
			}
		}
		return "custom", visible
	}
	if strings.EqualFold(strings.TrimSpace(s.store.cfg.ModelAccessMode), "custom") {
		return "custom", clientVisibleModels(s.store.cfg.Models)
	}
	return "full", nil
}
