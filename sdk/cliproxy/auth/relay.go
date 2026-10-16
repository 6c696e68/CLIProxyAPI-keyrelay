package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// RelayRequest is the token-free request accepted from a trusted relay host.
// TargetBaseURL must be the host-validated configured HTTPS base URL.
type RelayRequest struct {
	TargetBaseURL string
	Model         string
	Method        string
	RelativePath  string
	Headers       http.Header
	Body          []byte
	ReplaySafe    bool
}

// RelayRequestPolicy controls Codex account selection and bounded retries.
type RelayRequestPolicy struct {
	RotationMode   string
	PlusPreference string
	// AffinityKey binds sticky selection only after a successful upstream response.
	AffinityKey string
	// StickyTTL controls the sliding affinity lifetime; zero keeps the default.
	StickyTTL time.Duration
	// MaxRetries bounds additional outbound sends, including a refreshed 401 replay.
	// Refresh and preparation failures may try the remaining snapshot candidates.
	MaxRetries int
	Retry401   bool
	Retry429   bool
	Retry5xx   bool
}

// RelayResponse contains upstream response data with credential-bearing
// headers and exact selected-token echoes removed.
type RelayResponse struct {
	StatusCode   int
	Headers      http.Header
	Body         []byte
	BodySent     bool
	RetryOutcome string
}

const (
	relayRetryOutcomeNone        = "not_retried"
	relayRetryOutcomeExhausted   = "retry_exhausted"
	relayRetryOutcomeNoCandidate = "no_distinct_candidate"
	relayMaxRetries              = 8
	relayAffinityTTL             = time.Hour
	relayAffinityMaxEntries      = 4096
)

type relayAffinityKey struct {
	manager *Manager
	model   string
	key     string
}

type relayAffinityEntry struct {
	authID  string
	expires time.Time
	ttl     time.Duration
}

var relayAffinities = struct {
	sync.Mutex
	entries map[relayAffinityKey]relayAffinityEntry
}{entries: make(map[relayAffinityKey]relayAffinityEntry)}

// ExecuteRelayRequest selects and executes one trusted Codex relay request. All
// auth snapshots, credential preparation, refresh, retries, and result updates
// remain inside this package.
func (m *Manager) ExecuteRelayRequest(ctx context.Context, request RelayRequest, policy RelayRequestPolicy) (RelayResponse, error) {
	return m.executeRelayRequest(ctx, request, policy, nil)
}

