package keyshop

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAdminPlanRoutes(t *testing.T) {
	a := newPartnerAPI(t, nil)
	res := callManagementAPI(t, a.service, http.MethodGet, "/plugins/key-shop/plans", nil, nil)
	if res.Status != http.StatusOK {
		t.Fatalf("list status=%d body=%s", res.Status, res.Body)
	}
	var listed struct {
		Plans []Plan `json:"plans"`
	}
	if err := json.Unmarshal(res.Body, &listed); err != nil || len(listed.Plans) != 1 || listed.Plans[0].ID != a.plan.ID {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}

	create := map[string]any{
		"name": "Gold", "description": "first", "group_id": a.group.ID,
		"ttl_seconds": 3600, "token_limit": 100, "request_limit": 10,
	}
	res = callManagementAPI(t, a.service, http.MethodPost, "/plugins/key-shop/plans", nil, create)
	if res.Status != http.StatusOK {
		t.Fatalf("create status=%d body=%s", res.Status, res.Body)
	}
	var created struct {
		Plan Plan `json:"plan"`
	}
	if err := json.Unmarshal(res.Body, &created); err != nil || created.Plan.ID == "" {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	// An absent active field creates an enabled plan.
	if !created.Plan.Active || created.Plan.Revision != 1 || created.Plan.Group.Name != "g" {
		t.Fatalf("plan=%+v", created.Plan)
	}

	update := map[string]any{
		"id": created.Plan.ID, "name": "Gold", "description": "second", "group_id": a.group.ID,
		"ttl_seconds": 7200, "token_limit": 200, "request_limit": 20, "active": true,
	}
	res = callManagementAPI(t, a.service, http.MethodPost, "/plugins/key-shop/plans/update", nil, update)
	if res.Status != http.StatusOK {
		t.Fatalf("update status=%d body=%s", res.Status, res.Body)
	}
	var updated struct {
		Plan Plan `json:"plan"`
	}
	if err := json.Unmarshal(res.Body, &updated); err != nil || updated.Plan.Revision != 2 || updated.Plan.TTLSeconds != 7200 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}

	bad := map[string]any{"name": "Broken", "group_id": "ghost", "ttl_seconds": 3600}
	res = callManagementAPI(t, a.service, http.MethodPost, "/plugins/key-shop/plans", nil, bad)
	if res.Status != http.StatusBadRequest {
		t.Fatalf("unknown group status=%d body=%s", res.Status, res.Body)
	}
	res = callManagementAPI(t, a.service, http.MethodPost, "/plugins/key-shop/plans", nil, map[string]any{
		"name": "Gold", "group_id": a.group.ID, "ttl_seconds": 3600,
	})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("duplicate name status=%d body=%s", res.Status, res.Body)
	}
	res = callManagementAPI(t, a.service, http.MethodPost, "/plugins/key-shop/plans/update", nil, map[string]any{
		"id": "ghost", "name": "Ghost", "group_id": a.group.ID, "ttl_seconds": 3600, "active": true,
	})
	if res.Status != http.StatusNotFound {
		t.Fatalf("missing plan status=%d body=%s", res.Status, res.Body)
	}

	res = callManagementAPI(t, a.service, http.MethodPost, "/plugins/key-shop/plans/delete", nil, map[string]any{"id": created.Plan.ID})
	if res.Status != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", res.Status, res.Body)
	}
	res = callManagementAPI(t, a.service, http.MethodPost, "/plugins/key-shop/plans/delete", nil, map[string]any{"id": created.Plan.ID})
	if res.Status != http.StatusNotFound {
		t.Fatalf("delete again status=%d body=%s", res.Status, res.Body)
	}

	// An order keeps its plan, so delete reports the order count.
	a.mintOK(t, "order-0001", []string{newMintSecret(t)})
	res = callManagementAPI(t, a.service, http.MethodPost, "/plugins/key-shop/plans/delete", nil, map[string]any{"id": a.plan.ID})
	if res.Status != http.StatusConflict || res.JSON["order_count"] != float64(1) {
		t.Fatalf("in-use delete status=%d body=%s", res.Status, res.Body)
	}
}
