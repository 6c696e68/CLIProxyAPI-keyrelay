package keyshop

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T, now time.Time) *Store {
	t.Helper()
	store, err := Open(Config{
		DataFile:       filepath.Join(t.TempDir(), "key-shop.db"),
		DefaultTTL:     168 * time.Hour,
		DefaultLimit:   100_000_000,
		ReserveTokens:  32_768,
		ReservationTTL: 15 * time.Minute,
		RetentionDays:  30,
		Now:            func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStatsSummary(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	if _, _, err := store.Create(CreateInput{Label: "a", TTLSeconds: 3600, TokenLimit: 100, TokenLimitSet: true}); err != nil {
		t.Fatal(err)
	}
	disabled, _, err := store.Create(CreateInput{Label: "b", TTLSeconds: 3600, TokenLimit: 50, TokenLimitSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(UpdateInput{ID: disabled.ID, Status: "disabled", StatusSet: true}); err != nil {
		t.Fatal(err)
	}
	summary, err := store.StatsSummary()
	if err != nil {
		t.Fatal(err)
	}
	if summary.KeysTotal != 2 || summary.KeysActive != 1 || summary.KeysDisabled != 1 {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestStoreCreateHidesSecretOnList(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	key, secret, err := store.Create(CreateInput{Label: "buyer"})
	if err != nil || !SecretMatches(secret, mustHash(t, store, key.ID)) {
		t.Fatal(err)
	}
	listed, err := store.List("")
	if err != nil || len(listed) != 1 {
		t.Fatalf("list err=%v len=%d", err, len(listed))
	}
	if listed[0].Display == "" || listed[0].ID != key.ID {
		t.Fatal("list missing display")
	}
	if key.Label == "" || listed[0].Label != key.Label {
		t.Fatalf("default label was not persisted: created=%q listed=%q", key.Label, listed[0].Label)
	}
}

func TestExplicitLabelAndIDRemainStableAcrossUpdatesAndRotation(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	key, secret, err := store.Create(CreateInput{Label: "random-label-7f4a"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.Update(UpdateInput{ID: key.ID, Status: "disabled", StatusSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != key.ID || updated.Label != "random-label-7f4a" {
		t.Fatalf("update changed identity or label: before=%+v after=%+v", key, updated)
	}
	renamed, err := store.Update(UpdateInput{
		ID: key.ID, Label: "renamed-random-string", LabelSet: true,
		Status: "active", StatusSet: true, TTLSeconds: 7200, TTLSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ID != key.ID {
		t.Fatalf("update rewrote key id: before=%s after=%s", key.ID, renamed.ID)
	}
	if renamed.Label != "renamed-random-string" || renamed.ExpiresAt == nil || renamed.Status == "disabled" {
		t.Fatalf("label, status, or expiry was not applied without touching id: %+v", renamed)
	}
	rotated, rotatedSecret, err := store.Rotate(renamed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.ID != renamed.ID || rotated.Label != renamed.Label || rotatedSecret == secret {
		t.Fatalf("rotation changed identity/label or kept secret: before=%+v after=%+v", renamed, rotated)
	}
}

func TestListTreatsOpenReservationAsExhausted(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	_, secret, err := store.Create(CreateInput{Label: "held", TTLSeconds: 3600, TokenLimit: 10, TokenLimitSet: true})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := store.Admit(AdmitInput{Secret: secret, RequestID: "hold", Path: "/v1/chat/completions", Generate: true, MaxTokens: 10})
	if err != nil || !admission.Allowed || admission.Reserved < 1 {
		t.Fatalf("admission=%+v err=%v", admission, err)
	}
	exhausted, total, err := store.ListPageFiltered("", "exhausted", 1, 25)
	if err != nil || total != 1 || len(exhausted) != 1 || exhausted[0].Status != "exhausted" {
		t.Fatalf("exhausted=%+v total=%d err=%v", exhausted, total, err)
	}
	active, activeTotal, err := store.ListPageFiltered("", "active", 1, 25)
	if err != nil || activeTotal != 0 || len(active) != 0 {
		t.Fatalf("active=%+v total=%d err=%v", active, activeTotal, err)
	}
}

func TestListPageDoesNotDeadlockWithOneConnection(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	if _, _, err := store.Create(CreateInput{Label: "live", TokenLimit: 10, TokenLimitSet: true}); err != nil {
		t.Fatal(err)
	}
	type listResult struct {
		count int
		total int
		err   error
	}
	done := make(chan listResult, 1)
	go func() {
		keys, total, err := store.ListPageFiltered("", "", 1, 25)
		done <- listResult{count: len(keys), total: total, err: err}
	}()
	select {
	case result := <-done:
		if result.err != nil || result.total != 1 || result.count != 1 {
			t.Fatalf("keys=%d total=%d err=%v", result.count, result.total, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("list deadlocked while the only sqlite connection was busy")
	}
}

func TestListPageFiltersAndBoundsResults(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	for i := 0; i < 3; i++ {
		if _, _, err := store.Create(CreateInput{Label: "page", TokenLimit: 100, TokenLimitSet: true}); err != nil {
			t.Fatal(err)
		}
	}
	keys, total, err := store.ListPageFiltered("page", "active", 2, 2)
	if err != nil || total != 3 || len(keys) != 1 {
		t.Fatalf("keys=%d total=%d err=%v", len(keys), total, err)
	}
}

func TestRequestsPageUsesStableCursor(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	key, secret, err := store.Create(CreateInput{TokenLimit: 100, TokenLimitSet: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, err := store.Admit(AdmitInput{Secret: secret, RequestID: id, Generate: false}); err != nil {
			t.Fatal(err)
		}
		if err := store.Finalize(FinalizeInput{RequestID: id, Outcome: "succeeded", RequestedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	first, page, err := store.RequestsPage(RequestQuery{KeyID: key.ID, Limit: 2})
	if err != nil || len(first) != 2 || !page.HasMore || page.NextCursor == "" || page.Total != 3 {
		t.Fatalf("first=%d page=%+v err=%v", len(first), page, err)
	}
	second, page, err := store.RequestsPage(RequestQuery{KeyID: key.ID, Limit: 2, Cursor: page.NextCursor})
	if err != nil || len(second) != 1 || page.HasMore || page.Total != 3 || second[0].RequestID == first[0].RequestID || second[0].RequestID == first[1].RequestID {
		t.Fatalf("second=%+v page=%+v err=%v", second, page, err)
	}
}

func TestAdmitDeniesExpiredAndExhausted(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	expired, secret, err := store.Create(CreateInput{TTLSeconds: -60, TokenLimit: 10, TokenLimitSet: true})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := store.Admit(AdmitInput{Secret: secret, RequestID: "exp", Path: "/v1/chat/completions", Generate: true})
	if err != nil || admission.Allowed || admission.Code != "key_expired" {
		t.Fatalf("expired admission=%+v err=%v", admission, err)
	}
	_ = expired
	live, secret, err := store.Create(CreateInput{TTLSeconds: 3600, TokenLimit: 1, TokenLimitSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Admit(AdmitInput{Secret: secret, RequestID: "seed", Path: "/v1/chat/completions", Generate: true, MaxTokens: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(FinalizeInput{RequestID: "seed", Outcome: "succeeded", Input: 1, RequestedAt: now}); err != nil {
		t.Fatal(err)
	}
	admission, err = store.Admit(AdmitInput{Secret: secret, RequestID: "over", Path: "/v1/messages/count_tokens", Generate: false})
	if err != nil || admission.Allowed || admission.Code != "token_limit_exceeded" {
		t.Fatalf("exhausted admission=%+v err=%v key=%s", admission, err, live.ID)
	}
}

func TestConcurrentAdmitCannotOverspend(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	_, secret, err := store.Create(CreateInput{TTLSeconds: 3600, TokenLimit: 1, TokenLimitSet: true})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		id := "req-" + string(rune('a'+i))
		go func() {
			defer wg.Done()
			admission, err := store.Admit(AdmitInput{Secret: secret, RequestID: id, Path: "/v1/chat/completions", Generate: true, MaxTokens: 1})
			if err != nil {
				t.Error(err)
				return
			}
			if admission.Allowed {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 1 {
		t.Fatalf("allowed = %d, want 1", allowed)
	}
}

func TestFinalizeBillsOnceAndFailureBillsZero(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	_, secret, err := store.Create(CreateInput{TTLSeconds: 3600, TokenLimit: 100, TokenLimitSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Admit(AdmitInput{Secret: secret, RequestID: "ok", Path: "/v1/chat/completions", Generate: true, MaxTokens: 10}); err != nil {
		t.Fatal(err)
	}
	if err := store.NoteUsage(UsageNote{RequestID: "ok", Input: 2, Output: 3}); err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(FinalizeInput{RequestID: "ok", Outcome: "succeeded", RequestedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(FinalizeInput{RequestID: "ok", Outcome: "succeeded", RequestedAt: now}); err != nil {
		t.Fatal(err)
	}
	session, err := store.Session(secret, 10)
	if err != nil || session.Key.TokensUsed != 5 {
		t.Fatalf("used=%d err=%v", session.Key.TokensUsed, err)
	}
	var notes int
	if err := store.db.QueryRow(`SELECT COUNT(1) FROM usage_notes WHERE request_id=?`, "ok").Scan(&notes); err != nil || notes != 0 {
		t.Fatalf("notes=%d err=%v", notes, err)
	}
	if _, err := store.Admit(AdmitInput{Secret: secret, RequestID: "bad", Path: "/v1/chat/completions", Generate: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.NoteUsage(UsageNote{RequestID: "bad", Input: 9, Output: 9, Failed: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(FinalizeInput{RequestID: "bad", Outcome: "failed", RequestedAt: now}); err != nil {
		t.Fatal(err)
	}
	session, err = store.Session(secret, 10)
	if err != nil || session.Key.TokensUsed != 5 {
		t.Fatalf("used after failure=%d err=%v", session.Key.TokensUsed, err)
	}
}

func TestFinalizeBillsUpstreamTotal(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	key, secret, err := store.Create(CreateInput{TTLSeconds: 3600, TokenLimit: 10_000, TokenLimitSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Admit(AdmitInput{Secret: secret, RequestID: "claude", Path: "/v1/messages", Generate: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.NoteUsage(UsageNote{RequestID: "claude", Input: 10, Output: 50, Cached: 1000, Total: 1060}); err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(FinalizeInput{RequestID: "claude", Outcome: "succeeded", RequestedAt: now}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Requests(RequestQuery{KeyID: key.ID})
	if err != nil || len(rows) != 1 || rows[0].Billed != 1060 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	updated, err := store.keyByID(key.ID)
	if err != nil || updated.TokensUsed != 1060 {
		t.Fatalf("tokens=%d err=%v", updated.TokensUsed, err)
	}
	// A note arriving after finalize must not bill the total a second time.
	if err = store.NoteUsage(UsageNote{RequestID: "claude", Input: 10, Output: 50, Cached: 1000, Total: 1060}); err != nil {
		t.Fatal(err)
	}
	if updated, err = store.keyByID(key.ID); err != nil || updated.TokensUsed != 1060 {
		t.Fatalf("tokens after late note=%d err=%v", updated.TokensUsed, err)
	}
}

func TestLateUsageBillsUpstreamTotalOnce(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	key, secret, err := store.Create(CreateInput{TTLSeconds: 3600, TokenLimit: 10_000, TokenLimitSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Admit(AdmitInput{Secret: secret, RequestID: "late", Path: "/v1/messages", Generate: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(FinalizeInput{RequestID: "late", Outcome: "succeeded", RequestedAt: now}); err != nil {
		t.Fatal(err)
	}
	note := UsageNote{RequestID: "late", Input: 10, Output: 50, Cached: 1000, Total: 1060}
	if err := store.NoteUsage(note); err != nil {
		t.Fatal(err)
	}
	if err := store.NoteUsage(note); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Requests(RequestQuery{KeyID: key.ID})
	if err != nil || len(rows) != 1 || rows[0].Billed != 1060 || rows[0].Input != 10 || rows[0].Output != 50 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if updated, err := store.keyByID(key.ID); err != nil || updated.TokensUsed != 1060 {
		t.Fatalf("tokens=%d err=%v", updated.TokensUsed, err)
	}
}

// TestLateUsageBillsTotalWithoutInputOutput covers a note that only carries an
// upstream total, so the request row stays empty but the total still bills.
func TestLateUsageBillsTotalWithoutInputOutput(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	key, secret, err := store.Create(CreateInput{TTLSeconds: 3600, TokenLimit: 10_000, TokenLimitSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Admit(AdmitInput{Secret: secret, RequestID: "total-only", Path: "/v1/messages", Generate: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(FinalizeInput{RequestID: "total-only", Outcome: "succeeded", RequestedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.NoteUsage(UsageNote{RequestID: "total-only", Total: 42}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Requests(RequestQuery{KeyID: key.ID})
	if err != nil || len(rows) != 1 || rows[0].Billed != 42 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if updated, err := store.keyByID(key.ID); err != nil || updated.TokensUsed != 42 {
		t.Fatalf("tokens=%d err=%v", updated.TokensUsed, err)
	}
}

func TestSweepDropsOldReservationWithoutBilling(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	_, secret, err := store.Create(CreateInput{TTLSeconds: 3600, TokenLimit: 100, TokenLimitSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Admit(AdmitInput{Secret: secret, RequestID: "old", Path: "/v1/chat/completions", Generate: true}); err != nil {
		t.Fatal(err)
	}
	store.cfg.Now = func() time.Time { return now.Add(16 * time.Minute) }
	if err := store.Sweep(); err != nil {
		t.Fatal(err)
	}
	session, err := store.Session(secret, 10)
	if err != nil || session.Key.TokensUsed != 0 {
		t.Fatalf("used=%d err=%v", session.Key.TokensUsed, err)
	}
	admission, err := store.Admit(AdmitInput{Secret: secret, RequestID: "again", Path: "/v1/chat/completions", Generate: true, MaxTokens: 100})
	if err != nil || !admission.Allowed {
		t.Fatalf("readmit=%+v err=%v", admission, err)
	}
}

func TestSweepKeepsPendingUsageNoteUntilFinalize(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	key, secret, err := store.Create(CreateInput{TTLSeconds: 3600, UnlimitedTokens: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Admit(AdmitInput{Secret: secret, RequestID: "live", Path: "/v1/chat/completions", Generate: true}); err != nil {
		t.Fatal(err)
	}
	note := UsageNote{
		RequestID: "live", Input: 48, Output: 20, Reasoning: 4, Cached: 8,
		TTFT: 570 * time.Millisecond, Latency: 775 * time.Millisecond,
		Provider: "openai-compatible-fake", AuthID: "openai-compatibility:fake-vippro:c64418315987",
		AuthIndex: "0", AuthType: "apikey", Effort: "high", ServiceTier: "priority",
	}
	if err := store.NoteUsage(note); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Admit(AdmitInput{Secret: secret, RequestID: "done", Path: "/v1/chat/completions", Generate: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.NoteUsage(UsageNote{RequestID: "done", Input: 1, Output: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(FinalizeInput{RequestID: "done", Outcome: "succeeded", RequestedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO usage_notes (request_id, input_tokens, output_tokens) VALUES ('done', 9, 9)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO usage_notes (request_id, input_tokens, output_tokens) VALUES ('orphan', 3, 3)`); err != nil {
		t.Fatal(err)
	}
	if err := store.Sweep(); err != nil {
		t.Fatal(err)
	}
	var pendingNotes, terminalNotes, orphanNotes int
	if err := store.db.QueryRow(`SELECT COUNT(1) FROM usage_notes WHERE request_id='live'`).Scan(&pendingNotes); err != nil || pendingNotes != 1 {
		t.Fatalf("pending notes=%d err=%v", pendingNotes, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(1) FROM usage_notes WHERE request_id='done'`).Scan(&terminalNotes); err != nil || terminalNotes != 0 {
		t.Fatalf("terminal notes=%d err=%v", terminalNotes, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(1) FROM usage_notes WHERE request_id='orphan'`).Scan(&orphanNotes); err != nil || orphanNotes != 0 {
		t.Fatalf("orphan notes=%d err=%v", orphanNotes, err)
	}
	if err := store.Finalize(FinalizeInput{RequestID: "live", Outcome: "succeeded", RequestedAt: now}); err != nil {
		t.Fatal(err)
	}
	session, err := store.Session(secret, 10)
	if err != nil || session.Key.TokensUsed != 70 {
		t.Fatalf("used=%d err=%v", session.Key.TokensUsed, err)
	}
	rows, _, _, err := store.AdminRequests(AdminRequestQuery{KeyID: key.ID, Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	var live AdminRequest
	for _, row := range rows {
		if row.RequestID == "live" {
			live = row
		}
	}
	if live.Status != "succeeded" || live.Input != 48 || live.Output != 20 || live.TTFTMs != 570 || live.LatencyMs != 775 || live.AuthID != note.AuthID || live.Effort != "high" {
		t.Fatalf("live row=%+v", live)
	}
	var doneInput, doneOutput int64
	if err := store.db.QueryRow(`SELECT input_tokens, output_tokens FROM requests WHERE request_id='done'`).Scan(&doneInput, &doneOutput); err != nil || doneInput != 1 || doneOutput != 1 {
		t.Fatalf("terminal row tokens=%d/%d err=%v", doneInput, doneOutput, err)
	}
}

func TestSweepCancelCopiesUsageNoteBeforeDelete(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	key, secret, err := store.Create(CreateInput{TTLSeconds: 3600, TokenLimit: 100, TokenLimitSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Admit(AdmitInput{Secret: secret, RequestID: "expire", Path: "/v1/chat/completions", Generate: true}); err != nil {
		t.Fatal(err)
	}
	note := UsageNote{
		RequestID: "expire", Input: 48, Output: 20, Reasoning: 4, Cached: 8,
		TTFT: 260 * time.Millisecond, Latency: 900 * time.Millisecond,
		Provider: "openai-compatible-fake", AuthID: "openai-compatibility:fake-vippro:c64418315987",
		AuthIndex: "1", AuthType: "apikey", Effort: "max", ServiceTier: "priority",
	}
	if err := store.NoteUsage(note); err != nil {
		t.Fatal(err)
	}
	store.cfg.Now = func() time.Time { return now.Add(16 * time.Minute) }
	if err := store.Sweep(); err != nil {
		t.Fatal(err)
	}
	var notes, reservations int
	if err := store.db.QueryRow(`SELECT COUNT(1) FROM usage_notes WHERE request_id='expire'`).Scan(&notes); err != nil || notes != 0 {
		t.Fatalf("notes=%d err=%v", notes, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(1) FROM reservations WHERE request_id='expire'`).Scan(&reservations); err != nil || reservations != 0 {
		t.Fatalf("reservations=%d err=%v", reservations, err)
	}
	session, err := store.Session(secret, 10)
	if err != nil || session.Key.TokensUsed != 0 {
		t.Fatalf("canceled row billed tokens_used=%d err=%v", session.Key.TokensUsed, err)
	}
	rows, _, _, err := store.AdminRequests(AdminRequestQuery{KeyID: key.ID, Limit: 25})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	row := rows[0]
	if row.Status != "canceled" || row.Billed != 0 || row.Input != 48 || row.Output != 20 || row.Reasoning != 4 || row.Cached != 8 || row.TTFTMs != 260 || row.LatencyMs != 900 || row.AuthID != note.AuthID || row.Provider != note.Provider || row.Effort != "max" || row.ServiceTier != "priority" {
		t.Fatalf("canceled row=%+v", row)
	}
}

func TestUnlimitedLimitIsNeverExhausted(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	_, secret, err := store.Create(CreateInput{TTLSeconds: 3600, UnlimitedTokens: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Admit(AdmitInput{Secret: secret, RequestID: "u", Path: "/v1/chat/completions", Generate: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.NoteUsage(UsageNote{RequestID: "u", Input: 1, Output: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(FinalizeInput{RequestID: "u", Outcome: "succeeded", RequestedAt: now}); err != nil {
		t.Fatal(err)
	}
	admission, err := store.Admit(AdmitInput{Secret: secret, RequestID: "u2", Path: "/v1/chat/completions", Generate: true})
	if err != nil || !admission.Allowed {
		t.Fatalf("unlimited admission=%+v err=%v", admission, err)
	}
}

func TestDatabaseFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.db")
	store, err := Open(Config{DataFile: path, Now: func() time.Time { return time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestRequestsFilterByTimeAndRejectionIsLogged(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	key, secret, err := store.Create(CreateInput{TTLSeconds: -60, TokenLimit: 10, TokenLimitSet: true})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := store.Admit(AdmitInput{Secret: secret, RequestID: "denied", Path: "/v1/chat/completions", Generate: true})
	if err != nil || admission.Code != "key_expired" {
		t.Fatalf("admission=%+v err=%v", admission, err)
	}
	if err := store.RecordRejection("denied", key.ID, "key_expired", 403); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Requests(RequestQuery{KeyID: key.ID, From: now.Add(-time.Minute), To: now.Add(time.Minute)})
	if err != nil || len(rows) != 1 || rows[0].Status != "rejected" || rows[0].Billed != 0 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	later, err := store.Requests(RequestQuery{KeyID: key.ID, From: now.Add(time.Hour)})
	if err != nil || len(later) != 0 {
		t.Fatalf("later=%+v err=%v", later, err)
	}
	rejected, rejectedPage, err := store.RequestsPage(RequestQuery{KeyID: key.ID, Status: "rejected", Limit: 25})
	if err != nil || rejectedPage.Total != 1 || len(rejected) != 1 {
		t.Fatalf("rejected total=%d rows=%d err=%v", rejectedPage.Total, len(rejected), err)
	}
	succeeded, succeededPage, err := store.RequestsPage(RequestQuery{KeyID: key.ID, Status: "succeeded", Limit: 25})
	if err != nil || succeededPage.Total != 0 || len(succeeded) != 0 {
		t.Fatalf("succeeded total=%d rows=%d err=%v", succeededPage.Total, len(succeeded), err)
	}
}

func TestRunSweeperStopsWhenCanceled(t *testing.T) {
	store := testStore(t, time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		store.RunSweeper(ctx)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sweeper did not stop")
	}
}

func mustHash(t *testing.T, store *Store, id string) []byte {
	t.Helper()
	hash, err := store.secretHash(id)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
