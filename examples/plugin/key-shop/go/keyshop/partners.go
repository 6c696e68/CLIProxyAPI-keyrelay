package keyshop

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Partner errors are typed so the HTTP layer maps each spec error code once.
var (
	ErrPartnerAPIDisabled  = errors.New("partner api disabled")
	ErrPartnerUnauthorized = errors.New("unauthorized")
	ErrPartnerDisabled     = errors.New("partner disabled")
	ErrPartnerNotFound     = errors.New("partner not found")
	ErrPartnerInvalid      = errors.New("invalid partner")
	ErrPartnerBadRequest   = errors.New("bad request")
	ErrRefVoided           = errors.New("ref voided")
	ErrRefConflict         = errors.New("ref conflict")
	ErrRefNotFound         = errors.New("ref not found")
	ErrRefConfirmed        = errors.New("ref confirmed")
	ErrOrderConflict       = errors.New("order conflict")
	ErrPlanNotAllowed      = errors.New("plan not allowed")
	ErrPlanInactive        = errors.New("plan inactive")
	ErrDailyCapReached     = errors.New("daily cap reached")
	ErrSecretConflict      = errors.New("secret conflict")
)

// PlanChangedError reports a mint whose plan revision is no longer current.
// Plan carries the current revision so the HTTP layer can return it.
type PlanChangedError struct{ Plan Plan }

func (e *PlanChangedError) Error() string {
	return fmt.Sprintf("plan changed: current revision is %d", e.Plan.Revision)
}

var errInvalidPartnerCursor = errors.New("invalid cursor")

var (
	partnerRefPattern    = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,80}$`)
	partnerSecretPattern = regexp.MustCompile(`^ck_[A-Za-z0-9_-]{43}$`)
)

// partnerRefMaxLength is the upper bound of partnerRefPattern. It also bounds
// the ref stored on partner_calls rows before the caller is authenticated.
const partnerRefMaxLength = 80

const (
	partnerTextLimit = 200

	defaultPartnerListLimit = 100
	maxPartnerListLimit     = 500
)

// Partner is an authenticated seller allowed to mint keys.
type Partner struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	SecretPrefix   string        `json:"secret_prefix"`
	SecretLast4    string        `json:"secret_last4"`
	Status         string        `json:"status"`
	AllowedPlanIDs []string      `json:"allowed_plan_ids"`
	DailyKeyCap    *int64        `json:"daily_key_cap"`
	MaxQuantity    *int64        `json:"max_quantity"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
	LastUsedAt     *time.Time    `json:"last_used_at"`
	Stats          *PartnerStats `json:"stats,omitempty"`
}

// PartnerStats is the 24h monitoring block on the partner list.
type PartnerStats struct {
	CallsOK            int64 `json:"calls_ok"`
	CallsClientError   int64 `json:"calls_client_error"`
	CallsServerError   int64 `json:"calls_server_error"`
	Minted             int64 `json:"minted"`
	Confirmed          int64 `json:"confirmed"`
	Voided             int64 `json:"voided"`
	UnconfirmedOverdue int64 `json:"unconfirmed_overdue"`
	DailyUsed          int64 `json:"daily_used"`
	DailyCap           int64 `json:"daily_cap"`
}

// PartnerInput creates a partner. A nil cap means the config default applies.
type PartnerInput struct {
	Name           string
	AllowedPlanIDs []string
	DailyKeyCap    *int64
	MaxQuantity    *int64
}

// PartnerUpdate changes only the fields whose Set flag is true. DailyKeyCap and
// MaxQuantity are cleared by passing a set flag with a nil value.
type PartnerUpdate struct {
	Name            string
	NameSet         bool
	Status          string
	StatusSet       bool
	AllowedPlanIDs  []string
	AllowedPlansSet bool
	DailyKeyCap     *int64
	DailyCapSet     bool
	MaxQuantity     *int64
	MaxQuantitySet  bool
}

// PartnerOrderState is the confirm response.
type PartnerOrderState struct {
	Ref   string `json:"ref"`
	State string `json:"state"`
}

// PartnerVoidResult is the void response.
type PartnerVoidResult struct {
	Ref          string `json:"ref"`
	State        string `json:"state"`
	KeysDisabled int64  `json:"keys_disabled"`
}

