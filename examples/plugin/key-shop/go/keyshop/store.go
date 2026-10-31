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

// DefaultTunnelWatchInterval is how often a managed tunnel is checked for a
// dead child and a new quick-tunnel URL. It is exported so the plugin config
// metadata reports the same value the manager uses.
const DefaultTunnelWatchInterval = 2 * time.Second

// DefaultTunnelLogMaxKB is the default cloudflared.log cap in KiB (1 MiB). It
// is exported so the plugin config metadata reports the same value the manager
// uses.
const DefaultTunnelLogMaxKB = bytesPerKB

type Config struct {
	DataFile             string
	DefaultTTL           time.Duration
	DefaultLimit         int64
	DefaultRequestLimit  int64
	ReserveTokens        int64
	ReservationTTL       time.Duration
	RetentionDays        int
	DeletedRetentionDays int
	ModelAccessMode      string
	Models               []ModelAccessModel
	CloudflaredPath      string
	TunnelsEnabled       *bool
	TunnelTargetURL      string
	Now                  func() time.Time

	// Partner API settings. Open fills the defaults for empty values; a non-nil
	// PartnerDailyKeyCap of 0 means unlimited, and nil makes Open set the
	// default. Validation of configured values belongs to the plugin reconfigure
	// path.
	PartnerAPIEnabled          bool
	PublicBaseURL              string
	PublicDashboardPath        string
	PartnerMaxQuantity         int64
	PartnerDailyKeyCap         *int64
	PartnerMaxBodyKB           int64
	PartnerUnconfirmedAlertMin int64
	PartnerLogRetentionDays    int64
	PartnerKeyLabelTemplate    string
	PartnerKeyNoteTemplate     string
}

type ModelAccessModel struct {
	Name  string `json:"name" yaml:"name"`
	Alias string `json:"alias,omitempty" yaml:"alias,omitempty"`
}

type Store struct {
	db  *sql.DB
	cfg Config
	mu  sync.Mutex
}

type Key struct {
	ID                    string
	Display               string
	Label                 string
	Customer              string
	Note                  string
	Status                string
	CreatedAt             time.Time
	ExpiresAt             *time.Time
	TokenLimit            int64
	TokensUsed            int64
	RequestCount          int64
	RequestLimit          int64
	RequestQuotaUsed      int64
	RequestSuccessCount   int64
	RequestFailureCount   int64
	RequestCanceledCount  int64
	RequestRejectedCount  int64
	RequestLimitReached   bool
	TokenLimitReached     bool
	RequestQuotaRemaining int64
	LastUsedAt            *time.Time
	GroupID               string
	PartnerID             string `json:"partner_id"`
	PartnerRef            string `json:"partner_ref"`
}

type CreateInput struct {
	Label, Customer, Note string
	PartnerID, PartnerRef string
	TTLSeconds            int64
	NeverExpires          bool
	TokenLimit            int64
	TokenLimitSet         bool
	UnlimitedTokens       bool
	RequestLimit          int64
	RequestLimitSet       bool
	UnlimitedRequests     bool
	GroupID               string
}

type UpdateInput struct {
	ID, Label, Customer, Note, Status string
	GroupID                           string
	GroupSet                          bool
	LabelSet, CustomerSet, NoteSet    bool
	StatusSet                         bool
	TTLSeconds                        int64
	TTLSet, NeverExpires              bool
	TokenLimit                        int64
	TokenLimitSet, UnlimitedTokens    bool
	RequestLimit                      int64
	RequestLimitSet                   bool
	UnlimitedRequests                 bool
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
	RequestID, Model, RequestedModel, Transport, Protocol, Outcome string
	HTTPStatus                                                     int
	Input, Output, Reasoning, Cached, Total                        int64
	Latency, TTFT                                                  time.Duration
	RequestedAt                                                    time.Time
}

type UsageNote struct {
	RequestID                        string
	Input, Output, Reasoning, Cached int64
	Total                            int64
	Failed                           bool
	TTFT, Latency                    time.Duration
	Provider, AuthID, AuthIndex      string
	AuthType, Effort, ServiceTier    string
}

type Request struct {
	RequestID     string
	KeyID         string
	Model         string
	Transport     string
	Protocol      string
	Status        string
	HTTPStatus    int
	LatencyMs     int64
	TTFTMs        int64
	Input         int64
	Output        int64
	Reasoning     int64
	Cached        int64
	Billed        int64
	RejectionCode string
	RequestedAt   time.Time
	CompletedAt   time.Time
}

type RequestQuery struct {
	KeyID, Status, Model, RejectionCode string
	From, To                            time.Time
	Limit, Offset                       int
	Cursor                              string
}

type PageInfo struct {
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
	Total      int    `json:"total"`
}

