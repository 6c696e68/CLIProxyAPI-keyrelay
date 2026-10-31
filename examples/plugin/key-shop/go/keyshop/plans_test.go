package keyshop

import (
	"errors"
	"testing"
	"time"
)

var plansNow = time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)

func planInput(name, groupID string) PlanInput {
	return PlanInput{
		Name:         name,
		GroupID:      groupID,
		TTLSeconds:   3600,
		TokenLimit:   1000,
		RequestLimit: 100,
		Active:       true,
	}
}

func mustPlan(t *testing.T, s *Store, in PlanInput) Plan {
	t.Helper()
	plan, err := s.CreatePlan(in)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestCreatePlanAndGet(t *testing.T) {
	s := testStore(t, plansNow)
	group, err := s.CreateGroup(GroupInput{
		Name:   "g1",
		Models: []GroupModel{{Name: "upstream-name", Alias: "client-name"}, {Name: "m2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := mustPlan(t, s, planInput("Gold", group.ID))
	if plan.Revision != 1 || !plan.Active || plan.GroupID != group.ID {
		t.Fatalf("plan=%+v", plan)
	}
	if plan.CreatedAt.IsZero() || plan.UpdatedAt.IsZero() {
		t.Fatalf("timestamps missing: %+v", plan)
	}
	// The plan exposes client-visible model names, not upstream names.
	if plan.Group.ID != group.ID || plan.Group.Name != "g1" || plan.Group.AllModels {
		t.Fatalf("group=%+v", plan.Group)
	}
	if len(plan.Group.Models) != 2 || plan.Group.Models[0] != "client-name" || plan.Group.Models[1] != "m2" {
		t.Fatalf("models=%v", plan.Group.Models)
	}
	got, err := s.GetPlan(plan.ID)
	if err != nil || got.ID != plan.ID || got.Group.Name != "g1" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err = s.GetPlan("ghost"); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("missing plan err=%v", err)
	}
	list, err := s.ListPlans()
	if err != nil || len(list) != 1 || list[0].ID != plan.ID {
		t.Fatalf("list=%+v err=%v", list, err)
	}
}

func TestUpdatePlanReplacesFieldsAndBumpsRevision(t *testing.T) {
	s := testStore(t, plansNow)
	group := mustGroup(t, s, "g1", "m1")
	other, err := s.CreateGroup(GroupInput{Name: "g2", Models: groupModels("m2")})
	if err != nil {
		t.Fatal(err)
	}
	plan := mustPlan(t, s, planInput("Gold", group.ID))

	same, err := s.UpdatePlan(plan.ID, planInput("Gold", group.ID))
	if err != nil {
		t.Fatal(err)
	}
	if same.Revision != plan.Revision || !same.UpdatedAt.Equal(plan.UpdatedAt) {
		t.Fatalf("a no-op update must not bump the revision: before=%+v after=%+v", plan, same)
	}

	next := planInput("Gold", other.ID)
	next.Description = "new description"
	next.TTLSeconds = 7200
	next.NeverExpires = true
	next.UnlimitedTokens = true
	next.UnlimitedRequests = true
	next.Active = false
	updated, err := s.UpdatePlan(plan.ID, next)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != plan.Revision+1 {
		t.Fatalf("revision=%d want %d", updated.Revision, plan.Revision+1)
	}
	if updated.GroupID != other.ID || updated.Group.Name != "g2" || updated.Description != "new description" ||
		updated.TTLSeconds != 7200 || !updated.NeverExpires || !updated.UnlimitedTokens || updated.TokenLimit != 0 ||
		!updated.UnlimitedRequests || updated.RequestLimit != 0 || updated.Active {
		t.Fatalf("updated=%+v", updated)
	}

	// A case-only rename still bumps the revision because the stored name changes.
	rename := next
	rename.Name = "gold"
	renamed, err := s.UpdatePlan(plan.ID, rename)
	if err != nil || renamed.Revision != updated.Revision+1 {
		t.Fatalf("renamed=%+v err=%v", renamed, err)
	}
	if _, err = s.CreatePlan(planInput("GOLD", group.ID)); !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("duplicate name err=%v", err)
	}
	if _, err = s.UpdatePlan("ghost", planInput("Ghost", group.ID)); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("missing plan update err=%v", err)
	}
}

func TestPlanValidation(t *testing.T) {
	s := testStore(t, plansNow)
	group := mustGroup(t, s, "g1", "m1")

	bad := func(name string, in PlanInput, want error) {
		t.Helper()
		if _, err := s.CreatePlan(in); !errors.Is(err, want) {
			t.Fatalf("%s: err=%v want %v", name, err, want)
		}
	}
	empty := planInput("", group.ID)
	bad("empty name", empty, ErrPlanInvalid)
	noGroup := planInput("No group", "")
	bad("empty group", noGroup, ErrGroupRequired)
	ghost := planInput("Ghost group", "ghost")
	bad("unknown group", ghost, ErrGroupNotFound)
	zeroTTL := planInput("Zero ttl", group.ID)
	zeroTTL.TTLSeconds = 0
	bad("zero ttl", zeroTTL, ErrPlanInvalid)
	negativeTokens := planInput("Negative tokens", group.ID)
	negativeTokens.TokenLimit = -1
	bad("negative token limit", negativeTokens, ErrPlanInvalid)
	negativeRequests := planInput("Negative requests", group.ID)
	negativeRequests.RequestLimit = -1
	bad("negative request limit", negativeRequests, ErrPlanInvalid)

	// never_expires makes ttl_seconds optional.
	lifetime := PlanInput{Name: "lifetime", GroupID: group.ID, NeverExpires: true, Active: true}
	plan := mustPlan(t, s, lifetime)
	if !plan.NeverExpires || plan.TTLSeconds != 0 {
		t.Fatalf("plan=%+v", plan)
	}
	// Unlimited flags zero the stored limits.
	unlimited := planInput("unlimited", group.ID)
	unlimited.UnlimitedTokens = true
	unlimited.TokenLimit = 42
	unlimited.UnlimitedRequests = true
	unlimited.RequestLimit = 42
	plan = mustPlan(t, s, unlimited)
	if !plan.UnlimitedTokens || plan.TokenLimit != 0 || !plan.UnlimitedRequests || plan.RequestLimit != 0 {
		t.Fatalf("plan=%+v", plan)
	}
}

func TestDeletePlanBlockedByOrders(t *testing.T) {
	s := testStore(t, plansNow)
	group := mustGroup(t, s, "g1", "m1")
	plan := mustPlan(t, s, planInput("Gold", group.ID))
	other := mustPlan(t, s, planInput("Silver", group.ID))

	if _, err := s.db.Exec(`INSERT INTO partner_orders (partner_id, ref, plan_id, state, created_at) VALUES (?,?,?,'minted',?)`,
		"partner-1", "ref-0001", plan.ID, partnerTime(plansNow)); err != nil {
		t.Fatal(err)
	}
	var inUse *PlanInUseError
	if err := s.DeletePlan(plan.ID); !errors.As(err, &inUse) || inUse.OrderCount != 1 {
		t.Fatalf("delete err=%v", err)
	}
	if err := s.DeletePlan(other.ID); err != nil {
		t.Fatalf("unused plan delete err=%v", err)
	}
	if _, err := s.GetPlan(other.ID); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("deleted plan err=%v", err)
	}
	if err := s.DeletePlan("ghost"); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("missing plan err=%v", err)
	}
}

func TestActivePlanBlocksGroupDelete(t *testing.T) {
	s := testStore(t, plansNow)
	group := mustGroup(t, s, "g1", "m1")
	plan := mustPlan(t, s, planInput("Gold", group.ID))

	var inUse *GroupInUseError
	if err := s.DeleteGroup(group.ID); !errors.As(err, &inUse) || inUse.Plans != 1 || inUse.Count != 0 {
		t.Fatalf("delete err=%v", err)
	}

	inactive := planInput("Gold", group.ID)
	inactive.Active = false
	if _, err := s.UpdatePlan(plan.ID, inactive); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteGroup(group.ID); err != nil {
		t.Fatalf("inactive plan must not block the group delete: %v", err)
	}
	// An inactive plan may keep a dangling group id and still be readable.
	got, err := s.GetPlan(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Group.ID != group.ID || got.Group.Name != "" || len(got.Group.Models) != 0 {
		t.Fatalf("group=%+v", got.Group)
	}
}