// PartnerKey is one minted key. Status is only set by the status op.
type PartnerKey struct {
	ID        string     `json:"id"`
	Display   string     `json:"display"`
	Status    string     `json:"status,omitempty"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// PartnerStatusResult is the status response.
type PartnerStatusResult struct {
	Ref     string       `json:"ref"`
	State   string       `json:"state"`
	OrderID string       `json:"order_id"`
	Keys    []PartnerKey `json:"keys"`
}

// PartnerMintInput is one idempotent mint request.
type PartnerMintInput struct {
	Ref          string
	PlanID       string
	PlanRevision int64
	Quantity     int64
	Secrets      []string
	Customer     string
	Note         string
}

// PartnerMintResult is the mint response payload.
type PartnerMintResult struct {
	Ref      string       `json:"ref"`
	State    string       `json:"state"`
	Replayed bool         `json:"replayed"`
	Keys     []PartnerKey `json:"keys"`
	Plan     Plan         `json:"plan"`
}

// PartnerOrder is one partner_orders row.
type PartnerOrder struct {
	PartnerID    string     `json:"partner_id"`
	Ref          string     `json:"ref"`
	PlanID       string     `json:"plan_id"`
	PlanRevision int64      `json:"plan_revision"`
	Quantity     int64      `json:"quantity"`
	State        string     `json:"state"`
	OrderID      string     `json:"order_id"`
	Buyer        string     `json:"buyer"`
	Customer     string     `json:"customer"`
	VoidReason   string     `json:"void_reason"`
	CreatedAt    time.Time  `json:"created_at"`
	ConfirmedAt  *time.Time `json:"confirmed_at"`
	VoidedAt     *time.Time `json:"voided_at"`
	Fingerprint  string     `json:"-"`
}

// PartnerRef is one refs item.
type PartnerRef struct {
	Ref       string    `json:"ref"`
	State     string    `json:"state"`
	Quantity  int64     `json:"quantity"`
	PlanID    string    `json:"plan_id"`
	OrderID   string    `json:"order_id"`
	CreatedAt time.Time `json:"created_at"`
}

// PartnerRefsQuery filters the refs op. Zero times mean unbounded.
type PartnerRefsQuery struct {
	Since  time.Time
	Until  time.Time
	Cursor string
	Limit  int
}

// PartnerRefsResult is the refs response.
type PartnerRefsResult struct {
	Refs       []PartnerRef `json:"refs"`
	NextCursor string       `json:"next_cursor"`
}

// PartnerCall is one partner_calls row. It never carries a token or secret.
type PartnerCall struct {
	ID         int64     `json:"id"`
	PartnerID  string    `json:"partner_id"`
	At         time.Time `json:"at"`
	Op         string    `json:"op"`
	HTTPStatus int       `json:"http_status"`
	Code       string    `json:"code"`
	Ref        string    `json:"ref"`
	LatencyMS  int64     `json:"latency_ms"`
	ClientIP   string    `json:"client_ip"`
}

// PartnerCallQuery filters the admin call log. Status is "ok" or "error".
type PartnerCallQuery struct {
	PartnerID string
	Op        string
	Status    string
	From      time.Time
	To        time.Time
	Limit     int
	Cursor    string
}

// PartnerOrderQuery filters the admin order list. State is an exact match.
type PartnerOrderQuery struct {
	PartnerID string
	State     string
	Ref       string
	From      time.Time
	To        time.Time
	Limit     int
	Cursor    string
}

const partnerSelect = `SELECT id, name, secret_prefix, secret_last4, status, allowed_plans_json, daily_key_cap, max_quantity, created_at, updated_at, last_used_at FROM partners`

const partnerOrderSelect = `SELECT partner_id, ref, plan_id, plan_revision, quantity, fingerprint, state, order_id, buyer, customer, void_reason, created_at, confirmed_at, voided_at FROM partner_orders`

func newPartnerSecret() (secret string, hash []byte, prefix, last4 string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", nil, "", "", err
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	secret = "ksp_" + body
	return secret, HashSecret(secret), body[:4], body[len(body)-4:], nil
}

func scanPartner(sc rowScanner) (Partner, error) {
	var partner Partner
	var raw string
	var daily, max sql.NullInt64
	var created, updated string
	var lastUsed sql.NullString
	if err := sc.Scan(&partner.ID, &partner.Name, &partner.SecretPrefix, &partner.SecretLast4, &partner.Status, &raw, &daily, &max, &created, &updated, &lastUsed); err != nil {
		return Partner{}, err
	}
	partner.AllowedPlanIDs = decodePlanIDs(raw)
	if daily.Valid {
		value := daily.Int64
		partner.DailyKeyCap = &value
	}
	if max.Valid {
		value := max.Int64
		partner.MaxQuantity = &value
	}
	partner.CreatedAt = parseTime(created)
	partner.UpdatedAt = parseTime(updated)
	partner.LastUsedAt = parseOptional(lastUsed)
	return partner, nil
}

// partnerOn loads a live partner. Soft-deleted partners are not visible anywhere.
func (s *Store) partnerOn(q rowQuery, id string) (Partner, error) {
	partner, err := scanPartner(q.QueryRow(partnerSelect+` WHERE id=? AND deleted_at IS NULL`, id))
	if err == sql.ErrNoRows {
		return Partner{}, ErrPartnerNotFound
	}
	return partner, err
}

func decodePlanIDs(raw string) []string {
	out := []string{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return []string{}
	}
	return out
}

func encodePlanIDs(ids []string) string {
	if ids == nil {
		ids = []string{}
	}
	raw, _ := json.Marshal(ids)
	if raw == nil {
		return "[]"
	}
	return string(raw)
}

func normalizePlanIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func (s *Store) requirePlansOn(q rowQuery, ids []string) error {
	for _, id := range ids {
		var n int
		if err := q.QueryRow(`SELECT COUNT(1) FROM plans WHERE id=?`, id).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrPlanNotFound
		}
	}
	return nil
}

func cloneInt64(v *int64) *int64 {
	if v == nil {
		return nil
	}
	value := *v
	return &value
}

func nullableInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func partnerWriteError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(message, "UNIQUE") {
		if strings.Contains(message, "secret_hash") {
			return fmt.Errorf("%w: partner secret collision", ErrPartnerInvalid)
		}
		return fmt.Errorf("%w: name already exists", ErrPartnerInvalid)
	}
	return err
}

func normalizePartnerInput(in PartnerInput) (PartnerInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return in, fmt.Errorf("%w: name is required", ErrPartnerInvalid)
	}
	in.AllowedPlanIDs = normalizePlanIDs(in.AllowedPlanIDs)
	if in.DailyKeyCap != nil && *in.DailyKeyCap < 0 {
		return in, fmt.Errorf("%w: daily_key_cap must not be negative", ErrPartnerInvalid)
	}
	if in.MaxQuantity != nil && *in.MaxQuantity < 1 {
		return in, fmt.Errorf("%w: max_quantity must be positive", ErrPartnerInvalid)
	}
	in.DailyKeyCap = cloneInt64(in.DailyKeyCap)
	in.MaxQuantity = cloneInt64(in.MaxQuantity)
	return in, nil
}

// CreatePartner stores a new live partner and returns its secret once.
func (s *Store) CreatePartner(in PartnerInput) (Partner, string, error) {
	in, err := normalizePartnerInput(in)
	if err != nil {
		return Partner{}, "", err
	}
	secret, hash, prefix, last4, err := newPartnerSecret()
	if err != nil {
		return Partner{}, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.requirePlansOn(s.db, in.AllowedPlanIDs); err != nil {
		return Partner{}, "", err
	}
	id, err := newID()
	if err != nil {
		return Partner{}, "", err
	}
	now := partnerTime(s.cfg.Now())
	_, err = s.db.Exec(`INSERT INTO partners (id, name, secret_hash, secret_prefix, secret_last4, status, allowed_plans_json, daily_key_cap, max_quantity, created_at, updated_at) VALUES (?,?,?,?,?,'active',?,?,?,?,?)`,
		id, in.Name, hash, prefix, last4, encodePlanIDs(in.AllowedPlanIDs), nullableInt64(in.DailyKeyCap), nullableInt64(in.MaxQuantity), now, now)
	if err != nil {
		return Partner{}, "", partnerWriteError(err)
	}
	partner, err := s.partnerOn(s.db, id)
	return partner, secret, err
}

// UpdatePartner changes only the flagged fields.
func (s *Store) UpdatePartner(id string, in PartnerUpdate) (Partner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.partnerOn(s.db, id)
	if err != nil {
		return Partner{}, err
	}
	name, status := current.Name, current.Status
	if in.NameSet {
		name = strings.TrimSpace(in.Name)
		if name == "" {
			return Partner{}, fmt.Errorf("%w: name is required", ErrPartnerInvalid)
		}
	}
	if in.StatusSet {
		status = strings.TrimSpace(in.Status)
		if status != "active" && status != "disabled" {
			return Partner{}, fmt.Errorf("%w: status must be active or disabled", ErrPartnerInvalid)
		}
	}
	allowed := current.AllowedPlanIDs
	if in.AllowedPlansSet {
		allowed = normalizePlanIDs(in.AllowedPlanIDs)
		if err = s.requirePlansOn(s.db, allowed); err != nil {
			return Partner{}, err
		}
	}
	daily, max := current.DailyKeyCap, current.MaxQuantity
	if in.DailyCapSet {
		if in.DailyKeyCap != nil && *in.DailyKeyCap < 0 {
			return Partner{}, fmt.Errorf("%w: daily_key_cap must not be negative", ErrPartnerInvalid)
		}
		daily = cloneInt64(in.DailyKeyCap)
	}
	if in.MaxQuantitySet {
		if in.MaxQuantity != nil && *in.MaxQuantity < 1 {
			return Partner{}, fmt.Errorf("%w: max_quantity must be positive", ErrPartnerInvalid)
		}
		max = cloneInt64(in.MaxQuantity)
	}
	_, err = s.db.Exec(`UPDATE partners SET name=?, status=?, allowed_plans_json=?, daily_key_cap=?, max_quantity=?, updated_at=? WHERE id=? AND deleted_at IS NULL`,
		name, status, encodePlanIDs(allowed), nullableInt64(daily), nullableInt64(max), partnerTime(s.cfg.Now()), id)
	if err != nil {
		return Partner{}, partnerWriteError(err)
	}
	return s.partnerOn(s.db, id)
}

// RotatePartner replaces the secret and returns the new one. The old secret dies at once.
func (s *Store) RotatePartner(id string) (Partner, string, error) {
	secret, hash, prefix, last4, err := newPartnerSecret()
	if err != nil {
		return Partner{}, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE partners SET secret_hash=?, secret_prefix=?, secret_last4=?, updated_at=? WHERE id=? AND deleted_at IS NULL`,
		hash, prefix, last4, partnerTime(s.cfg.Now()), id)
	if err != nil {
		return Partner{}, "", partnerWriteError(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Partner{}, "", ErrPartnerNotFound
	}
	partner, err := s.partnerOn(s.db, id)
	return partner, secret, err
}

