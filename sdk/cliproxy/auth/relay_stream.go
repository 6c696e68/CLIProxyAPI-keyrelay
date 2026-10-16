package auth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RelayPoolStatus reports aggregate Codex pool health without exposing auth metadata.
type RelayPoolStatus struct {
	Total    int `json:"total"`
	Eligible int `json:"eligible"`
	Plus     int `json:"plus"`
	Disabled int `json:"disabled"`
	Cooling  int `json:"cooling"`
}

type relayStreamAttempt struct {
	auth     *Auth
	upstream *http.Response
	secrets  []string
}

// ExecuteRelayStream opens an incremental, token-redacted Codex stream using the
// same account selection, refresh, retries, and send budget as ExecuteRelayRequest.
// Non-success bodies are buffered up to 1 MiB; oversized bodies are closed and rejected.
func (m *Manager) ExecuteRelayStream(ctx context.Context, request RelayRequest, policy RelayRequestPolicy) (RelayResponse, io.ReadCloser, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	attempt := &relayStreamAttempt{}
	response, errExecute := m.executeRelayRequest(ctx, request, policy, attempt)
	if errExecute != nil || attempt.upstream == nil {
		return response, nil, errExecute
	}
	body := newRelayRedactingBody(ctx, attempt.upstream.Body, attempt.secrets, func(eof bool, errRead error) {
		if eof {
			m.MarkResult(ctx, relayResult(attempt.auth, request.Model, nil))
		} else if errRead != nil && ctx.Err() == nil {
			m.MarkResult(ctx, relayResult(attempt.auth, request.Model, &Error{
				Code: ErrorCodeTransientTransport, Message: "relay upstream stream read failed", Retryable: false, HTTPStatus: http.StatusBadGateway,
			}))
		}
	})
	return response, body, nil
}

func (s *relayStreamAttempt) send(m *Manager, ctx context.Context, auth *Auth, request RelayRequest, targetURL string) (RelayResponse, error, bool) {
	upstream, secrets, errOpen, sent := m.relayOpenUpstream(ctx, auth, request, targetURL)
	if errOpen != nil {
		return RelayResponse{}, errOpen, sent
	}
	if upstream.StatusCode >= 200 && upstream.StatusCode < 400 {
		s.auth, s.upstream, s.secrets = auth, upstream, secrets
		return RelayResponse{
			StatusCode: upstream.StatusCode,
			Headers:    sanitizeRelayResponseHeaders(upstream.Header, secrets...),
			BodySent:   len(request.Body) > 0,
		}, nil, true
	}
	defer upstream.Body.Close()
	const maxErrorBodyBytes = 1 << 20
	payload, errRead := io.ReadAll(io.LimitReader(upstream.Body, maxErrorBodyBytes+1))
	if errRead != nil {
		return RelayResponse{}, errors.New("relay upstream response read failed"), true
	}
	if len(payload) > maxErrorBodyBytes {
		return RelayResponse{}, errors.New("relay upstream response too large"), true
	}
	return RelayResponse{
		StatusCode: upstream.StatusCode,
		Headers:    sanitizeRelayResponseHeaders(upstream.Header, secrets...),
		Body:       redactRelaySecrets(payload, secrets...),
		BodySent:   len(request.Body) > 0,
	}, nil, true
}

// RelayStatus returns safe aggregate Codex OAuth auth counts.
func (m *Manager) RelayStatus(model string) RelayPoolStatus {
	var status RelayPoolStatus
	if m == nil {
		return status
	}
	now := time.Now()
	for _, auth := range m.snapshotAuths() {
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			continue
		}
		if auth.AuthKind() != AuthKindOAuth {
			continue
		}
		status.Total++
		if strings.EqualFold(strings.TrimSpace(auth.Attributes["plan_type"]), "plus") {
			status.Plus++
		}
		if auth.Disabled || auth.Status == StatusDisabled {
			status.Disabled++
			continue
		}
		if !auth.HasValidAccessToken(now) && !authHasRefreshCredential(auth) {
			status.Cooling++
			continue
		}
		blocked, _, _ := isAuthBlockedForModel(auth, model, now)
		if blocked {
			status.Cooling++
			continue
		}
		status.Eligible++
	}
	return status
}

type relayRedactingBody struct {
	ctx        context.Context
	source     io.ReadCloser
	secrets    []string
	pending    []byte
	output     []byte
	done       atomic.Bool
	onDone     func(bool, error)
	stopCancel func() bool
	closeOnce  sync.Once
}

func newRelayRedactingBody(ctx context.Context, source io.ReadCloser, secrets []string, onDone func(bool, error)) *relayRedactingBody {
	body := &relayRedactingBody{ctx: ctx, source: source, secrets: append([]string(nil), secrets...), onDone: onDone}
	body.stopCancel = context.AfterFunc(ctx, func() { _ = source.Close() })
	return body
}

func (b *relayRedactingBody) Read(dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	for len(b.output) == 0 {
		if b.done.Load() {
			if errContext := b.ctx.Err(); errContext != nil {
				return 0, errContext
			}
			return 0, io.EOF
		}
		if errContext := b.ctx.Err(); errContext != nil {
			b.finish(false, nil)
			return 0, errContext
		}
		buffer := make([]byte, 32*1024)
		n, errRead := b.source.Read(buffer)
		if errContext := b.ctx.Err(); errContext != nil {
			b.finish(false, nil)
			return 0, errContext
		}
		if n > 0 {
			b.pending = append(b.pending, buffer[:n]...)
		}
		b.output = b.redactSafePrefix(errRead == io.EOF)
		if errRead != nil {
			if errRead != io.EOF {
				b.finish(false, errRead)
				return 0, errors.New("relay upstream stream read failed")
			}
			b.done.Store(true)
			b.finish(true, nil)
		}
		if n == 0 && errRead == nil {
			continue
		}
	}
	count := copy(dst, b.output)
	b.output = b.output[count:]
	return count, nil
}

func (b *relayRedactingBody) redactSafePrefix(eof bool) []byte {
	var output []byte
	consumed := 0
	for consumed < len(b.pending) {
		rest := b.pending[consumed:]
		matched, partial := 0, false
		for _, secret := range b.secrets {
			if secret == "" {
				continue
			}
			if len(rest) >= len(secret) && bytes.Equal(rest[:len(secret)], []byte(secret)) {
				if len(secret) > matched {
					matched = len(secret)
				}
			} else if !eof && len(rest) < len(secret) && bytes.Equal(rest, []byte(secret[:len(rest)])) {
				partial = true
			}
		}
		if matched > 0 {
			output = append(output, "[REDACTED]"...)
			consumed += matched
		} else if partial {
			break
		} else {
			output = append(output, rest[0])
			consumed++
		}
	}
	b.pending = append(b.pending[:0], b.pending[consumed:]...)
	return output
}

func (b *relayRedactingBody) finish(eof bool, errRead error) {
	b.closeOnce.Do(func() {
		b.done.Store(true)
		if b.stopCancel != nil {
			b.stopCancel()
		}
		_ = b.source.Close()
		if b.onDone != nil {
			b.onDone(eof, errRead)
		}
	})
}

func (b *relayRedactingBody) Close() error {
	b.finish(false, nil)
	return nil
}
