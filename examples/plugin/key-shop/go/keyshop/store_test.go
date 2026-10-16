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
	if err := store.RecordRejection("denied", key.ID, 403); err != nil {
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
