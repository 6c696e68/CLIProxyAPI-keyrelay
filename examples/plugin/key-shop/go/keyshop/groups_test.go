package keyshop

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var groupsNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func groupModels(names ...string) []GroupModel {
	out := make([]GroupModel, 0, len(names))
	for _, name := range names {
		out = append(out, GroupModel{Name: name})
	}
	return out
}

func mustGroup(t *testing.T, s *Store, name string, models ...string) Group {
	t.Helper()
	g, err := s.CreateGroup(GroupInput{Name: name, Models: groupModels(models...)})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestSystemGroupIsSeededAndProtected(t *testing.T) {
	s := testStore(t, groupsNow)
	groups, err := s.ListGroups()
	if err != nil || len(groups) != 1 || groups[0].ID != AllModelsGroupID || !groups[0].AllModels || !groups[0].IsSystem {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
	if err = s.DeleteGroup(AllModelsGroupID); !errors.Is(err, ErrGroupSystem) {
		t.Fatalf("delete system err=%v", err)
	}
	if _, err = s.UpdateGroup(AllModelsGroupID, GroupInput{Name: "x", AllModels: true}); !errors.Is(err, ErrGroupSystem) {
		t.Fatalf("update system err=%v", err)
	}
}

func TestGroupCRUDValidation(t *testing.T) {
	s := testStore(t, groupsNow)
	g := mustGroup(t, s, "vippro", "vippro/a", "VIPPRO/A", " vippro/b ", "")
	if len(g.Models) != 2 || g.Models[0].Name != "vippro/a" || g.Models[1].Name != "vippro/b" {
		t.Fatalf("models should be trimmed and de-duplicated: %+v", g.Models)
	}
	if _, err := s.CreateGroup(GroupInput{Name: "VIPPRO", Models: groupModels("x")}); !errors.Is(err, ErrGroupInvalid) {
		t.Fatalf("duplicate name err=%v", err)
	}
	if _, err := s.CreateGroup(GroupInput{Name: "empty"}); !errors.Is(err, ErrGroupInvalid) {
		t.Fatalf("empty selection err=%v", err)
	}
	if _, err := s.CreateGroup(GroupInput{Name: " "}); !errors.Is(err, ErrGroupInvalid) {
		t.Fatalf("blank name err=%v", err)
	}
	updated, err := s.UpdateGroup(g.ID, GroupInput{Name: "vippro", Description: "d", Models: groupModels("vippro/c")})
	if err != nil || len(updated.Models) != 1 || updated.Description != "d" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if _, err = s.UpdateGroup("nope", GroupInput{Name: "n", Models: groupModels("x")}); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("unknown err=%v", err)
	}
	if err = s.DeleteGroup(g.ID); err != nil {
		t.Fatal(err)
	}
}

func TestGroupAllowsIgnoresThinkingSuffixAndCase(t *testing.T) {
	s := testStore(t, groupsNow)
	g := mustGroup(t, s, "two", "vippro/gpt-6-sol", "vippro/gpt-6-luna")
	for _, model := range []string{"vippro/gpt-6-sol", "VIPPRO/GPT-6-SOL", "vippro/gpt-6-sol(high)", "vippro/gpt-6-luna"} {
		if !s.GroupAllows(g.ID, model) {
			t.Fatalf("%q should be allowed", model)
		}
	}
	for _, model := range []string{"vippro/gpt-6-astra", "gpt-6-sol", ""} {
		if s.GroupAllows(g.ID, model) {
			t.Fatalf("%q should be denied", model)
		}
	}
	if !s.GroupAllows("", "anything") || !s.GroupAllows(AllModelsGroupID, "anything") {
		t.Fatal("unassigned keys and the all-models group must allow every model")
	}
	if s.GroupAllows("missing-group", "vippro/gpt-6-sol") {
		t.Fatal("an unknown group must fail closed")
	}
}

func TestKeysCarryGroupAndGroupInUseBlocksDelete(t *testing.T) {
	s := testStore(t, groupsNow)
	g := mustGroup(t, s, "g1", "m1")
	key, _, err := s.Create(CreateInput{Label: "k", GroupID: g.ID})
	if err != nil || key.GroupID != g.ID {
		t.Fatalf("key=%+v err=%v", key, err)
	}
	if _, _, err = s.Create(CreateInput{Label: "bad", GroupID: "ghost"}); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("unknown group err=%v", err)
	}
	var inUse *GroupInUseError
	if err = s.DeleteGroup(g.ID); !errors.As(err, &inUse) || inUse.Count != 1 {
		t.Fatalf("delete err=%v", err)
	}
	other := mustGroup(t, s, "g2", "m2")
	moved, err := s.Update(UpdateInput{ID: key.ID, GroupID: other.ID, GroupSet: true})
	if err != nil || moved.GroupID != other.ID {
		t.Fatalf("moved=%+v err=%v", moved, err)
	}
	if _, err = s.Update(UpdateInput{ID: key.ID, GroupSet: true}); !errors.Is(err, ErrGroupRequired) {
		t.Fatalf("clearing the group must fail: %v", err)
	}
	if err = s.DeleteGroup(g.ID); err != nil {
		t.Fatalf("group should be deletable once empty: %v", err)
	}
	groups, _ := s.ListGroups()
	for _, item := range groups {
		if item.ID == other.ID && item.KeyCount != 1 {
			t.Fatalf("key_count=%d", item.KeyCount)
		}
	}
}

