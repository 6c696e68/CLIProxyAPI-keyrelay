package keyshop

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func testTunnelStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(Config{DataFile: filepath.Join(t.TempDir(), "keys.db"), Now: func() time.Time {
		return time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func writeFakeCloudflared(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cloudflared")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

const fakeStay = `#!/bin/sh
printf '%s\n' "$*" > "$HOME/argv"
env > "$HOME/env"
printf '%s\n' "https://abc-def.trycloudflare.com"
printf '%s\n' "https://abc-def.trycloudflare.com" >&2
printf '%s\n' "see $TUNNEL_TOKEN"
trap 'exit 0' TERM INT
sleep 60 &
wait
`

const fakeCrash = `#!/bin/sh
printf '%s\n' "$*" > "$HOME/argv"
env > "$HOME/env"
exit 1
`

func newTestManager(t *testing.T, store *Store, script string) *TunnelManager {
	t.Helper()
	enabled := true
	m := NewTunnelManager(store, TunnelManagerConfig{
		CloudflaredPath: script,
		Enabled:         &enabled,
		DataDir:         store.DataDir(),
		Command: func(ctx context.Context, bin string, args []string, env []string, dir string) *exec.Cmd {
			cmd := exec.CommandContext(ctx, bin, args...)
			cmd.Env = env
			cmd.Dir = dir
			return cmd
		},
		Sleep: func(time.Duration) {},
		Grace: 200 * time.Millisecond,
	})
	t.Cleanup(func() { m.Shutdown() })
	return m
}

func waitStatus(t *testing.T, m *TunnelManager, id, want string) TunnelRow {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var row TunnelRow
	for time.Now().Before(deadline) {
		got, err := m.store.GetTunnel(id)
		if err != nil {
			t.Fatal(err)
		}
		row = got
		if m.View(got).Status == want {
			return got
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("status = %s, want %s (err %q)", m.View(row).Status, want, row.LastError)
	return row
}

func TestTunnelValidation(t *testing.T) {
	if _, err := validateName("  "); err == nil {
		t.Fatal("empty name")
	}
	if _, err := validateName("bad\nname"); err == nil {
		t.Fatal("control name")
	}
	name, err := validateName("  edge  ")
	if err != nil || name != "edge" {
		t.Fatalf("name: %q %v", name, err)
	}
	if validateKind("named") == nil {
		t.Fatal("kind")
	}
	if _, err := validateTarget("ftp://example", true); err == nil {
		t.Fatal("scheme")
	}
	if _, err := validateTarget("http://user:pass@example", true); err == nil {
		t.Fatal("userinfo")
	}
	if _, err := validateTarget("http://ok.example:8080/x", false); err != nil {
		t.Fatal(err)
	}
	if _, err := validateTarget("", true); err == nil {
		t.Fatal("required target")
	}
	if validateToken("short", true) == nil {
		t.Fatal("short token")
	}
	if validateToken("has space-token-value-ok", true) == nil {
		t.Fatal("whitespace token")
	}
	if validateToken(strings.Repeat("a", 20), true) != nil {
		t.Fatal("min token")
	}
}

func TestTunnelCRUDAndCap(t *testing.T) {
	store := testTunnelStore(t)
	row, err := store.InsertTunnel(TunnelRow{Name: "Alpha", Kind: "quick", TargetURL: "http://127.0.0.1:9", Desired: "stopped"})
	if err != nil || !strings.HasPrefix(row.ID, "tn_") || len(row.ID) != 15 {
		t.Fatalf("insert: %+v %v", row, err)
	}
	if _, err := store.InsertTunnel(TunnelRow{Name: "alpha", Kind: "quick", TargetURL: "http://127.0.0.1:9"}); err != ErrTunnelNameTaken {
		t.Fatalf("case: %v", err)
	}
	for i := 0; i < maxTunnels-1; i++ {
		if _, err := store.InsertTunnel(TunnelRow{Name: "n" + string(rune('A'+i)), Kind: "quick", TargetURL: "http://127.0.0.1:9"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.InsertTunnel(TunnelRow{Name: "overflow", Kind: "quick", TargetURL: "http://127.0.0.1:9"}); err != ErrTunnelLimit {
		t.Fatalf("cap: %v", err)
	}
	row.Name = "Beta"
	saved, err := store.UpdateTunnel(row)
	if err != nil || saved.Name != "Beta" || saved.Kind != "quick" {
		t.Fatalf("update: %+v %v", saved, err)
	}
	if err := store.DeleteTunnel(row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetTunnel(row.ID); err != ErrTunnelNotFound {
		t.Fatalf("gone: %v", err)
	}
}

func TestTunnelSecretNeverInView(t *testing.T) {
	store := testTunnelStore(t)
	token := strings.Repeat("s", 24)
	row, err := store.InsertTunnel(TunnelRow{Name: "secret", Kind: "token", Token: token})
	if err != nil {
		t.Fatal(err)
	}
	view := NewTunnelManager(store, TunnelManagerConfig{DataDir: store.DataDir()}).View(row)
	if !view.HasToken || view.TokenHint != "ssss" {
		t.Fatalf("hint: %+v", view)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatalf("token leaked: %s", raw)
	}
}

func TestFakeCloudflaredLifecycle(t *testing.T) {
	store := testTunnelStore(t)
	script := writeFakeCloudflared(t, fakeStay)
	m := newTestManager(t, store, script)
	token := strings.Repeat("t", 32)
	row, err := store.InsertTunnel(TunnelRow{Name: "quick", Kind: "quick", TargetURL: "http://127.0.0.1:8080", Token: ""})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(row.ID); err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, m, row.ID, "running")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ = store.GetTunnel(row.ID)
		if got.PublicURL == "https://abc-def.trycloudflare.com" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if got.PublicURL != "https://abc-def.trycloudflare.com" {
		t.Fatalf("public url %q", got.PublicURL)
	}
	argv, err := os.ReadFile(filepath.Join(store.DataDir(), "tunnels", row.ID, "argv"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(argv)) != "tunnel --no-autoupdate --metrics 127.0.0.1:0 --url http://127.0.0.1:8080" {
		t.Fatalf("argv %q", argv)
	}
	envRaw, err := os.ReadFile(filepath.Join(store.DataDir(), "tunnels", row.ID, "env"))
	if err != nil {
		t.Fatal(err)
	}
	env := string(envRaw)
	if !strings.Contains(env, "KEYSHOP_TUNNEL_ID="+row.ID) {
		t.Fatalf("marker missing: %s", env)
	}
	if !strings.Contains(env, "HOME="+filepath.Join(store.DataDir(), "tunnels", row.ID)) {
		t.Fatalf("home: %s", env)
	}
	for _, banned := range []string{"TUNNEL_", "CLOUDFLARED_"} {
		for _, line := range strings.Split(env, "\n") {
			if strings.HasPrefix(line, banned) {
				t.Fatalf("inherited %s", line)
			}
		}
	}
	logs := m.Logs(row.ID, 50)
	joined := strings.Join(logs, "\n")
	if strings.Contains(joined, token) {
		t.Fatal("token in logs")
	}
	if err := m.Stop(row.ID); err != nil {
		t.Fatal(err)
	}
	stopped := waitStatus(t, m, row.ID, "stopped")
	if stopped.Desired != "stopped" {
		t.Fatalf("desired %s", stopped.Desired)
	}
	restarts := m.View(stopped).Restarts
	time.Sleep(50 * time.Millisecond)
	if again := m.View(stopped); again.Restarts != restarts || again.Status == "running" {
		t.Fatalf("stop restarted: %+v", again)
	}
	_ = token
}

func TestTokenOnlyViaEnvAndRedaction(t *testing.T) {
	t.Setenv("TUNNEL_DEVICE", "device-secret")
	t.Setenv("CLOUDFLARED_CONFIG", "/tmp/device")
	t.Setenv("HOME", t.TempDir())
	store := testTunnelStore(t)
	script := writeFakeCloudflared(t, fakeStay)
	m := newTestManager(t, store, script)
	token := strings.Repeat("z", 28)
	row, err := store.InsertTunnel(TunnelRow{Name: "named", Kind: "token", Token: token, TargetURL: "https://app.example"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(row.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, row.ID, "running")
	argvPath := filepath.Join(store.DataDir(), "tunnels", row.ID, "argv")
	deadline := time.Now().Add(2 * time.Second)
	var argv []byte
	for {
		argv, err = os.ReadFile(argvPath)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if string(argv) != "tunnel --no-autoupdate --metrics 127.0.0.1:0 run\n" {
		t.Fatalf("argv %q", argv)
	}
	if strings.Contains(string(argv), token) {
		t.Fatal("token on argv")
	}
	// The fake script writes argv before env, so wait for the env file to carry the token.
	envPath := filepath.Join(store.DataDir(), "tunnels", row.ID, "env")
	var env string
	envDeadline := time.Now().Add(2 * time.Second)
	for {
		envRaw, _ := os.ReadFile(envPath)
		env = string(envRaw)
		if strings.Contains(env, "TUNNEL_TOKEN="+token) || time.Now().After(envDeadline) {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !strings.Contains(env, "TUNNEL_TOKEN="+token) {
		t.Fatal("token env missing")
	}
	if strings.Contains(env, "TUNNEL_DEVICE") || strings.Contains(env, "CLOUDFLARED_CONFIG") || strings.Contains(env, "device-secret") {
		t.Fatalf("inherited env:\n%s", env)
	}
	logDeadline := time.Now().Add(2 * time.Second)
	var logs []string
	for time.Now().Before(logDeadline) {
		logs = m.Logs(row.ID, 500)
		if strings.Contains(strings.Join(logs, "\n"), "[redacted]") {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	joined := strings.Join(logs, "\n")
	if strings.Contains(joined, token) || !strings.Contains(joined, "[redacted]") {
		t.Fatalf("redaction: %q", joined)
	}
}

func TestCrashRestartsUntilStop(t *testing.T) {
	store := testTunnelStore(t)
	script := writeFakeCloudflared(t, fakeCrash)
	m := newTestManager(t, store, script)
	row, err := store.InsertTunnel(TunnelRow{Name: "crash", Kind: "quick", TargetURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(row.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if m.View(row).Restarts >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if m.View(row).Restarts < 1 {
		t.Fatal("expected restart")
	}
	if err := m.Stop(row.ID); err != nil {
		t.Fatal(err)
	}
	stopped := waitStatus(t, m, row.ID, "stopped")
	held := m.View(stopped).Restarts
	time.Sleep(40 * time.Millisecond)
	if m.View(stopped).Restarts != held {
		t.Fatal("restarted after stop")
	}
}

func TestDeleteRemovesOnlyTunnelHome(t *testing.T) {
	store := testTunnelStore(t)
	script := writeFakeCloudflared(t, fakeStay)
	m := newTestManager(t, store, script)
	row, err := store.InsertTunnel(TunnelRow{Name: "gone", Kind: "quick", TargetURL: "http://127.0.0.1:2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(row.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, row.ID, "running")
	sibling := filepath.Join(store.DataDir(), "tunnels", "keep-me")
	if err := os.WriteFile(sibling, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(store.DataDir(), "not-tunnels")
	if err := os.WriteFile(outside, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.DataDir(), "tunnels", row.ID)); !os.IsNotExist(err) {
		t.Fatalf("home remains: %v", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetTunnel(row.ID); err != ErrTunnelNotFound {
		t.Fatal(err)
	}
}

func TestForeignProcessNeverSignaled(t *testing.T) {
	foreignBin := writeFakeCloudflared(t, "#!/bin/sh\ntrap 'exit 0' TERM INT\nsleep 60\n")
	foreign := exec.Command(foreignBin)
	if err := foreign.Start(); err != nil {
		t.Fatal(err)
	}
	if filepath.Base(foreignBin) != "cloudflared" {
		t.Fatalf("foreign executable name = %s", filepath.Base(foreignBin))
	}
	t.Cleanup(func() {
		if foreign.Process != nil {
			_ = foreign.Process.Kill()
			_, _ = foreign.Process.Wait()
		}
	})
	store := testTunnelStore(t)
	row, err := store.InsertTunnel(TunnelRow{Name: "foreign", Kind: "quick", TargetURL: "http://127.0.0.1:3", Desired: "stopped", PID: foreign.Process.Pid})
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	var mu sync.Mutex
	var signaled []int
	spawned := 0
	m := NewTunnelManager(store, TunnelManagerConfig{
		CloudflaredPath: writeFakeCloudflared(t, fakeStay),
		Enabled:         &enabled,
		Command: func(ctx context.Context, bin string, args []string, env []string, dir string) *exec.Cmd {
			spawned++
			if bin == foreignBin {
				t.Errorf("reconcile spawned the foreign cloudflared %s", bin)
			}
			cmd := exec.CommandContext(ctx, bin, args...)
			cmd.Env = env
			cmd.Dir = dir
			return cmd
		},
		DataDir: store.DataDir(),
		Sleep:   func(time.Duration) {},
		Grace:   100 * time.Millisecond,
	})
	m.SetSignalHook(func(pid int, sig os.Signal) error {
		mu.Lock()
		signaled = append(signaled, pid)
		mu.Unlock()
		if pid == foreign.Process.Pid {
			t.Errorf("signaled foreign pid %d with %v", pid, sig)
		}
		return nil
	})
	t.Cleanup(m.Shutdown)
	m.Reconcile()
	if err := m.Stop(row.ID); err != nil {
		t.Fatal(err)
	}
	m.Shutdown()
	if err := foreign.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("foreign process was signaled: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, pid := range signaled {
		if pid == foreign.Process.Pid {
			t.Fatalf("hook saw foreign pid")
		}
	}
	cleared, err := store.GetTunnel(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "linux" && cleared.PID != 0 {
		t.Fatalf("non-linux pid not cleared: %d", cleared.PID)
	}
	if spawned != 0 {
		t.Fatalf("foreign cloudflared caused %d spawns", spawned)
	}
}

func TestReconcileAdoptsOwnedPidWithoutSecondChild(t *testing.T) {
	store := testTunnelStore(t)
	root := t.TempDir()
	row, err := store.InsertTunnel(TunnelRow{
		Name: "owned", Kind: "token", Token: strings.Repeat("o", 24),
		Desired: "running", PID: 5151,
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "5151")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := []byte("KEYSHOP_TUNNEL_ID=" + row.ID + "\x00")
	if err := os.WriteFile(filepath.Join(dir, "environ"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	enabled := true
	spawned := 0
	var got []int
	m := NewTunnelManager(store, TunnelManagerConfig{
		CloudflaredPath: writeFakeCloudflared(t, fakeStay),
		Enabled:         &enabled,
		DataDir:         store.DataDir(),
		ProcRoot:        root,
		Command: func(ctx context.Context, bin string, args []string, env []string, dir string) *exec.Cmd {
			spawned++
			cmd := exec.CommandContext(ctx, bin, args...)
			cmd.Env = env
			cmd.Dir = dir
			return cmd
		},
	})
	m.SetSignalHook(func(pid int, sig os.Signal) error {
		got = append(got, pid)
		if !ownsPID(root, pid, row.ID) {
			t.Errorf("signaled pid %d without ownership", pid)
		}
		_ = sig
		return nil
	})
	m.Reconcile()
	if spawned != 0 {
		t.Fatalf("adopted pid still spawned %d", spawned)
	}
	if m.View(row).Status != "running" || m.View(row).PID != 5151 {
		t.Fatalf("not tracking adopted pid: %+v", m.View(row))
	}
	if err := os.WriteFile(filepath.Join(dir, "environ"), []byte("KEYSHOP_TUNNEL_ID=tn_other\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(row.ID); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("signaled after marker disappeared: %v", got)
	}
}

func TestOwnershipCheck(t *testing.T) {
	root := t.TempDir()
	id := "tn_aabbccddeeff"
	dir := filepath.Join(root, "4242")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := []byte("PATH=/usr/bin\x00KEYSHOP_TUNNEL_ID=" + id + "\x00HOME=/tmp\x00")
	if err := os.WriteFile(filepath.Join(dir, "environ"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if !ownsPID(root, 4242, id) {
		t.Fatal("expected ownership")
	}
	if ownsPID(root, 4242, "tn_other") {
		t.Fatal("wrong id matched")
	}
	if ownsPID("/proc", 4242, id) && runtime.GOOS != "linux" {
		t.Fatal("non-linux default proc root must not own")
	}
	store := testTunnelStore(t)
	row, err := store.InsertTunnel(TunnelRow{Name: "stale", Kind: "quick", TargetURL: "http://127.0.0.1:4", PID: 4242, Desired: "stopped"})
	if err != nil {
		t.Fatal(err)
	}
	m := NewTunnelManager(store, TunnelManagerConfig{DataDir: store.DataDir(), ProcRoot: "/proc"})
	m.SetSignalHook(func(int, os.Signal) error {
		t.Fatal("ownership clear must not signal")
		return nil
	})
	m.adoptOrClear(row)
	cleared, err := store.GetTunnel(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "linux" && cleared.PID != 0 {
		t.Fatalf("pid %d", cleared.PID)
	}
}

func TestTunnelsDisabledNeverSpawns(t *testing.T) {
	store := testTunnelStore(t)
	off := false
	m := NewTunnelManager(store, TunnelManagerConfig{Enabled: &off, DataDir: store.DataDir(), CloudflaredPath: "/nonexistent/cloudflared"})
	row, err := store.InsertTunnel(TunnelRow{Name: "off", Kind: "quick", TargetURL: "http://127.0.0.1:5", Desired: "running", Autostart: true})
	if err != nil {
		t.Fatal(err)
	}
	m.Reconcile()
	if err := m.Start(row.ID); err == nil {
		t.Fatal("start should fail closed")
	}
	svc := NewService(store, nil, nil)
	svc.SetTunnelManager(m)
	status, body := manage(t, svc, "GET", "/tunnels", nil)
	if status != 404 {
		t.Fatalf("list %d %s", status, body)
	}
}
