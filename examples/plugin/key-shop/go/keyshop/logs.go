package keyshop

import (
	"database/sql"
	"strconv"
	"time"
)

// AdminRequest is a request row joined with the key, customer and group that made it.
// It carries upstream account details, so it is only returned to the operator.
type AdminRequest struct {
	RequestID      string    `json:"request_id"`
	KeyID          string    `json:"key_id"`
	KeyLabel       string    `json:"key_label"`
	KeyDisplay     string    `json:"key_display"`
	Customer       string    `json:"customer"`
	GroupID        string    `json:"group_id"`
	GroupName      string    `json:"group_name"`
	Model          string    `json:"model"`
	RequestedModel string    `json:"requested_model"`
	Transport      string    `json:"transport"`
	Protocol       string    `json:"protocol"`
	Status         string    `json:"status"`
	HTTPStatus     int       `json:"http_status"`
	RejectionCode  string    `json:"rejection_code"`
	LatencyMs      int64     `json:"latency_ms"`
	TTFTMs         int64     `json:"ttft_ms"`
	TokPerSec      *float64  `json:"tok_per_s"`
	Input          int64     `json:"input"`
	Output         int64     `json:"output"`
	Reasoning      int64     `json:"reasoning"`
	Cached         int64     `json:"cached"`
	Billed         int64     `json:"billed"`
	Provider       string    `json:"provider"`
	AuthID         string    `json:"auth_id"`
	AuthIndex      string    `json:"auth_index"`
	AuthType       string    `json:"auth_type"`
	Effort         string    `json:"effort"`
	ServiceTier    string    `json:"service_tier"`
	RequestedAt    time.Time `json:"requested_at"`
	CompletedAt    time.Time `json:"completed_at"`
}

// AdminRequestQuery filters the operator request log. Empty fields do not filter.
type AdminRequestQuery struct {
	KeyID, Customer, GroupID, Status string
	Model, Account, Effort, Code     string
	Search                           string
	From, To                         time.Time
	Limit                            int
	Cursor                           string
}