// DeletePartner soft-deletes a partner.
func (s *Store) DeletePartner(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := partnerTime(s.cfg.Now())
	res, err := s.db.Exec(`UPDATE partners SET deleted_at=?, updated_at=? WHERE id=? AND deleted_at IS NULL`, now, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrPartnerNotFound
	}
	return nil
}

// ListPartners returns live partners with their 24h stats.
func (s *Store) ListPartners() ([]Partner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(partnerSelect + ` WHERE deleted_at IS NULL ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	out := []Partner{}
	for rows.Next() {
		partner, err := scanPartner(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, partner)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		stats, err := s.partnerStatsOn(s.db, out[i])
		if err != nil {
			return nil, err
		}
		out[i].Stats = &stats
	}
	return out, nil
}

// AuthenticatePartner resolves a ksp_ bearer token and stamps last_used_at.
func (s *Store) AuthenticatePartner(token string) (Partner, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Partner{}, ErrPartnerUnauthorized
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	partner, err := scanPartner(s.db.QueryRow(partnerSelect+` WHERE secret_hash=? AND deleted_at IS NULL`, HashSecret(token)))
	if err == sql.ErrNoRows {
		return Partner{}, ErrPartnerUnauthorized
	}
	if err != nil {
		return Partner{}, err
	}
	if partner.Status == "disabled" {
		// Every authenticated call stamps last_used_at, including the 403 path.
		now := s.cfg.Now().UTC()
		if _, err = s.db.Exec(`UPDATE partners SET last_used_at=? WHERE id=?`, partnerTime(now), partner.ID); err != nil {
			return Partner{}, err
		}
		partner.LastUsedAt = &now
		return partner, ErrPartnerDisabled
	}
	now := s.cfg.Now().UTC()
	if _, err = s.db.Exec(`UPDATE partners SET last_used_at=? WHERE id=?`, partnerTime(now), partner.ID); err != nil {
		return Partner{}, err
	}
	partner.LastUsedAt = &now
	return partner, nil
}

// effectiveDailyCap is the partner override, else the config default. Zero is unlimited.
func (s *Store) effectiveDailyCap(partner Partner) int64 {
	if partner.DailyKeyCap != nil {
		return *partner.DailyKeyCap
	}
	if s.cfg.PartnerDailyKeyCap == nil {
		return 0
	}
	return *s.cfg.PartnerDailyKeyCap
}

// effectiveMaxQuantity is the partner override, else the config default.
func (s *Store) effectiveMaxQuantity(partner Partner) int64 {
	if partner.MaxQuantity != nil {
		return *partner.MaxQuantity
	}
	return s.cfg.PartnerMaxQuantity
}

func (s *Store) partnerDailyUsedOn(q rowQuery, partnerID string, now time.Time) (int64, error) {
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	var used int64
	err := q.QueryRow(`SELECT COALESCE(SUM(quantity),0) FROM partner_orders WHERE partner_id=? AND state<>'voided' AND created_at >= ? AND created_at < ?`,
		partnerID, partnerTime(start), partnerTime(start.AddDate(0, 0, 1))).Scan(&used)
	return used, err
}

func (s *Store) partnerStatsOn(q rowQuery, partner Partner) (PartnerStats, error) {
	now := s.cfg.Now().UTC()
	since := partnerTime(now.Add(-24 * time.Hour))
	overdueBefore := partnerTime(now.Add(-time.Duration(s.cfg.PartnerUnconfirmedAlertMin) * time.Minute))
	var stats PartnerStats
	err := q.QueryRow(`SELECT
COALESCE(SUM(CASE WHEN http_status < 400 THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN http_status >= 400 AND http_status < 500 THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN http_status >= 500 THEN 1 ELSE 0 END),0)
FROM partner_calls WHERE partner_id=? AND at >= ?`, partner.ID, since).
		Scan(&stats.CallsOK, &stats.CallsClientError, &stats.CallsServerError)
	if err != nil {
		return PartnerStats{}, err
	}
	err = q.QueryRow(`SELECT
COALESCE(SUM(CASE WHEN state='minted' AND created_at >= ? THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN state='confirmed' AND confirmed_at >= ? THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN state='voided' AND voided_at >= ? THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN state='minted' AND created_at < ? THEN 1 ELSE 0 END),0)
FROM partner_orders WHERE partner_id=?`, since, since, since, overdueBefore, partner.ID).
		Scan(&stats.Minted, &stats.Confirmed, &stats.Voided, &stats.UnconfirmedOverdue)
	if err != nil {
		return PartnerStats{}, err
	}
	used, err := s.partnerDailyUsedOn(q, partner.ID, now)
	if err != nil {
		return PartnerStats{}, err
	}
	stats.DailyUsed = used
	stats.DailyCap = s.effectiveDailyCap(partner)
	return stats, nil
}

func validatePartnerMint(in PartnerMintInput, maxQuantity int64) error {
	if !partnerRefPattern.MatchString(in.Ref) {
		return fmt.Errorf("%w: ref must be 8-80 characters of A-Z a-z 0-9 . _ : -", ErrPartnerBadRequest)
	}
	if in.Quantity < 1 || in.Quantity > maxQuantity {
		return fmt.Errorf("%w: quantity must be between 1 and %d", ErrPartnerBadRequest, maxQuantity)
	}
	if int64(len(in.Secrets)) != in.Quantity {
		return fmt.Errorf("%w: secrets must match quantity", ErrPartnerBadRequest)
	}
	seen := make(map[string]struct{}, len(in.Secrets))
	for _, secret := range in.Secrets {
		if !partnerSecretPattern.MatchString(secret) {
			return fmt.Errorf("%w: secret must be ck_ followed by 43 characters", ErrPartnerBadRequest)
		}
		if _, dup := seen[secret]; dup {
			return fmt.Errorf("%w: duplicate secret", ErrPartnerBadRequest)
		}
		seen[secret] = struct{}{}
	}
	if utf8.RuneCountInString(in.Customer) > partnerTextLimit || utf8.RuneCountInString(in.Note) > partnerTextLimit {
		return fmt.Errorf("%w: customer and note must be at most %d characters", ErrPartnerBadRequest, partnerTextLimit)
	}
	return nil
}

// partnerFingerprint identifies one mint body so a retry can replay it. The
// secrets are hashed, never stored.
func partnerFingerprint(in PartnerMintInput) string {
	hashes := make([]string, 0, len(in.Secrets))
	for _, secret := range in.Secrets {
		hashes = append(hashes, hex.EncodeToString(HashSecret(secret)))
	}
	raw := in.PlanID + "|" + strconv.FormatInt(in.PlanRevision, 10) + "|" + strconv.FormatInt(in.Quantity, 10) + "|" + strings.Join(hashes, "|")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func partnerSecretParts(secret string) (prefix, last4 string) {
	body := strings.TrimPrefix(secret, "ck_")
	if len(body) < 8 {
		return body, body
	}
	return body[:4], body[len(body)-4:]
}

func renderPartnerTemplate(template string, plan Plan, partner Partner, ref string, index int) string {
	short := ref
	if len(short) > 8 {
		short = short[len(short)-8:]
	}
	return strings.NewReplacer(
		"{plan}", plan.Name,
		"{partner}", partner.Name,
		"{ref}", short,
		"{i}", strconv.Itoa(index),
	).Replace(template)
}

func partnerPlanAllowed(partner Partner, planID string) bool {
	if len(partner.AllowedPlanIDs) == 0 {
		return true
	}
	for _, allowed := range partner.AllowedPlanIDs {
		if allowed == planID {
			return true
		}
	}
	return false
}

// PartnerMint creates the order and its keys in one immediate transaction.
func (s *Store) PartnerMint(partnerID string, in PartnerMintInput) (PartnerMintResult, error) {
	in.Ref = strings.TrimSpace(in.Ref)
	in.PlanID = strings.TrimSpace(in.PlanID)
	in.Customer = strings.TrimSpace(in.Customer)
	in.Note = strings.TrimSpace(in.Note)
	fingerprint := partnerFingerprint(in)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return PartnerMintResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	partner, err := s.partnerOn(tx, partnerID)
	if errors.Is(err, ErrPartnerNotFound) {
		return PartnerMintResult{}, ErrPartnerUnauthorized
	}
	if err != nil {
		return PartnerMintResult{}, err
	}
	if partner.Status == "disabled" {
		return PartnerMintResult{}, ErrPartnerDisabled
	}
	if err = validatePartnerMint(in, s.effectiveMaxQuantity(partner)); err != nil {
		return PartnerMintResult{}, err
	}
	order, found, err := s.partnerOrderOn(tx, partner.ID, in.Ref)
	if err != nil {
		return PartnerMintResult{}, err
	}
	if found {
		switch order.State {
		case "voided":
			return PartnerMintResult{}, ErrRefVoided
		case "minted", "confirmed":
			if order.Fingerprint != fingerprint {
				return PartnerMintResult{}, ErrRefConflict
			}
			keys, err := s.partnerKeysOn(tx, partner.ID, in.Ref)
			if err != nil {
				return PartnerMintResult{}, err
			}
			plan, err := s.planOn(tx, order.PlanID)
			if err != nil {
				return PartnerMintResult{}, err
			}
			return PartnerMintResult{Ref: in.Ref, State: order.State, Replayed: true, Keys: keys, Plan: plan}, nil
		}
	}
	plan, err := s.planOn(tx, in.PlanID)
	if err != nil {
		return PartnerMintResult{}, err
	}
	if !partnerPlanAllowed(partner, plan.ID) {
		return PartnerMintResult{}, ErrPlanNotAllowed
	}
	if !plan.Active {
		return PartnerMintResult{}, ErrPlanInactive
	}
	if plan.Revision != in.PlanRevision {
		return PartnerMintResult{}, &PlanChangedError{Plan: plan}
	}
	now := s.cfg.Now().UTC()
	used, err := s.partnerDailyUsedOn(tx, partner.ID, now)
	if err != nil {
		return PartnerMintResult{}, err
	}
	if dailyCap := s.effectiveDailyCap(partner); dailyCap > 0 && used+in.Quantity > dailyCap {
		return PartnerMintResult{}, ErrDailyCapReached
	}
	for _, secret := range in.Secrets {
		var existing int
		if err = tx.QueryRow(`SELECT COUNT(1) FROM keys WHERE secret_hash=?`, HashSecret(secret)).Scan(&existing); err != nil {
			return PartnerMintResult{}, err
		}
		if existing > 0 {
			return PartnerMintResult{}, ErrSecretConflict
		}
	}
	nowText := partnerTime(now)
	_, err = tx.Exec(`INSERT INTO partner_orders (partner_id, ref, plan_id, plan_revision, quantity, fingerprint, state, order_id, buyer, customer, void_reason, created_at) VALUES (?,?,?,?,?,?,'minted','','',?,'',?)`,
		partner.ID, in.Ref, plan.ID, plan.Revision, in.Quantity, fingerprint, in.Customer, nowText)
	if err != nil {
		return PartnerMintResult{}, err
	}
	keys := make([]PartnerKey, 0, len(in.Secrets))
	for i, secret := range in.Secrets {
		prefix, last4 := partnerSecretParts(secret)
		key, err := s.insertKeyOn(tx, CreateInput{
			Label:             renderPartnerTemplate(s.cfg.PartnerKeyLabelTemplate, plan, partner, in.Ref, i+1),
			Customer:          in.Customer,
			Note:              renderPartnerTemplate(s.cfg.PartnerKeyNoteTemplate, plan, partner, in.Ref, i+1),
			PartnerID:         partner.ID,
			PartnerRef:        in.Ref,
			TTLSeconds:        plan.TTLSeconds,
			NeverExpires:      plan.NeverExpires,
			TokenLimit:        plan.TokenLimit,
			TokenLimitSet:     !plan.UnlimitedTokens,
			UnlimitedTokens:   plan.UnlimitedTokens,
			RequestLimit:      plan.RequestLimit,
			RequestLimitSet:   !plan.UnlimitedRequests,
			UnlimitedRequests: plan.UnlimitedRequests,
			GroupID:           plan.GroupID,
		}, HashSecret(secret), prefix, last4)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") && strings.Contains(err.Error(), "secret_hash") {
				return PartnerMintResult{}, ErrSecretConflict
			}
			return PartnerMintResult{}, err
		}
		keys = append(keys, PartnerKey{ID: key.ID, Display: key.Display, ExpiresAt: key.ExpiresAt})
	}
	if err = tx.Commit(); err != nil {
		return PartnerMintResult{}, err
	}
	return PartnerMintResult{Ref: in.Ref, State: "minted", Keys: keys, Plan: plan}, nil
}

// PartnerConfirm marks a minted order confirmed. It is idempotent by order_id.
func (s *Store) PartnerConfirm(partnerID, ref, orderID, buyer string) (PartnerOrderState, error) {
	ref = strings.TrimSpace(ref)
	orderID = strings.TrimSpace(orderID)
	buyer = strings.TrimSpace(buyer)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return PartnerOrderState{}, err
	}
	defer func() { _ = tx.Rollback() }()
	order, found, err := s.partnerOrderOn(tx, partnerID, ref)
	if err != nil {
		return PartnerOrderState{}, err
	}
	if !found {
		return PartnerOrderState{}, ErrRefNotFound
	}
	switch order.State {
	case "voided":
		return PartnerOrderState{}, ErrRefVoided
	case "confirmed":
		if order.OrderID == orderID {
			return PartnerOrderState{Ref: ref, State: "confirmed"}, nil
		}
		return PartnerOrderState{}, ErrOrderConflict
	}
	if _, err = tx.Exec(`UPDATE partner_orders SET state='confirmed', order_id=?, buyer=?, confirmed_at=? WHERE partner_id=? AND ref=? AND state='minted'`,
		orderID, buyer, partnerTime(s.cfg.Now()), partnerID, ref); err != nil {
		return PartnerOrderState{}, err
	}
	if buyer != "" {
		if _, err = tx.Exec(`UPDATE keys SET customer=? WHERE partner_id=? AND partner_ref=? AND deleted_at IS NULL`, buyer, partnerID, ref); err != nil {
			return PartnerOrderState{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return PartnerOrderState{}, err
	}
	return PartnerOrderState{Ref: ref, State: "confirmed"}, nil
}

// PartnerVoid cancels an order and disables its keys. A missing ref leaves a tombstone.
func (s *Store) PartnerVoid(partnerID, ref, reason string) (PartnerVoidResult, error) {
	ref = strings.TrimSpace(ref)
	reason = strings.TrimSpace(reason)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return PartnerVoidResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	order, found, err := s.partnerOrderOn(tx, partnerID, ref)
	if err != nil {
		return PartnerVoidResult{}, err
	}
	now := partnerTime(s.cfg.Now())
	if !found {
		if _, err = tx.Exec(`INSERT INTO partner_orders (partner_id, ref, state, void_reason, created_at, voided_at) VALUES (?,?,'voided',?,?,?)`,
			partnerID, ref, reason, now, now); err != nil {
			return PartnerVoidResult{}, err
		}
		if err = tx.Commit(); err != nil {
			return PartnerVoidResult{}, err
		}
		return PartnerVoidResult{Ref: ref, State: "voided"}, nil
	}
	switch order.State {
	case "voided":
		return PartnerVoidResult{Ref: ref, State: "voided"}, nil
	case "confirmed":
		return PartnerVoidResult{}, ErrRefConfirmed
	}
	if _, err = tx.Exec(`UPDATE partner_orders SET state='voided', void_reason=?, voided_at=? WHERE partner_id=? AND ref=?`, reason, now, partnerID, ref); err != nil {
		return PartnerVoidResult{}, err
	}
	res, err := tx.Exec(`UPDATE keys SET status='disabled' WHERE partner_id=? AND partner_ref=? AND deleted_at IS NULL`, partnerID, ref)
	if err != nil {
		return PartnerVoidResult{}, err
	}
	disabled, _ := res.RowsAffected()
	if err = tx.Commit(); err != nil {
		return PartnerVoidResult{}, err
	}
	return PartnerVoidResult{Ref: ref, State: "voided", KeysDisabled: disabled}, nil
}

// PartnerStatus returns the order and its keys with computed key status.
func (s *Store) PartnerStatus(partnerID, ref string) (PartnerStatusResult, error) {
	ref = strings.TrimSpace(ref)
	s.mu.Lock()
	defer s.mu.Unlock()
	order, found, err := s.partnerOrderOn(s.db, partnerID, ref)
	if err != nil {
		return PartnerStatusResult{}, err
	}
	if !found {
		return PartnerStatusResult{}, ErrRefNotFound
	}
	keys, err := s.partnerKeyStatusesOn(s.db, partnerID, ref)
	if err != nil {
		return PartnerStatusResult{}, err
	}
	return PartnerStatusResult{Ref: order.Ref, State: order.State, OrderID: order.OrderID, Keys: keys}, nil
}

// PartnerRefs pages this partner's orders by creation time.
func (s *Store) PartnerRefs(partnerID string, query PartnerRefsQuery) (PartnerRefsResult, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = defaultPartnerListLimit
	}
	if limit > maxPartnerListLimit {
		limit = maxPartnerListLimit
	}
	where := []string{"partner_id=?"}
	args := []any{partnerID}
	if !query.Since.IsZero() {
		where = append(where, "created_at >= ?")
		args = append(args, partnerTime(query.Since))
	}
	if !query.Until.IsZero() {
		where = append(where, "created_at <= ?")
		args = append(args, partnerTime(query.Until))
	}
	if strings.TrimSpace(query.Cursor) != "" {
		at, cursorRef, err := decodePartnerCursor(query.Cursor)
		if err != nil {
			return PartnerRefsResult{}, fmt.Errorf("%w: invalid cursor", ErrPartnerBadRequest)
		}
		where = append(where, "(created_at > ? OR (created_at = ? AND ref > ?))")
		args = append(args, at, at, cursorRef)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT ref, state, quantity, plan_id, order_id, created_at FROM partner_orders WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at, ref LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return PartnerRefsResult{}, err
	}
	defer rows.Close()
	refs := []PartnerRef{}
	times := []string{}
	for rows.Next() {
		var item PartnerRef
		var at string
		if err = rows.Scan(&item.Ref, &item.State, &item.Quantity, &item.PlanID, &item.OrderID, &at); err != nil {
			return PartnerRefsResult{}, err
		}
		item.CreatedAt = parseTime(at)
		refs = append(refs, item)
		times = append(times, at)
	}
	if err = rows.Err(); err != nil {
		return PartnerRefsResult{}, err
	}
	result := PartnerRefsResult{Refs: refs}
	if len(refs) > limit {
		result.Refs = refs[:limit]
		result.NextCursor = encodePartnerCursor(times[limit-1], refs[limit-1].Ref)
	}
	return result, nil
}

func scanPartnerOrder(sc rowScanner) (PartnerOrder, error) {
	var order PartnerOrder
	var created string
	var confirmed, voided sql.NullString
	if err := sc.Scan(&order.PartnerID, &order.Ref, &order.PlanID, &order.PlanRevision, &order.Quantity, &order.Fingerprint, &order.State, &order.OrderID, &order.Buyer, &order.Customer, &order.VoidReason, &created, &confirmed, &voided); err != nil {
		return PartnerOrder{}, err
	}
	order.CreatedAt = parseTime(created)
	order.ConfirmedAt = parseOptional(confirmed)
	order.VoidedAt = parseOptional(voided)
	return order, nil
}

func (s *Store) partnerOrderOn(q rowQuery, partnerID, ref string) (PartnerOrder, bool, error) {
	order, err := scanPartnerOrder(q.QueryRow(partnerOrderSelect+` WHERE partner_id=? AND ref=?`, partnerID, ref))
	if err == sql.ErrNoRows {
		return PartnerOrder{}, false, nil
	}
	if err != nil {
		return PartnerOrder{}, false, err
	}
	return order, true, nil
}

func (s *Store) partnerKeysOn(q querySource, partnerID, ref string) ([]PartnerKey, error) {
	// rowid keeps the original insert order, so a replay maps keys to the
	// secrets in the same order the mint request listed them.
	rows, err := q.Query(`SELECT id, display_prefix, display_last4, expires_at FROM keys WHERE partner_id=? AND partner_ref=? AND deleted_at IS NULL ORDER BY rowid`, partnerID, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []PartnerKey{}
	for rows.Next() {
		var id, prefix, last4 string
		var expires sql.NullString
		if err = rows.Scan(&id, &prefix, &last4, &expires); err != nil {
			return nil, err
		}
		keys = append(keys, PartnerKey{ID: id, Display: Display(prefix, last4), ExpiresAt: parseOptional(expires)})
	}
	return keys, rows.Err()
}

func (s *Store) partnerKeyStatusesOn(q querySource, partnerID, ref string) ([]PartnerKey, error) {
	rows, err := q.Query(`SELECT `+keyColumns+`, `+reservedByKey+` FROM keys WHERE partner_id=? AND partner_ref=? AND deleted_at IS NULL ORDER BY rowid`, partnerID, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []PartnerKey{}
	for rows.Next() {
		key, err := s.scanKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, PartnerKey{ID: key.ID, Display: key.Display, Status: key.Status, ExpiresAt: key.ExpiresAt})
	}
	return keys, rows.Err()
}

// RecordPartnerCall writes one audit row. It never receives a token or secret.
func (s *Store) RecordPartnerCall(call PartnerCall) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO partner_calls (partner_id, at, op, http_status, code, ref, latency_ms, client_ip) VALUES (?,?,?,?,?,?,?,?)`,
		call.PartnerID, partnerTime(s.cfg.Now()), call.Op, call.HTTPStatus, call.Code, call.Ref, call.LatencyMS, call.ClientIP)
	return err
}

