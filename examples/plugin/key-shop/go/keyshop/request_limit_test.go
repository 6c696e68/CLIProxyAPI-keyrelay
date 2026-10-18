package keyshop

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestRequestLimitIsConsumedAtAdmission(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	key, secret, err := store.Create(CreateInput{
		Label: "limited", TTLSeconds: 3600, TokenLimit: 1000, TokenLimitSet: true,
		RequestLimit: 1, RequestLimitSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if key.RequestLimit != 1 || key.RequestQuotaUsed != 0 {
		t.Fatalf("created limit=%d used=%d", key.RequestLimit, key.RequestQuotaUsed)
	}
	first, err := store.Admit(AdmitInput{Secret: secret, RequestID: "one", Path: "/v1/chat/completions", Generate: true, MaxTokens: 10})
	if err != nil || !first.Allowed {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	listed, err := store.Lookup(secret)
	if err != nil {
		t.Fatal(err)
	}
	if listed.RequestQuotaUsed != 1 || listed.RequestCount != 1 || !listed.RequestLimitReached {
		t.Fatalf("quota was not consumed at admission: %+v", listed)
	}
	second, err := store.Admit(AdmitInput{Secret: secret, RequestID: "two", Path: "/v1/chat/completions", Generate: true})
	if err != nil || second.Allowed || second.Code != "request_limit_exceeded" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if err := store.RecordRejection("two", key.ID, "request_limit_exceeded", 429); err != nil {
		t.Fatal(err)
	}
	listed, err = store.Lookup(secret)
	if err != nil {
		t.Fatal(err)
	}
	if listed.RequestQuotaUsed != 2 || listed.RequestRejectedCount != 1 || listed.RequestCount != 2 || !listed.RequestLimitReached {
		t.Fatalf("rejection must consume request quota: %+v", listed)
	}
}

func TestErrorRequestsConsumeQuotaUntilCutoff(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	key, secret, err := store.Create(CreateInput{TTLSeconds: 3600, UnlimitedTokens: true, RequestLimit: 2, RequestLimitSet: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"bad-1", "bad-2"} {
		if err := store.RecordRejection(id, key.ID, "model_not_allowed", 403); err != nil {
			t.Fatal(err)
		}
	}
	denied, err := store.Admit(AdmitInput{Secret: secret, RequestID: "bad-3", Path: "/v1/chat/completions", Generate: true})
	if err != nil || denied.Allowed || denied.Code != "request_limit_exceeded" {
		t.Fatalf("spam was not cut off: %+v err=%v", denied, err)
	}
	if err := store.RecordRejection("bad-3", key.ID, denied.Code, 429); err != nil {
		t.Fatal(err)
	}
	listed, err := store.Lookup(secret)
	if err != nil || listed.RequestQuotaUsed != 3 || listed.RequestRejectedCount != 3 || !listed.RequestLimitReached {
		t.Fatalf("error spam accounting=%+v err=%v", listed, err)
	}
}

func TestAdmittedFailureAndCancelConsumeQuotaOnce(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	_, secret, err := store.Create(CreateInput{TTLSeconds: 3600, TokenLimit: 100, TokenLimitSet: true, RequestLimit: 3, RequestLimitSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Admit(AdmitInput{Secret: secret, RequestID: "ok", Path: "/v1/chat/completions", Generate: true, MaxTokens: 10}); err != nil {
		t.Fatal(err)
	}
	if err := store.NoteUsage(UsageNote{RequestID: "ok", Input: 2, Output: 3}); err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(FinalizeInput{RequestID: "ok", Outcome: "succeeded", Model: "gpt", RequestedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(FinalizeInput{RequestID: "ok", Outcome: "succeeded", RequestedAt: now}); err != nil {
		t.Fatal(err)
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
	if _, err := store.Admit(AdmitInput{Secret: secret, RequestID: "stop", Path: "/v1/chat/completions", Generate: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.Finalize(FinalizeInput{RequestID: "stop", Outcome: "canceled", RequestedAt: now}); err != nil {
		t.Fatal(err)
	}
	session, err := store.Session(secret, 10)
	if err != nil {
		t.Fatal(err)
	}
	key := session.Key
	if key.TokensUsed != 5 || key.RequestQuotaUsed != 3 || key.RequestSuccessCount != 1 || key.RequestFailureCount != 1 || key.RequestCanceledCount != 1 {
		t.Fatalf("accounting=%+v", key)
	}
	if len(session.Requests) != 3 {
		t.Fatalf("requests=%d", len(session.Requests))
	}
}

func TestDuplicateAdmissionDoesNotConsumeTwice(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	_, secret, err := store.Create(CreateInput{TTLSeconds: 3600, RequestLimit: 1, RequestLimitSet: true, UnlimitedTokens: true})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Admit(AdmitInput{Secret: secret, RequestID: "same", Path: "/v1/chat/completions", Generate: true, MaxTokens: 4})
	if err != nil || !first.Allowed {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	again, err := store.Admit(AdmitInput{Secret: secret, RequestID: "same", Path: "/v1/chat/completions", Generate: true, MaxTokens: 4})
	if err != nil || !again.Allowed {
		t.Fatalf("duplicate pending=%+v err=%v", again, err)
	}
	key, err := store.Lookup(secret)
	if err != nil || key.RequestQuotaUsed != 1 {
		t.Fatalf("duplicate consumed quota: %+v err=%v", key, err)
	}
	_, otherSecret, err := store.Create(CreateInput{TTLSeconds: 3600, UnlimitedTokens: true, UnlimitedRequests: true})
	if err != nil {
		t.Fatal(err)
	}
	conflict, err := store.Admit(AdmitInput{Secret: otherSecret, RequestID: "same", Path: "/v1/chat/completions", Generate: true})
	if err != nil || conflict.Allowed || conflict.Code != "request_id_conflict" {
		t.Fatalf("conflict=%+v err=%v", conflict, err)
	}
}

func TestRaisingRequestLimitAllowsNextCallWithoutReset(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	key, secret, err := store.Create(CreateInput{TTLSeconds: 3600, TokenLimit: 50, TokenLimitSet: true, RequestLimit: 1, RequestLimitSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Admit(AdmitInput{Secret: secret, RequestID: "used", Generate: false}); err != nil {
		t.Fatal(err)
	}
	denied, err := store.Admit(AdmitInput{Secret: secret, RequestID: "blocked", Generate: false})
	if err != nil || denied.Code != "request_limit_exceeded" {
		t.Fatalf("denied=%+v err=%v", denied, err)
	}
	updated, err := store.Update(UpdateInput{ID: key.ID, RequestLimit: 2, RequestLimitSet: true})
	if err != nil || updated.RequestLimit != 2 || updated.RequestQuotaUsed != 1 || updated.RequestLimitReached {
		t.Fatalf("raised=%+v err=%v", updated, err)
	}
	allowed, err := store.Admit(AdmitInput{Secret: secret, RequestID: "next", Generate: false})
	if err != nil || !allowed.Allowed {
		t.Fatalf("after raise=%+v err=%v", allowed, err)
	}
	lowered, err := store.Update(UpdateInput{ID: key.ID, RequestLimit: 1, RequestLimitSet: true})
	if err != nil || !lowered.RequestLimitReached || lowered.RequestQuotaUsed != 2 {
		t.Fatalf("lowered=%+v err=%v", lowered, err)
	}
}

func TestConcurrentRequestLimitCannotOvershoot(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	_, secret, err := store.Create(CreateInput{TTLSeconds: 3600, UnlimitedTokens: true, RequestLimit: 1, RequestLimitSet: true})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 8; i++ {
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
			} else if admission.Code != "request_limit_exceeded" {
				t.Errorf("unexpected denial %s", admission.Code)
			}
		}()
	}
	wg.Wait()
	if allowed != 1 {
		t.Fatalf("allowed=%d", allowed)
	}
	key, err := store.Lookup(secret)
	if err != nil || key.RequestQuotaUsed != 1 {
		t.Fatalf("used=%d err=%v", key.RequestQuotaUsed, err)
	}
}

func TestSweepCancelsPendingWithoutRefundingRequestQuota(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	_, secret, err := store.Create(CreateInput{TTLSeconds: 3600, TokenLimit: 100, TokenLimitSet: true, RequestLimit: 1, RequestLimitSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Admit(AdmitInput{Secret: secret, RequestID: "old", Path: "/v1/chat/completions", Generate: true, MaxTokens: 8}); err != nil {
		t.Fatal(err)
	}
	store.cfg.Now = func() time.Time { return now.Add(16 * time.Minute) }
	if err := store.Sweep(); err != nil {
		t.Fatal(err)
	}
	key, err := store.Lookup(secret)
	if err != nil || key.TokensUsed != 0 || key.RequestQuotaUsed != 1 || key.RequestCanceledCount != 1 || !key.RequestLimitReached {
		t.Fatalf("after sweep=%+v err=%v", key, err)
	}
	denied, err := store.Admit(AdmitInput{Secret: secret, RequestID: "again", Path: "/v1/chat/completions", Generate: true})
	if err != nil || denied.Allowed || denied.Code != "request_limit_exceeded" {
		t.Fatalf("readmit=%+v err=%v", denied, err)
	}
	if err := store.Finalize(FinalizeInput{RequestID: "old", Outcome: "succeeded", Input: 5, RequestedAt: now}); err != nil {
		t.Fatal(err)
	}
	key, err = store.Lookup(secret)
	if err != nil || key.TokensUsed != 0 || key.RequestSuccessCount != 0 || key.RequestCanceledCount != 1 {
		t.Fatalf("late finalize refunded or double-counted: %+v err=%v", key, err)
	}
}

func TestLegacyRequestCountBecomesQuotaWithoutReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE keys (
		id TEXT PRIMARY KEY, secret_hash BLOB NOT NULL UNIQUE, display_prefix TEXT NOT NULL, display_last4 TEXT NOT NULL,
		label TEXT NOT NULL DEFAULT '', customer TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '', status TEXT NOT NULL,
		created_at TEXT NOT NULL, expires_at TEXT, token_limit INTEGER NOT NULL, tokens_used INTEGER NOT NULL DEFAULT 0,
		request_count INTEGER NOT NULL DEFAULT 0, last_used_at TEXT, deleted_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE requests (
		request_id TEXT PRIMARY KEY, key_id TEXT NOT NULL, model TEXT NOT NULL DEFAULT '', transport TEXT NOT NULL DEFAULT '',
		protocol TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, http_status INTEGER NOT NULL DEFAULT 0, latency_ms INTEGER NOT NULL DEFAULT 0,
		ttft_ms INTEGER NOT NULL DEFAULT 0, input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
		reasoning_tokens INTEGER NOT NULL DEFAULT 0, cached_tokens INTEGER NOT NULL DEFAULT 0, billed_tokens INTEGER NOT NULL DEFAULT 0,
		requested_at TEXT NOT NULL, completed_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	secret, hash, prefix, last4, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO keys (id, secret_hash, display_prefix, display_last4, label, status, created_at, token_limit, request_count) VALUES (?,?,?,?,?,?,?,?,?)`,
		"legacy", hash, prefix, last4, "old", "active", now, 100, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO requests (request_id, key_id, status, requested_at, completed_at) VALUES (?,?,?,?,?)`, "s", "legacy", "succeeded", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO requests (request_id, key_id, status, requested_at, completed_at) VALUES (?,?,?,?,?)`, "r", "legacy", "rejected", now, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(Config{DataFile: path, Now: func() time.Time { return time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	key, err := store.Lookup(secret)
	if err != nil {
		t.Fatal(err)
	}
	if key.RequestCount != 4 || key.RequestQuotaUsed != 4 || key.RequestSuccessCount != 1 || key.RequestRejectedCount != 1 || key.RequestLimit != 0 {
		t.Fatalf("migrated=%+v", key)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(Config{DataFile: path, Now: func() time.Time { return time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again, err := reopened.Lookup(secret)
	if err != nil {
		t.Fatal(err)
	}
	if again.RequestQuotaUsed != 4 || again.RequestSuccessCount != 1 || again.RequestRejectedCount != 1 {
		t.Fatalf("migration replayed counters: %+v", again)
	}
}