// RequestSummary aggregates every row that matches the filter, not only the current page.
type RequestSummary struct {
	Total        int64   `json:"total"`
	Succeeded    int64   `json:"succeeded"`
	Failed       int64   `json:"failed"`
	Canceled     int64   `json:"canceled"`
	Rejected     int64   `json:"rejected"`
	Input        int64   `json:"input"`
	Output       int64   `json:"output"`
	Reasoning    int64   `json:"reasoning"`
	Cached       int64   `json:"cached"`
	Billed       int64   `json:"billed"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	AvgTTFTMs    float64 `json:"avg_ttft_ms"`
	ErrorRate    float64 `json:"error_rate"`
}

const adminRequestFrom = ` FROM requests r
LEFT JOIN keys k ON k.id = r.key_id
LEFT JOIN model_groups g ON g.id = k.group_id`

func (q AdminRequestQuery) where() (string, []any) {
	where := ` WHERE 1=1`
	var args []any
	add := func(clause string, values ...any) {
		where += ` AND ` + clause
		args = append(args, values...)
	}
	if q.KeyID != "" {
		add(`r.key_id=?`, q.KeyID)
	}
	if q.Customer != "" {
		add(`k.customer LIKE ?`, like(q.Customer))
	}
	if q.GroupID != "" {
		add(`k.group_id=?`, q.GroupID)
	}
	if q.Status != "" {
		add(`r.status=?`, q.Status)
	}
	if q.Model != "" {
		add(`(r.model=? OR r.requested_model=?)`, q.Model, q.Model)
	}
	if q.Account != "" {
		add(`(r.auth_id LIKE ? OR r.auth_index LIKE ?)`, like(q.Account), like(q.Account))
	}
	if q.Effort != "" {
		add(`r.effort=?`, q.Effort)
	}
	if q.Code != "" {
		add(`r.rejection_code=?`, q.Code)
	}
	if !q.From.IsZero() {
		add(`r.requested_at>=?`, q.From.UTC().Format(time.RFC3339Nano))
	}
	if !q.To.IsZero() {
		add(`r.requested_at<=?`, q.To.UTC().Format(time.RFC3339Nano))
	}
	if q.Search != "" {
		p := like(q.Search)
		add(`(r.request_id LIKE ? OR r.model LIKE ? OR r.requested_model LIKE ? OR k.label LIKE ? OR k.customer LIKE ? OR k.id LIKE ?)`, p, p, p, p, p, p)
	}
	return where, args
}

func tokPerSec(output, latencyMs, ttftMs int64) *float64 {
	if output <= 0 || latencyMs <= 0 {
		return nil
	}
	window := latencyMs
	// A non-stream response often records TTFT one millisecond under latency.
	// That is not a generation window; dividing by it reports tens of thousands of tok/s.
	if ttftMs > 0 && ttftMs < latencyMs && (latencyMs-ttftMs)*20 >= latencyMs {
		window = latencyMs - ttftMs
	}
	value := float64(output) / (float64(window) / 1000)
	return &value
}

// AdminRequests returns one page of the operator request log plus a summary of the whole filter.
func (s *Store) AdminRequests(q AdminRequestQuery) ([]AdminRequest, PageInfo, RequestSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := q.Limit
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	cursorTime, cursorID, err := decodeCursor(q.Cursor)
	if err != nil {
		return nil, PageInfo{}, RequestSummary{}, err
	}
	where, args := q.where()
	summary, err := s.requestSummaryOn(where, args)
	if err != nil {
		return nil, PageInfo{}, RequestSummary{}, err
	}
	query := `SELECT r.request_id, r.key_id, COALESCE(k.label,''), COALESCE(k.display_prefix,''), COALESCE(k.display_last4,''), COALESCE(k.customer,''), COALESCE(k.group_id,''), COALESCE(g.name,''),
r.model, r.requested_model, r.transport, r.protocol, r.status, r.http_status, r.rejection_code, r.latency_ms, r.ttft_ms,
r.input_tokens, r.output_tokens, r.reasoning_tokens, r.cached_tokens, r.billed_tokens,
r.provider, r.auth_id, r.auth_index, r.auth_type, r.effort, r.service_tier, r.requested_at, r.completed_at` + adminRequestFrom + where
	pageArgs := append([]any{}, args...)
	if cursorTime != "" {
		query += ` AND (r.requested_at < ? OR (r.requested_at = ? AND r.request_id < ?))`
		pageArgs = append(pageArgs, cursorTime, cursorTime, cursorID)
	}
	query += ` ORDER BY r.requested_at DESC, r.request_id DESC LIMIT ?`
	pageArgs = append(pageArgs, limit+1)
	rows, err := s.db.Query(query, pageArgs...)
	if err != nil {
		return nil, PageInfo{}, RequestSummary{}, err
	}
	defer rows.Close()
	items := []AdminRequest{}
	for rows.Next() {
		var item AdminRequest
		var prefix, last4, requested, completed string
		if err = rows.Scan(&item.RequestID, &item.KeyID, &item.KeyLabel, &prefix, &last4, &item.Customer, &item.GroupID, &item.GroupName,
			&item.Model, &item.RequestedModel, &item.Transport, &item.Protocol, &item.Status, &item.HTTPStatus, &item.RejectionCode, &item.LatencyMs, &item.TTFTMs,
			&item.Input, &item.Output, &item.Reasoning, &item.Cached, &item.Billed,
			&item.Provider, &item.AuthID, &item.AuthIndex, &item.AuthType, &item.Effort, &item.ServiceTier, &requested, &completed); err != nil {
			return nil, PageInfo{}, RequestSummary{}, err
		}
		if prefix != "" {
			item.KeyDisplay = Display(prefix, last4)
		}
		item.RequestedAt = parseTime(requested)
		item.CompletedAt = parseTime(completed)
		item.TokPerSec = tokPerSec(item.Output, item.LatencyMs, item.TTFTMs)
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, PageInfo{}, RequestSummary{}, err
	}
	page := PageInfo{Total: int(summary.Total)}
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		page.NextCursor = encodeCursor(last.RequestedAt, last.RequestID)
		page.HasMore = true
	}
	return items, page, summary, nil
}

func (s *Store) requestSummaryOn(where string, args []any) (RequestSummary, error) {
	var out RequestSummary
	var latencyN, ttftN int64
	var latencySum, ttftSum int64
	err := s.db.QueryRow(`SELECT COUNT(1),
COALESCE(SUM(CASE WHEN r.status='succeeded' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN r.status='failed' THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN r.status IN ('canceled','cancelled') THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN r.status='rejected' THEN 1 ELSE 0 END),0),
COALESCE(SUM(r.input_tokens),0), COALESCE(SUM(r.output_tokens),0), COALESCE(SUM(r.reasoning_tokens),0), COALESCE(SUM(r.cached_tokens),0), COALESCE(SUM(r.billed_tokens),0),
COALESCE(SUM(CASE WHEN r.latency_ms>0 THEN r.latency_ms ELSE 0 END),0), COALESCE(SUM(CASE WHEN r.latency_ms>0 THEN 1 ELSE 0 END),0),
COALESCE(SUM(CASE WHEN r.ttft_ms>0 THEN r.ttft_ms ELSE 0 END),0), COALESCE(SUM(CASE WHEN r.ttft_ms>0 THEN 1 ELSE 0 END),0)`+adminRequestFrom+where, args...).
		Scan(&out.Total, &out.Succeeded, &out.Failed, &out.Canceled, &out.Rejected, &out.Input, &out.Output, &out.Reasoning, &out.Cached, &out.Billed,
			&latencySum, &latencyN, &ttftSum, &ttftN)
	if err != nil {
		return RequestSummary{}, err
	}
	if latencyN > 0 {
		out.AvgLatencyMs = float64(latencySum) / float64(latencyN)
	}
	if ttftN > 0 {
		out.AvgTTFTMs = float64(ttftSum) / float64(ttftN)
	}
	if done := out.Succeeded + out.Failed; done > 0 {
		out.ErrorRate = float64(out.Failed) / float64(done)
	}
	return out, nil
}

// UsageBucket is one point of the usage time series.
type UsageBucket struct {
	Start     string `json:"start"`
	Requests  int64  `json:"requests"`
	Succeeded int64  `json:"succeeded"`
	Failed    int64  `json:"failed"`
	Rejected  int64  `json:"rejected"`
	Canceled  int64  `json:"canceled"`
	Input     int64  `json:"input"`
	Output    int64  `json:"output"`
	Billed    int64  `json:"billed"`
}

// UsageRank is one row of a top-N table.
type UsageRank struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Detail   string `json:"detail,omitempty"`
	Requests int64  `json:"requests"`
	Billed   int64  `json:"billed"`
}

// LatencyStats holds percentiles for succeeded requests, in milliseconds.
type LatencyStats struct {
	LatencyP50 int64 `json:"latency_p50"`
	LatencyP95 int64 `json:"latency_p95"`
	TTFTP50    int64 `json:"ttft_p50"`
	TTFTP95    int64 `json:"ttft_p95"`
}

type UsageStats struct {
	From          string         `json:"from"`
	To            string         `json:"to"`
	Bucket        string         `json:"bucket"`
	Summary       RequestSummary `json:"summary"`
	Series        []UsageBucket  `json:"series"`
	TopModels     []UsageRank    `json:"top_models"`
	TopKeys       []UsageRank    `json:"top_keys"`
	TopCustomers  []UsageRank    `json:"top_customers"`
	TopAccounts   []UsageRank    `json:"top_accounts"`
	Latency       LatencyStats   `json:"latency"`
	CacheHitRatio float64        `json:"cache_hit_ratio"`
}

const topLimit = 8

// UsageStats aggregates the request log for the dashboard. bucket is "hour" or "day".
func (s *Store) UsageStats(from, to time.Time, bucket string) (UsageStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if to.IsZero() {
		to = s.cfg.Now().UTC()
	}
	if from.IsZero() {
		from = to.Add(-24 * time.Hour)
	}
	width := 13 // "2006-01-02T15"
	if bucket != "hour" {
		bucket, width = "day", 10
	}
	out := UsageStats{From: from.UTC().Format(time.RFC3339), To: to.UTC().Format(time.RFC3339), Bucket: bucket}
	q := AdminRequestQuery{From: from, To: to}
	where, args := q.where()
	var err error
	if out.Summary, err = s.requestSummaryOn(where, args); err != nil {
		return UsageStats{}, err
	}
	if out.Summary.Input > 0 {
		out.CacheHitRatio = float64(out.Summary.Cached) / float64(out.Summary.Input)
	}
	out.Series = []UsageBucket{}
	rows, err := s.db.Query(`SELECT substr(r.requested_at,1,`+strconv.Itoa(width)+`) AS b, COUNT(1),
SUM(CASE WHEN r.status='succeeded' THEN 1 ELSE 0 END), SUM(CASE WHEN r.status='failed' THEN 1 ELSE 0 END),
SUM(CASE WHEN r.status='rejected' THEN 1 ELSE 0 END), SUM(CASE WHEN r.status IN ('canceled','cancelled') THEN 1 ELSE 0 END),
SUM(r.input_tokens), SUM(r.output_tokens), SUM(r.billed_tokens)`+adminRequestFrom+where+` GROUP BY b ORDER BY b`, args...)
	if err != nil {
		return UsageStats{}, err
	}
	for rows.Next() {
		var item UsageBucket
		if err = rows.Scan(&item.Start, &item.Requests, &item.Succeeded, &item.Failed, &item.Rejected, &item.Canceled, &item.Input, &item.Output, &item.Billed); err != nil {
			_ = rows.Close()
			return UsageStats{}, err
		}
		if width == 13 {
			item.Start += ":00:00Z"
		} else {
			item.Start += "T00:00:00Z"
		}
		out.Series = append(out.Series, item)
	}
	if err = rows.Close(); err != nil {
		return UsageStats{}, err
	}
	if out.TopModels, err = s.rank(`CASE WHEN r.model='' THEN r.requested_model ELSE r.model END`, `''`, where, args); err != nil {
		return UsageStats{}, err
	}
	if out.TopKeys, err = s.rank(`r.key_id`, `COALESCE(MAX(k.customer),'')`, where, args, `COALESCE(MAX(k.label), r.key_id)`); err != nil {
		return UsageStats{}, err
	}
	if out.TopCustomers, err = s.rank(`COALESCE(k.customer,'')`, `''`, where+` AND COALESCE(k.customer,'')<>''`, args); err != nil {
		return UsageStats{}, err
	}
	if out.TopAccounts, err = s.rank(`r.auth_id`, `COALESCE(MAX(r.provider),'')`, where+` AND r.auth_id<>''`, args); err != nil {
		return UsageStats{}, err
	}
	if out.Latency, err = s.latency(where, args); err != nil {
		return UsageStats{}, err
	}
	return out, nil
}

func (s *Store) rank(idExpr, detailExpr, where string, args []any, labelExpr ...string) ([]UsageRank, error) {
	label := idExpr
	if len(labelExpr) > 0 {
		label = labelExpr[0]
	}
	rows, err := s.db.Query(`SELECT `+idExpr+` AS rank_id, `+label+`, `+detailExpr+`, COUNT(1) AS n, COALESCE(SUM(r.billed_tokens),0)`+adminRequestFrom+where+
		` AND `+idExpr+` <> '' GROUP BY rank_id ORDER BY n DESC, rank_id LIMIT `+strconv.Itoa(topLimit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageRank{}
	for rows.Next() {
		var item UsageRank
		if err = rows.Scan(&item.ID, &item.Label, &item.Detail, &item.Requests, &item.Billed); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) latency(where string, args []any) (LatencyStats, error) {
	var out LatencyStats
	var err error
	if out.LatencyP50, err = s.percentile("latency_ms", 50, where, args); err != nil {
		return out, err
	}
	if out.LatencyP95, err = s.percentile("latency_ms", 95, where, args); err != nil {
		return out, err
	}
	if out.TTFTP50, err = s.percentile("ttft_ms", 50, where, args); err != nil {
		return out, err
	}
	out.TTFTP95, err = s.percentile("ttft_ms", 95, where, args)
	return out, err
}

// percentile uses the nearest-rank method over succeeded requests that recorded the column.
func (s *Store) percentile(column string, pct int, where string, args []any) (int64, error) {
	filter := where + ` AND r.status='succeeded' AND r.` + column + ` > 0`
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(1)`+adminRequestFrom+filter, args...).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	rank := (n*pct + 99) / 100
	if rank < 1 {
		rank = 1
	}
	var value int64
	err := s.db.QueryRow(`SELECT r.`+column+adminRequestFrom+filter+` ORDER BY r.`+column+` LIMIT 1 OFFSET ?`, append(append([]any{}, args...), rank-1)...).Scan(&value)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return value, err
}
