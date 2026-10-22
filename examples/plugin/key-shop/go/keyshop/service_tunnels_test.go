package keyshop

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTunnelHandlers(t *testing.T) {
	store := testTunnelStore(t)
	script := writeFakeCloudflared(t, fakeStay)
	m := newTestManager(t, store, script)
	svc := NewService(store, nil, nil)
	svc.SetTunnelManager(m)

	token := strings.Repeat("p", 30)
	status, body := manage(t, svc, http.MethodPost, "/tunnels", map[string]any{
		"name": "edge", "kind": "token", "token": token, "autostart": false,
	})
	if status != http.StatusCreated {
		t.Fatalf("create %d %s", status, body)
	}
	var created TunnelView
	if json.Unmarshal(body, &created) != nil || created.ID == "" || created.HasToken != true || created.TokenHint != token[len(token)-4:] {
		t.Fatalf("created %+v %s", created, body)
	}
	if strings.Contains(string(body), token) {
		t.Fatalf("token in body %s", body)
	}

	status, body = manage(t, svc, http.MethodPost, "/tunnels", map[string]any{"name": "EDGE", "kind": "token", "token": token})
	if status != http.StatusConflict {
		t.Fatalf("dup %d %s", status, body)
	}

	status, body = manage(t, svc, http.MethodGet, "/tunnels", nil)
	if status != 200 || strings.Contains(string(body), token) {
		t.Fatalf("list %d %s", status, body)
	}

	status, body = manage(t, svc, http.MethodPost, "/tunnels/start", map[string]any{"id": created.ID})
	if status != 200 {
		t.Fatalf("start %d %s", status, body)
	}
	waitStatus(t, m, created.ID, "running")

	status, body = manage(t, svc, http.MethodPost, "/tunnels/start", map[string]any{"id": created.ID})
	if status != http.StatusConflict {
		t.Fatalf("already %d %s", status, body)
	}

	status, body = manage(t, svc, http.MethodPost, "/tunnels/update", map[string]any{"id": created.ID, "name": "edge-2", "token": ""})
	if status != 200 || strings.Contains(string(body), token) {
		t.Fatalf("update %d %s", status, body)
	}
	kept, err := store.GetTunnel(created.ID)
	if err != nil || kept.Token != token || kept.Name != "edge-2" {
		t.Fatalf("token not kept: %+v %v", kept, err)
	}

	status, body = manage(t, svc, http.MethodGet, "/tunnels/logs", map[string][]string{"id": {created.ID}, "lines": {"20"}})
	if status != 200 {
		t.Fatalf("logs %d %s", status, body)
	}
	if strings.Contains(string(body), token) {
		t.Fatalf("logs leaked %s", body)
	}

	status, body = manage(t, svc, http.MethodPost, "/tunnels/stop", map[string]any{"id": created.ID})
	if status != 200 {
		t.Fatalf("stop %d %s", status, body)
	}
	waitStatus(t, m, created.ID, "stopped")

	status, body = manage(t, svc, http.MethodPost, "/tunnels/delete", map[string]any{"id": created.ID})
	if status != 200 || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("delete %d %s", status, body)
	}
	status, body = manage(t, svc, http.MethodPost, "/tunnels/start", map[string]any{"id": "tn_missing0000"})
	if status != http.StatusNotFound {
		t.Fatalf("missing %d %s", status, body)
	}

	status, body = manage(t, svc, http.MethodPost, "/tunnels", map[string]any{"name": "bad", "kind": "nope"})
	if status != http.StatusBadRequest {
		t.Fatalf("kind %d %s", status, body)
	}

	missing := NewTunnelManager(store, TunnelManagerConfig{CloudflaredPath: filepath.Join(t.TempDir(), "missing-bin"), DataDir: store.DataDir()})
	svc.SetTunnelManager(missing)
	row, err := store.InsertTunnel(TunnelRow{Name: "nofind", Kind: "quick", TargetURL: "http://127.0.0.1:9"})
	if err != nil {
		t.Fatal(err)
	}
	status, body = manage(t, svc, http.MethodPost, "/tunnels/start", map[string]any{"id": row.ID})
	if status != http.StatusConflict || !strings.Contains(string(body), "cloudflared not found") {
		t.Fatalf("missing bin %d %s", status, body)
	}
}

func TestQuickCreateRequiresTarget(t *testing.T) {
	store := testTunnelStore(t)
	enabled := true
	m := NewTunnelManager(store, TunnelManagerConfig{
		Enabled:         &enabled,
		CloudflaredPath: writeFakeCloudflared(t, fakeStay),
		DataDir:         store.DataDir(),
		Command: func(ctx context.Context, bin string, args []string, env []string, dir string) *exec.Cmd {
			cmd := exec.CommandContext(ctx, bin, args...)
			cmd.Env = env
			cmd.Dir = dir
			return cmd
		},
	})
	t.Cleanup(m.Shutdown)
	svc := NewService(store, nil, nil)
	svc.SetTunnelManager(m)
	status, _ := manage(t, svc, http.MethodPost, "/tunnels", map[string]any{"name": "q", "kind": "quick"})
	if status != http.StatusBadRequest {
		t.Fatalf("status %d", status)
	}
}

func TestRoutesIncludeTunnels(t *testing.T) {
	raw := call(t, testService(t), "management.register", "")
	if !strings.Contains(string(raw), "/plugins/key-shop/tunnels/logs") {
		t.Fatalf("routes missing tunnels: %s", raw)
	}
}

