package keyshop

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Config struct {
	DataFile       string
	DefaultTTL     time.Duration
	DefaultLimit   int64
	ReserveTokens  int64
	ReservationTTL time.Duration
	RetentionDays  int
	Now            func() time.Time
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
	Limit                int
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

func (s *Store) List(query string) ([]Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id FROM keys WHERE deleted_at IS NULL AND (label LIKE ? OR customer LIKE ? OR id LIKE ?) ORDER BY created_at DESC`, like(query), like(query), like(query))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Key
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		key, err := s.keyByID(id)
		if err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
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
		in.Label, in.Customer, in.Note, status, formatTime(expires), limit, in.ID)
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
	row, err := s.lookup(HashSecret(secret))
	if err != nil {
		return Key{}, err
	}
	if row.deleted {
		return Key{}, sql.ErrNoRows
	}
	return s.keyByID(row.id)
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
	row, err := s.lookupOn(tx, HashSecret(in.Secret))
	if err == sql.ErrNoRows {
		return Admission{Code: "key_not_found"}, nil
	}
	if err != nil {
		return Admission{}, err
	}
	if row.deleted {
		return Admission{Code: "key_not_found"}, nil
	}
	now := s.cfg.Now().UTC()
	if row.status == "disabled" {
		return Admission{Code: "key_disabled", KeyID: row.id}, nil
	}
	if row.expires != nil && !row.expires.After(now) {
		return Admission{Code: "key_expired", KeyID: row.id}, nil
	}
	reserved, err := s.reservedOn(tx, row.id)
	if err != nil {
		return Admission{}, err
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
	if _, err = tx.Exec(`INSERT INTO reservations (request_id, key_id, reserved_tokens, created_at) VALUES (?,?,?,?)`,
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
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM requests WHERE request_id=?`, requestID).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	now := s.cfg.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`UPDATE keys SET request_count=request_count+1, last_used_at=? WHERE id=?`, now, keyID); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO requests (request_id, key_id, model, transport, protocol, status, http_status, latency_ms, ttft_ms, input_tokens, output_tokens, reasoning_tokens, cached_tokens, billed_tokens, requested_at, completed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		requestID, keyID, "", "http", "", "rejected", httpStatus, 0, 0, 0, 0, 0, 0, 0, now, now)
	return err
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
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM requests WHERE request_id=?`, in.RequestID).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	var keyID string
	err := s.db.QueryRow(`SELECT key_id FROM reservations WHERE request_id=?`, in.RequestID).Scan(&keyID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	input, output, reasoning, cached := in.Input, in.Output, in.Reasoning, in.Cached
	failed := false
	err = s.db.QueryRow(`SELECT input_tokens, output_tokens, reasoning_tokens, cached_tokens, failed FROM usage_notes WHERE request_id=?`, in.RequestID).
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
		if _, err := s.db.Exec(`UPDATE keys SET tokens_used=tokens_used+?, request_count=request_count+1, last_used_at=? WHERE id=?`, billed, now.Format(time.RFC3339Nano), keyID); err != nil {
			return err
		}
	} else {
		if _, err := s.db.Exec(`UPDATE keys SET request_count=request_count+1, last_used_at=? WHERE id=?`, now.Format(time.RFC3339Nano), keyID); err != nil {
			return err
		}
	}
	_, err = s.db.Exec(`INSERT INTO requests (request_id, key_id, model, transport, protocol, status, http_status, latency_ms, ttft_ms, input_tokens, output_tokens, reasoning_tokens, cached_tokens, billed_tokens, requested_at, completed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		in.RequestID, keyID, in.Model, in.Transport, in.Protocol, status, in.HTTPStatus, in.Latency.Milliseconds(), in.TTFT.Milliseconds(), input, output, reasoning, cached, billed, requested.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM reservations WHERE request_id=?`, in.RequestID)
	return err
}

func (s *Store) Requests(q RequestQuery) ([]Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	from, to := formatTime(&q.From), formatTime(&q.To)
	if q.From.IsZero() {
		from = ""
	}
	if q.To.IsZero() {
		to = ""
	}
	rows, err := s.db.Query(`SELECT request_id, key_id, model, transport, protocol, status, http_status, latency_ms, ttft_ms, input_tokens, output_tokens, reasoning_tokens, cached_tokens, billed_tokens, requested_at, completed_at FROM requests WHERE (?='' OR key_id=?) AND (?='' OR status=?) AND (?='' OR model=?) AND (?='' OR requested_at>=?) AND (?='' OR requested_at<=?) ORDER BY requested_at DESC LIMIT ?`,
		q.KeyID, q.KeyID, q.Status, q.Status, q.Model, q.Model, from, from, to, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRequests(rows)
}

func (s *Store) Session(secret string, limit int) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.lookup(HashSecret(secret))
	if err != nil {
		return Session{}, err
	}
	key, err := s.keyByID(row.id)
	if err != nil {
		return Session{}, err
	}
	if limit <= 0 {
		limit = 30
	}
	rows, err := s.db.Query(`SELECT request_id, key_id, model, transport, protocol, status, http_status, latency_ms, ttft_ms, input_tokens, output_tokens, reasoning_tokens, cached_tokens, billed_tokens, requested_at, completed_at FROM requests WHERE key_id=? ORDER BY requested_at DESC LIMIT ?`, row.id, limit)
	if err != nil {
		return Session{}, err
	}
	defer rows.Close()
	reqs, err := scanRequests(rows)
	if err != nil {
		return Session{}, err
	}
	return Session{Key: key, Requests: reqs}, nil
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
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cfg.Now().UTC()
	if _, err := s.db.Exec(`DELETE FROM reservations WHERE created_at < ?`, now.Add(-s.cfg.ReservationTTL).Format(time.RFC3339Nano)); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM requests WHERE requested_at < ?`, now.AddDate(0, 0, -s.cfg.RetentionDays).Format(time.RFC3339Nano))
	return err
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
	var hash []byte
	if err := s.db.QueryRow(`SELECT secret_hash FROM keys WHERE id=? AND deleted_at IS NULL`, id).Scan(&hash); err != nil {
		return Key{}, err
	}
	row, err := s.lookup(hash)
	if err != nil {
		return Key{}, err
	}
	reserved, err := s.reserved(id)
	if err != nil {
		return Key{}, err
	}
	return Key{
		ID: id, Display: Display(row.prefix, row.last4), Label: row.label, Customer: row.customer, Note: row.note,
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
