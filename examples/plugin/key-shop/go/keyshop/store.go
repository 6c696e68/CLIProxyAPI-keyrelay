package keyshop

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Config struct {
	DataFile             string
	DefaultTTL           time.Duration
	DefaultLimit         int64
	ReserveTokens        int64
	ReservationTTL       time.Duration
	RetentionDays        int
	DeletedRetentionDays int
	Now                  func() time.Time
}

type Store struct {
	db  *sql.DB
	cfg Config
	mu  sync.Mutex
}

type Key struct {
	ID           string
	Display      string
	Label        string
	Customer     string
	Note         string
	Status       string
	CreatedAt    time.Time
	ExpiresAt    *time.Time
	TokenLimit   int64
	TokensUsed   int64
	RequestCount int64
	LastUsedAt   *time.Time
}

type CreateInput struct {
	Label, Customer, Note string
	TTLSeconds            int64
	NeverExpires          bool
	TokenLimit            int64
	TokenLimitSet         bool
	UnlimitedTokens       bool
}

type UpdateInput struct {
	ID, Label, Customer, Note, Status string
	LabelSet, CustomerSet, NoteSet    bool
	StatusSet                         bool
	TTLSeconds                        int64
	TTLSet, NeverExpires              bool
	TokenLimit                        int64
	TokenLimitSet, UnlimitedTokens    bool
}

type AdmitInput struct {
	Secret, RequestID, Path string
	MaxTokens               int64
	Generate                bool
}

type Admission struct {
	Allowed  bool
	Ignored  bool
	Code     string
	KeyID    string
	Reserved int64
}

type FinalizeInput struct {
	RequestID, Model, Transport, Protocol, Outcome string
	HTTPStatus                                     int
	Input, Output, Reasoning, Cached               int64
	Latency, TTFT                                  time.Duration
	RequestedAt                                    time.Time
}

type UsageNote struct {
	RequestID                        string
	Input, Output, Reasoning, Cached int64
	Failed                           bool
}

type Request struct {
	RequestID   string
	KeyID       string
	Model       string
	Transport   string
	Protocol    string
	Status      string
	HTTPStatus  int
	LatencyMs   int64
	TTFTMs      int64
	Input       int64
	Output      int64
	Reasoning   int64
	Cached      int64
	Billed      int64
	RequestedAt time.Time
	CompletedAt time.Time
}

type RequestQuery struct {
	KeyID, Status, Model string
	From, To             time.Time
	Limit, Offset        int
	Cursor               string
}