func keyCreateBody(t *testing.T, svc *Service, path string, body map[string]any, host string) (int, []byte) {
	t.Helper()
	payload := map[string]any{
		"Method":  http.MethodPost,
		"Path":    "/v0/management/plugins/key-shop" + path,
		"Body":    body,
		"Headers": map[string][]string{"Host": {host}},
	}
	raw := call(t, svc, "management.handle", string(mustJSON(t, payload)))
	var res struct {
		StatusCode int
		Body       []byte
	}
	decodeResult(t, raw, &res)
	return res.StatusCode, res.Body
}

func TestCreateKeyWithoutTunnelUnchanged(t *testing.T) {
	svc := testService(t)
	status, body := keyCreateBody(t, svc, "/keys", map[string]any{"group_id": AllModelsGroupID, "ttl_seconds": 3600, "token_limit": 1000}, "127.0.0.1:9999")
	if status != 200 || strings.Contains(string(body), `"tunnel"`) || !strings.Contains(string(body), `"secret"`) {
		t.Fatalf("unchanged %d %s", status, body)
	}
}

func TestCreateKeyWithQuickTunnel(t *testing.T) {
	store := testTunnelStore(t)
	m := newTestManager(t, store, writeFakeCloudflared(t, fakeStay))
	svc := NewService(store, nil, nil)
	svc.SetTunnelManager(m)
	status, body := keyCreateBody(t, svc, "/keys", map[string]any{
		"group_id": AllModelsGroupID, "label": "Ada", "ttl_seconds": 3600,
		"tunnel": map[string]any{"enabled": true, "target_url": "http://127.0.0.1:8080"},
	}, "ignored:1")
	if status != 200 {
		t.Fatalf("status %d %s", status, body)
	}
	var out struct {
		Secret string
		Tunnel keyTunnelResult
	}
	if json.Unmarshal(body, &out) != nil || out.Secret == "" {
		t.Fatalf("body %s", body)
	}
	if out.Tunnel.Endpoint != "https://abc-def.trycloudflare.com/v1" || strings.Contains(out.Tunnel.Endpoint, "//v1") {
		t.Fatalf("endpoint %q body %s", out.Tunnel.Endpoint, body)
	}
}

func TestCreateBulkSharesOneTunnel(t *testing.T) {
	store := testTunnelStore(t)
	m := newTestManager(t, store, writeFakeCloudflared(t, fakeStay))
	svc := NewService(store, nil, nil)
	svc.SetTunnelManager(m)
	status, body := keyCreateBody(t, svc, "/keys/bulk", map[string]any{
		"group_id": AllModelsGroupID, "count": 2, "label": "batch",
		"tunnel": map[string]any{"enabled": true},
	}, "example.test:18444")
	if status != 200 {
		t.Fatalf("status %d %s", status, body)
	}
	var out struct {
		Keys   []CreatedKey
		Tunnel keyTunnelResult
	}
	if json.Unmarshal(body, &out) != nil || len(out.Keys) != 2 || out.Tunnel.ID == "" {
		t.Fatalf("body %s", body)
	}
	rows, err := store.ListTunnels()
	if err != nil || len(rows) != 1 || rows[0].TargetURL != "http://127.0.0.1:18444" {
		t.Fatalf("rows %+v %v", rows, err)
	}
	if !strings.HasSuffix(out.Tunnel.Endpoint, "/v1") || strings.Contains(out.Tunnel.Endpoint, "//v1") {
		t.Fatalf("endpoint %q", out.Tunnel.Endpoint)
	}
}

func TestCreateKeyTunnelMissingBinaryKeepsKeys(t *testing.T) {
	store := testTunnelStore(t)
	m := NewTunnelManager(store, TunnelManagerConfig{CloudflaredPath: filepath.Join(t.TempDir(), "missing"), DataDir: store.DataDir()})
	svc := NewService(store, nil, nil)
	svc.SetTunnelManager(m)
	status, body := keyCreateBody(t, svc, "/keys", map[string]any{
		"group_id": AllModelsGroupID, "tunnel": map[string]any{"enabled": true, "target_url": "http://127.0.0.1:9"},
	}, "")
	if status != 200 || !strings.Contains(string(body), "cloudflared not found") || !strings.Contains(string(body), `"secret"`) {
		t.Fatalf("missing %d %s", status, body)
	}
}

func TestCreateKeyTunnelDisabledKeepsKeys(t *testing.T) {
	store := testTunnelStore(t)
	off := false
	m := NewTunnelManager(store, TunnelManagerConfig{Enabled: &off, DataDir: store.DataDir()})
	svc := NewService(store, nil, nil)
	svc.SetTunnelManager(m)
	status, body := keyCreateBody(t, svc, "/keys", map[string]any{
		"group_id": AllModelsGroupID, "tunnel": map[string]any{"enabled": true, "target_url": "http://127.0.0.1:9"},
	}, "")
	if status != 200 || !strings.Contains(string(body), "tunnels are disabled") || !strings.Contains(string(body), `"secret"`) {
		t.Fatalf("disabled %d %s", status, body)
	}
}

func TestMigrationAddsTunnelsTable(t *testing.T) {
	store := testTunnelStore(t)
	if err := store.tunnelsTableExists(); err != nil {
		t.Fatal(err)
	}
	// Additive: keys table still present.
	var name string
	if err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='keys'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
}
