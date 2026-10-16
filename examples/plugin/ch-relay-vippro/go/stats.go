package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	statsRetention           = 90 * 24 * time.Hour
	accountCheckWorkers      = 8
	accountCheckInterval     = 30 * time.Minute
	accountCheckInitialDelay = 2 * time.Second
)

type hourBucket struct {
	Start        time.Time `json:"start"`
	Requests     int64     `json:"requests"`
	Success      int64     `json:"success"`
	Failed       int64     `json:"failed"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	TotalTokens  int64     `json:"total_tokens"`
}

type accountSnapshot struct {
	Running         bool   `json:"running"`
	Checked         int    `json:"checked"`
	Total           int    `json:"total"`
	Eligible        int    `json:"eligible"`
	Plus            int    `json:"plus"`
	Cooling         int    `json:"cooling"`
	Unavailable     int    `json:"unavailable"`
	DisabledSkipped int    `json:"disabled_skipped"`
	CheckedAt       string `json:"checked_at"`
	DurationMS      int64  `json:"duration_ms"`
}

// statsPayload is the GET stats and POST accounts/check body.
// Totals are the sum of retained hourly buckets. accounts is null before the first check.
type emailUsage struct {
	Email       string `json:"email"`
	Success     int64  `json:"success"`
	Failed      int64  `json:"failed"`
	TotalTokens int64  `json:"total_tokens"`
}

type statsPayload struct {
	Requests     int64            `json:"requests"`
	Success      int64            `json:"success"`
	Failed       int64            `json:"failed"`
	InputTokens  int64            `json:"input_tokens"`
	OutputTokens int64            `json:"output_tokens"`
	TotalTokens  int64            `json:"total_tokens"`
	Since        time.Time        `json:"since"`
	Buckets      []hourBucket     `json:"buckets"`
	UsageByEmail []emailUsage     `json:"usage_by_email"`
	Accounts     *accountSnapshot `json:"accounts"`
}

type statsPersist struct {
	Requests     int64        `json:"requests"`
	Success      int64        `json:"success"`
	Failed       int64        `json:"failed"`
	InputTokens  int64        `json:"input_tokens"`
	OutputTokens int64        `json:"output_tokens"`
	TotalTokens  int64        `json:"total_tokens"`
	Since        time.Time    `json:"since"`
	Buckets      []hourBucket `json:"buckets"`
	UsageByEmail []emailUsage `json:"usage_by_email,omitempty"`
}

type accountKind int

const (
	kindUnavailable accountKind = iota
	kindCooling
	kindEligible
)

var (
	statsMu       sync.Mutex
	stats         usageCounters
	emailUsageMap map[string]*emailUsage
	hourBuckets   []hourBucket
	statsPath     string
	accountMu     sync.Mutex
	accountSnap   *accountSnapshot
	checkCancel   context.CancelFunc
	checkWG       sync.WaitGroup
	checkerMu     sync.Mutex
	checkerCancel context.CancelFunc
	checkerWG     sync.WaitGroup
	// accountCheckAutoStart is false in tests so GET stats is not racing a scan.
	accountCheckAutoStart = true
)

type usageCounters struct {
	Requests     int64
	Success      int64
	Failed       int64
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
	Since        time.Time
}

func resetStatsMemory() {
	statsMu.Lock()
	stats = usageCounters{}
	hourBuckets = nil
	emailUsageMap = map[string]*emailUsage{}
	statsPath = ""
	statsMu.Unlock()
	accountMu.Lock()
	accountSnap = nil
	accountMu.Unlock()
}

func setStatsFile(path string) {
	path = strings.TrimSpace(path)
	statsMu.Lock()
	statsPath = path
	statsMu.Unlock()
	if path == "" {
		return
	}
	loadStats(path)
}

func loadStats(path string) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil || len(bytesTrimSpace(raw)) == 0 {
		return
	}
	var loaded statsPersist
	if errUnmarshal := json.Unmarshal(raw, &loaded); errUnmarshal != nil {
		return
	}
	statsMu.Lock()
	defer statsMu.Unlock()
	if statsPath != path {
		return
	}
	hourBuckets = loaded.Buckets
	stats.Since = loaded.Since
	emailUsageMap = map[string]*emailUsage{}
	for _, row := range loaded.UsageByEmail {
		key := emailKey(row.Email)
		emailUsageMap[key] = &emailUsage{
			Email:       displayEmail(row.Email),
			Success:     row.Success,
			Failed:      row.Failed,
			TotalTokens: row.TotalTokens,
		}
	}
	sumBucketsLocked()
}

func bytesTrimSpace(raw []byte) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}

func noteRequest(success bool, in, out, total int64, email string) {
	if in < 0 {
		in = 0
	}
	if out < 0 {
		out = 0
	}
	if total < 0 {
		total = 0
	}
	statsMu.Lock()
	defer statsMu.Unlock()
	now := nowFn()
	if stats.Since.IsZero() {
		stats.Since = now.UTC()
	}
	addBucketLocked(hourStart(now), success, in, out, total)
	addEmailLocked(email, success, total)
	pruneBucketsLocked(now)
	sumBucketsLocked()
	if statsPath == "" {
		return
	}
	_ = writeStatsFile(statsPath, persistLocked())
}

func addBucketLocked(start time.Time, success bool, in, out, total int64) {
	for i := range hourBuckets {
		if !hourBuckets[i].Start.Equal(start) {
			continue
		}
		bucket := &hourBuckets[i]
		bucket.Requests++
		if success {
			bucket.Success++
		} else {
			bucket.Failed++
		}
		bucket.InputTokens += in
		bucket.OutputTokens += out
		bucket.TotalTokens += total
		return
	}
	bucket := hourBucket{
		Start:        start,
		Requests:     1,
		InputTokens:  in,
		OutputTokens: out,
		TotalTokens:  total,
	}
	if success {
		bucket.Success = 1
	} else {
		bucket.Failed = 1
	}
	hourBuckets = append(hourBuckets, bucket)
	sort.Slice(hourBuckets, func(i, j int) bool {
		return hourBuckets[i].Start.Before(hourBuckets[j].Start)
	})
}

func pruneBucketsLocked(now time.Time) {
	if len(hourBuckets) == 0 {
		return
	}
	cutoff := now.Add(-statsRetention)
	kept := make([]hourBucket, 0, len(hourBuckets))
	for _, bucket := range hourBuckets {
		if !bucket.Start.Before(cutoff) {
			kept = append(kept, bucket)
		}
	}
	hourBuckets = kept
}

func sumBucketsLocked() {
	since := stats.Since
	next := usageCounters{Since: since}
	for _, bucket := range hourBuckets {
		next.Requests += bucket.Requests
		next.Success += bucket.Success
		next.Failed += bucket.Failed
		next.InputTokens += bucket.InputTokens
		next.OutputTokens += bucket.OutputTokens
		next.TotalTokens += bucket.TotalTokens
	}
	stats = next
}

func hourStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC)
}

func bucketsCopyLocked() []hourBucket {
	out := append([]hourBucket(nil), hourBuckets...)
	if out == nil {
		out = []hourBucket{}
	}
	return out
}

func persistLocked() statsPersist {
	return statsPersist{
		Requests:     stats.Requests,
		Success:      stats.Success,
		Failed:       stats.Failed,
		InputTokens:  stats.InputTokens,
		OutputTokens: stats.OutputTokens,
		TotalTokens:  stats.TotalTokens,
		Since:        stats.Since,
		Buckets:      bucketsCopyLocked(),
		UsageByEmail: emailRowsLocked(),
	}
}

func emailKey(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func displayEmail(email string) string {
	return strings.TrimSpace(email)
}

func addEmailLocked(email string, success bool, total int64) {
	if emailUsageMap == nil {
		emailUsageMap = map[string]*emailUsage{}
	}
	key := emailKey(email)
	row := emailUsageMap[key]
	if row == nil {
		row = &emailUsage{Email: displayEmail(email)}
		emailUsageMap[key] = row
	}
	if success {
		row.Success++
	} else {
		row.Failed++
	}
	if total > 0 {
		row.TotalTokens += total
	}
}

func emailRowsLocked() []emailUsage {
	rows := make([]emailUsage, 0, len(emailUsageMap))
	for _, row := range emailUsageMap {
		if row == nil {
			continue
		}
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(i, j int) bool {
		left := rows[i].Success + rows[i].Failed
		right := rows[j].Success + rows[j].Failed
		if left != right {
			return left > right
		}
		if rows[i].TotalTokens != rows[j].TotalTokens {
			return rows[i].TotalTokens > rows[j].TotalTokens
		}
		return rows[i].Email < rows[j].Email
	})
	if rows == nil {
		rows = []emailUsage{}
	}
	return rows
}

func statsSnapshot() statsPayload {
	statsMu.Lock()
	since := stats.Since
	if since.IsZero() {
		since = nowFn().UTC()
	}
	payload := statsPayload{
		Requests:     stats.Requests,
		Success:      stats.Success,
		Failed:       stats.Failed,
		InputTokens:  stats.InputTokens,
		OutputTokens: stats.OutputTokens,
		TotalTokens:  stats.TotalTokens,
		Since:        since,
		Buckets:      bucketsCopyLocked(),
		UsageByEmail: emailRowsLocked(),
	}
	statsMu.Unlock()

	accountMu.Lock()
	if accountSnap != nil {
		copied := *accountSnap
		payload.Accounts = &copied
	}
	accountMu.Unlock()
	return payload
}

func writeStatsFile(path string, payload statsPersist) error {
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return errMarshal
	}
	dir := filepath.Dir(path)
	if errDir := os.MkdirAll(dir, 0o755); errDir != nil {
		return errDir
	}
	tmp, errCreate := os.CreateTemp(dir, ".ch-relay-vippro-stats-*")
	if errCreate != nil {
		return errCreate
	}
	tmpName := tmp.Name()
	if _, errWrite := tmp.Write(raw); errWrite != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return errWrite
	}
	if errClose := tmp.Close(); errClose != nil {
		_ = os.Remove(tmpName)
		return errClose
	}
	if errRename := os.Rename(tmpName, path); errRename != nil {
		_ = os.Remove(tmpName)
		return errRename
	}
	return nil
}

func usageProtocol(format, source string) string {
	for _, candidate := range []string{format, source} {
		switch strings.ToLower(strings.TrimSpace(candidate)) {
		case "openai-response", "openai-responses", "openai_responses", "responses", "codex":
			return "openai-response"
		}
	}
	return "openai"
}

func responseUsage(protocol string, payload []byte) (int64, int64, int64) {
	return countsFromDetail(helps.ParsePluginExecutorResponseUsage(protocol, payload))
}

func countsFromDetail(detail usage.Detail) (int64, int64, int64) {
	in := detail.InputTokens
	out := detail.OutputTokens
	total := detail.TotalTokens
	if total == 0 {
		total = in + out
	}
	return in, out, total
}

func managementRoutes() map[string]any {
	return map[string]any{
		"routes": []map[string]string{
			{"Method": http.MethodGet, "Path": "/ch-relay-vippro/stats"},
			{"Method": http.MethodPost, "Path": "/ch-relay-vippro/accounts/check"},
			{
				"Method":      http.MethodGet,
				"Path":        "/dashboard",
				"Menu":        "Dashboard",
				"Description": "Requests, tokens, and email usage",
			},
		},
	}
}

func managementStatus(raw []byte) ([]byte, error) {
	var req managementCall
	if len(bytesTrimSpace(raw)) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}
	return handleManagement(req)
}

func handleManagement(req managementCall) ([]byte, error) {
	path := managementPath(req.Path)
	switch {
	case strings.HasSuffix(path, "/ch-relay-vippro/accounts/check"):
		if !strings.EqualFold(strings.TrimSpace(req.Method), http.MethodPost) {
			return managementJSON(http.StatusMethodNotAllowed, []byte(`{"error":"method not allowed"}`))
		}
		startAccountCheck(req.HostCallbackID)
		return managementJSON(http.StatusOK, mustStatsJSON())
	case strings.HasSuffix(path, "/ch-relay-vippro/dashboard"), strings.HasSuffix(path, "/dashboard"),
		strings.HasSuffix(path, "/ch-relay-vippro/usage"), strings.HasSuffix(path, "/usage"):
		if method := strings.TrimSpace(req.Method); method != "" && !strings.EqualFold(method, http.MethodGet) {
			return managementJSON(http.StatusMethodNotAllowed, []byte(`{"error":"method not allowed"}`))
		}
		if strings.EqualFold(req.Query.Get("format"), "json") {
			return managementJSON(http.StatusOK, dashboardJSON())
		}
		return managementHTML(http.StatusOK, dashboardHTML)
	case strings.HasSuffix(path, "/ch-relay-vippro/stats"):
		if method := strings.TrimSpace(req.Method); method != "" && !strings.EqualFold(method, http.MethodGet) {
			return managementJSON(http.StatusMethodNotAllowed, []byte(`{"error":"method not allowed"}`))
		}
		return managementJSON(http.StatusOK, mustStatsJSON())
	default:
		return managementJSON(http.StatusNotFound, []byte(`{"error":"not found"}`))
	}
}

func managementPath(path string) string {
	path = strings.TrimSpace(path)
	if i := strings.Index(path, "?"); i >= 0 {
		path = path[:i]
	}
	return strings.TrimRight(path, "/")
}

func mustStatsJSON() []byte {
	raw, errMarshal := json.Marshal(statsSnapshot())
	if errMarshal != nil {
		return []byte(`{"error":"stats"}`)
	}
	return raw
}

func maskEmail(email string) string {
	trimmed := strings.TrimSpace(email)
	at := strings.LastIndex(trimmed, "@")
	if at <= 0 || at == len(trimmed)-1 {
		return trimmed
	}
	local := trimmed[:at]
	if len(local) > 3 {
		local = local[:3]
	}
	domain := trimmed[at+1:]
	suffix := ""
	if dot := strings.LastIndex(domain, "."); dot >= 0 {
		suffix = domain[dot:]
	}
	return local + "***@****" + suffix
}

type dashBar struct {
	X, Y, W, H       int
	Label            string
	Requests, Tokens int64
}

func dashboardRange(rang string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(rang)) {
	case "day":
		return "day", "Sum of the last 24 hours."
	case "week":
		return "week", "One bar per day for the last 7 days."
	case "month":
		return "month", "One bar per day for the last 30 days."
	default:
		return "hour", "Each bar is one hour over the last 24 hours."
	}
}

func dashboardBars(buckets []hourBucket, rang string, now time.Time) []dashBar {
	points := dashboardPoints(buckets, rang, now)
	maxReq := int64(1)
	for _, point := range points {
		if point.Requests > maxReq {
			maxReq = point.Requests
		}
	}
	bars := make([]dashBar, len(points))
	for i, point := range points {
		height := int(point.Requests * 100 / maxReq)
		if point.Requests > 0 && height < 2 {
			height = 2
		}
		bars[i] = dashBar{X: i * 12, Y: 100 - height, W: 8, H: height, Label: point.Label, Requests: point.Requests, Tokens: point.Tokens}
	}
	return bars
}

type chartPoint struct {
	Label            string
	Requests, Tokens int64
}

func dashboardPoints(buckets []hourBucket, rang string, now time.Time) []chartPoint {
	end := hourStart(now.UTC())
	switch rang {
	case "day":
		return []chartPoint{sumPoints(fillHours(buckets, end.Add(-23*time.Hour), 24), "24h")}
	case "week":
		return fillDays(buckets, end, 7)
	case "month":
		return fillDays(buckets, end, 30)
	default:
		return fillHours(buckets, end.Add(-23*time.Hour), 24)
	}
}

func sumPoints(points []chartPoint, label string) chartPoint {
	var total chartPoint
	total.Label = label
	for _, point := range points {
		total.Requests += point.Requests
		total.Tokens += point.Tokens
	}
	return total
}

func fillHours(buckets []hourBucket, start time.Time, count int) []chartPoint {
	totals := bucketTotals(buckets, hourStart)
	out := make([]chartPoint, count)
	for i := 0; i < count; i++ {
		at := start.Add(time.Duration(i) * time.Hour)
		item := totals[at.Unix()]
		out[i] = chartPoint{Label: at.Format("15:04"), Requests: item.Requests, Tokens: item.Tokens}
	}
	return out
}

func fillDays(buckets []hourBucket, end time.Time, count int) []chartPoint {
	totals := bucketTotals(buckets, dayStart)
	first := dayStart(end).AddDate(0, 0, -(count - 1))
	out := make([]chartPoint, count)
	for i := 0; i < count; i++ {
		at := first.AddDate(0, 0, i)
		item := totals[at.Unix()]
		out[i] = chartPoint{Label: at.Format("01-02"), Requests: item.Requests, Tokens: item.Tokens}
	}
	return out
}

func dayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

type chartTotals struct {
	Requests int64
	Tokens   int64
}

func bucketTotals(buckets []hourBucket, keyFn func(time.Time) time.Time) map[int64]chartTotals {
	totals := map[int64]chartTotals{}
	for _, bucket := range buckets {
		key := keyFn(bucket.Start).Unix()
		item := totals[key]
		item.Requests += bucket.Requests
		item.Tokens += bucket.TotalTokens
		totals[key] = item
	}
	return totals
}

func dashboardJSON() []byte {
	snap := statsSnapshot()
	now := nowFn()
	type bar struct {
		Label    string `json:"label"`
		Requests int64  `json:"requests"`
		Tokens   int64  `json:"tokens"`
		Height   int    `json:"height"`
	}
	type chart struct {
		Hint string `json:"hint"`
		Bars []bar  `json:"bars"`
	}
	charts := map[string]chart{}
	for _, rang := range []string{"hour", "day", "week", "month"} {
		name, hint := dashboardRange(rang)
		points := dashboardBars(snap.Buckets, name, now)
		bars := make([]bar, len(points))
		for i, item := range points {
			bars[i] = bar{Label: item.Label, Requests: item.Requests, Tokens: item.Tokens, Height: item.H}
		}
		charts[name] = chart{Hint: hint, Bars: bars}
	}
	type email struct {
		Email   string `json:"email"`
		Masked  string `json:"masked"`
		Success int64  `json:"success"`
		Failed  int64  `json:"failed"`
		Tokens  int64  `json:"tokens"`
	}
	emails := make([]email, 0, len(snap.UsageByEmail))
	for _, row := range snap.UsageByEmail {
		emails = append(emails, email{Email: row.Email, Masked: maskEmail(row.Email), Success: row.Success, Failed: row.Failed, Tokens: row.TotalTokens})
	}
	progress := ""
	if snap.Accounts != nil && snap.Accounts.Running {
		progress = fmt.Sprintf("Checking %d / %d", snap.Accounts.Checked, snap.Accounts.Total)
	}
	raw, errMarshal := json.Marshal(map[string]any{
		"since":    snap.Since.UTC().Format(time.RFC3339),
		"requests": snap.Requests, "success": snap.Success, "failed": snap.Failed,
		"input_tokens": snap.InputTokens, "output_tokens": snap.OutputTokens, "total_tokens": snap.TotalTokens,
		"emails": emails, "charts": charts, "accounts": snap.Accounts, "progress": progress,
	})
	if errMarshal != nil {
		return []byte(`{"emails":[]}`)
	}
	return raw
}

func managementHTML(status int, body []byte) ([]byte, error) {
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       body,
	})
}

func managementJSON(status int, body []byte) ([]byte, error) {
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       body,
	})
}

func startAccountChecker() {
	checkerMu.Lock()
	defer checkerMu.Unlock()
	if checkerCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	checkerCancel = cancel
	checkerWG.Add(1)
	go accountCheckerLoop(ctx)
}

func accountCheckerLoop(ctx context.Context) {
	defer checkerWG.Done()
	timer := time.NewTimer(accountCheckInitialDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	if ctx.Err() != nil {
		return
	}
	startAccountCheck("")
	ticker := time.NewTicker(accountCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			startAccountCheck("")
		}
	}
}

func stopAccountChecker() {
	checkerMu.Lock()
	cancelChecker := checkerCancel
	checkerCancel = nil
	checkerMu.Unlock()
	if cancelChecker != nil {
		cancelChecker()
	}
	accountMu.Lock()
	cancelCheck := checkCancel
	accountMu.Unlock()
	if cancelCheck != nil {
		cancelCheck()
	}
	checkerWG.Wait()
	checkWG.Wait()
}

func startAccountCheck(callbackID string) {
	accountMu.Lock()
	if accountSnap != nil && accountSnap.Running {
		accountMu.Unlock()
		return
	}
	if accountSnap == nil {
		accountSnap = &accountSnapshot{}
	}
	accountSnap.Running = true
	ctx, cancel := context.WithCancel(context.Background())
	checkCancel = cancel
	checkWG.Add(1)
	accountMu.Unlock()
	go func() {
		defer checkWG.Done()
		defer cancel()
		runAccountCheck(callbackID, ctx)
	}()
}

func runAccountCheck(callbackID string, ctx context.Context) {
	started := nowFn()
	listed := false
	defer func() {
		accountMu.Lock()
		defer accountMu.Unlock()
		if accountSnap == nil {
			return
		}
		accountSnap.Running = false
		if !listed {
			return
		}
		accountSnap.CheckedAt = nowFn().UTC().Format(time.RFC3339)
		accountSnap.DurationMS = nowFn().Sub(started).Milliseconds()
	}()
	if ctx.Err() != nil {
		return
	}
	raw, errList := callHost(pluginabi.MethodHostAuthList, map[string]string{"host_callback_id": callbackID})
	if errList != nil || ctx.Err() != nil {
		return
	}
	var listedFiles struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	if errUnmarshal := json.Unmarshal(raw, &listedFiles); errUnmarshal != nil {
		return
	}
	enabled := make([]pluginapi.HostAuthFileEntry, 0, len(listedFiles.Files))
	disabled := 0
	for _, file := range listedFiles.Files {
		if !strings.EqualFold(strings.TrimSpace(file.Provider), "codex") {
			continue
		}
		if file.Disabled {
			disabled++
			continue
		}
		enabled = append(enabled, file)
	}
	accountMu.Lock()
	if accountSnap == nil {
		accountSnap = &accountSnapshot{}
	}
	accountSnap.Running = true
	accountSnap.Checked = 0
	accountSnap.Total = len(enabled)
	accountSnap.Eligible = 0
	accountSnap.Plus = 0
	accountSnap.Cooling = 0
	accountSnap.Unavailable = 0
	accountSnap.DisabledSkipped = disabled
	accountSnap.DurationMS = nowFn().Sub(started).Milliseconds()
	accountMu.Unlock()
	listed = true
	if len(enabled) == 0 || ctx.Err() != nil {
		return
	}
	runAccountWorkers(ctx, callbackID, enabled, started)
}

func runAccountWorkers(ctx context.Context, callbackID string, enabled []pluginapi.HostAuthFileEntry, started time.Time) {
	workers := accountCheckWorkers
	if len(enabled) < workers {
		workers = len(enabled)
	}
	jobs := make(chan pluginapi.HostAuthFileEntry)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for file := range jobs {
				if ctx.Err() != nil {
					continue
				}
				kind, plus := classifyEnabled(file, callbackID, nowFn())
				accountMu.Lock()
				if accountSnap != nil {
					accountSnap.Checked++
					switch kind {
					case kindEligible:
						accountSnap.Eligible++
						if plus {
							accountSnap.Plus++
						}
					case kindCooling:
						accountSnap.Cooling++
					default:
						accountSnap.Unavailable++
					}
					accountSnap.DurationMS = nowFn().Sub(started).Milliseconds()
				}
				accountMu.Unlock()
			}
		}()
	}
	for _, file := range enabled {
		if ctx.Err() != nil {
			break
		}
		jobs <- file
	}
	close(jobs)
	wg.Wait()
}

func classifyEnabled(file pluginapi.HostAuthFileEntry, callbackID string, now time.Time) (accountKind, bool) {
	index := strings.TrimSpace(file.AuthIndex)
	token := ""
	plus := false
	var errGet error
	if index != "" {
		token, plus, errGet = readAuth(callbackID, index)
	}
	if file.Unavailable || errGet != nil || strings.TrimSpace(token) == "" {
		return kindUnavailable, false
	}
	if !file.NextRetryAfter.IsZero() && file.NextRetryAfter.After(now) {
		return kindCooling, false
	}
	return kindEligible, plus
}