func TestCreateBatchAppliesGroup(t *testing.T) {
	s := testStore(t, groupsNow)
	g := mustGroup(t, s, "batch", "m")
	created, err := s.CreateBatch(CreateInput{Label: "b", GroupID: g.ID}, 3)
	if err != nil || len(created) != 3 {
		t.Fatalf("created=%d err=%v", len(created), err)
	}
	for _, item := range created {
		if item.Key.GroupID != g.ID {
			t.Fatalf("group=%q", item.Key.GroupID)
		}
	}
	if _, err = s.CreateBatch(CreateInput{Label: "x", GroupID: "ghost"}, 2); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("err=%v", err)
	}
	keys, total, _ := s.ListPage("", 1, 50)
	if total != 3 || len(keys) != 3 {
		t.Fatalf("a failed batch must not leave keys behind: total=%d", total)
	}
}

func makeKeys(t *testing.T, s *Store, n int, group string) []string {
	t.Helper()
	created, err := s.CreateBatch(CreateInput{Label: "k", GroupID: group}, n)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, n)
	for _, item := range created {
		ids = append(ids, item.Key.ID)
	}
	return ids
}

func TestBulkUpdateChangesOnlyRequestedFields(t *testing.T) {
	s := testStore(t, groupsNow)
	g1, g2 := mustGroup(t, s, "g1", "m1"), mustGroup(t, s, "g2", "m2")
	ids := makeKeys(t, s, 3, g1.ID)
	n, err := s.BulkUpdate(BulkUpdateInput{IDs: ids[:2], GroupID: g2.ID, GroupSet: true, Status: "disabled", StatusSet: true, TokenLimit: 777, TokenLimitSet: true, NeverExpires: true})
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	for i, id := range ids {
		key, err := s.keyByID(id)
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			if key.GroupID != g2.ID || key.Status != "disabled" || key.TokenLimit != 777 || key.ExpiresAt != nil {
				t.Fatalf("key %d not updated: %+v", i, key)
			}
			if key.Label == "" {
				t.Fatal("labels must be untouched")
			}
		} else if key.GroupID != g1.ID || key.Status != "active" || key.TokenLimit == 777 {
			t.Fatalf("untouched key changed: %+v", key)
		}
	}
}

func TestBulkUpdateIsAtomicAndValidated(t *testing.T) {
	s := testStore(t, groupsNow)
	g := mustGroup(t, s, "g", "m")
	other := mustGroup(t, s, "o", "m")
	ids := makeKeys(t, s, 2, g.ID)
	if _, err := s.BulkUpdate(BulkUpdateInput{IDs: append([]string{"ghost"}, ids...), GroupID: other.ID, GroupSet: true}); !errors.Is(err, ErrKeysNotFound) {
		t.Fatalf("err=%v", err)
	}
	for _, id := range ids {
		if key, _ := s.keyByID(id); key.GroupID != g.ID {
			t.Fatal("an unknown id must not change any key")
		}
	}
	cases := []BulkUpdateInput{
		{IDs: ids},
		{IDs: ids, Status: "weird", StatusSet: true},
		{IDs: ids, GroupSet: true},
		{IDs: ids, GroupID: "ghost", GroupSet: true},
		{IDs: nil, StatusSet: true, Status: "active"},
		{IDs: ids, TokenLimit: -1, TokenLimitSet: true},
	}
	for i, in := range cases {
		if _, err := s.BulkUpdate(in); err == nil {
			t.Fatalf("case %d should fail", i)
		}
	}
	if _, err := s.BulkUpdate(BulkUpdateInput{IDs: make([]string, 0), Status: "active", StatusSet: true}); err == nil {
		t.Fatal("empty id list must fail")
	}
	many := make([]string, MaxBulkKeys+1)
	for i := range many {
		many[i] = string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676))
	}
	if _, err := s.BulkUpdate(BulkUpdateInput{IDs: many, Status: "active", StatusSet: true}); err == nil {
		t.Fatal("over the cap must fail")
	}
}