type PageInfo struct {
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

type StatsSummary struct {
	KeysTotal        int   `json:"keys_total"`
	KeysActive       int   `json:"keys_active"`
	KeysDisabled     int   `json:"keys_disabled"`
	KeysExpired      int   `json:"keys_expired"`
	KeysExhausted    int   `json:"keys_exhausted"`
	TokensUsedTotal  int64 `json:"tokens_used_total"`
	RequestsTotal    int64 `json:"requests_total"`
	ExpiringWithin7d int   `json:"expiring_within_7d"`
}

type Session struct {
	Key      Key
	Requests []Request
	BaseURL  string
}

func Open(cfg Config) (*Store, error) {
	if cfg.DefaultTTL == 0 {
		cfg.DefaultTTL = 168 * time.Hour
	}
	if cfg.DefaultLimit == 0 {
		cfg.DefaultLimit = 100_000_000
	}
	if cfg.ReserveTokens == 0 {
		cfg.ReserveTokens = 32_768
	}
	if cfg.ReservationTTL == 0 {
		cfg.ReservationTTL = 15 * time.Minute
	}
	if cfg.RetentionDays == 0 {
		cfg.RetentionDays = 30
	}
	if cfg.DeletedRetentionDays == 0 {
		cfg.DeletedRetentionDays = 90
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	path := cfg.DataFile
	if strings.TrimSpace(path) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, ".cliproxyapi", "key-shop", "key-shop.db")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, cfg: cfg}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS keys (
  id TEXT PRIMARY KEY,
  secret_hash BLOB NOT NULL UNIQUE,
  display_prefix TEXT NOT NULL,
  display_last4 TEXT NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  customer TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT,
  token_limit INTEGER NOT NULL,
  tokens_used INTEGER NOT NULL DEFAULT 0,
  request_count INTEGER NOT NULL DEFAULT 0,
  last_used_at TEXT,
  deleted_at TEXT
);
CREATE TABLE IF NOT EXISTS reservations (
  request_id TEXT PRIMARY KEY,
  key_id TEXT NOT NULL,
  reserved_tokens INTEGER NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS requests (
  request_id TEXT PRIMARY KEY,
  key_id TEXT NOT NULL,
  model TEXT NOT NULL DEFAULT '',
  transport TEXT NOT NULL DEFAULT '',
  protocol TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  http_status INTEGER NOT NULL DEFAULT 0,
  latency_ms INTEGER NOT NULL DEFAULT 0,
  ttft_ms INTEGER NOT NULL DEFAULT 0,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens INTEGER NOT NULL DEFAULT 0,
  cached_tokens INTEGER NOT NULL DEFAULT 0,
  billed_tokens INTEGER NOT NULL DEFAULT 0,
  requested_at TEXT NOT NULL,
  completed_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS usage_notes (
  request_id TEXT PRIMARY KEY,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens INTEGER NOT NULL DEFAULT 0,
  cached_tokens INTEGER NOT NULL DEFAULT 0,
  failed INTEGER NOT NULL DEFAULT 0
);`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
CREATE INDEX IF NOT EXISTS idx_keys_created_at ON keys(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_keys_deleted_at ON keys(deleted_at);
CREATE INDEX IF NOT EXISTS idx_keys_live_created ON keys(created_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_reservations_key ON reservations(key_id);
CREATE INDEX IF NOT EXISTS idx_reservations_created ON reservations(created_at);
CREATE INDEX IF NOT EXISTS idx_requests_key_time ON requests(key_id, requested_at DESC, request_id DESC);
CREATE INDEX IF NOT EXISTS idx_requests_time ON requests(requested_at DESC, request_id DESC);
CREATE INDEX IF NOT EXISTS idx_requests_status_time ON requests(status, requested_at DESC, request_id DESC);`)
	return err
}

func (s *Store) Create(in CreateInput) (Key, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	secret, hash, prefix, last4, err := NewSecret()
	if err != nil {
		return Key{}, "", err
	}
	key, err := s.insertKey(in, hash, prefix, last4)
	if err != nil {
		return Key{}, "", err
	}
	return key, secret, nil
}

func (s *Store) insertKey(in CreateInput, hash []byte, prefix, last4 string) (Key, error) {
	id, err := newID()
	if err != nil {
		return Key{}, err
	}
	now := s.cfg.Now().UTC()
	expires := s.expiry(in.NeverExpires, in.TTLSeconds, true, now)
	limit := s.limit(in.UnlimitedTokens, in.TokenLimitSet, in.TokenLimit)
	_, err = s.db.Exec(`INSERT INTO keys (id, secret_hash, display_prefix, display_last4, label, customer, note, status, created_at, expires_at, token_limit) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		id, hash, prefix, last4, in.Label, in.Customer, in.Note, "active", now.Format(time.RFC3339Nano), formatTime(expires), limit)
	if err != nil {
		return Key{}, err
	}
	return s.keyByID(id)
}

func (s *Store) StatsSummary() (StatsSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out StatsSummary
	now := s.cfg.Now().UTC()
	within7d := now.Add(7 * 24 * time.Hour)
	nowText := now.Format(time.RFC3339Nano)
	withinText := within7d.Format(time.RFC3339Nano)
	err := s.db.QueryRow(`SELECT
COUNT(1), COALESCE(SUM(tokens_used), 0), COALESCE(SUM(request_count), 0),
COALESCE(SUM(CASE WHEN status='disabled' THEN 1 ELSE 0 END), 0),
COALESCE(SUM(CASE WHEN status <> 'disabled' AND expires_at IS NOT NULL AND expires_at <= ? THEN 1 ELSE 0 END), 0),
COALESCE(SUM(CASE WHEN status <> 'disabled' AND (expires_at IS NULL OR expires_at > ?) AND token_limit > 0 AND tokens_used >= token_limit THEN 1 ELSE 0 END), 0),
COALESCE(SUM(CASE WHEN status <> 'disabled' AND expires_at > ? AND expires_at <= ? THEN 1 ELSE 0 END), 0)
FROM keys WHERE deleted_at IS NULL`, nowText, nowText, nowText, withinText).
		Scan(&out.KeysTotal, &out.TokensUsedTotal, &out.RequestsTotal, &out.KeysDisabled, &out.KeysExpired, &out.KeysExhausted, &out.ExpiringWithin7d)
	if err != nil {
		return StatsSummary{}, err
	}
	out.KeysActive = out.KeysTotal - out.KeysDisabled - out.KeysExpired - out.KeysExhausted
	if out.KeysActive < 0 {
		out.KeysActive = 0
	}
	return out, nil
}

func (s *Store) List(query string) ([]Key, error) {
	keys, _, err := s.ListPage(query, 1, 100)
	return keys, err
}

func (s *Store) ListPage(query string, page, pageSize int) ([]Key, int, error) {
	return s.ListPageFiltered(query, "", page, pageSize)
}

func (s *Store) ListPageFiltered(query, status string, page, pageSize int) ([]Key, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 25
	}
	var total int
	where, args := keyListWhere(query, status, s.cfg.Now().UTC())
	from, reserved := listSource(status)
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM `+from+` WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	offset := (page - 1) * pageSize
	rows, err := s.db.Query(`SELECT `+keyColumns+`, `+reserved+` FROM `+from+` WHERE `+where+` ORDER BY keys.created_at DESC, keys.id DESC LIMIT ? OFFSET ?`, append(args, pageSize, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Key
	for rows.Next() {
		key, err := s.scanKey(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, key)
	}
	return out, total, rows.Err()
}

func keyListWhere(query, status string, now time.Time) (string, []any) {
	pattern := like(query)
	where := `keys.deleted_at IS NULL AND (keys.label LIKE ? OR keys.customer LIKE ? OR keys.id LIKE ?)`
	args := []any{pattern, pattern, pattern}
	nowText := now.Format(time.RFC3339Nano)
	held := `COALESCE(open_res.reserved, 0)`
	switch status {
	case "active":
		where += ` AND keys.status='active' AND (keys.expires_at IS NULL OR keys.expires_at > ?) AND (keys.token_limit=0 OR keys.tokens_used + ` + held + ` < keys.token_limit)`
		args = append(args, nowText)
	case "disabled":
		where += ` AND keys.status='disabled'`
	case "expired":
		where += ` AND keys.status<>'disabled' AND keys.expires_at IS NOT NULL AND keys.expires_at <= ?`
		args = append(args, nowText)
	case "exhausted":
		where += ` AND keys.status<>'disabled' AND (keys.expires_at IS NULL OR keys.expires_at > ?) AND keys.token_limit > 0 AND keys.tokens_used + ` + held + ` >= keys.token_limit`
		args = append(args, nowText)
	}
	return where, args
}

const keyColumns = `keys.id, keys.display_prefix, keys.display_last4, keys.label, keys.customer, keys.note, keys.status, keys.created_at, keys.expires_at, keys.token_limit, keys.tokens_used, keys.request_count, keys.last_used_at`

const reservedExpr = `COALESCE(open_res.reserved, 0)`

const reservedByKey = `COALESCE((SELECT SUM(reserved_tokens) FROM reservations WHERE key_id = keys.id), 0)`

const keyFrom = `keys LEFT JOIN (SELECT key_id, SUM(reserved_tokens) AS reserved FROM reservations GROUP BY key_id) open_res ON open_res.key_id = keys.id`

func listSource(status string) (string, string) {
	if status == "active" || status == "exhausted" {
		return keyFrom, reservedExpr
	}
	return "keys", reservedByKey
}

func (s *Store) Update(in UpdateInput) (Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.StatusSet && in.Status != "active" && in.Status != "disabled" {
		return Key{}, fmt.Errorf("invalid status")
	}
	key, err := s.keyByID(in.ID)
	if err != nil {
		return Key{}, err
	}
	status := "active"
	if key.Status == "disabled" {
		status = "disabled"
	}
	if in.StatusSet {
		status = in.Status
	}
	label, customer, note := key.Label, key.Customer, key.Note
	if in.LabelSet {
		label = in.Label
	}
	if in.CustomerSet {
		customer = in.Customer
	}
	if in.NoteSet {
		note = in.Note
	}
	expires := key.ExpiresAt
	if in.NeverExpires {
		expires = nil
	} else if in.TTLSet {
		expires = s.expiry(false, in.TTLSeconds, true, s.cfg.Now().UTC())
	}
	limit := key.TokenLimit
	if in.UnlimitedTokens {
		limit = 0
	} else if in.TokenLimitSet {
		limit = in.TokenLimit
	}
	_, err = s.db.Exec(`UPDATE keys SET label=?, customer=?, note=?, status=?, expires_at=?, token_limit=? WHERE id=? AND deleted_at IS NULL`,
		label, customer, note, status, formatTime(expires), limit, in.ID)
	if err != nil {
		return Key{}, err
	}
	return s.keyByID(in.ID)
}

func (s *Store) Rotate(id string) (Key, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	secret, hash, prefix, last4, err := NewSecret()
	if err != nil {
		return Key{}, "", err
	}
	res, err := s.db.Exec(`UPDATE keys SET secret_hash=?, display_prefix=?, display_last4=? WHERE id=? AND deleted_at IS NULL`, hash, prefix, last4, id)
	if err != nil {
		return Key{}, "", err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return Key{}, "", sql.ErrNoRows
	}
	key, err := s.keyByID(id)
	return key, secret, err
}

func (s *Store) ResetUsage(id string) (Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE keys SET tokens_used=0 WHERE id=? AND deleted_at IS NULL`, id)
	if err != nil {
		return Key{}, err
	}
	return s.keyByID(id)
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE keys SET deleted_at=? WHERE id=? AND deleted_at IS NULL`, s.cfg.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *Store) Lookup(secret string) (Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scanKey(s.db.QueryRow(`SELECT `+keyColumns+`, `+reservedByKey+` FROM keys WHERE keys.secret_hash=? AND keys.deleted_at IS NULL`, HashSecret(secret)))
}

func (s *Store) Admit(in AdmitInput) (Admission, error) {
	if !strings.HasPrefix(in.Secret, "ck_") {
		return Admission{Ignored: true}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Admission{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var row keyRow
	var expires sql.NullString
	var reserved int64
	err = tx.QueryRow(`SELECT keys.id, keys.status, keys.expires_at, keys.token_limit, keys.tokens_used, `+reservedByKey+` FROM keys WHERE keys.secret_hash=? AND keys.deleted_at IS NULL`, HashSecret(in.Secret)).
		Scan(&row.id, &row.status, &expires, &row.limit, &row.used, &reserved)
	if err == sql.ErrNoRows {
		return Admission{Code: "key_not_found"}, nil
	}
	if err != nil {
		return Admission{}, err
	}
	row.expires = parseOptional(expires)
	now := s.cfg.Now().UTC()
	if row.status == "disabled" {
		return Admission{Code: "key_disabled", KeyID: row.id}, nil
	}
	if row.expires != nil && !row.expires.After(now) {
		return Admission{Code: "key_expired", KeyID: row.id}, nil
	}
	if row.limit > 0 && row.used+reserved >= row.limit {
		return Admission{Code: "token_limit_exceeded", KeyID: row.id}, nil
	}
	amount := int64(0)
	if in.Generate && !countPath(in.Path) && row.limit > 0 {
		want := in.MaxTokens
		if want <= 0 || want > s.cfg.ReserveTokens {
			want = s.cfg.ReserveTokens
		}
		remain := row.limit - row.used - reserved
		if want > remain {
			want = remain
		}
		if want < 1 {
			return Admission{Code: "token_limit_exceeded", KeyID: row.id}, nil
		}
		amount = want
	}
	if _, err = tx.Exec(`INSERT INTO reservations (request_id, key_id, reserved_tokens, created_at) VALUES (?,?,?,?)
ON CONFLICT(request_id) DO NOTHING`,
		in.RequestID, row.id, amount, now.Format(time.RFC3339Nano)); err != nil {
		return Admission{}, err
	}
	if err := tx.Commit(); err != nil {
		return Admission{}, err
	}
	return Admission{Allowed: true, KeyID: row.id, Reserved: amount}, nil
}

func (s *Store) RecordRejection(requestID, keyID string, httpStatus int) error {
	if strings.TrimSpace(requestID) == "" || strings.TrimSpace(keyID) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM requests WHERE request_id=?`, requestID).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	now := s.cfg.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(`UPDATE keys SET request_count=request_count+1, last_used_at=? WHERE id=?`, now, keyID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO requests (request_id, key_id, model, transport, protocol, status, http_status, latency_ms, ttft_ms, input_tokens, output_tokens, reasoning_tokens, cached_tokens, billed_tokens, requested_at, completed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		requestID, keyID, "", "http", "", "rejected", httpStatus, 0, 0, 0, 0, 0, 0, 0, now, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) NoteUsage(note UsageNote) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM requests WHERE request_id=?`, note.RequestID).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	failed := 0
	if note.Failed {
		failed = 1
	}
	_, err := s.db.Exec(`INSERT INTO usage_notes (request_id, input_tokens, output_tokens, reasoning_tokens, cached_tokens, failed) VALUES (?,?,?,?,?,?)
ON CONFLICT(request_id) DO UPDATE SET input_tokens=excluded.input_tokens, output_tokens=excluded.output_tokens, reasoning_tokens=excluded.reasoning_tokens, cached_tokens=excluded.cached_tokens, failed=excluded.failed`,
		note.RequestID, note.Input, note.Output, note.Reasoning, note.Cached, failed)
	return err
}

func (s *Store) Finalize(in FinalizeInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM requests WHERE request_id=?`, in.RequestID).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	var keyID string
	err = tx.QueryRow(`SELECT key_id FROM reservations WHERE request_id=?`, in.RequestID).Scan(&keyID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	input, output, reasoning, cached := in.Input, in.Output, in.Reasoning, in.Cached
	failed := false
	err = tx.QueryRow(`SELECT input_tokens, output_tokens, reasoning_tokens, cached_tokens, failed FROM usage_notes WHERE request_id=?`, in.RequestID).
		Scan(&input, &output, &reasoning, &cached, &failed)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	billed := int64(0)
	status := in.Outcome
	if status == "" {
		status = "succeeded"
	}
	if status == "succeeded" && !failed {
		billed = BilledTokens(input, output)
	} else if status == "succeeded" && failed {
		status = "failed"
	}
	now := s.cfg.Now().UTC()
	requested := in.RequestedAt
	if requested.IsZero() {
		requested = now
	}
	if billed > 0 {
		if _, err := tx.Exec(`UPDATE keys SET tokens_used=tokens_used+?, request_count=request_count+1, last_used_at=? WHERE id=?`, billed, now.Format(time.RFC3339Nano), keyID); err != nil {
			return err
		}
	} else if _, err := tx.Exec(`UPDATE keys SET request_count=request_count+1, last_used_at=? WHERE id=?`, now.Format(time.RFC3339Nano), keyID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO requests (request_id, key_id, model, transport, protocol, status, http_status, latency_ms, ttft_ms, input_tokens, output_tokens, reasoning_tokens, cached_tokens, billed_tokens, requested_at, completed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		in.RequestID, keyID, in.Model, in.Transport, in.Protocol, status, in.HTTPStatus, in.Latency.Milliseconds(), in.TTFT.Milliseconds(), input, output, reasoning, cached, billed, requested.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM reservations WHERE request_id=?`, in.RequestID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM usage_notes WHERE request_id=?`, in.RequestID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Requests(q RequestQuery) ([]Request, error) {
	rows, _, err := s.RequestsPage(q)
	return rows, err
}

func (s *Store) RequestsPage(q RequestQuery) ([]Request, PageInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	from, to := formatTime(&q.From), formatTime(&q.To)
	if q.From.IsZero() {
		from = ""
	}
	if q.To.IsZero() {
		to = ""
	}
	cursorTime, cursorID, err := decodeCursor(q.Cursor)
	if err != nil {
		return nil, PageInfo{}, err
	}
	query := `SELECT request_id, key_id, model, transport, protocol, status, http_status, latency_ms, ttft_ms, input_tokens, output_tokens, reasoning_tokens, cached_tokens, billed_tokens, requested_at, completed_at FROM requests WHERE (?='' OR key_id=?) AND (?='' OR status=?) AND (?='' OR model=?) AND (?='' OR requested_at>=?) AND (?='' OR requested_at<=?)`
	args := []any{q.KeyID, q.KeyID, q.Status, q.Status, q.Model, q.Model, from, from, to, to}
	if cursorTime != "" {
		query += ` AND (requested_at < ? OR (requested_at = ? AND request_id < ?))`
		args = append(args, cursorTime, cursorTime, cursorID)
	}
	query += ` ORDER BY requested_at DESC, request_id DESC LIMIT ? OFFSET ?`
	args = append(args, limit+1, offset)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, PageInfo{}, err
	}
	defer rows.Close()
	items, err := scanRequests(rows)
	if err != nil {
		return nil, PageInfo{}, err
	}
	page := PageInfo{}
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		page.NextCursor = encodeCursor(last.RequestedAt, last.RequestID)
		page.HasMore = true
	}
	return items, page, nil
}

func (s *Store) Session(secret string, limit int) (Session, error) {
	session, _, err := s.SessionPage(secret, limit, "")
	return session, err
}

func (s *Store) SessionPage(secret string, limit int, cursor string) (Session, PageInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.lookup(HashSecret(secret))
	if err != nil {
		return Session{}, PageInfo{}, err
	}
	key, err := s.keyByID(row.id)
	if err != nil {
		return Session{}, PageInfo{}, err
	}
	if limit <= 0 {
		limit = 30
	}
	cursorTime, cursorID, err := decodeCursor(cursor)
	if err != nil {
		return Session{}, PageInfo{}, err
	}
	query := `SELECT request_id, key_id, model, transport, protocol, status, http_status, latency_ms, ttft_ms, input_tokens, output_tokens, reasoning_tokens, cached_tokens, billed_tokens, requested_at, completed_at FROM requests WHERE key_id=?`
	args := []any{row.id}
	if cursorTime != "" {
		query += ` AND (requested_at < ? OR (requested_at = ? AND request_id < ?))`
		args = append(args, cursorTime, cursorTime, cursorID)
	}
	query += ` ORDER BY requested_at DESC, request_id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return Session{}, PageInfo{}, err
	}
	defer rows.Close()
	reqs, err := scanRequests(rows)
	if err != nil {
		return Session{}, PageInfo{}, err
	}
	page := PageInfo{}
	if len(reqs) > limit {
		reqs = reqs[:limit]
		last := reqs[len(reqs)-1]
		page.NextCursor = encodeCursor(last.RequestedAt, last.RequestID)
		page.HasMore = true
	}
	return Session{Key: key, Requests: reqs}, page, nil
}

func (s *Store) RunSweeper(ctx context.Context) {
	if s == nil || ctx == nil {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.Sweep()
		}
	}
}

func (s *Store) Sweep() error {
	for range 8 {
		n, err := s.sweepBatch()
		if err != nil || n == 0 {
			return err
		}
	}
	return nil
}

func (s *Store) sweepBatch() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cfg.Now().UTC()
	cutoff := now.AddDate(0, 0, -s.cfg.RetentionDays).Format(time.RFC3339Nano)
	deletedCutoff := now.AddDate(0, 0, -s.cfg.DeletedRetentionDays).Format(time.RFC3339Nano)
	reservationCutoff := now.Add(-s.cfg.ReservationTTL).Format(time.RFC3339Nano)
	total := 0
	deleteN := func(query string, args ...any) error {
		res, err := s.db.Exec(query, args...)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		total += int(n)
		return nil
	}
	if err := deleteN(`DELETE FROM reservations WHERE request_id IN (SELECT request_id FROM reservations WHERE created_at < ? LIMIT 500)`, reservationCutoff); err != nil {
		return total, err
	}
	// Notes are copied into the request row at finalize. Drop leftovers in small
	// batches so a large history cannot hold the admission lock.
	if err := deleteN(`DELETE FROM usage_notes WHERE request_id IN (SELECT notes.request_id FROM usage_notes AS notes INNER JOIN requests ON requests.request_id = notes.request_id LIMIT 500)`); err != nil {
		return total, err
	}
	if err := deleteN(`DELETE FROM usage_notes WHERE request_id IN (SELECT request_id FROM requests WHERE requested_at < ? LIMIT 500)`, cutoff); err != nil {
		return total, err
	}
	if err := deleteN(`DELETE FROM requests WHERE request_id IN (SELECT request_id FROM requests WHERE requested_at < ? LIMIT 500)`, cutoff); err != nil {
		return total, err
	}
	if err := deleteN(`DELETE FROM keys WHERE id IN (SELECT id FROM keys WHERE deleted_at IS NOT NULL AND deleted_at < ? LIMIT 500)`, deletedCutoff); err != nil {
		return total, err
	}
	if total > 0 {
		return total, nil
	}
	if err := deleteN(`DELETE FROM usage_notes WHERE request_id IN (SELECT request_id FROM usage_notes AS notes WHERE NOT EXISTS (SELECT 1 FROM requests WHERE requests.request_id = notes.request_id) AND NOT EXISTS (SELECT 1 FROM reservations WHERE reservations.request_id = notes.request_id) LIMIT 200)`); err != nil {
		return total, err
	}
	return total, nil
}

type requestCursor struct {
	Time string `json:"time"`
	ID   string `json:"id"`
}

func encodeCursor(at time.Time, id string) string {
	raw, _ := json.Marshal(requestCursor{Time: at.UTC().Format(time.RFC3339Nano), ID: id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(value string) (string, string, error) {
	if strings.TrimSpace(value) == "" {
		return "", "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", "", fmt.Errorf("invalid cursor")
	}
	var cursor requestCursor
	if json.Unmarshal(raw, &cursor) != nil || cursor.Time == "" || cursor.ID == "" {
		return "", "", fmt.Errorf("invalid cursor")
	}
	if _, err := time.Parse(time.RFC3339Nano, cursor.Time); err != nil {
		return "", "", fmt.Errorf("invalid cursor")
	}
	return cursor.Time, cursor.ID, nil
}

func (s *Store) secretHash(id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var hash []byte
	err := s.db.QueryRow(`SELECT secret_hash FROM keys WHERE id=?`, id).Scan(&hash)
	return hash, err
}

type keyRow struct {
	id, status, prefix, last4, label, customer, note string
	created                                          time.Time
	expires, lastUsed                                *time.Time
	limit, used, count                               int64
	deleted                                          bool
}

type rowQuery interface {
	QueryRow(query string, args ...any) *sql.Row
}

func (s *Store) lookup(hash []byte) (keyRow, error) {
	return s.lookupOn(s.db, hash)
}

func (s *Store) lookupOn(q rowQuery, hash []byte) (keyRow, error) {
	var row keyRow
	var created, expires, lastUsed, deleted sql.NullString
	err := q.QueryRow(`SELECT id, status, display_prefix, display_last4, label, customer, note, created_at, expires_at, token_limit, tokens_used, request_count, last_used_at, deleted_at FROM keys WHERE secret_hash=?`, hash).
		Scan(&row.id, &row.status, &row.prefix, &row.last4, &row.label, &row.customer, &row.note, &created, &expires, &row.limit, &row.used, &row.count, &lastUsed, &deleted)
	if err != nil {
		return keyRow{}, err
	}
	row.created = parseTime(created.String)
	row.expires = parseOptional(expires)
	row.lastUsed = parseOptional(lastUsed)
	row.deleted = deleted.Valid && deleted.String != ""
	return row, nil
}

func (s *Store) keyByID(id string) (Key, error) {
	row := s.db.QueryRow(`SELECT `+keyColumns+`, `+reservedByKey+` FROM keys WHERE keys.id=? AND keys.deleted_at IS NULL`, id)
	return s.scanKey(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanKey(sc rowScanner) (Key, error) {
	var row keyRow
	var created, expires, lastUsed sql.NullString
	var reserved int64
	if err := sc.Scan(&row.id, &row.prefix, &row.last4, &row.label, &row.customer, &row.note, &row.status, &created, &expires, &row.limit, &row.used, &row.count, &lastUsed, &reserved); err != nil {
		return Key{}, err
	}
	row.created = parseTime(created.String)
	row.expires = parseOptional(expires)
	row.lastUsed = parseOptional(lastUsed)
	return Key{
		ID: row.id, Display: Display(row.prefix, row.last4), Label: row.label, Customer: row.customer, Note: row.note,
		Status: s.computed(row, reserved), CreatedAt: row.created, ExpiresAt: row.expires, TokenLimit: row.limit,
		TokensUsed: row.used, RequestCount: row.count, LastUsedAt: row.lastUsed,
	}, nil
}

func (s *Store) computed(row keyRow, reserved int64) string {
	if row.status == "disabled" {
		return "disabled"
	}
	if row.expires != nil && !row.expires.After(s.cfg.Now().UTC()) {
		return "expired"
	}
	if row.limit > 0 && row.used+reserved >= row.limit {
		return "exhausted"
	}
	return "active"
}

func (s *Store) reserved(id string) (int64, error) {
	return s.reservedOn(s.db, id)
}

func (s *Store) reservedOn(q rowQuery, id string) (int64, error) {
	var n sql.NullInt64
	err := q.QueryRow(`SELECT COALESCE(SUM(reserved_tokens),0) FROM reservations WHERE key_id=?`, id).Scan(&n)
	return n.Int64, err
}

func (s *Store) expiry(never bool, seconds int64, set bool, now time.Time) *time.Time {
	if never {
		return nil
	}
	if !set || seconds == 0 {
		t := now.Add(s.cfg.DefaultTTL)
		return &t
	}
	t := now.Add(time.Duration(seconds) * time.Second)
	return &t
}

func (s *Store) limit(unlimited, set bool, value int64) int64 {
	if unlimited {
		return 0
	}
	if !set {
		return s.cfg.DefaultLimit
	}
	return value
}

func countPath(path string) bool {
	return strings.Contains(path, "count_tokens") || strings.Contains(path, "countTokens")
}

func like(q string) string {
	return "%" + q + "%"
}

func formatTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(v string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}
	}
	return t
}

func parseOptional(v sql.NullString) *time.Time {
	if !v.Valid || v.String == "" {
		return nil
	}
	t := parseTime(v.String)
	return &t
}

func scanRequests(rows *sql.Rows) ([]Request, error) {
	var out []Request
	for rows.Next() {
		var item Request
		var requested, completed string
		if err := rows.Scan(&item.RequestID, &item.KeyID, &item.Model, &item.Transport, &item.Protocol, &item.Status, &item.HTTPStatus, &item.LatencyMs, &item.TTFTMs, &item.Input, &item.Output, &item.Reasoning, &item.Cached, &item.Billed, &requested, &completed); err != nil {
			return nil, err
		}
		item.RequestedAt = parseTime(requested)
		item.CompletedAt = parseTime(completed)
		out = append(out, item)
	}
	return out, rows.Err()
}

func newID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