func (m *Manager) executeRelayRequest(ctx context.Context, request RelayRequest, policy RelayRequestPolicy, stream *relayStreamAttempt) (RelayResponse, error) {
	if m == nil {
		return RelayResponse{}, errors.New("auth manager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	base, errTarget := validateRelayRequest(request)
	if errTarget != nil {
		return RelayResponse{}, errTarget
	}
	if policy.RotationMode == "" {
		policy.RotationMode = "round-robin"
	}
	if policy.RotationMode != "round-robin" && policy.RotationMode != "sticky" && policy.RotationMode != "fill-first" && policy.RotationMode != "least-recent" {
		return RelayResponse{}, errors.New("invalid relay rotation mode")
	}
	policy.PlusPreference = strings.ToLower(strings.TrimSpace(policy.PlusPreference))
	if policy.PlusPreference == "" {
		policy.PlusPreference = "ignore"
	}
	if policy.PlusPreference != "ignore" && policy.PlusPreference != "prefer" && policy.PlusPreference != "only" {
		return RelayResponse{}, errors.New("invalid relay Plus preference")
	}
	if len(policy.AffinityKey) > 1024 || policy.MaxRetries < 0 || policy.StickyTTL < 0 || request.ReplaySafe && !(request.Method == http.MethodGet && request.RelativePath == "/models") {
		return RelayResponse{}, errors.New("invalid relay policy")
	}
	if policy.MaxRetries > relayMaxRetries {
		policy.MaxRetries = relayMaxRetries
	}
	if policy.StickyTTL == 0 {
		policy.StickyTTL = relayAffinityTTL
	}
	send := m.relayRoundTrip
	if stream != nil {
		send = func(ctx context.Context, auth *Auth, request RelayRequest, target string) (RelayResponse, error, bool) {
			return stream.send(m, ctx, auth, request, target)
		}
	}

	target := *base
	target.Path = strings.TrimRight(target.Path, "/") + request.RelativePath
	target.RawPath = ""

	candidates, errCandidates := m.relayCandidates(ctx, request.Model, policy.PlusPreference)
	if errCandidates != nil {
		return RelayResponse{}, errCandidates
	}
	if len(candidates) == 0 {
		if policy.PlusPreference == "only" {
			return RelayResponse{}, errors.New("no_plus_codex_auth_available")
		}
		return RelayResponse{}, &Error{Code: "auth_not_found", Message: "no eligible Codex auth available", HTTPStatus: http.StatusServiceUnavailable}
	}

	// Each snapshot candidate is tried once; failed preparation is not an outbound send.
	tried := make(map[string]struct{}, len(candidates))
	response := RelayResponse{RetryOutcome: relayRetryOutcomeNone}
	retryOutcome := relayRetryOutcomeNone
	var preSendError error
	sends := 0
	maxSends := 1 + policy.MaxRetries
	clearAffinity := func() {
		if policy.RotationMode == "sticky" && policy.AffinityKey != "" {
			relayAffinityDelete(m, request.Model, policy.AffinityKey)
		}
	}
	for {
		if errContext := ctx.Err(); errContext != nil {
			return response, errContext
		}
		auth, errPick := m.pickRelayAuth(request.Model, policy, candidates, tried)
		if errPick != nil {
			if policy.PlusPreference == "only" && sends == 0 {
				return response, errors.New("no_plus_codex_auth_available")
			}
			if len(tried) == 0 {
				return RelayResponse{}, errPick
			}
			if sends == 0 && preSendError != nil {
				return response, preSendError
			}
			response.RetryOutcome = relayRetryOutcomeNoCandidate
			if response.StatusCode >= http.StatusBadRequest {
				return response, nil
			}
			return response, errors.New("relay retry has no distinct eligible Codex auth")
		}
		tried[auth.ID] = struct{}{}

		if !auth.HasValidAccessToken(time.Now()) {
			refreshed, errRefresh := m.refreshAuthForRequest(ctx, auth.ID, authAccessToken(auth))
			if errRefresh != nil || refreshed == nil || !refreshed.HasValidAccessToken(time.Now()) {
				clearAffinity()
				m.MarkResult(ctx, relayResult(auth, request.Model, &Error{Code: ErrorCodeTransientTransport, Message: "Codex access token unavailable", Retryable: false, HTTPStatus: http.StatusUnauthorized}))
				preSendError = errors.New("no eligible Codex auth available")
				continue
			}
			auth = refreshed
		}
		if policy.PlusPreference == "only" && !m.relayAuthIsPlus(auth) {
			clearAffinity()
			continue
		}

		executor := m.executorFor("codex")
		prepared, errPrepare := m.prepareRequestAuth(ctx, executor, auth)
		if errPrepare != nil {
			clearAffinity()
			m.MarkResult(ctx, relayResult(auth, request.Model, resultErrorFromError(errPrepare)))
			preSendError = errors.New("Codex relay request preparation failed")
			continue
		}
		if prepared != nil {
			auth = prepared
		}
		if policy.PlusPreference == "only" && !m.relayAuthIsPlus(auth) {
			clearAffinity()
			continue
		}
		if sends >= maxSends {
			response.RetryOutcome = relayRetryOutcomeExhausted
			if response.StatusCode >= http.StatusBadRequest {
				return response, nil
			}
			return response, fmt.Errorf("relay retries exhausted before upstream send")
		}
		resp, errRoundTrip, sent := send(ctx, auth, request, target.String())
		if sent {
			sends++
		}
		resp.RetryOutcome = retryOutcome
		if errRoundTrip != nil {
			resp.BodySent = sent && len(request.Body) > 0
			if !sent {
				clearAffinity()
				m.MarkResult(ctx, relayResult(auth, request.Model, resultErrorFromError(errRoundTrip)))
				preSendError = errRoundTrip
				continue
			}
			clearAffinity()
			if errContext := ctx.Err(); errContext != nil {
				return resp, errContext
			}
			m.MarkResult(ctx, relayResult(auth, request.Model, &Error{Code: ErrorCodeTransientTransport, Message: "relay upstream transport failed", Retryable: request.ReplaySafe, HTTPStatus: http.StatusBadGateway}))
			if request.ReplaySafe && request.Method == http.MethodGet && request.RelativePath == "/models" && sends < maxSends {
				retryOutcome = "retried"
				response = resp
				continue
			}
			if resp.BodySent {
				resp.RetryOutcome = "replay_suppressed_body_sent"
			}
			return resp, errors.New("relay upstream transport failed")
		}

		resp.BodySent = len(request.Body) > 0
		if request.ReplaySafe && resp.StatusCode == http.StatusUnauthorized && policy.Retry401 && sends < maxSends {
			if refreshed, ok := m.tryRefreshAfterUnauthorized(ctx, auth, relayStatusError(resp.StatusCode), false); ok && refreshed != nil {
				if policy.PlusPreference == "only" && !m.relayAuthIsPlus(refreshed) {
					clearAffinity()
					response = resp
					continue
				}
				refreshedPrepared, errPrepareRetry := m.prepareRequestAuth(ctx, executor, refreshed)
				if errPrepareRetry == nil {
					if refreshedPrepared != nil {
						refreshed = refreshedPrepared
					}
					if policy.PlusPreference == "only" && !m.relayAuthIsPlus(refreshed) {
						clearAffinity()
						response = resp
						continue
					}
					if sends >= maxSends {
						resp.RetryOutcome = relayRetryOutcomeExhausted
						return resp, nil
					}
					retryResponse, errRetry, retrySent := send(ctx, refreshed, request, target.String())
					if retrySent {
						sends++
					}
					retryResponse.RetryOutcome = "retried"
					if errRetry != nil {
						if errContext := ctx.Err(); errContext != nil {
							return retryResponse, errContext
						}
						clearAffinity()
						if !retrySent {
							m.MarkResult(ctx, relayResult(refreshed, request.Model, resultErrorFromError(errRetry)))
						} else {
							retryResponse.BodySent = len(request.Body) > 0
							m.MarkResult(ctx, relayResult(refreshed, request.Model, &Error{Code: ErrorCodeTransientTransport, Message: "relay upstream transport failed", Retryable: request.ReplaySafe, HTTPStatus: http.StatusBadGateway}))
							if retryResponse.BodySent {
								retryResponse.RetryOutcome = "replay_suppressed_body_sent"
							}
							return retryResponse, errRetry
						}
					} else {
						retryResponse.BodySent = len(request.Body) > 0
						resp, auth = retryResponse, refreshed
						retryOutcome = "retried"
						resp.RetryOutcome = retryOutcome
					}
				} else {
					clearAffinity()
					m.MarkResult(ctx, relayResult(refreshed, request.Model, resultErrorFromError(errPrepareRetry)))
				}
			}
		}
		statusError := relayStatusError(resp.StatusCode)
		if stream == nil || statusError != nil {
			m.MarkResult(ctx, relayResult(auth, request.Model, statusError))
		}
		if stream != nil {
			stream.auth = auth
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			if policy.RotationMode == "sticky" && policy.AffinityKey != "" {
				relayAffinitySet(m, request.Model, policy.AffinityKey, auth.ID, policy.StickyTTL)
			}
			return resp, nil
		}
		clearAffinity()
		retryStatus := request.ReplaySafe && request.Method == http.MethodGet && request.RelativePath == "/models" &&
			(resp.StatusCode == http.StatusUnauthorized && policy.Retry401 ||
				resp.StatusCode == http.StatusTooManyRequests && policy.Retry429 ||
				resp.StatusCode >= 500 && resp.StatusCode <= 599 && policy.Retry5xx)
		if !retryStatus {
			return resp, nil
		}
		if sends >= maxSends {
			resp.RetryOutcome = relayRetryOutcomeExhausted
			return resp, nil
		}
		response = resp
		retryOutcome = "retried"
	}
}

func validateRelayRequest(request RelayRequest) (*url.URL, error) {
	baseText := strings.TrimSpace(request.TargetBaseURL)
	base, err := url.Parse(baseText)
	if err != nil || base == nil || !strings.EqualFold(base.Scheme, "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.ForceQuery || strings.Contains(baseText, "#") || base.Opaque != "" {
		return nil, errors.New("invalid relay HTTPS target")
	}
	if request.RelativePath != "/models" && strings.TrimSpace(request.Model) == "" {
		return nil, errors.New("relay model is required")
	}
	if !strings.HasPrefix(request.RelativePath, "/") || strings.ContainsAny(request.RelativePath, "?#\\") || strings.Contains(request.RelativePath, "..") {
		return nil, errors.New("invalid relay relative path")
	}
	switch {
	case request.Method == http.MethodGet && request.RelativePath == "/models":
	case request.Method == http.MethodPost && (request.RelativePath == "/chat/completions" || request.RelativePath == "/responses"):
	default:
		return nil, errors.New("relay method and path are not allowed")
	}
	return base, nil
}

func (m *Manager) relayCandidates(ctx context.Context, model, plusPreference string) ([]*Auth, error) {
	_ = ctx
	now := time.Now()
	m.mu.RLock()
	candidates := make([]*Auth, 0, len(m.auths))
	hasPlus := false
	for _, candidate := range m.auths {
		if candidate == nil || !strings.EqualFold(strings.TrimSpace(candidate.Provider), "codex") || candidate.Disabled || candidate.Status == StatusDisabled || candidate.AuthKind() != AuthKindOAuth {
			continue
		}
		isPlus := strings.EqualFold(strings.TrimSpace(candidate.Attributes["plan_type"]), "plus")
		hasPlus = hasPlus || isPlus
		if plusPreference == "only" && !isPlus || !m.relayAuthAvailable(candidate, model, now) {
			continue
		}
		candidates = append(candidates, candidate.Clone())
	}
	m.mu.RUnlock()
	if plusPreference == "only" && !hasPlus {
		return nil, errors.New("no_plus_codex_auth_available")
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	return candidates, nil
}

func (m *Manager) relayAuthIsPlus(auth *Auth) bool {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Attributes["plan_type"]), "plus") {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	current := m.auths[auth.ID]
	return current != nil && strings.EqualFold(strings.TrimSpace(current.Attributes["plan_type"]), "plus")
}

func (m *Manager) relayAuthAvailable(auth *Auth, model string, now time.Time) bool {
	if auth == nil {
		return false
	}
	if !auth.HasValidAccessToken(now) {
		if !authHasRefreshCredential(auth) {
			return false
		}
		// Check the existing availability policy without treating refreshable expiry
		// as a cooldown. Credentials are refreshed only after this auth is selected.
		availabilityAuth := *auth
		availabilityAuth.Metadata = nil
		availabilityAuth.RejectedAccessToken = ""
		auth = &availabilityAuth
	}
	blocked, _, _ := isAuthBlockedForModel(auth, model, now)
	return !blocked
}

func (m *Manager) currentRelayCandidate(candidate *Auth) (*Auth, bool) {
	if m == nil || candidate == nil {
		return nil, false
	}
	m.mu.RLock()
	current := m.auths[candidate.ID]
	if current == nil || current.RegistrationEpoch != candidate.RegistrationEpoch || !strings.EqualFold(strings.TrimSpace(current.Provider), "codex") || current.Disabled || current.Status == StatusDisabled || current.AuthKind() != AuthKindOAuth {
		m.mu.RUnlock()
		return nil, false
	}
	clone := current.Clone()
	m.mu.RUnlock()
	return clone, true
}

func (m *Manager) pickRelayAuth(model string, policy RelayRequestPolicy, candidates []*Auth, tried map[string]struct{}) (*Auth, error) {
	available := make([]*Auth, 0, len(candidates))
	now := time.Now()
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		current, ok := m.currentRelayCandidate(candidate)
		if !ok || policy.PlusPreference == "only" && !strings.EqualFold(strings.TrimSpace(current.Attributes["plan_type"]), "plus") {
			continue
		}
		if _, used := tried[current.ID]; used || !m.relayAuthAvailable(current, model, now) {
			continue
		}
		available = append(available, current)
	}
	if policy.RotationMode == "sticky" && policy.AffinityKey != "" {
		if authID := relayAffinityGet(m, model, policy.AffinityKey); authID != "" {
			found := false
			for _, candidate := range available {
				if candidate.ID == authID {
					found = true
					break
				}
			}
			if !found {
				relayAffinityDelete(m, model, policy.AffinityKey)
			}
		}
	}

	if len(available) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no unused Codex auth available", HTTPStatus: http.StatusServiceUnavailable}
	}
	if policy.PlusPreference == "prefer" {
		plus := make([]*Auth, 0, len(available))
		for _, candidate := range available {
			if strings.EqualFold(strings.TrimSpace(candidate.Attributes["plan_type"]), "plus") {
				plus = append(plus, candidate)
			}
		}
		if len(plus) > 0 {
			available = plus
		}
	}
	available = highestPriorityAuths(available)
	if policy.RotationMode == "sticky" && policy.AffinityKey != "" {
		if authID := relayAffinityGet(m, model, policy.AffinityKey); authID != "" {
			for _, candidate := range available {
				if candidate.ID == authID {
					return candidate.Clone(), nil
				}
			}
		}
	}
	selected := available[0]
	switch policy.RotationMode {
	case "fill-first":
	case "least-recent":
		for _, candidate := range available[1:] {
			if candidate.UpdatedAt.Before(selected.UpdatedAt) {
				selected = candidate
			}
		}
	default:
		rotationKey := "relay:" + canonicalModelKey(model) + ":" + policy.PlusPreference
		m.mu.Lock()
		lastID := m.relayLastPicked[rotationKey]
		selected = available[successorIndex(available, lastID)]
		m.relayLastPicked[rotationKey] = selected.ID
		m.mu.Unlock()
	}
	return selected.Clone(), nil
}