func TestBulkDeleteIsAtomic(t *testing.T) {
	s := testStore(t, groupsNow)
	ids := makeKeys(t, s, 3, "")
	if _, err := s.BulkDelete([]string{ids[0], "ghost"}); !errors.Is(err, ErrKeysNotFound) {
		t.Fatalf("err=%v", err)
	}
	if _, total, _ := s.ListPage("", 1, 50); total != 3 {
		t.Fatalf("total=%d, a failed bulk delete must not delete", total)
	}
	n, err := s.BulkDelete([]string{ids[0], ids[1], ids[1]})
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, total, _ := s.ListPage("", 1, 50); total != 1 {
		t.Fatalf("total=%d", total)
	}
	if _, err = s.BulkDelete([]string{ids[0]}); !errors.Is(err, ErrKeysNotFound) {
		t.Fatalf("an already deleted key must be reported: %v", err)
	}
}

func finishRequest(t *testing.T, s *Store, keySecret, id, model string, note UsageNote, fin FinalizeInput) {
	t.Helper()
	adm, err := s.Admit(AdmitInput{Secret: keySecret, RequestID: id, Generate: true})
	if err != nil || !adm.Allowed {
		t.Fatalf("admit %s: %+v %v", id, adm, err)
	}
	note.RequestID = id
	if err = s.NoteUsage(note); err != nil {
		t.Fatal(err)
	}
	fin.RequestID, fin.Model = id, model
	if err = s.Finalize(fin); err != nil {
		t.Fatal(err)
	}
}