// ListPartnerCalls pages the call log, newest first.
func (s *Store) ListPartnerCalls(query PartnerCallQuery) ([]PartnerCall, string, error) {
	limit := clampPartnerLimit(query.Limit)
	where := []string{}
	args := []any{}
	if partnerID := strings.TrimSpace(query.PartnerID); partnerID != "" {
		where = append(where, "partner_id=?")
		args = append(args, partnerID)
	}
	if op := strings.TrimSpace(query.Op); op != "" {
		where = append(where, "op=?")
		args = append(args, op)
	}
	switch strings.ToLower(strings.TrimSpace(query.Status)) {
	case "ok":
		where = append(where, "http_status < 400")
	case "error":
		where = append(where, "http_status >= 400")
	}
	if !query.From.IsZero() {
		where = append(where, "at >= ?")
		args = append(args, partnerTime(query.From))
	}
	if !query.To.IsZero() {
		where = append(where, "at <= ?")
		args = append(args, partnerTime(query.To))
	}
	if strings.TrimSpace(query.Cursor) != "" {
		at, id, err := decodePartnerCursor(query.Cursor)
		if err != nil {
			return nil, "", fmt.Errorf("%w: invalid cursor", ErrPartnerBadRequest)
		}
		callID, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return nil, "", fmt.Errorf("%w: invalid cursor", ErrPartnerBadRequest)
		}
		where = append(where, "(at < ? OR (at = ? AND id < ?))")
		args = append(args, at, at, callID)
	}
	clause := "1=1"
	if len(where) > 0 {
		clause = strings.Join(where, " AND ")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, partner_id, at, op, http_status, code, ref, latency_ms, client_ip FROM partner_calls WHERE `+clause+` ORDER BY at DESC, id DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	calls := []PartnerCall{}
	for rows.Next() {
		var call PartnerCall
		var at string
		if err = rows.Scan(&call.ID, &call.PartnerID, &at, &call.Op, &call.HTTPStatus, &call.Code, &call.Ref, &call.LatencyMS, &call.ClientIP); err != nil {
			return nil, "", err
		}
		call.At = parseTime(at)
		calls = append(calls, call)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(calls) > limit {
		calls = calls[:limit]
		last := calls[limit-1]
		next = encodePartnerCursor(partnerTime(last.At), strconv.FormatInt(last.ID, 10))
	}
	return calls, next, nil
}

// ListPartnerOrders pages orders, newest first.
func (s *Store) ListPartnerOrders(query PartnerOrderQuery) ([]PartnerOrder, string, error) {
	limit := clampPartnerLimit(query.Limit)
	where := []string{}
	args := []any{}
	if partnerID := strings.TrimSpace(query.PartnerID); partnerID != "" {
		where = append(where, "partner_id=?")
		args = append(args, partnerID)
	}
	if state := strings.TrimSpace(query.State); state != "" {
		where = append(where, "state=?")
		args = append(args, state)
	}
	if ref := strings.TrimSpace(query.Ref); ref != "" {
		where = append(where, "ref=?")
		args = append(args, ref)
	}
	if !query.From.IsZero() {
		where = append(where, "created_at >= ?")
		args = append(args, partnerTime(query.From))
	}
	if !query.To.IsZero() {
		where = append(where, "created_at <= ?")
		args = append(args, partnerTime(query.To))
	}
	if strings.TrimSpace(query.Cursor) != "" {
		at, id, err := decodePartnerCursor(query.Cursor)
		if err != nil {
			return nil, "", fmt.Errorf("%w: invalid cursor", ErrPartnerBadRequest)
		}
		where = append(where, "(created_at < ? OR (created_at = ? AND (partner_id || '|' || ref) < ?))")
		args = append(args, at, at, id)
	}
	clause := "1=1"
	if len(where) > 0 {
		clause = strings.Join(where, " AND ")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(partnerOrderSelect+` WHERE `+clause+` ORDER BY created_at DESC, (partner_id || '|' || ref) DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	orders := []PartnerOrder{}
	for rows.Next() {
		order, err := scanPartnerOrder(rows)
		if err != nil {
			return nil, "", err
		}
		orders = append(orders, order)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(orders) > limit {
		orders = orders[:limit]
		last := orders[limit-1]
		next = encodePartnerCursor(partnerTime(last.CreatedAt), last.PartnerID+"|"+last.Ref)
	}
	return orders, next, nil
}

func clampPartnerLimit(limit int) int {
	if limit <= 0 {
		return defaultPartnerListLimit
	}
	if limit > maxPartnerListLimit {
		return maxPartnerListLimit
	}
	return limit
}

// partnerCursor is the opaque page position shared by the partner lists.
type partnerCursor struct {
	At string `json:"at"`
	ID string `json:"id"`
}

func encodePartnerCursor(at, id string) string {
	raw, _ := json.Marshal(partnerCursor{At: at, ID: id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodePartnerCursor(value string) (string, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return "", "", errInvalidPartnerCursor
	}
	var cursor partnerCursor
	if json.Unmarshal(raw, &cursor) != nil || cursor.At == "" || cursor.ID == "" {
		return "", "", errInvalidPartnerCursor
	}
	if _, err = time.Parse(partnerTimeFormat, cursor.At); err != nil {
		return "", "", errInvalidPartnerCursor
	}
	return cursor.At, cursor.ID, nil
}