type StatsSummary struct {
	KeysTotal          int   `json:"keys_total"`
	KeysActive         int   `json:"keys_active"`
	KeysDisabled       int   `json:"keys_disabled"`
	KeysExpired        int   `json:"keys_expired"`
	KeysExhausted      int   `json:"keys_exhausted"`
	TokensUsedTotal    int64 `json:"tokens_used_total"`
	RequestsTotal      int64 `json:"requests_total"`
	RequestsAdmitted   int64 `json:"requests_admitted"`
	RequestsSucceeded  int64 `json:"requests_succeeded"`
	RequestsFailed     int64 `json:"requests_failed"`
	RequestsCanceled   int64 `json:"requests_canceled"`
	RequestsRejected   int64 `json:"requests_rejected"`
	KeysRequestLimited int   `json:"keys_request_limited"`
	ExpiringWithin7d   int   `json:"expiring_within_7d"`
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
	if cfg.PartnerMaxQuantity == 0 {
		cfg.PartnerMaxQuantity = 50
	}
	if cfg.PartnerDailyKeyCap == nil {
		cap := int64(300)
		cfg.PartnerDailyKeyCap = &cap
	}
	if cfg.PartnerMaxBodyKB == 0 {
		cfg.PartnerMaxBodyKB = 64
	}
	if cfg.PartnerUnconfirmedAlertMin == 0 {
		cfg.PartnerUnconfirmedAlertMin = 10
	}
	if cfg.PartnerLogRetentionDays == 0 {
		cfg.PartnerLogRetentionDays = 30
	}
	if strings.TrimSpace(cfg.PublicDashboardPath) == "" {
		cfg.PublicDashboardPath = "/client"
	}
	if strings.TrimSpace(cfg.PartnerKeyLabelTemplate) == "" {
		cfg.PartnerKeyLabelTemplate = "{plan}-{ref}-{i}"
	}
	if strings.TrimSpace(cfg.PartnerKeyNoteTemplate) == "" {
		cfg.PartnerKeyNoteTemplate = "partner:{partner} ref:{ref}"
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
	cfg.DataFile = path
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
  request_limit INTEGER NOT NULL DEFAULT 0,
  request_quota_used INTEGER NOT NULL DEFAULT 0,
  request_success_count INTEGER NOT NULL DEFAULT 0,
  request_failure_count INTEGER NOT NULL DEFAULT 0,
  request_canceled_count INTEGER NOT NULL DEFAULT 0,
  request_rejected_count INTEGER NOT NULL DEFAULT 0,
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
  completed_at TEXT NOT NULL,
  rejection_code TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS usage_notes (
  request_id TEXT PRIMARY KEY,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens INTEGER NOT NULL DEFAULT 0,
  cached_tokens INTEGER NOT NULL DEFAULT 0,
  total_tokens INTEGER NOT NULL DEFAULT 0,
  failed INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS model_groups (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  description TEXT NOT NULL DEFAULT '',
  all_models INTEGER NOT NULL DEFAULT 0,
  models_json TEXT NOT NULL DEFAULT '[]',
  aliases_json TEXT NOT NULL DEFAULT '{}',
  is_system INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tunnels (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL COLLATE NOCASE UNIQUE,
  kind TEXT NOT NULL,
  target_url TEXT NOT NULL DEFAULT '',
  token TEXT NOT NULL DEFAULT '',
  autostart INTEGER NOT NULL DEFAULT 0,
  desired TEXT NOT NULL DEFAULT 'stopped',
  pid INTEGER NOT NULL DEFAULT 0,
  public_url TEXT NOT NULL DEFAULT '',
  last_error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS plans (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  description TEXT NOT NULL DEFAULT '',
  group_id TEXT NOT NULL,
  ttl_seconds INTEGER NOT NULL DEFAULT 0,
  never_expires INTEGER NOT NULL DEFAULT 0,
  token_limit INTEGER NOT NULL DEFAULT 0,
  unlimited_tokens INTEGER NOT NULL DEFAULT 0,
  request_limit INTEGER NOT NULL DEFAULT 0,
  unlimited_requests INTEGER NOT NULL DEFAULT 0,
  active INTEGER NOT NULL DEFAULT 1,
  revision INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS partners (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL COLLATE NOCASE,
  secret_hash BLOB NOT NULL UNIQUE,
  secret_prefix TEXT NOT NULL,
  secret_last4 TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  allowed_plans_json TEXT NOT NULL DEFAULT '[]',
  daily_key_cap INTEGER,
  max_quantity INTEGER,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_used_at TEXT,
  deleted_at TEXT
);
CREATE TABLE IF NOT EXISTS partner_orders (
  partner_id TEXT NOT NULL,
  ref TEXT NOT NULL,
  plan_id TEXT NOT NULL DEFAULT '',
  plan_revision INTEGER NOT NULL DEFAULT 0,
  quantity INTEGER NOT NULL DEFAULT 0,
  fingerprint TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  order_id TEXT NOT NULL DEFAULT '',
  buyer TEXT NOT NULL DEFAULT '',
  customer TEXT NOT NULL DEFAULT '',
  void_reason TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  confirmed_at TEXT,
  voided_at TEXT,
  PRIMARY KEY (partner_id, ref)
);
CREATE TABLE IF NOT EXISTS partner_calls (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  partner_id TEXT NOT NULL DEFAULT '',
  at TEXT NOT NULL,
  op TEXT NOT NULL,
  http_status INTEGER NOT NULL,
  code TEXT NOT NULL DEFAULT '',
  ref TEXT NOT NULL DEFAULT '',
  latency_ms INTEGER NOT NULL DEFAULT 0,
  client_ip TEXT NOT NULL DEFAULT ''
);`)
	if err != nil {
		return err
	}
	if err = s.ensureColumns(); err != nil {
		return err
	}
	if _, err = s.db.Exec(`
CREATE INDEX IF NOT EXISTS idx_keys_created_at ON keys(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_keys_deleted_at ON keys(deleted_at);
CREATE INDEX IF NOT EXISTS idx_keys_live_created ON keys(created_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_reservations_key ON reservations(key_id);
CREATE INDEX IF NOT EXISTS idx_reservations_created ON reservations(created_at);
CREATE INDEX IF NOT EXISTS idx_requests_key_time ON requests(key_id, requested_at DESC, request_id DESC);
CREATE INDEX IF NOT EXISTS idx_requests_time ON requests(requested_at DESC, request_id DESC);
CREATE INDEX IF NOT EXISTS idx_requests_status_time ON requests(status, requested_at DESC, request_id DESC);
CREATE INDEX IF NOT EXISTS idx_requests_rejection ON requests(key_id, rejection_code, requested_at DESC);
CREATE INDEX IF NOT EXISTS idx_requests_model_time ON requests(model, requested_at DESC);
CREATE INDEX IF NOT EXISTS idx_keys_group ON keys(group_id);
CREATE INDEX IF NOT EXISTS idx_keys_partner ON keys(partner_id, partner_ref);
CREATE UNIQUE INDEX IF NOT EXISTS idx_partners_name_live ON partners(name) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_partner_orders_created ON partner_orders(partner_id, created_at);
CREATE INDEX IF NOT EXISTS idx_partner_orders_state ON partner_orders(state, created_at);
CREATE INDEX IF NOT EXISTS idx_partner_calls_at ON partner_calls(partner_id, at DESC);`); err != nil {
		return err
	}
	if err = s.seedSystemGroup(); err != nil {
		return err
	}
	return s.backfillRequestAccounting()
}

func (s *Store) ensureColumns() error {
	if err := addColumns(s.db, "keys", map[string]string{
		"request_limit":          "INTEGER NOT NULL DEFAULT 0",
		"request_quota_used":     "INTEGER NOT NULL DEFAULT 0",
		"request_success_count":  "INTEGER NOT NULL DEFAULT 0",
		"request_failure_count":  "INTEGER NOT NULL DEFAULT 0",
		"request_canceled_count": "INTEGER NOT NULL DEFAULT 0",
		"request_rejected_count": "INTEGER NOT NULL DEFAULT 0",
		"group_id":               "TEXT NOT NULL DEFAULT ''",
		"partner_id":             "TEXT NOT NULL DEFAULT ''",
		"partner_ref":            "TEXT NOT NULL DEFAULT ''",
	}); err != nil {
		return err
	}
	if err := addColumns(s.db, "usage_notes", map[string]string{
		"total_tokens": "INTEGER NOT NULL DEFAULT 0",
		"ttft_ms":      "INTEGER NOT NULL DEFAULT 0",
		"latency_ms":   "INTEGER NOT NULL DEFAULT 0",
		"provider":     "TEXT NOT NULL DEFAULT ''",
		"auth_id":      "TEXT NOT NULL DEFAULT ''",
		"auth_index":   "TEXT NOT NULL DEFAULT ''",
		"auth_type":    "TEXT NOT NULL DEFAULT ''",
		"effort":       "TEXT NOT NULL DEFAULT ''",
		"service_tier": "TEXT NOT NULL DEFAULT ''",
	}); err != nil {
		return err
	}
	if err := addColumns(s.db, "model_groups", map[string]string{
		"aliases_json": "TEXT NOT NULL DEFAULT '{}'",
	}); err != nil {
		return err
	}
	return addColumns(s.db, "requests", map[string]string{
		"rejection_code":  "TEXT NOT NULL DEFAULT ''",
		"requested_model": "TEXT NOT NULL DEFAULT ''",
		"provider":        "TEXT NOT NULL DEFAULT ''",
		"auth_id":         "TEXT NOT NULL DEFAULT ''",
		"auth_index":      "TEXT NOT NULL DEFAULT ''",
		"auth_type":       "TEXT NOT NULL DEFAULT ''",
		"effort":          "TEXT NOT NULL DEFAULT ''",
		"service_tier":    "TEXT NOT NULL DEFAULT ''",
	})
}

func addColumns(db *sql.DB, table string, columns map[string]string) error {
	existing, err := tableColumns(db, table)
	if err != nil {
		return err
	}
	for name, definition := range columns {
		if existing[name] {
			continue
		}
		if _, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + name + ` ` + definition); err != nil {
			return err
		}
	}
	return nil
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue sql.NullString
		if err = rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

func (s *Store) backfillRequestAccounting() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	var applied int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE version=1`).Scan(&applied); err != nil {
		return err
	}
	if applied > 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`UPDATE keys SET request_quota_used = request_count`); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE keys SET
		request_success_count = COALESCE((SELECT COUNT(1) FROM requests WHERE requests.key_id = keys.id AND requests.status = 'succeeded'), 0),
		request_failure_count = COALESCE((SELECT COUNT(1) FROM requests WHERE requests.key_id = keys.id AND requests.status = 'failed'), 0),
		request_canceled_count = COALESCE((SELECT COUNT(1) FROM requests WHERE requests.key_id = keys.id AND requests.status IN ('canceled', 'cancelled')), 0),
		request_rejected_count = COALESCE((SELECT COUNT(1) FROM requests WHERE requests.key_id = keys.id AND requests.status = 'rejected'), 0)`); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (1, ?)`, s.cfg.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	_, err = s.db.Exec(`PRAGMA user_version = 1`)
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
	return s.insertKeyOn(s.db, in, hash, prefix, last4)
}

// insertKeyOn runs the key insert on a caller-owned handle so a mint can share
// one immediate transaction with its order row. Every current key invariant
// still applies here.
func (s *Store) insertKeyOn(q execQuery, in CreateInput, hash []byte, prefix, last4 string) (Key, error) {
	id, err := newID()
	if err != nil {
		return Key{}, err
	}
	label := defaultLabel(in.Label, id)
	now := s.cfg.Now().UTC()
	expires := s.expiry(in.NeverExpires, in.TTLSeconds, true, now)
	limit := s.limit(in.UnlimitedTokens, in.TokenLimitSet, in.TokenLimit)
	requestLimit, err := s.requestLimit(in.UnlimitedRequests, in.RequestLimitSet, in.RequestLimit)
	if err != nil {
		return Key{}, err
	}
	if err = s.requireGroupOn(q, in.GroupID); err != nil {
		return Key{}, err
	}
	_, err = q.Exec(`INSERT INTO keys (id, secret_hash, display_prefix, display_last4, label, customer, note, status, created_at, expires_at, token_limit, request_limit, group_id, partner_id, partner_ref) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, hash, prefix, last4, label, in.Customer, in.Note, "active", now.Format(time.RFC3339Nano), formatTime(expires), limit, requestLimit, in.GroupID, in.PartnerID, in.PartnerRef)
	if err != nil {
		return Key{}, err
	}
	return s.keyByIDOn(q, id)
}

func defaultLabel(label, id string) string {
	if strings.TrimSpace(label) != "" {
		return label
	}
	if len(id) >= 8 {
		return "key-" + id[:8]
	}
	return "key-" + id
}

func batchLabel(label, id string) string {
	if strings.TrimSpace(label) == "" {
		return defaultLabel(label, id)
	}
	if len(id) >= 4 {
		return label + "-" + id[:4]
	}
	return label + "-" + id
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
COALESCE(SUM(request_quota_used), 0), COALESCE(SUM(request_success_count), 0), COALESCE(SUM(request_failure_count), 0), COALESCE(SUM(request_canceled_count), 0), COALESCE(SUM(request_rejected_count), 0),
COALESCE(SUM(CASE WHEN status='disabled' THEN 1 ELSE 0 END), 0),
COALESCE(SUM(CASE WHEN status <> 'disabled' AND expires_at IS NOT NULL AND expires_at <= ? THEN 1 ELSE 0 END), 0),
COALESCE(SUM(CASE WHEN status <> 'disabled' AND (expires_at IS NULL OR expires_at > ?) AND token_limit > 0 AND tokens_used >= token_limit THEN 1 ELSE 0 END), 0),
COALESCE(SUM(CASE WHEN status <> 'disabled' AND (expires_at IS NULL OR expires_at > ?) AND request_limit > 0 AND request_quota_used >= request_limit THEN 1 ELSE 0 END), 0),
COALESCE(SUM(CASE WHEN status <> 'disabled' AND expires_at > ? AND expires_at <= ? THEN 1 ELSE 0 END), 0),
COALESCE(SUM(CASE WHEN status <> 'disabled' AND (expires_at IS NULL OR expires_at > ?) AND (token_limit=0 OR tokens_used < token_limit) AND (request_limit=0 OR request_quota_used < request_limit) THEN 1 ELSE 0 END), 0)
FROM keys WHERE deleted_at IS NULL`, nowText, nowText, nowText, nowText, withinText, nowText).
		Scan(&out.KeysTotal, &out.TokensUsedTotal, &out.RequestsTotal, &out.RequestsAdmitted, &out.RequestsSucceeded, &out.RequestsFailed, &out.RequestsCanceled, &out.RequestsRejected, &out.KeysDisabled, &out.KeysExpired, &out.KeysExhausted, &out.KeysRequestLimited, &out.ExpiringWithin7d, &out.KeysActive)
	if err != nil {
		return StatsSummary{}, err
	}
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
	return s.ListPageGroup(query, status, "", page, pageSize)
}

// ListPageGroup is ListPageFiltered limited to one model group. "none" selects legacy keys without a group.
func (s *Store) ListPageGroup(query, status, group string, page, pageSize int) ([]Key, int, error) {
	return s.ListPagePartner(query, status, group, "", "", page, pageSize)
}

// ListPagePartner is ListPageGroup limited to one partner and/or one partner ref.
func (s *Store) ListPagePartner(query, status, group, partner, ref string, page, pageSize int) ([]Key, int, error) {
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
	switch group {
	case "":
	case "none":
		where += ` AND keys.group_id=''`
	default:
		where += ` AND keys.group_id=?`
		args = append(args, group)
	}
	if partner = strings.TrimSpace(partner); partner != "" {
		where += ` AND keys.partner_id=?`
		args = append(args, partner)
	}
	if ref = strings.TrimSpace(ref); ref != "" {
		where += ` AND keys.partner_ref=?`
		args = append(args, ref)
	}
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
		where += ` AND keys.status='active' AND (keys.expires_at IS NULL OR keys.expires_at > ?) AND (keys.token_limit=0 OR keys.tokens_used + ` + held + ` < keys.token_limit) AND (keys.request_limit=0 OR keys.request_quota_used < keys.request_limit)`
		args = append(args, nowText)
	case "disabled":
		where += ` AND keys.status='disabled'`
	case "expired":
		where += ` AND keys.status<>'disabled' AND keys.expires_at IS NOT NULL AND keys.expires_at <= ?`
		args = append(args, nowText)
	case "exhausted":
		where += ` AND keys.status<>'disabled' AND (keys.expires_at IS NULL OR keys.expires_at > ?) AND ((keys.token_limit > 0 AND keys.tokens_used + ` + held + ` >= keys.token_limit) OR (keys.request_limit > 0 AND keys.request_quota_used >= keys.request_limit))`
		args = append(args, nowText)
	}
	return where, args
}

const keyColumns = `keys.id, keys.display_prefix, keys.display_last4, keys.label, keys.customer, keys.note, keys.status, keys.created_at, keys.expires_at, keys.token_limit, keys.tokens_used, keys.request_count, keys.last_used_at, keys.request_limit, keys.request_quota_used, keys.request_success_count, keys.request_failure_count, keys.request_canceled_count, keys.request_rejected_count, keys.group_id, keys.partner_id, keys.partner_ref`

const requestColumns = `request_id, key_id, model, transport, protocol, status, http_status, latency_ms, ttft_ms, input_tokens, output_tokens, reasoning_tokens, cached_tokens, billed_tokens, requested_at, completed_at, rejection_code`

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
	requestLimit := key.RequestLimit
	if in.UnlimitedRequests {
		requestLimit = 0
	} else if in.RequestLimitSet {
		if in.RequestLimit < 0 {
			return Key{}, fmt.Errorf("invalid request limit")
		}
		requestLimit = in.RequestLimit
	}
	groupID := key.GroupID
	if in.GroupSet {
		if strings.TrimSpace(in.GroupID) == "" {
			return Key{}, ErrGroupRequired
		}
		if err = s.requireGroupOn(s.db, in.GroupID); err != nil {
			return Key{}, err
		}
		groupID = in.GroupID
	}
	_, err = s.db.Exec(`UPDATE keys SET label=?, customer=?, note=?, status=?, expires_at=?, token_limit=?, request_limit=?, group_id=? WHERE id=? AND deleted_at IS NULL`,
		label, customer, note, status, formatTime(expires), limit, requestLimit, groupID, in.ID)
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
	if strings.TrimSpace(in.RequestID) == "" {
		return Admission{Code: "request_id_required"}, nil
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
	err = tx.QueryRow(`SELECT keys.id, keys.status, keys.expires_at, keys.token_limit, keys.tokens_used, `+reservedByKey+`, keys.request_limit, keys.request_quota_used FROM keys WHERE keys.secret_hash=? AND keys.deleted_at IS NULL`, HashSecret(in.Secret)).
		Scan(&row.id, &row.status, &expires, &row.limit, &row.used, &reserved, &row.requestLimit, &row.quotaUsed)
	if err == sql.ErrNoRows {
		return Admission{Code: "key_not_found"}, nil
	}
	if err != nil {
		return Admission{}, err
	}
	row.expires = parseOptional(expires)
	if admission, done, err := s.existingAdmission(tx, in.RequestID, row.id); err != nil || done {
		return admission, err
	}
	now := s.cfg.Now().UTC()
	if row.status == "disabled" {
		return Admission{Code: "key_disabled", KeyID: row.id}, nil
	}
	if row.expires != nil && !row.expires.After(now) {
		return Admission{Code: "key_expired", KeyID: row.id}, nil
	}
	if row.requestLimit > 0 && row.quotaUsed >= row.requestLimit {
		return Admission{Code: "request_limit_exceeded", KeyID: row.id}, nil
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
	nowText := now.Format(time.RFC3339Nano)
	res, err := tx.Exec(`UPDATE keys SET request_count=request_count+1, request_quota_used=request_quota_used+1, last_used_at=? WHERE id=? AND deleted_at IS NULL AND (request_limit=0 OR request_quota_used < request_limit)`, nowText, row.id)
	if err != nil {
		return Admission{}, err
	}
	changed, _ := res.RowsAffected()
	if changed != 1 {
		return Admission{Code: "request_limit_exceeded", KeyID: row.id}, nil
	}
	if _, err = tx.Exec(`INSERT INTO requests (`+requestColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		in.RequestID, row.id, "", "http", "", "pending", 0, 0, 0, 0, 0, 0, 0, 0, nowText, nowText, ""); err != nil {
		return Admission{}, err
	}
	if _, err = tx.Exec(`INSERT INTO reservations (request_id, key_id, reserved_tokens, created_at) VALUES (?,?,?,?)`,
		in.RequestID, row.id, amount, nowText); err != nil {
		return Admission{}, err
	}
	if err := tx.Commit(); err != nil {
		return Admission{}, err
	}
	return Admission{Allowed: true, KeyID: row.id, Reserved: amount}, nil
}

func (s *Store) existingAdmission(tx *sql.Tx, requestID, keyID string) (Admission, bool, error) {
	var existingKey, existingStatus, existingCode string
	err := tx.QueryRow(`SELECT key_id, status, rejection_code FROM requests WHERE request_id=?`, requestID).Scan(&existingKey, &existingStatus, &existingCode)
	if err != nil && err != sql.ErrNoRows {
		return Admission{}, false, err
	}
	if err == nil {
		if existingKey != keyID {
			return Admission{Code: "request_id_conflict", KeyID: keyID}, true, nil
		}
		switch existingStatus {
		case "pending":
			var held int64
			_ = tx.QueryRow(`SELECT reserved_tokens FROM reservations WHERE request_id=?`, requestID).Scan(&held)
			return Admission{Allowed: true, KeyID: keyID, Reserved: held}, true, nil
		case "rejected":
			if existingCode == "" {
				existingCode = "rejected"
			}
			return Admission{Code: existingCode, KeyID: keyID}, true, nil
		default:
			return Admission{Code: "duplicate_request", KeyID: keyID}, true, nil
		}
	}
	var reservationKey string
	err = tx.QueryRow(`SELECT key_id FROM reservations WHERE request_id=?`, requestID).Scan(&reservationKey)
	if err == sql.ErrNoRows {
		return Admission{}, false, nil
	}
	if err != nil {
		return Admission{}, false, err
	}
	if reservationKey != keyID {
		return Admission{Code: "request_id_conflict", KeyID: keyID}, true, nil
	}
	var held int64
	_ = tx.QueryRow(`SELECT reserved_tokens FROM reservations WHERE request_id=?`, requestID).Scan(&held)
	return Admission{Allowed: true, KeyID: keyID, Reserved: held}, true, nil
}

func (s *Store) RecordRejection(requestID, keyID, code string, httpStatus int, models ...string) error {
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
	var status string
	err = tx.QueryRow(`SELECT status FROM requests WHERE request_id=?`, requestID).Scan(&status)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	now := s.cfg.Now().UTC().Format(time.RFC3339Nano)
	model, requested := "", ""
	if len(models) > 0 {
		model = strings.TrimSpace(models[0])
	}
	if len(models) > 1 {
		requested = strings.TrimSpace(models[1])
	}
	if _, err = tx.Exec(`UPDATE keys SET request_count=request_count+1, request_quota_used=request_quota_used+1, request_rejected_count=request_rejected_count+1, last_used_at=? WHERE id=? AND deleted_at IS NULL`, now, keyID); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO requests (`+requestColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		requestID, keyID, model, "http", "", "rejected", httpStatus, 0, 0, 0, 0, 0, 0, 0, now, now, code); err != nil {
		return err
	}
	if requested != "" {
		if _, err = tx.Exec(`UPDATE requests SET requested_model=? WHERE request_id=?`, requested, requestID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) RejectSecret(secret, requestID, code string, httpStatus int) error {
	if !strings.HasPrefix(secret, "ck_") || strings.TrimSpace(requestID) == "" {
		return nil
	}
	key, err := s.Lookup(secret)
	if err != nil {
		return nil
	}
	return s.RecordRejection(requestID, key.ID, code, httpStatus)
}

func (s *Store) NoteUsage(note UsageNote) error {
	if strings.TrimSpace(note.RequestID) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var status string
	err := s.db.QueryRow(`SELECT status FROM requests WHERE request_id=?`, note.RequestID).Scan(&status)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && status != "pending" {
		return s.applyLateUsage(note)
	}
	failed := 0
	if note.Failed {
		failed = 1
	}
	_, err = s.db.Exec(`INSERT INTO usage_notes (request_id, input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens, failed, ttft_ms, latency_ms, provider, auth_id, auth_index, auth_type, effort, service_tier) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(request_id) DO UPDATE SET input_tokens=excluded.input_tokens, output_tokens=excluded.output_tokens, reasoning_tokens=excluded.reasoning_tokens, cached_tokens=excluded.cached_tokens, total_tokens=excluded.total_tokens, failed=excluded.failed,
  ttft_ms=excluded.ttft_ms, latency_ms=excluded.latency_ms, provider=excluded.provider, auth_id=excluded.auth_id, auth_index=excluded.auth_index, auth_type=excluded.auth_type, effort=excluded.effort, service_tier=excluded.service_tier`,
		note.RequestID, note.Input, note.Output, note.Reasoning, note.Cached, note.Total, failed, note.TTFT.Milliseconds(), note.Latency.Milliseconds(), note.Provider, note.AuthID, note.AuthIndex, note.AuthType, note.Effort, note.ServiceTier)
	return err
}

// applyLateUsage fills a row that finalized before the usage note arrived.
// The host completion payload does not carry latency, and a fast completion
// can also land before token counts. Billing is applied only when the row
// has not already billed.
func (s *Store) applyLateUsage(note UsageNote) error {
	var status string
	var input, output, latency, ttft, billed int64
	var provider string
	err := s.db.QueryRow(`SELECT status, input_tokens, output_tokens, latency_ms, ttft_ms, billed_tokens, provider FROM requests WHERE request_id=?`, note.RequestID).
		Scan(&status, &input, &output, &latency, &ttft, &billed, &provider)
	if err != nil {
		return err
	}
	nextLatency := latency
	if nextLatency <= 0 && note.Latency > 0 {
		nextLatency = note.Latency.Milliseconds()
	}
	nextTTFT := ttft
	if nextTTFT <= 0 && note.TTFT > 0 {
		nextTTFT = note.TTFT.Milliseconds()
	}
	nextInput, nextOutput, nextReasoning, nextCached := input, output, int64(0), int64(0)
	var reasoning, cached int64
	_ = s.db.QueryRow(`SELECT reasoning_tokens, cached_tokens FROM requests WHERE request_id=?`, note.RequestID).Scan(&reasoning, &cached)
	nextReasoning, nextCached = reasoning, cached
	addBilled := int64(0)
	if input+output == 0 && (note.Input+note.Output > 0 || note.Total > 0) {
		nextInput, nextOutput, nextReasoning, nextCached = note.Input, note.Output, note.Reasoning, note.Cached
		if status == "succeeded" && billed == 0 {
			addBilled = BilledTokens(note.Input, note.Output, note.Total)
		}
	}
	if _, err = s.db.Exec(`UPDATE requests SET input_tokens=?, output_tokens=?, reasoning_tokens=?, cached_tokens=?, billed_tokens=billed_tokens+?, latency_ms=?, ttft_ms=?,
provider=CASE WHEN provider='' THEN ? ELSE provider END,
auth_id=CASE WHEN auth_id='' THEN ? ELSE auth_id END,
auth_index=CASE WHEN auth_index='' THEN ? ELSE auth_index END,
auth_type=CASE WHEN auth_type='' THEN ? ELSE auth_type END,
effort=CASE WHEN effort='' THEN ? ELSE effort END,
service_tier=CASE WHEN service_tier='' THEN ? ELSE service_tier END
WHERE request_id=?`,
		nextInput, nextOutput, nextReasoning, nextCached, addBilled, nextLatency, nextTTFT,
		note.Provider, note.AuthID, note.AuthIndex, note.AuthType, note.Effort, note.ServiceTier, note.RequestID); err != nil {
		return err
	}
	if addBilled > 0 {
		_, err = s.db.Exec(`UPDATE keys SET tokens_used=tokens_used+? WHERE id=(SELECT key_id FROM requests WHERE request_id=?)`, addBilled, note.RequestID)
	}
	return err
}

func (s *Store) Finalize(in FinalizeInput) error {
	if strings.TrimSpace(in.RequestID) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var keyID, status string
	err = tx.QueryRow(`SELECT key_id, status FROM requests WHERE request_id=?`, in.RequestID).Scan(&keyID, &status)
	if err == sql.ErrNoRows {
		return s.finalizeLegacy(tx, in)
	}
	if err != nil {
		return err
	}
	if status != "pending" {
		return nil
	}
	return s.finalizePending(tx, in, keyID, false)
}

func (s *Store) finalizeLegacy(tx *sql.Tx, in FinalizeInput) error {
	var keyID string
	err := tx.QueryRow(`SELECT key_id FROM reservations WHERE request_id=?`, in.RequestID).Scan(&keyID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return s.finalizePending(tx, in, keyID, true)
}

func (s *Store) finalizePending(tx *sql.Tx, in FinalizeInput, keyID string, legacy bool) error {
	input, output, reasoning, cached, total, failed, meta, err := usageSnapshot(tx, in)
	if err != nil {
		return err
	}
	if in.TTFT <= 0 && meta.TTFTMs > 0 {
		in.TTFT = time.Duration(meta.TTFTMs) * time.Millisecond
	}
	if in.Latency <= 0 && meta.LatencyMs > 0 {
		in.Latency = time.Duration(meta.LatencyMs) * time.Millisecond
	}
	status, column, bill := requestOutcome(in.Outcome, failed)
	billed := int64(0)
	if bill {
		billed = BilledTokens(input, output, total)
	}
	now := s.cfg.Now().UTC()
	requested := in.RequestedAt
	if requested.IsZero() {
		requested = now
	}
	requestedText := requested.UTC().Format(time.RFC3339Nano)
	completedText := now.Format(time.RFC3339Nano)
	if legacy {
		if _, err = tx.Exec(`INSERT INTO requests (`+requestColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			in.RequestID, keyID, in.Model, in.Transport, in.Protocol, status, in.HTTPStatus, in.Latency.Milliseconds(), in.TTFT.Milliseconds(), input, output, reasoning, cached, billed, requestedText, completedText, ""); err != nil {
			return err
		}
	} else {
		res, errUpdate := tx.Exec(`UPDATE requests SET model=?, transport=?, protocol=?, status=?, http_status=?, latency_ms=?, ttft_ms=?, input_tokens=?, output_tokens=?, reasoning_tokens=?, cached_tokens=?, billed_tokens=?, requested_at=?, completed_at=?, rejection_code='' WHERE request_id=? AND status='pending'`,
			in.Model, in.Transport, in.Protocol, status, in.HTTPStatus, in.Latency.Milliseconds(), in.TTFT.Milliseconds(), input, output, reasoning, cached, billed, requestedText, completedText, in.RequestID)
		if errUpdate != nil {
			return errUpdate
		}
		changed, _ := res.RowsAffected()
		if changed != 1 {
			return nil
		}
	}
	if _, err = tx.Exec(`UPDATE requests SET requested_model=?, provider=?, auth_id=?, auth_index=?, auth_type=?, effort=?, service_tier=? WHERE request_id=?`,
		in.RequestedModel, meta.Provider, meta.AuthID, meta.AuthIndex, meta.AuthType, meta.Effort, meta.ServiceTier, in.RequestID); err != nil {
		return err
	}
	if err = incrementOutcome(tx, column, billed, keyID, legacy, completedText); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM reservations WHERE request_id=?`, in.RequestID); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM usage_notes WHERE request_id=?`, in.RequestID); err != nil {
		return err
	}
	return tx.Commit()
}

// usageMeta carries the non-token facts a usage note records about a request.
type usageMeta struct {
	TTFTMs, LatencyMs             int64
	Provider, AuthID, AuthIndex   string
	AuthType, Effort, ServiceTier string
}

func usageSnapshot(tx *sql.Tx, in FinalizeInput) (int64, int64, int64, int64, int64, bool, usageMeta, error) {
	input, output, reasoning, cached, total := in.Input, in.Output, in.Reasoning, in.Cached, in.Total
	var failed bool
	var meta usageMeta
	err := tx.QueryRow(`SELECT input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens, failed, ttft_ms, latency_ms, provider, auth_id, auth_index, auth_type, effort, service_tier FROM usage_notes WHERE request_id=?`, in.RequestID).
		Scan(&input, &output, &reasoning, &cached, &total, &failed, &meta.TTFTMs, &meta.LatencyMs, &meta.Provider, &meta.AuthID, &meta.AuthIndex, &meta.AuthType, &meta.Effort, &meta.ServiceTier)
	if err == sql.ErrNoRows {
		return input, output, reasoning, cached, total, false, usageMeta{}, nil
	}
	if err != nil {
		return 0, 0, 0, 0, 0, false, usageMeta{}, err
	}
	return input, output, reasoning, cached, total, failed, meta, nil
}

func requestOutcome(outcome string, usageFailed bool) (string, string, bool) {
	switch strings.ToLower(strings.TrimSpace(outcome)) {
	case "canceled", "cancelled":
		return "canceled", "request_canceled_count", false
	case "failed":
		return "failed", "request_failure_count", false
	case "", "succeeded":
		if usageFailed {
			return "failed", "request_failure_count", false
		}
		return "succeeded", "request_success_count", true
	default:
		return "failed", "request_failure_count", false
	}
}

func incrementOutcome(tx *sql.Tx, column string, billed int64, keyID string, countAdmission bool, usedAt string) error {
	quota := ""
	if countAdmission {
		quota = `, request_count=request_count+1, request_quota_used=request_quota_used+1, last_used_at=?`
	}
	switch column {
	case "request_success_count", "request_failure_count", "request_canceled_count":
	default:
		return fmt.Errorf("unknown request outcome")
	}
	if countAdmission {
		if billed > 0 {
			_, err := tx.Exec(`UPDATE keys SET tokens_used=tokens_used+?, `+column+`=`+column+`+1`+quota+` WHERE id=?`, billed, usedAt, keyID)
			return err
		}
		_, err := tx.Exec(`UPDATE keys SET `+column+`=`+column+`+1`+quota+` WHERE id=?`, usedAt, keyID)
		return err
	}
	if billed > 0 {
		_, err := tx.Exec(`UPDATE keys SET tokens_used=tokens_used+?, `+column+`=`+column+`+1 WHERE id=?`, billed, keyID)
		return err
	}
	_, err := tx.Exec(`UPDATE keys SET `+column+`=`+column+`+1 WHERE id=?`, keyID)
	return err
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
	filter := `(?='' OR key_id=?) AND (?='' OR status=?) AND (?='' OR model=?) AND (?='' OR rejection_code=?) AND (?='' OR requested_at>=?) AND (?='' OR requested_at<=?)`
	filterArgs := []any{q.KeyID, q.KeyID, q.Status, q.Status, q.Model, q.Model, q.RejectionCode, q.RejectionCode, from, from, to, to}
	var total int
	if err = s.db.QueryRow(`SELECT COUNT(1) FROM requests WHERE `+filter, filterArgs...).Scan(&total); err != nil {
		return nil, PageInfo{}, err
	}
	query := `SELECT ` + requestColumns + ` FROM requests WHERE ` + filter
	args := append([]any{}, filterArgs...)
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
	page := PageInfo{Total: total}
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
	var total int
	if err = s.db.QueryRow(`SELECT COUNT(1) FROM requests WHERE key_id=?`, row.id).Scan(&total); err != nil {
		return Session{}, PageInfo{}, err
	}
	query := `SELECT ` + requestColumns + ` FROM requests WHERE key_id=?`
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
	page := PageInfo{Total: total}
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

func (s *Store) cancelExpiredLocked(now time.Time, cutoff string) (int, error) {
	rows, err := s.db.Query(`SELECT request_id, key_id FROM reservations WHERE created_at < ? LIMIT 500`, cutoff)
	if err != nil {
		return 0, err
	}
	type expiredReservation struct {
		requestID string
		keyID     string
	}
	var expired []expiredReservation
	for rows.Next() {
		var item expiredReservation
		if err = rows.Scan(&item.requestID, &item.keyID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		expired = append(expired, item)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	if len(expired) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	nowText := now.Format(time.RFC3339Nano)
	canceled := 0
	for _, item := range expired {
		res, errExec := tx.Exec(`UPDATE requests SET status='canceled', completed_at=? WHERE request_id=? AND key_id=? AND status='pending'`, nowText, item.requestID, item.keyID)
		if errExec != nil {
			return 0, errExec
		}
		changed, _ := res.RowsAffected()
		if changed == 1 {
			if _, errExec = tx.Exec(`UPDATE keys SET request_canceled_count=request_canceled_count+1 WHERE id=?`, item.keyID); errExec != nil {
				return 0, errExec
			}
			if errExec = copyUsageNote(tx, item.requestID); errExec != nil {
				return 0, errExec
			}
			canceled++
		}
		if _, errExec = tx.Exec(`DELETE FROM reservations WHERE request_id=?`, item.requestID); errExec != nil {
			return 0, errExec
		}
		if _, errExec = tx.Exec(`DELETE FROM usage_notes WHERE request_id=?`, item.requestID); errExec != nil {
			return 0, errExec
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return canceled, nil
}

// copyUsageNote writes a staged usage note onto its request before the note is deleted.
// Reservation expiry cancels the row in the same transaction, so the log would otherwise
// keep zero tokens, latency, and account. Canceled rows are not billed.
func copyUsageNote(tx *sql.Tx, requestID string) error {
	var input, output, reasoning, cached, ttft, latency int64
	var provider, authID, authIndex, authType, effort, tier string
	err := tx.QueryRow(`SELECT input_tokens, output_tokens, reasoning_tokens, cached_tokens, ttft_ms, latency_ms, provider, auth_id, auth_index, auth_type, effort, service_tier FROM usage_notes WHERE request_id=?`, requestID).
		Scan(&input, &output, &reasoning, &cached, &ttft, &latency, &provider, &authID, &authIndex, &authType, &effort, &tier)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE requests SET input_tokens=?, output_tokens=?, reasoning_tokens=?, cached_tokens=?,
ttft_ms=CASE WHEN ttft_ms<=0 THEN ? ELSE ttft_ms END,
latency_ms=CASE WHEN latency_ms<=0 THEN ? ELSE latency_ms END,
provider=CASE WHEN provider='' THEN ? ELSE provider END,
auth_id=CASE WHEN auth_id='' THEN ? ELSE auth_id END,
auth_index=CASE WHEN auth_index='' THEN ? ELSE auth_index END,
auth_type=CASE WHEN auth_type='' THEN ? ELSE auth_type END,
effort=CASE WHEN effort='' THEN ? ELSE effort END,
service_tier=CASE WHEN service_tier='' THEN ? ELSE service_tier END
WHERE request_id=?`,
		input, output, reasoning, cached, ttft, latency, provider, authID, authIndex, authType, effort, tier, requestID)
	return err
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
	canceled, err := s.cancelExpiredLocked(now, reservationCutoff)
	if err != nil {
		return total, err
	}
	total += canceled
	if err := deleteN(`DELETE FROM reservations WHERE request_id IN (SELECT request_id FROM reservations WHERE created_at < ? LIMIT 500)`, reservationCutoff); err != nil {
		return total, err
	}
	// Notes are copied into the request row at finalize. Drop leftovers for
	// terminal rows only. A pending row still needs its note: NoteUsage stages
	// tokens and account facts there, and Finalize copies them. Deleting that
	// note between usage.handle and request.complete leaves zeros and never bills.
	if err := deleteN(`DELETE FROM usage_notes WHERE request_id IN (SELECT notes.request_id FROM usage_notes AS notes INNER JOIN requests ON requests.request_id = notes.request_id WHERE requests.status<>'pending' LIMIT 500)`); err != nil {
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
	partnerCutoff := partnerTime(now.AddDate(0, 0, -int(s.cfg.PartnerLogRetentionDays)))
	if err := deleteN(`DELETE FROM partner_calls WHERE id IN (SELECT id FROM partner_calls WHERE at < ? LIMIT 500)`, partnerCutoff); err != nil {
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
	requestLimit, quotaUsed, success, failure        int64
	canceled, rejected                               int64
	deleted                                          bool
	group                                            string
	partnerID, partnerRef                            string
}

type rowQuery interface {
	QueryRow(query string, args ...any) *sql.Row
}

// execQuery is the subset shared by *sql.DB and *sql.Tx.
type execQuery interface {
	rowQuery
	Exec(query string, args ...any) (sql.Result, error)
}

// querySource adds row iteration for reads inside or outside a transaction.
type querySource interface {
	execQuery
	Query(query string, args ...any) (*sql.Rows, error)
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
	return s.keyByIDOn(s.db, id)
}

func (s *Store) keyByIDOn(q rowQuery, id string) (Key, error) {
	return s.scanKey(q.QueryRow(`SELECT `+keyColumns+`, `+reservedByKey+` FROM keys WHERE keys.id=? AND keys.deleted_at IS NULL`, id))
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanKey(sc rowScanner) (Key, error) {
	var row keyRow
	var created, expires, lastUsed sql.NullString
	var reserved int64
	if err := sc.Scan(&row.id, &row.prefix, &row.last4, &row.label, &row.customer, &row.note, &row.status, &created, &expires, &row.limit, &row.used, &row.count, &lastUsed, &row.requestLimit, &row.quotaUsed, &row.success, &row.failure, &row.canceled, &row.rejected, &row.group, &row.partnerID, &row.partnerRef, &reserved); err != nil {
		return Key{}, err
	}
	row.created = parseTime(created.String)
	row.expires = parseOptional(expires)
	row.lastUsed = parseOptional(lastUsed)
	tokenReached := row.limit > 0 && row.used+reserved >= row.limit
	requestReached := row.requestLimit > 0 && row.quotaUsed >= row.requestLimit
	remaining := int64(0)
	if row.requestLimit > 0 {
		remaining = row.requestLimit - row.quotaUsed
		if remaining < 0 {
			remaining = 0
		}
	}
	return Key{
		ID: row.id, Display: Display(row.prefix, row.last4), Label: row.label, Customer: row.customer, Note: row.note,
		Status: s.computed(row, reserved), CreatedAt: row.created, ExpiresAt: row.expires, TokenLimit: row.limit,
		TokensUsed: row.used, RequestCount: row.count, RequestLimit: row.requestLimit, RequestQuotaUsed: row.quotaUsed,
		RequestSuccessCount: row.success, RequestFailureCount: row.failure, RequestCanceledCount: row.canceled,
		RequestRejectedCount: row.rejected, RequestLimitReached: requestReached, TokenLimitReached: tokenReached,
		RequestQuotaRemaining: remaining, LastUsedAt: row.lastUsed, GroupID: row.group, PartnerID: row.partnerID, PartnerRef: row.partnerRef,
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
	if row.requestLimit > 0 && row.quotaUsed >= row.requestLimit {
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

func (s *Store) requestLimit(unlimited, set bool, value int64) (int64, error) {
	if unlimited {
		return 0, nil
	}
	if !set {
		if s.cfg.DefaultRequestLimit < 0 {
			return 0, fmt.Errorf("invalid request limit")
		}
		return s.cfg.DefaultRequestLimit, nil
	}
	if value < 0 {
		return 0, fmt.Errorf("invalid request limit")
	}
	return value, nil
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

// partnerTimeFormat keeps partner timestamps fixed width, so text ordering on
// partner_orders and partner_calls is chronological.
const partnerTimeFormat = "2006-01-02T15:04:05.000000000Z07:00"

// partnerTime formats a partner-owned timestamp in UTC with a fixed width.
func partnerTime(t time.Time) string {
	return t.UTC().Format(partnerTimeFormat)
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
		if err := rows.Scan(&item.RequestID, &item.KeyID, &item.Model, &item.Transport, &item.Protocol, &item.Status, &item.HTTPStatus, &item.LatencyMs, &item.TTFTMs, &item.Input, &item.Output, &item.Reasoning, &item.Cached, &item.Billed, &requested, &completed, &item.RejectionCode); err != nil {
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