func TestFinalizeStoresTTFTAccountAndEffortFromUsageNote(t *testing.T) {
	s := testStore(t, groupsNow)
	g := mustGroup(t, s, "g", "m")
	key, secret, err := s.Create(CreateInput{Label: "buyer-key", Customer: "Acme", GroupID: g.ID})
	if err != nil {
		t.Fatal(err)
	}
	finishRequest(t, s, secret, "req-1", "vippro/gpt-6-astra",
		UsageNote{Input: 48, Output: 385, Reasoning: 1200, Cached: 36, TTFT: 2660 * time.Millisecond,
			Provider: "ch-relay-vippro", AuthID: "acct@example.com", AuthIndex: "ab12", AuthType: "codex", Effort: "max", ServiceTier: "priority"},
		FinalizeInput{RequestedModel: "gpt-6-astra", Transport: "http", Protocol: "openai", Outcome: "succeeded", HTTPStatus: 200, Latency: 35900 * time.Millisecond, RequestedAt: groupsNow})
	rows, page, summary, err := s.AdminRequests(AdminRequestQuery{})
	if err != nil || len(rows) != 1 || page.Total != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	r := rows[0]
	if r.TTFTMs != 2660 || r.LatencyMs != 35900 || r.AuthID != "acct@example.com" || r.Effort != "max" || r.Provider != "ch-relay-vippro" || r.ServiceTier != "priority" || r.RequestedModel != "gpt-6-astra" {
		t.Fatalf("row=%+v", r)
	}
	if r.KeyID != key.ID || r.KeyLabel != "buyer-key" || r.Customer != "Acme" || r.GroupName != "g" || r.KeyDisplay == "" {
		t.Fatalf("key identity missing: %+v", r)
	}
	if r.TokPerSec == nil || *r.TokPerSec < 11 || *r.TokPerSec > 12 {
		t.Fatalf("tok/s=%v want ~11.6", r.TokPerSec)
	}
	if summary.Total != 1 || summary.Succeeded != 1 || summary.Output != 385 || summary.Cached != 36 || summary.AvgTTFTMs != 2660 {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestTokPerSecIgnoresTinyTTFTGap(t *testing.T) {
	full := tokPerSec(20, 253, 252)
	if full == nil || *full < 79 || *full > 80 {
		t.Fatalf("non-stream tok/s=%v, want about 79", full)
	}
	stream := tokPerSec(20, 775, 570)
	if stream == nil || *stream < 97 || *stream > 98 {
		t.Fatalf("stream tok/s=%v, want about 97.6", stream)
	}
	if tokPerSec(0, 775, 570) != nil || tokPerSec(20, 0, 0) != nil {
		t.Fatal("invalid windows must be null")
	}
}

func TestFinalizeUsesUsageLatencyWhenCompletionOmitsIt(t *testing.T) {
	s := testStore(t, groupsNow)
	g := mustGroup(t, s, "g", "m")
	_, secret, err := s.Create(CreateInput{Label: "buyer-key", Customer: "Acme", GroupID: g.ID})
	if err != nil {
		t.Fatal(err)
	}
	finishRequest(t, s, secret, "req-lat", "vippro/gpt-6-sol",
		UsageNote{Input: 48, Output: 20, TTFT: 350 * time.Millisecond, Latency: 900 * time.Millisecond, Provider: "openai-compatible-fake", AuthID: "openai-compatibility:fake:abc"},
		FinalizeInput{RequestedModel: "gpt-6-sol", Transport: "http", Protocol: "openai", Outcome: "succeeded", HTTPStatus: 200, RequestedAt: groupsNow})
	rows, _, _, err := s.AdminRequests(AdminRequestQuery{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	if rows[0].LatencyMs != 900 || rows[0].TTFTMs != 350 || rows[0].TokPerSec == nil || *rows[0].TokPerSec < 36 || *rows[0].TokPerSec > 37 {
		t.Fatalf("row=%+v tok=%v", rows[0], rows[0].TokPerSec)
	}
}

func TestLateUsageFillsLatencyAndTokensAfterFinalize(t *testing.T) {
	s := testStore(t, groupsNow)
	g := mustGroup(t, s, "g", "m")
	key, secret, err := s.Create(CreateInput{Label: "buyer-key", Customer: "Acme", GroupID: g.ID})
	if err != nil {
		t.Fatal(err)
	}
	adm, err := s.Admit(AdmitInput{Secret: secret, RequestID: "late", Generate: true})
	if err != nil || !adm.Allowed {
		t.Fatalf("admit %+v %v", adm, err)
	}
	if err = s.Finalize(FinalizeInput{RequestID: "late", Model: "vippro/gpt-6-sol", RequestedModel: "gpt-6-sol", Transport: "http", Protocol: "openai", Outcome: "succeeded", HTTPStatus: 200, RequestedAt: groupsNow}); err != nil {
		t.Fatal(err)
	}
	if err = s.NoteUsage(UsageNote{RequestID: "late", Input: 48, Output: 20, Cached: 8, Reasoning: 4, TTFT: 260 * time.Millisecond, Latency: 780 * time.Millisecond, Provider: "openai-compatible-fake", AuthID: "openai-compatibility:fake:abc", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	rows, _, _, err := s.AdminRequests(AdminRequestQuery{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	r := rows[0]
	if r.LatencyMs != 780 || r.TTFTMs != 260 || r.Input != 48 || r.Output != 20 || r.Billed != 68 || r.Provider == "" || r.Effort != "high" || r.TokPerSec == nil {
		t.Fatalf("late row=%+v tok=%v", r, r.TokPerSec)
	}
	updated, err := s.keyByID(key.ID)
	if err != nil || updated.TokensUsed != 68 {
		t.Fatalf("tokens=%d err=%v", updated.TokensUsed, err)
	}
}

func TestRecordRejectionKeepsModelNames(t *testing.T) {
	s := testStore(t, groupsNow)
	g := mustGroup(t, s, "g", "vippro/gpt-6-sol")
	key, _, err := s.Create(CreateInput{Label: "buyer-key", Customer: "Acme", GroupID: g.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordRejection("denied-model", key.ID, "model_not_allowed", 403, "vippro/gpt-6-astra", "gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	rows, _, _, err := s.AdminRequests(AdminRequestQuery{})
	if err != nil || len(rows) != 1 || rows[0].Model != "vippro/gpt-6-astra" || rows[0].RequestedModel != "gpt-6-astra" || rows[0].Status != "rejected" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

func TestAdminRequestsFiltersAndCursor(t *testing.T) {
	now := groupsNow
	s := testStore(t, now)
	s.cfg.Now = func() time.Time { return now }
	ga, gb := mustGroup(t, s, "ga", "m"), mustGroup(t, s, "gb", "m")
	_, secA, _ := s.Create(CreateInput{Label: "alpha", Customer: "Acme", GroupID: ga.ID})
	_, secB, _ := s.Create(CreateInput{Label: "beta", Customer: "Bravo", GroupID: gb.ID})
	for i, c := range []struct {
		secret, id, model, effort, outcome string
	}{
		{secA, "r1", "m-one", "max", "succeeded"},
		{secA, "r2", "m-two", "", "failed"},
		{secB, "r3", "m-one", "high", "succeeded"},
		{secB, "r4", "m-three", "", "succeeded"},
	} {
		finishRequest(t, s, c.secret, c.id, c.model, UsageNote{Input: 10, Output: 20, Effort: c.effort, AuthID: "acct-" + c.id},
			FinalizeInput{Outcome: c.outcome, HTTPStatus: 200, Latency: time.Second, RequestedAt: now.Add(time.Duration(i) * time.Second)})
	}
	count := func(q AdminRequestQuery) int {
		_, page, _, err := s.AdminRequests(q)
		if err != nil {
			t.Fatal(err)
		}
		return page.Total
	}
	checks := map[string]struct {
		q    AdminRequestQuery
		want int
	}{
		"all":      {AdminRequestQuery{}, 4},
		"customer": {AdminRequestQuery{Customer: "acme"}, 2},
		"group":    {AdminRequestQuery{GroupID: gb.ID}, 2},
		"model":    {AdminRequestQuery{Model: "m-one"}, 2},
		"status":   {AdminRequestQuery{Status: "failed"}, 1},
		"effort":   {AdminRequestQuery{Effort: "max"}, 1},
		"account":  {AdminRequestQuery{Account: "r3"}, 1},
		"search":   {AdminRequestQuery{Search: "beta"}, 2},
		"time":     {AdminRequestQuery{From: now.Add(2 * time.Second)}, 2},
		"combined": {AdminRequestQuery{Customer: "Bravo", Model: "m-one"}, 1},
	}
	for name, c := range checks {
		if got := count(c.q); got != c.want {
			t.Errorf("%s: got %d want %d", name, got, c.want)
		}
	}
	first, page, summary, err := s.AdminRequests(AdminRequestQuery{Limit: 3})
	if err != nil || len(first) != 3 || !page.HasMore || page.Total != 4 || summary.Total != 4 {
		t.Fatalf("first=%d page=%+v err=%v", len(first), page, err)
	}
	if first[0].RequestID != "r4" {
		t.Fatalf("newest first expected, got %s", first[0].RequestID)
	}
	rest, page, _, err := s.AdminRequests(AdminRequestQuery{Limit: 3, Cursor: page.NextCursor})
	if err != nil || len(rest) != 1 || rest[0].RequestID != "r1" || page.HasMore {
		t.Fatalf("rest=%+v err=%v", rest, err)
	}
}

func TestRejectedRequestShowsKeyAndCustomer(t *testing.T) {
	s := testStore(t, groupsNow)
	g := mustGroup(t, s, "g", "m")
	key, secret, _ := s.Create(CreateInput{Label: "k", Customer: "Zed", GroupID: g.ID})
	if err := s.RejectSecret(secret, "rej-1", "model_not_allowed", 403); err != nil {
		t.Fatal(err)
	}
	rows, _, summary, err := s.AdminRequests(AdminRequestQuery{Status: "rejected"})
	if err != nil || len(rows) != 1 || rows[0].KeyID != key.ID || rows[0].Customer != "Zed" || rows[0].RejectionCode != "model_not_allowed" || summary.Rejected != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if rows[0].TokPerSec != nil {
		t.Fatal("rejected rows have no tok/s")
	}
}

func TestUsageStatsAggregatesSeriesTopsAndLatency(t *testing.T) {
	now := groupsNow
	s := testStore(t, now)
	_, sec, _ := s.Create(CreateInput{Label: "k1", Customer: "Acme"})
	_, sec2, _ := s.Create(CreateInput{Label: "k2", Customer: "Bravo"})
	rows := []struct {
		secret, id, model string
		ttft, lat         time.Duration
		at                time.Duration
		outcome           string
	}{
		{sec, "a", "m1", 100 * time.Millisecond, 1000 * time.Millisecond, -30 * time.Minute, "succeeded"},
		{sec, "b", "m1", 200 * time.Millisecond, 2000 * time.Millisecond, -90 * time.Minute, "succeeded"},
		{sec2, "c", "m2", 300 * time.Millisecond, 3000 * time.Millisecond, -150 * time.Minute, "succeeded"},
		{sec2, "d", "m2", 0, 4000 * time.Millisecond, -10 * time.Minute, "failed"},
	}
	for _, r := range rows {
		finishRequest(t, s, r.secret, r.id, r.model, UsageNote{Input: 100, Output: 10, Cached: 50, TTFT: r.ttft, AuthID: "acct", Provider: "p"},
			FinalizeInput{Outcome: r.outcome, HTTPStatus: 200, Latency: r.lat, RequestedAt: now.Add(r.at)})
	}
	st, err := s.UsageStats(now.Add(-6*time.Hour), now.Add(time.Minute), "hour")
	if err != nil {
		t.Fatal(err)
	}
	if st.Summary.Total != 4 || st.Summary.Failed != 1 || len(st.Series) != 3 {
		t.Fatalf("summary=%+v series=%d", st.Summary, len(st.Series))
	}
	if st.CacheHitRatio != 0.5 {
		t.Fatalf("cache ratio=%v", st.CacheHitRatio)
	}
	if len(st.TopModels) != 2 || st.TopModels[0].Requests != 2 {
		t.Fatalf("top models=%+v", st.TopModels)
	}
	if len(st.TopKeys) != 2 || st.TopKeys[0].Label == "" || st.TopKeys[0].Detail == "" {
		t.Fatalf("top keys=%+v", st.TopKeys)
	}
	if len(st.TopCustomers) != 2 || len(st.TopAccounts) != 1 || st.TopAccounts[0].Requests != 4 {
		t.Fatalf("customers=%+v accounts=%+v", st.TopCustomers, st.TopAccounts)
	}
	if st.Latency.LatencyP50 != 2000 || st.Latency.LatencyP95 != 3000 || st.Latency.TTFTP50 != 200 {
		t.Fatalf("latency=%+v", st.Latency)
	}
	if st.Series[0].Start >= st.Series[len(st.Series)-1].Start {
		t.Fatalf("series must be ascending: %+v", st.Series)
	}
	day, err := s.UsageStats(now.Add(-6*time.Hour), now.Add(time.Minute), "day")
	if err != nil || len(day.Series) != 1 || day.Series[0].Requests != 4 || day.Series[0].Start != "2026-10-19T00:00:00Z" {
		t.Fatalf("day=%+v err=%v", day.Series, err)
	}
	empty, err := s.UsageStats(now.AddDate(0, 0, -400), now.AddDate(0, 0, -399), "day")
	if err != nil || empty.Summary.Total != 0 || len(empty.Series) != 0 || len(empty.TopModels) != 0 {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
}

// A database written before groups and log fields existed must open, keep its
// keys unassigned (full access), and gain the new columns.
func TestLegacyDatabaseMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE keys (id TEXT PRIMARY KEY, secret_hash BLOB NOT NULL UNIQUE, display_prefix TEXT NOT NULL, display_last4 TEXT NOT NULL, label TEXT NOT NULL DEFAULT '', customer TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, created_at TEXT NOT NULL, expires_at TEXT, token_limit INTEGER NOT NULL, tokens_used INTEGER NOT NULL DEFAULT 0, request_count INTEGER NOT NULL DEFAULT 0, last_used_at TEXT, deleted_at TEXT);
CREATE TABLE requests (request_id TEXT PRIMARY KEY, key_id TEXT NOT NULL, model TEXT NOT NULL DEFAULT '', transport TEXT NOT NULL DEFAULT '', protocol TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, http_status INTEGER NOT NULL DEFAULT 0, latency_ms INTEGER NOT NULL DEFAULT 0, ttft_ms INTEGER NOT NULL DEFAULT 0, input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0, reasoning_tokens INTEGER NOT NULL DEFAULT 0, cached_tokens INTEGER NOT NULL DEFAULT 0, billed_tokens INTEGER NOT NULL DEFAULT 0, requested_at TEXT NOT NULL, completed_at TEXT NOT NULL);
CREATE TABLE usage_notes (request_id TEXT PRIMARY KEY, input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0, reasoning_tokens INTEGER NOT NULL DEFAULT 0, cached_tokens INTEGER NOT NULL DEFAULT 0, failed INTEGER NOT NULL DEFAULT 0);
CREATE TABLE reservations (request_id TEXT PRIMARY KEY, key_id TEXT NOT NULL, reserved_tokens INTEGER NOT NULL, created_at TEXT NOT NULL);
INSERT INTO keys (id, secret_hash, display_prefix, display_last4, label, customer, status, created_at, token_limit) VALUES ('legacy1', x'01', 'ck_ab', 'wxyz', 'old', 'Old Co', 'active', '2026-10-01T00:00:00Z', 100);
INSERT INTO requests (request_id, key_id, model, status, requested_at, completed_at) VALUES ('old-req', 'legacy1', 'm', 'succeeded', '2026-10-02T00:00:00Z', '2026-10-02T00:00:01Z');`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	s, err := Open(Config{DataFile: path, Now: func() time.Time { return groupsNow }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key, err := s.keyByID("legacy1")
	if err != nil || key.GroupID != "" {
		t.Fatalf("key=%+v err=%v", key, err)
	}
	if !s.GroupAllows(key.GroupID, "any-model") {
		t.Fatal("a legacy key without a group must keep full access")
	}
	rows, _, _, err := s.AdminRequests(AdminRequestQuery{})
	if err != nil || len(rows) != 1 || rows[0].Customer != "Old Co" || rows[0].Effort != "" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if groups, _ := s.ListGroups(); len(groups) != 1 || groups[0].ID != AllModelsGroupID {
		t.Fatalf("groups=%+v", groups)
	}
	if err = s.seedSystemGroup(); err != nil {
		t.Fatalf("migration must be idempotent: %v", err)
	}
}

func TestAliasesJSONMigratesOntoExistingGroups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "groups.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE model_groups (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  description TEXT NOT NULL DEFAULT '',
  all_models INTEGER NOT NULL DEFAULT 0,
  models_json TEXT NOT NULL DEFAULT '[]',
  is_system INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
INSERT INTO model_groups (id, name, description, all_models, models_json, is_system, created_at, updated_at)
VALUES ('g-old', 'legacy', '', 0, '["vippro/gpt-6-sol"]', 0, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	s, err := Open(Config{DataFile: path, Now: func() time.Time { return groupsNow }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err = s.db.QueryRow(`SELECT COUNT(1) FROM pragma_table_info('model_groups') WHERE name='aliases_json'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("aliases_json missing: n=%d err=%v", n, err)
	}
	g, err := s.Group("g-old")
	if err != nil || len(g.Models) != 1 || g.Models[0].Name != "vippro/gpt-6-sol" || g.Models[0].Alias != "" {
		t.Fatalf("migrated group=%+v err=%v", g, err)
	}
	var raw string
	if err = s.db.QueryRow(`SELECT models_json FROM model_groups WHERE id='g-old'`).Scan(&raw); err != nil || raw != `["vippro/gpt-6-sol"]` {
		t.Fatalf("models_json must stay a name array: %s err=%v", raw, err)
	}
}

func TestGroupAliasValidationAndAllows(t *testing.T) {
	s := testStore(t, groupsNow)
	g, err := s.CreateGroup(GroupInput{Name: "aliased", Models: []GroupModel{
		{Name: "vippro/gpt-6-sol", Alias: "gpt-6-sol"},
		{Name: "vippro/plain"},
	}})
	if err != nil || g.Models[0].Alias != "gpt-6-sol" || g.Models[1].Alias != "" {
		t.Fatalf("created=%+v err=%v", g, err)
	}
	for _, model := range []string{"gpt-6-sol", "GPT-6-SOL", "gpt-6-sol(high)", "vippro/gpt-6-sol", "vippro/plain", "vippro/plain(low)"} {
		if !g.Allows(model) {
			t.Fatalf("%q should be allowed by alias or name", model)
		}
	}
	if g.Allows("vippro/other") {
		t.Fatal("unknown model should be denied")
	}
	if _, err = s.CreateGroup(GroupInput{Name: "clash-name", Models: []GroupModel{{Name: "vippro/a", Alias: "vippro/b"}, {Name: "vippro/b"}}}); !errors.Is(err, ErrGroupInvalid) {
		t.Fatalf("alias matching another name err=%v", err)
	}
	if _, err = s.CreateGroup(GroupInput{Name: "clash-alias", Models: []GroupModel{{Name: "vippro/a", Alias: "shared"}, {Name: "vippro/b", Alias: "Shared"}}}); !errors.Is(err, ErrGroupInvalid) {
		t.Fatalf("duplicate alias err=%v", err)
	}
	cleared, err := s.CreateGroup(GroupInput{Name: "same", Models: []GroupModel{{Name: "vippro/a", Alias: "vippro/a"}}})
	if err != nil || cleared.Models[0].Alias != "" {
		t.Fatalf("alias equal to its own name should be stored empty: %+v err=%v", cleared, err)
	}
}

func TestStripPrefixAliasesIsIdempotentAndValid(t *testing.T) {
	// Names without the prefix are left unaliased. Applying the helper twice to
	// the same names yields the same alias map. Applying it to the already
	// stripped client-visible names is a fixed point: every alias stays empty.
	names := []string{"vippro/gpt-6-sol", "VIPPRO/gpt-6-luna", "gpt-5-codex", " vippro/gpt-6-sol ", "vippro/gpt-5-codex"}
	first := StripPrefixAliases(names, "vippro/")
	second := StripPrefixAliases(names, "vippro/")
	if !reflect.DeepEqual(aliasMapOf(first), aliasMapOf(second)) {
		t.Fatalf("applying twice did not yield the same alias map: %#v vs %#v", first, second)
	}
	got := aliasMapOf(first)
	want := map[string]string{
		"vippro/gpt-6-sol":   "gpt-6-sol",
		"VIPPRO/gpt-6-luna":  "gpt-6-luna",
		"gpt-5-codex":        "",
		"vippro/gpt-5-codex": "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("alias map=%v", got)
	}
	for _, model := range first {
		if !strings.HasPrefix(strings.ToLower(model.Name), "vippro/") && model.Alias != "" {
			t.Fatalf("unprefixed name should be left unaliased: %+v", model)
		}
	}
	visible := make([]string, 0, len(first))
	for _, model := range first {
		if model.Alias != "" {
			visible = append(visible, model.Alias)
		} else {
			visible = append(visible, model.Name)
		}
	}
	again := StripPrefixAliases(visible, "vippro/")
	for _, model := range again {
		if model.Alias != "" {
			t.Fatalf("second application should leave stripped names unaliased: %+v", model)
		}
	}
	if _, err := normalizeGroupInput(GroupInput{Name: "vippro", Models: first}); err != nil {
		t.Fatalf("prefix aliases must pass group validation: %v", err)
	}
}

func aliasMapOf(models []GroupModel) map[string]string {
	out := make(map[string]string, len(models))
	for _, model := range models {
		out[model.Name] = model.Alias
	}
	return out
}

func TestGroupResolveKeepsSuffixAndRequiresVisibleName(t *testing.T) {
	g := Group{Models: []GroupModel{
		{Name: "vippro/gpt-6-sol", Alias: "gpt-6-sol"},
		{Name: "vippro/gpt-6-luna"},
	}}
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"gpt-6-sol", "vippro/gpt-6-sol", true},
		{"GPT-6-SOL", "vippro/gpt-6-sol", true},
		{"gpt-6-sol(high)", "vippro/gpt-6-sol(high)", true},
		{"vippro/gpt-6-sol", "", false}, // aliased entry hides its upstream name
		{"vippro/gpt-6-luna", "vippro/gpt-6-luna", true},
		{"vippro/gpt-6-luna(low)", "vippro/gpt-6-luna(low)", true},
		{"", "", false},
		{"(high)", "", false},
		{"other", "", false},
	}
	for _, tc := range cases {
		got, ok := g.Resolve(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("Resolve(%q) = %q,%v want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	// Allows also admits the upstream name, because the host may have rewritten the alias already.
	if !g.Allows("vippro/gpt-6-sol(high)") || !g.Allows("gpt-6-sol") || g.Allows("other") {
		t.Fatal("Allows mismatch")
	}
	if got, ok := (Group{AllModels: true}).Resolve("anything(x)"); !ok || got != "anything(x)" {
		t.Fatalf("all-models Resolve = %q,%v", got, ok)
	}
}

func TestGroupPolicyOmitsKeyCountAndFailsClosed(t *testing.T) {
	s := testStore(t, groupsNow)
	g := mustGroup(t, s, "pol", "vippro/a")
	if _, _, err := s.Create(CreateInput{Label: "k", GroupID: g.ID}); err != nil {
		t.Fatal(err)
	}
	full, err := s.Group(g.ID)
	if err != nil || full.KeyCount != 1 {
		t.Fatalf("Group key count = %+v err=%v", full, err)
	}
	policy, err := s.GroupPolicy(g.ID)
	if err != nil || policy.KeyCount != 0 || !policy.Allows("vippro/a") {
		t.Fatalf("GroupPolicy = %+v err=%v", policy, err)
	}
	if _, err = s.GroupPolicy("missing"); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("missing group err=%v", err)
	}
}

func TestGroupAliasRejectsParentheses(t *testing.T) {
	s := testStore(t, groupsNow)
	_, err := s.CreateGroup(GroupInput{Name: "bad", Models: []GroupModel{{Name: "vippro/a", Alias: "a(high)"}}})
	if !errors.Is(err, ErrGroupInvalid) || !strings.Contains(err.Error(), "parentheses") {
		t.Fatalf("err=%v", err)
	}
}
