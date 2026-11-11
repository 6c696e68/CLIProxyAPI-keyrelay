package keyshop

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type usageRow struct {
	keyID, requestID, model, status string
	in, out, reasoning, cached      int64
	billed                          int64
	at                              time.Time
}

func insertUsageRow(t testing.TB, store *Store, row usageRow) {
	t.Helper()
	if row.at.IsZero() {
		row.at = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	}
	if row.status == "" {
		row.status = "succeeded"
	}
	_, err := store.db.Exec(`INSERT INTO requests (request_id, key_id, model, status, input_tokens, output_tokens, reasoning_tokens, cached_tokens, billed_tokens, requested_at, completed_at, rejection_code)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		row.requestID, row.keyID, row.model, row.status, row.in, row.out, row.reasoning, row.cached, row.billed,
		row.at.UTC().Format(time.RFC3339Nano), row.at.UTC().Format(time.RFC3339Nano), "")
	if err != nil {
		t.Fatal(err)
	}
}

func usageByModel(t testing.TB, items []ModelUsage) map[string]ModelUsage {
	t.Helper()
	out := make(map[string]ModelUsage, len(items))
	for _, item := range items {
		if _, dup := out[item.Model]; dup {
			t.Fatalf("duplicate model in breakdown: %q", item.Model)
		}
		out[item.Model] = item
	}
	return out
}

func sessionModelUsage(t *testing.T, svc *Service, secret, extraQuery string) (items []ModelUsage, since string, raw string) {
	t.Helper()
	rawBytes := call(t, svc, "management.handle", `{"Method":"GET","Path":"/api/session","Query":{`+extraQuery+`},"Headers":{"Authorization":["Bearer `+secret+`"]}}`)
	var env struct {
		Result struct {
			Status int
			Body   []byte
		}
	}
	if err := json.Unmarshal(rawBytes, &env); err != nil {
		t.Fatal(err)
	}
	raw = string(env.Result.Body)
	var body struct {
		ModelUsage      []ModelUsage `json:"model_usage"`
		ModelUsageSince string       `json:"model_usage_since"`
	}
	if err := json.Unmarshal(env.Result.Body, &body); err != nil {
		t.Fatalf("session body: %v raw=%s", err, raw)
	}
	return body.ModelUsage, body.ModelUsageSince, raw
}

func TestSessionModelUsageIsolatedPerKey(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	keyA, secretA, err := store.Create(CreateInput{Label: "a"})
	if err != nil {
		t.Fatal(err)
	}
	keyB, secretB, err := store.Create(CreateInput{Label: "b"})
	if err != nil {
		t.Fatal(err)
	}
	insertUsageRow(t, store, usageRow{keyID: keyA.ID, requestID: "r1", model: "gpt-5.2", in: 10, out: 20, reasoning: 3, cached: 4, billed: 37, at: now})
	insertUsageRow(t, store, usageRow{keyID: keyA.ID, requestID: "r2", model: "gpt-5.2", in: 1, out: 2, billed: 3, at: now.Add(time.Second)})
	insertUsageRow(t, store, usageRow{keyID: keyB.ID, requestID: "r3", model: "claude-x", in: 5, out: 5, billed: 10, at: now.Add(2 * time.Second)})

	svc := NewService(store, []byte("<admin>"), []byte("<dash>"))
	itemsA, sinceA, _ := sessionModelUsage(t, svc, secretA, "")
	if len(itemsA) != 1 || itemsA[0].Model != "gpt-5.2" {
		t.Fatalf("key A usage=%+v", itemsA)
	}
	item := itemsA[0]
	if item.Requests != 2 || item.InputTokens != 11 || item.OutputTokens != 22 || item.ReasoningTokens != 3 || item.CachedTokens != 4 || item.TotalTokens != 40 {
		t.Fatalf("key A aggregate=%+v", item)
	}
	if item.FirstUsedAt != now.UTC().Format(time.RFC3339Nano) || item.LastUsedAt != now.Add(time.Second).UTC().Format(time.RFC3339Nano) {
		t.Fatalf("key A timestamps first=%q last=%q", item.FirstUsedAt, item.LastUsedAt)
	}
	if sinceA != item.FirstUsedAt {
		t.Fatalf("since=%q want %q", sinceA, item.FirstUsedAt)
	}

	itemsB, sinceB, _ := sessionModelUsage(t, svc, secretB, "")
	if len(itemsB) != 1 || itemsB[0].Model != "claude-x" || itemsB[0].TotalTokens != 10 {
		t.Fatalf("key B usage=%+v", itemsB)
	}
	if sinceB != itemsB[0].FirstUsedAt {
		t.Fatalf("key B since=%q", sinceB)
	}
}

func TestSessionModelUsageOnlyBilledModels(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	key, secret, err := store.Create(CreateInput{Label: "a"})
	if err != nil {
		t.Fatal(err)
	}
	// Zero-token model rows must be hidden.
	insertUsageRow(t, store, usageRow{keyID: key.ID, requestID: "z1", model: "zero-model", at: now})
	// A billed model plus pending/rejected rows for another model only.
	insertUsageRow(t, store, usageRow{keyID: key.ID, requestID: "b1", model: "billed-model", in: 4, out: 6, billed: 10, at: now})
	insertUsageRow(t, store, usageRow{keyID: key.ID, requestID: "p1", model: "pending-model", status: "pending", in: 9, out: 9, billed: 18, at: now})
	insertUsageRow(t, store, usageRow{keyID: key.ID, requestID: "rj1", model: "rejected-model", status: "rejected", in: 7, out: 7, billed: 14, at: now})

	svc := NewService(store, []byte("<admin>"), []byte("<dash>"))
	items, _, _ := sessionModelUsage(t, svc, secret, "")
	byModel := usageByModel(t, items)
	if len(byModel) != 1 {
		t.Fatalf("usage=%+v", items)
	}
	item, ok := byModel["billed-model"]
	if !ok {
		t.Fatalf("billed-model missing: %+v", items)
	}
	// Pending/rejected rows must not be counted as requests.
	if item.Requests != 1 || item.TotalTokens != 10 {
		t.Fatalf("billed-model aggregate=%+v", item)
	}
	for _, hidden := range []string{"zero-model", "pending-model", "rejected-model"} {
		if _, ok := byModel[hidden]; ok {
			t.Fatalf("%s must be hidden, got %+v", hidden, items)
		}
	}
}

func TestSessionModelUsageSortOrder(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	key, secret, err := store.Create(CreateInput{Label: "a"})
	if err != nil {
		t.Fatal(err)
	}
	// billed: beta=5, alpha=5, gamma=9, delta=1 → order gamma, alpha, beta, delta.
	insertUsageRow(t, store, usageRow{keyID: key.ID, requestID: "1", model: "beta", billed: 5, at: now})
	insertUsageRow(t, store, usageRow{keyID: key.ID, requestID: "2", model: "alpha", billed: 5, at: now})
	insertUsageRow(t, store, usageRow{keyID: key.ID, requestID: "3", model: "gamma", billed: 9, at: now})
	insertUsageRow(t, store, usageRow{keyID: key.ID, requestID: "4", model: "delta", billed: 1, at: now})

	svc := NewService(store, []byte("<admin>"), []byte("<dash>"))
	items, _, _ := sessionModelUsage(t, svc, secret, "")
	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, item.Model)
	}
	want := []string{"gamma", "alpha", "beta", "delta"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order=%v want %v", got, want)
	}
}

func TestSessionModelUsageCappedAt200(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	key, secret, err := store.Create(CreateInput{Label: "a"})
	if err != nil {
		t.Fatal(err)
	}
	// 220 models; the 20 cheapest must be cut off by the cap.
	for i := 0; i < 220; i++ {
		insertUsageRow(t, store, usageRow{keyID: key.ID, requestID: fmt.Sprintf("r%03d", i), model: fmt.Sprintf("model-%03d", i), billed: int64(1000 - i), at: now.Add(time.Duration(i) * time.Second)})
	}
	svc := NewService(store, []byte("<admin>"), []byte("<dash>"))
	items, _, _ := sessionModelUsage(t, svc, secret, "")
	if len(items) != modelUsageLimit {
		t.Fatalf("len=%d want %d", len(items), modelUsageLimit)
	}
	if items[0].Model != "model-000" || items[modelUsageLimit-1].Model != "model-199" {
		t.Fatalf("cap kept wrong models: first=%q last=%q", items[0].Model, items[modelUsageLimit-1].Model)
	}
}

func TestSessionModelUsageEmptyKey(t *testing.T) {
	store := testStore(t, time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC))
	_, secret, err := store.Create(CreateInput{Label: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, []byte("<admin>"), []byte("<dash>"))
	items, since, raw := sessionModelUsage(t, svc, secret, "")
	if items == nil || len(items) != 0 {
		t.Fatalf("usage=%+v want []", items)
	}
	if since != "" {
		t.Fatalf("since=%q want empty", since)
	}
	if !strings.Contains(raw, `"model_usage":[]`) {
		t.Fatalf("model_usage not an empty array in %s", raw)
	}
}

func TestSessionModelUsageOnlyOnFirstPage(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	store := testStore(t, now)
	key, secret, err := store.Create(CreateInput{Label: "a"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		insertUsageRow(t, store, usageRow{keyID: key.ID, requestID: fmt.Sprintf("r%d", i), model: "m", billed: 5, at: now.Add(time.Duration(i) * time.Second)})
	}
	svc := NewService(store, []byte("<admin>"), []byte("<dash>"))
	_, _, first := sessionModelUsage(t, svc, secret, `"limit":["2"]`)
	if !strings.Contains(first, `"model_usage"`) {
		t.Fatalf("first page missing model_usage: %s", first)
	}
	var body struct {
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(first), &body); err != nil || body.NextCursor == "" {
		t.Fatalf("no cursor on first page err=%v body=%s", err, first)
	}
	_, _, second := sessionModelUsage(t, svc, secret, `"limit":["2"],"cursor":["`+body.NextCursor+`"]`)
	if strings.Contains(second, `"model_usage"`) {
		t.Fatalf("later page must omit model_usage: %s", second)
	}
}

func TestModelUsageQueryPlanUsesKeyIndex(t *testing.T) {
	store := testStore(t, time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC))
	rows, err := store.db.Query(`EXPLAIN QUERY PLAN
SELECT model, COUNT(1), SUM(billed_tokens), MIN(requested_at), MAX(requested_at)
FROM requests
WHERE key_id=? AND status NOT IN ('pending','rejected')
GROUP BY model
HAVING SUM(billed_tokens) > 0
ORDER BY SUM(billed_tokens) DESC, model ASC
LIMIT 200`, "k1")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plans []string
	for rows.Next() {
		var id, parent int
		var notUsed, detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		plans = append(plans, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plans, "\n")
	if !strings.Contains(joined, "(key_id=?)") {
		t.Fatalf("plan must search by key_id, got:\n%s", joined)
	}
	if strings.Contains(joined, "SCAN requests") {
		t.Fatalf("plan must not scan requests, got:\n%s", joined)
	}
}

// buildUsageBenchStore creates a synthetic store with 300000 request rows
// across two keys (heavy key holds 100000 rows over 12 models) and returns the
// heavy key. The aggregation is exercised through store.computeModelUsage so
// the measurement covers the cold query, not the TTL cache.
func buildUsageBenchStore(tb testing.TB) (*Store, string, string) {
	tb.Helper()
	store, err := Open(Config{
		DataFile:     filepath.Join(tb.TempDir(), "key-shop.db"),
		DefaultTTL:   168 * time.Hour,
		DefaultLimit: 100_000_000,
		Now:          func() time.Time { return time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = store.Close() })
	key, _, err := store.Create(CreateInput{Label: "heavy"})
	if err != nil {
		tb.Fatal(err)
	}
	others, _, err := store.Create(CreateInput{Label: "others"})
	if err != nil {
		tb.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := bulkInsertRequests(tb, store, key.ID, 100000, 12, base); err != nil {
		tb.Fatal(err)
	}
	if err := bulkInsertRequests(tb, store, others.ID, 200000, 20, base); err != nil {
		tb.Fatal(err)
	}
	return store, key.ID, others.ID
}

// BenchmarkModelUsage100k measures the cold aggregation over a 100000-row key
// inside a 300000-row log. Run with: go test ./keyshop/ -bench ModelUsage
func BenchmarkModelUsage100k(b *testing.B) {
	store, keyID, _ := buildUsageBenchStore(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.computeModelUsage(keyID); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkModelUsageCached measures the warm path: repeated ModelUsage calls
// served from the per-key TTL cache.
func BenchmarkModelUsageCached(b *testing.B) {
	store, keyID, _ := buildUsageBenchStore(b)
	if _, err := store.ModelUsage(keyID); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.ModelUsage(keyID); err != nil {
			b.Fatal(err)
		}
	}
}

// TestModelUsageColdSanity guards the cold aggregation with a generous 500ms
// limit; precise timing is measured by BenchmarkModelUsage100k. The flat limit
// only holds when the machine is fast enough that a plain 200000-row COUNT
// stays well under 100ms; under the race detector or on a slow machine both
// scale up together, so only the calibrated bound is asserted there.
func TestModelUsageColdSanity(t *testing.T) {
	store, keyID, baselineKeyID := buildUsageBenchStore(t)
	var rowCount int
	start := time.Now()
	if err := store.db.QueryRow(`SELECT COUNT(1) FROM requests WHERE key_id=?`, baselineKeyID).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	baseline := time.Since(start)

	start = time.Now()
	items, err := store.computeModelUsage(keyID)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 12 {
		t.Fatalf("models=%d want 12", len(items))
	}
	t.Logf("cold model_usage aggregation over 100000 rows took %s (baseline COUNT %s)", elapsed, baseline)
	if elapsed > 30*baseline+200*time.Millisecond {
		t.Fatalf("cold aggregation took %s, more than 30x the %s COUNT baseline", elapsed, baseline)
	}
	if baseline < 100*time.Millisecond && elapsed > 500*time.Millisecond {
		t.Fatalf("cold aggregation took %s, want <= 500ms", elapsed)
	}
}

// TestModelUsageCacheTTLAndInvalidation drives the per-key cache with a fake
// clock: hits within the TTL skip the query, hits after it recompute, and
// invalidation drops the entry immediately.
func TestModelUsageCacheTTLAndInvalidation(t *testing.T) {
	base := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	store := testStore(t, base)
	clock := base
	store.cfg.Now = func() time.Time { return clock }
	key, _, err := store.Create(CreateInput{Label: "a"})
	if err != nil {
		t.Fatal(err)
	}
	insertUsageRow(t, store, usageRow{keyID: key.ID, requestID: "r1", model: "a", billed: 10})

	items, err := store.ModelUsage(key.ID)
	if err != nil || len(items) != 1 || items[0].TotalTokens != 10 {
		t.Fatalf("first call err=%v items=%+v", err, items)
	}

	// Within the TTL the cached value is returned even though the log grew.
	insertUsageRow(t, store, usageRow{keyID: key.ID, requestID: "r2", model: "b", billed: 5})
	clock = base.Add(5 * time.Second)
	items, err = store.ModelUsage(key.ID)
	if err != nil || len(items) != 1 || items[0].TotalTokens != 10 {
		t.Fatalf("cached call err=%v items=%+v", err, items)
	}

	// After the TTL the query runs again and sees the new row.
	clock = base.Add(11 * time.Second)
	items, err = store.ModelUsage(key.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("expired call err=%v items=%+v", err, items)
	}

	// Invalidation drops the entry inside the TTL window.
	insertUsageRow(t, store, usageRow{keyID: key.ID, requestID: "r3", model: "c", billed: 7})
	clock = base.Add(15 * time.Second)
	store.invalidateModelUsage(key.ID)
	items, err = store.ModelUsage(key.ID)
	if err != nil || len(items) != 3 {
		t.Fatalf("invalidated call err=%v items=%+v", err, items)
	}
}

// TestModelUsageConcurrentLoads exercises the single-flight guard: many
// concurrent loads of one key all get the same computed result. Run with
// -race to check the cache bookkeeping.
func TestModelUsageConcurrentLoads(t *testing.T) {
	store := testStore(t, time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC))
	key, _, err := store.Create(CreateInput{Label: "a"})
	if err != nil {
		t.Fatal(err)
	}
	insertUsageRow(t, store, usageRow{keyID: key.ID, requestID: "r1", model: "a", billed: 10})
	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			items, err := store.ModelUsage(key.ID)
			if err != nil {
				errs[i] = err
				return
			}
			if len(items) != 1 || items[0].TotalTokens != 10 {
				errs[i] = fmt.Errorf("concurrent load %d got %+v", i, items)
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("load %d: %v", i, err)
		}
	}
}

// bulkInsertRequests writes count synthetic rows for keyID spread over models
// model-000..model-(models-1), inside one transaction.
func bulkInsertRequests(t testing.TB, store *Store, keyID string, count, models int, base time.Time) error {
	t.Helper()
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO requests (request_id, key_id, model, status, input_tokens, output_tokens, reasoning_tokens, cached_tokens, billed_tokens, requested_at, completed_at, rejection_code)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for i := 0; i < count; i++ {
		at := base.Add(time.Duration(i) * time.Millisecond).UTC().Format(time.RFC3339Nano)
		requestID := fmt.Sprintf("%s-%06d", keyID, i)
		model := fmt.Sprintf("model-%03d", i%models)
		status := "succeeded"
		if _, err := stmt.Exec(requestID, keyID, model, status, 100, 200, 10, 5, 315, at, at, ""); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}
