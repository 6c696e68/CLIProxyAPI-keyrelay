package keyshop

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"
)

type tunnelOpt struct {
	Enabled   bool   `json:"enabled"`
	TargetURL string `json:"target_url"`
}

type keyTunnelResult struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Status    string `json:"status,omitempty"`
	PublicURL string `json:"public_url,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	Error     string `json:"error,omitempty"`
}

// tunnelViewResponse keeps the view fields top-level and adds the start failure.
type tunnelViewResponse struct {
	TunnelView
	StartError string `json:"start_error,omitempty"`
}

func (s *Service) tunnelDisabled() bool {
	return s == nil || s.tunnels == nil || !s.tunnels.Enabled()
}

func (s *Service) listTunnels() map[string]any {
	if s.tunnelDisabled() {
		return textResponse(http.StatusNotFound, "not found")
	}
	rows, err := s.store.ListTunnels()
	if err != nil {
		return textResponse(http.StatusInternalServerError, "list failed")
	}
	views := make([]TunnelView, 0, len(rows))
	for _, row := range rows {
		views = append(views, s.tunnels.View(row))
	}
	found, path, version := s.tunnels.BinaryInfo()
	return jsonResponse(http.StatusOK, map[string]any{
		"tunnels":            views,
		"default_target_url": s.tunnels.DefaultTarget(),
		"cloudflared":        map[string]any{"found": found, "path": path, "version": version},
	})
}

func (s *Service) createTunnel(body []byte) map[string]any {
	if s.tunnelDisabled() {
		return textResponse(http.StatusNotFound, "not found")
	}
	var in struct {
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		TargetURL string `json:"target_url"`
		Token     string `json:"token"`
		Autostart bool   `json:"autostart"`
	}
	if json.Unmarshal(body, &in) != nil {
		return jsonError(http.StatusBadRequest, "bad json")
	}
	name, err := validateName(in.Name)
	if err != nil {
		return jsonError(http.StatusBadRequest, "invalid name")
	}
	if err = validateKind(in.Kind); err != nil {
		return jsonError(http.StatusBadRequest, "invalid kind")
	}
	requested := in.TargetURL
	if in.Kind == "quick" && strings.TrimSpace(requested) == "" {
		requested = s.tunnels.DefaultTarget()
	}
	target, err := validateTarget(requested, in.Kind == "quick")
	if err != nil {
		return jsonError(http.StatusBadRequest, "invalid target_url")
	}
	if err = validateToken(in.Token, in.Kind == "token"); err != nil {
		return jsonError(http.StatusBadRequest, "invalid token")
	}
	row, err := s.store.InsertTunnel(TunnelRow{
		Name: name, Kind: in.Kind, TargetURL: target, Token: in.Token, Autostart: in.Autostart, Desired: "stopped",
	})
	if err != nil {
		return tunnelStoreError(err)
	}
	// A new tunnel starts right away so the operator sees it running. A start
	// failure keeps the row: the view still carries the reason.
	out := tunnelViewResponse{TunnelView: s.tunnels.View(row)}
	if err = s.tunnels.Start(row.ID); err != nil {
		out.StartError = err.Error()
		return jsonResponse(http.StatusCreated, out)
	}
	if reloaded, errGet := s.store.GetTunnel(row.ID); errGet == nil {
		out.TunnelView = s.tunnels.View(reloaded)
	}
	return jsonResponse(http.StatusCreated, out)
}

func (s *Service) updateTunnel(body []byte) map[string]any {
	if s.tunnelDisabled() {
		return textResponse(http.StatusNotFound, "not found")
	}
	var in struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		TargetURL string `json:"target_url"`
		Token     string `json:"token"`
		Autostart bool   `json:"autostart"`
	}
	if json.Unmarshal(body, &in) != nil || in.ID == "" {
		return jsonError(http.StatusBadRequest, "invalid input")
	}
	cur, err := s.store.GetTunnel(in.ID)
	if err != nil {
		return tunnelStoreError(err)
	}
	wasRunning := s.tunnels.View(cur).Status == "running" || s.tunnels.View(cur).Status == "starting" || cur.Desired == "running"
	if bytes.Contains(body, []byte(`"name"`)) {
		name, errName := validateName(in.Name)
		if errName != nil {
			return jsonError(http.StatusBadRequest, "invalid name")
		}
		cur.Name = name
	}
	if bytes.Contains(body, []byte(`"target_url"`)) {
		requested := in.TargetURL
		if cur.Kind == "quick" && strings.TrimSpace(requested) == "" {
			requested = s.tunnels.DefaultTarget()
		}
		target, errTarget := validateTarget(requested, cur.Kind == "quick")
		if errTarget != nil {
			return jsonError(http.StatusBadRequest, "invalid target_url")
		}
		cur.TargetURL = target
	}
	if bytes.Contains(body, []byte(`"token"`)) && in.Token != "" {
		if err = validateToken(in.Token, true); err != nil {
			return jsonError(http.StatusBadRequest, "invalid token")
		}
		cur.Token = in.Token
	}
	if bytes.Contains(body, []byte(`"autostart"`)) {
		cur.Autostart = in.Autostart
	}
	saved, err := s.store.UpdateTunnel(cur)
	if err != nil {
		return tunnelStoreError(err)
	}
	if wasRunning {
		if err = s.tunnels.Restart(saved.ID); errors.Is(err, ErrCloudflaredMissing) {
			return jsonError(http.StatusConflict, "cloudflared not found")
		}
		saved, _ = s.store.GetTunnel(saved.ID)
	}
	return jsonResponse(http.StatusOK, s.tunnels.View(saved))
}

func (s *Service) deleteTunnel(body []byte) map[string]any {
	if s.tunnelDisabled() {
		return textResponse(http.StatusNotFound, "not found")
	}
	id := idFrom(body)
	if id == "" {
		return jsonError(http.StatusBadRequest, "invalid input")
	}
	if err := s.tunnels.Delete(id); err != nil {
		return tunnelStoreError(err)
	}
	return jsonResponse(http.StatusOK, map[string]any{"ok": true})
}

func (s *Service) startTunnel(body []byte) map[string]any {
	if s.tunnelDisabled() {
		return textResponse(http.StatusNotFound, "not found")
	}
	id := idFrom(body)
	if id == "" {
		return jsonError(http.StatusBadRequest, "invalid input")
	}
	if err := s.tunnels.Start(id); err != nil {
		return tunnelActionError(err)
	}
	row, err := s.store.GetTunnel(id)
	if err != nil {
		return tunnelStoreError(err)
	}
	return jsonResponse(http.StatusOK, s.tunnels.View(row))
}

func (s *Service) stopTunnel(body []byte) map[string]any {
	if s.tunnelDisabled() {
		return textResponse(http.StatusNotFound, "not found")
	}
	id := idFrom(body)
	if id == "" {
		return jsonError(http.StatusBadRequest, "invalid input")
	}
	if err := s.tunnels.Stop(id); err != nil {
		return tunnelActionError(err)
	}
	row, err := s.store.GetTunnel(id)
	if err != nil {
		return tunnelStoreError(err)
	}
	return jsonResponse(http.StatusOK, s.tunnels.View(row))
}

func (s *Service) tunnelLogs(id string, lines int) map[string]any {
	if s.tunnelDisabled() {
		return textResponse(http.StatusNotFound, "not found")
	}
	if id == "" {
		return jsonError(http.StatusBadRequest, "invalid input")
	}
	if _, err := s.store.GetTunnel(id); err != nil {
		return tunnelStoreError(err)
	}
	return jsonResponse(http.StatusOK, map[string]any{"lines": s.tunnels.Logs(id, lines)})
}

func tunnelStoreError(err error) map[string]any {
	switch {
	case errors.Is(err, ErrTunnelNotFound):
		return jsonError(http.StatusNotFound, "unknown id")
	case errors.Is(err, ErrTunnelNameTaken):
		return jsonError(http.StatusConflict, "duplicate name")
	case errors.Is(err, ErrTunnelLimit):
		return jsonError(http.StatusConflict, "tunnel limit reached")
	case errors.Is(err, ErrTunnelInvalid):
		return jsonError(http.StatusBadRequest, "invalid input")
	default:
		return textResponse(http.StatusInternalServerError, "tunnel error")
	}
}

func (s *Service) attachKeyTunnel(label, keyID, requested string, headers map[string][]string) keyTunnelResult {
	if s == nil || s.tunnels == nil || !s.tunnels.Enabled() {
		return keyTunnelResult{Error: "tunnels are disabled"}
	}
	target := strings.TrimSpace(requested)
	if target == "" {
		target = s.tunnels.DefaultTarget()
	}
	if target == "" {
		target = targetFromHost(headers)
	}
	if _, err := validateTarget(target, true); err != nil {
		return keyTunnelResult{Error: "invalid target_url"}
	}
	name := keyTunnelName(label, keyID)
	row, err := s.store.InsertTunnel(TunnelRow{
		Name: name, Kind: "quick", TargetURL: target, Desired: "stopped",
	})
	if err != nil {
		return keyTunnelResult{Error: err.Error()}
	}
	if err = s.tunnels.Start(row.ID); err != nil {
		return keyTunnelResult{ID: row.ID, Name: row.Name, Status: "stopped", Error: err.Error()}
	}
	deadline := time.Now().Add(25 * time.Second)
	for {
		got, errGet := s.store.GetTunnel(row.ID)
		view := s.tunnels.View(got)
		if errGet == nil && strings.HasPrefix(got.PublicURL, "https://") {
			return keyTunnelResult{
				ID: got.ID, Name: got.Name, Status: view.Status,
				PublicURL: got.PublicURL, Endpoint: tunnelEndpoint(got.PublicURL),
			}
		}
		if errGet == nil && (view.Status == "stopped" || got.Desired == "stopped") {
			msg := got.LastError
			if msg == "" {
				msg = "tunnel stopped before a public URL"
			}
			return keyTunnelResult{ID: got.ID, Name: got.Name, Status: view.Status, Error: msg}
		}
		if time.Now().After(deadline) {
			return keyTunnelResult{ID: row.ID, Name: row.Name, Status: view.Status, Error: "timed out waiting for the public URL"}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func keyTunnelName(label, keyID string) string {
	base := strings.TrimSpace(label)
	if base == "" {
		base = keyID
	}
	var b strings.Builder
	b.WriteString("key-")
	for _, r := range base {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
			b.WriteRune(r)
		} else if r == ' ' || r == '_' {
			b.WriteByte('-')
		}
		if b.Len() >= 50 {
			break
		}
	}
	var buf [2]byte
	_, _ = rand.Read(buf[:])
	return b.String() + "-" + hex.EncodeToString(buf[:])
}

func targetFromHost(headers map[string][]string) string {
	host := headerValue(headers, "Host")
	if host == "" {
		host = headerValue(headers, "X-Forwarded-Host")
	}
	_, port, err := net.SplitHostPort(host)
	if err != nil || port == "" {
		port = "8317"
	}
	return "http://127.0.0.1:" + port
}

func tunnelEndpoint(publicURL string) string {
	return strings.TrimRight(publicURL, "/") + "/v1"
}

func tunnelActionError(err error) map[string]any {
	switch {
	case errors.Is(err, ErrTunnelNotFound):
		return jsonError(http.StatusNotFound, "unknown id")
	case errors.Is(err, ErrAlreadyRunning):
		return jsonError(http.StatusConflict, "already running")
	case errors.Is(err, ErrCloudflaredMissing):
		return jsonError(http.StatusConflict, "cloudflared not found")
	default:
		return textResponse(http.StatusInternalServerError, "tunnel error")
	}
}