func (m *Manager) relayOpenUpstream(ctx context.Context, auth *Auth, request RelayRequest, targetURL string) (*http.Response, []string, error, bool) {
	var body io.Reader
	if request.Body != nil {
		body = bytes.NewReader(request.Body)
	}
	req, errNew := http.NewRequestWithContext(ctx, request.Method, targetURL, body)
	if errNew != nil {
		return nil, nil, errors.New("invalid relay request"), false
	}
	req.Header = sanitizeRelayRequestHeaders(request.Headers)
	req.Header.Del("Authorization")
	executor := m.executorFor("codex")
	if preparer, ok := executor.(RequestPreparer); ok && preparer != nil {
		if errPrepare := preparer.PrepareRequest(req, auth); errPrepare != nil {
			return nil, nil, errors.New("Codex relay request preparation failed"), false
		}
	} else {
		token := authAccessToken(auth)
		if token == "" {
			return nil, nil, errors.New("Codex access token unavailable"), false
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if request.Method == http.MethodPost && !request.ReplaySafe {
		// Prevent net/http from automatically replaying an ambiguous POST, even
		// when the executor adds an idempotency marker during preparation.
		req.GetBody = nil
		req.Header.Del("Idempotency-Key")
		req.Header.Del("X-Idempotency-Key")
	}
	secrets := relayRequestSecrets(req, auth)
	transport := m.roundTripperFor(auth)
	if transport == nil {
		transport, _ = ctx.Value(roundTripperContextKey{}).(http.RoundTripper)
	}
	if transport == nil {
		transport, _ = ctx.Value("cliproxy.roundtripper").(http.RoundTripper)
	}
	if transport == nil {
		transport = http.DefaultTransport
	}
	upstream, errRoundTrip := transport.RoundTrip(req)
	if errRoundTrip != nil {
		if upstream != nil && upstream.Body != nil {
			_ = upstream.Body.Close()
		}
		return nil, nil, errors.New("relay upstream transport failed"), true
	}
	if upstream == nil || upstream.Body == nil {
		return nil, nil, errors.New("relay upstream returned no response"), true
	}
	return upstream, secrets, nil, true
}

func (m *Manager) relayRoundTrip(ctx context.Context, auth *Auth, request RelayRequest, targetURL string) (RelayResponse, error, bool) {
	upstream, secrets, errOpen, sent := m.relayOpenUpstream(ctx, auth, request, targetURL)
	if errOpen != nil {
		return RelayResponse{}, errOpen, sent
	}
	defer upstream.Body.Close()
	payload, errRead := io.ReadAll(upstream.Body)
	if errRead != nil {
		return RelayResponse{}, errors.New("relay upstream response read failed"), true
	}
	return RelayResponse{
		StatusCode: upstream.StatusCode,
		Headers:    sanitizeRelayResponseHeaders(upstream.Header, secrets...),
		Body:       redactRelaySecrets(payload, secrets...),
	}, nil, true
}

func relayRequestSecrets(req *http.Request, auth *Auth) []string {
	secrets := make([]string, 0, 3)
	if authorization := req.Header.Get("Authorization"); authorization != "" {
		secrets = append(secrets, authorization)
		if strings.HasPrefix(strings.ToLower(authorization), "bearer ") {
			secrets = append(secrets, strings.TrimSpace(authorization[len("Bearer "):]))
		}
	}
	for _, header := range []string{"X-Api-Key", "X-Goog-Api-Key", "Proxy-Authorization"} {
		for _, value := range req.Header.Values(header) {
			if value != "" {
				secrets = append(secrets, value)
			}
		}
	}
	if token := authAccessToken(auth); token != "" {
		secrets = append(secrets, token)
	}
	if auth != nil && auth.Attributes != nil {
		if token := strings.TrimSpace(auth.Attributes[AttributeAPIKey]); token != "" {
			secrets = append(secrets, token)
		}
		for key := range auth.Attributes {
			if strings.HasPrefix(strings.ToLower(key), "header:") {
				for _, value := range req.Header.Values(strings.TrimSpace(key[len("header:"):])) {
					if value != "" {
						secrets = append(secrets, value)
					}
				}
			}
		}
	}
	return secrets
}

func sanitizeRelayRequestHeaders(headers http.Header) http.Header {
	out := make(http.Header)
	for key, values := range headers {
		switch http.CanonicalHeaderKey(key) {
		case "Accept", "Content-Type", "Openai-Beta", "User-Agent", "Traceparent", "Tracestate", "X-Request-Id", "Idempotency-Key":
			for _, value := range values {
				if !strings.ContainsAny(value, "\r\n") {
					out.Add(key, value)
				}
			}
		}
	}
	return out
}

func sanitizeRelayResponseHeaders(headers http.Header, secrets ...string) http.Header {
	out := make(http.Header)
	for key, values := range headers {
		switch http.CanonicalHeaderKey(key) {
		case "Content-Type", "Cache-Control", "Retry-After", "X-Request-Id", "Openai-Version", "Openai-Processing-Ms":
			for _, value := range values {
				out.Add(key, string(redactRelaySecrets([]byte(value), secrets...)))
			}
		}
	}
	return out
}

func redactRelaySecrets(data []byte, secrets ...string) []byte {
	for _, secret := range secrets {
		if secret != "" {
			data = bytes.ReplaceAll(data, []byte(secret), []byte("[REDACTED]"))
		}
	}
	return data
}

func relayStatusError(status int) *Error {
	if status < 400 {
		return nil
	}
	return &Error{Code: "relay_upstream_status", Message: fmt.Sprintf("Codex upstream returned HTTP %d", status), Retryable: status == 401 || status == 429 || status >= 500, HTTPStatus: status}
}

func relayResult(auth *Auth, model string, err *Error) Result {
	return Result{
		AuthID: auth.ID, Provider: "codex", Model: model, RouteModel: model,
		Success: err == nil, Error: err, CredentialVersion: auth.CredentialVersion,
		RegistrationEpoch: auth.RegistrationEpoch,
	}
}

func relayAffinityGet(manager *Manager, model, key string) string {
	now := time.Now()
	cacheKey := relayAffinityKey{manager: manager, model: canonicalModelKey(model), key: key}
	relayAffinities.Lock()
	defer relayAffinities.Unlock()
	for entryKey, entry := range relayAffinities.entries {
		if !now.Before(entry.expires) {
			delete(relayAffinities.entries, entryKey)
		}
	}
	entry, ok := relayAffinities.entries[cacheKey]
	if !ok {
		return ""
	}
	entry.expires = now.Add(entry.ttl)
	relayAffinities.entries[cacheKey] = entry
	return entry.authID
}

func relayAffinitySet(manager *Manager, model, key, authID string, ttl ...time.Duration) {
	lifetime := relayAffinityTTL
	if len(ttl) > 0 && ttl[0] > 0 {
		lifetime = ttl[0]
	}
	now := time.Now()
	cacheKey := relayAffinityKey{manager: manager, model: canonicalModelKey(model), key: key}
	relayAffinities.Lock()
	defer relayAffinities.Unlock()
	for entryKey, entry := range relayAffinities.entries {
		if entryKey == cacheKey {
			continue
		}
		if !now.Before(entry.expires) {
			delete(relayAffinities.entries, entryKey)
		}
	}
	if len(relayAffinities.entries) >= relayAffinityMaxEntries {
		clear(relayAffinities.entries)
	}
	relayAffinities.entries[cacheKey] = relayAffinityEntry{authID: authID, expires: now.Add(lifetime), ttl: lifetime}
}

func relayAffinityDelete(manager *Manager, model, key string) {
	relayAffinities.Lock()
	delete(relayAffinities.entries, relayAffinityKey{manager: manager, model: canonicalModelKey(model), key: key})
	relayAffinities.Unlock()
}
