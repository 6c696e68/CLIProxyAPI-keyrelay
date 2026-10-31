package keyshop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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
sleep 60 &
child=$!
trap 'kill "$child" 2>/dev/null; exit 0' TERM INT
wait "$child"
`

// fakeKeepAlive publishes its own ownership marker in a fake proc root and
// removes it on SIGTERM, the way /proc behaves for a real child.
const fakeKeepAlive = `#!/bin/sh
printf '%s\n' "$*" > "$HOME/argv"
env > "$HOME/env"
if [ -n "$FAKE_PROCROOT" ]; then
  mkdir -p "$FAKE_PROCROOT/$$" || exit 1
  printf 'KEYSHOP_TUNNEL_ID=%s' "$KEYSHOP_TUNNEL_ID" > "$FAKE_PROCROOT/$$/environ"
fi
printf '%s\n' "https://abc-def.trycloudflare.com"
printf '%s\n' "see $TUNNEL_TOKEN"
sleep 60 &
child=$!
trap 'if [ -n "$FAKE_PROCROOT" ]; then rm -rf "$FAKE_PROCROOT/$$"; fi; kill "$child" 2>/dev/null; exit 0' TERM INT
wait "$child"
`

// fakeLogCap writes one burst over a 1 KiB cap, marks it done, then writes
// again through the same stdout fd after the test creates append-now.
const fakeLogCap = `#!/bin/sh
printf '%s\n' "$*" > "$HOME/argv"
awk 'BEGIN { for (i = 0; i < 4096; i++) printf "x" }'
printf '\n'
: > "$HOME/burst-done"
n=0
while [ ! -f "$HOME/append-now" ] && [ "$n" -lt 600 ]; do
  sleep 0.01
  n=$((n + 1))
done
printf 'AFTER-TRUNCATE'
sleep 60 &
child=$!
trap 'kill "$child" 2>/dev/null; exit 0' TERM INT
wait "$child"
`

// fakeLateURL publishes the ownership marker, waits for url-now, then prints
// the quick URL through the manager's log fd.
const fakeLateURL = `#!/bin/sh
printf '%s\n' "$*" > "$HOME/argv"
env > "$HOME/env"
if [ -n "$FAKE_PROCROOT" ]; then
  mkdir -p "$FAKE_PROCROOT/$$" || exit 1
  printf 'KEYSHOP_TUNNEL_ID=%s' "$KEYSHOP_TUNNEL_ID" > "$FAKE_PROCROOT/$$/environ"
fi
n=0
while [ ! -f "$HOME/url-now" ] && [ "$n" -lt 600 ]; do
  sleep 0.01
  n=$((n + 1))
done
printf 'https://late-url.trycloudflare.com\n'
sleep 60 &
child=$!
trap 'if [ -n "$FAKE_PROCROOT" ]; then rm -rf "$FAKE_PROCROOT/$$"; fi; kill "$child" 2>/dev/null; exit 0' TERM INT
wait "$child"
`

const fakeCrash = `#!/bin/sh
printf '%s\n' "$*" > "$HOME/argv"
env > "$HOME/env"
exit 1
`

// testCommand mirrors defaultCommand: a cancelled context sends SIGTERM, so a
// fake script can clean up before it exits.
func testCommand(ctx context.Context, bin string, args []string, env []string, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	cmd.Dir = dir
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = shutdownGrace
	return cmd
}

func newTestManager(t *testing.T, store *Store, script string) *TunnelManager {
	t.Helper()
	enabled := true
	keepAlive := false
	m := NewTunnelManager(store, TunnelManagerConfig{
		CloudflaredPath: script,
		Enabled:         &enabled,
		KeepAlive:       &keepAlive,
		DataDir:         store.DataDir(),
		Command:         testCommand,
		Sleep:           func(time.Duration) {},
		Grace:           200 * time.Millisecond,
		WatchInterval:   25 * time.Millisecond,
	})
	t.Cleanup(func() { m.Shutdown() })
	return m
}

// newKeepAliveManager builds a manager that can adopt children through a fake
// proc root. The fake child publishes and removes its own ownership marker.
func newKeepAliveManager(t *testing.T, store *Store, script, root string, spawns *atomic.Int32) *TunnelManager {
	t.Helper()
	enabled := true
	keepAlive := true
	m := NewTunnelManager(store, TunnelManagerConfig{
		CloudflaredPath: script,
		Enabled:         &enabled,
		KeepAlive:       &keepAlive,
		DataDir:         store.DataDir(),
		ProcRoot:        root,
		Grace:           500 * time.Millisecond,
		WatchInterval:   25 * time.Millisecond,
		Sleep:           func(time.Duration) {},
		Command: func(ctx context.Context, bin string, args []string, env []string, dir string) *exec.Cmd {
			spawns.Add(1)
			cmd := testCommand(ctx, bin, args, env, dir)
			cmd.Env = append(cmd.Env, "FAKE_PROCROOT="+root)
			return cmd
		},
	})
	t.Cleanup(func() {
		rows, _ := store.ListTunnels()
		for _, row := range rows {
			_ = m.Stop(row.ID)
		}
		// A closed manager will not stop a row, so make sure no fake child of a
		// failed test outlives it.
		for _, row := range rows {
			if row.PID > 0 {
				_ = signalTestPID(row.PID, syscall.SIGTERM)
			}
		}
		m.Shutdown()
	})
	return m
}

// spawnFakeTunnel starts the fake cloudflared outside a manager and reaps it,
// so an adopted pid behaves like a child a previous plugin instance left behind.
func spawnFakeTunnel(t *testing.T, script, root, id, home string) *os.Process {
	t.Helper()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, "tunnel", "--no-autoupdate")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"KEYSHOP_TUNNEL_ID=" + id,
		"HOME=" + home,
		"FAKE_PROCROOT=" + root,
	}
	cmd.Dir = home
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = cmd.Process.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd.Process
}

// signalTestPID sends a signal to a known test pid. Tests never use the
// manager's ownership rules for their own bookkeeping.
func signalTestPID(pid int, sig syscall.Signal) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(sig)
}

func processAlive(pid int) bool {
	return signalTestPID(pid, syscall.Signal(0)) == nil
}

func waitOwned(t *testing.T, root string, pid int, id string) {
	t.Helper()
	deadline := time.Now().Add(shutdownGrace)
	for time.Now().Before(deadline) {
		if ownsPID(root, pid, id) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid %d never published the ownership marker", pid)
}

func waitPidGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(shutdownGrace)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid %d is still running", pid)
}

func waitURL(t *testing.T, store *Store, id string) string {
	t.Helper()
	deadline := time.Now().Add(shutdownGrace)
	for time.Now().Before(deadline) {
		row, err := store.GetTunnel(id)
		if err == nil && strings.HasPrefix(row.PublicURL, "https://") {
			return row.PublicURL
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("quick tunnel URL was not captured")
	return ""
}

func waitFileContains(t *testing.T, path, needle string) {
	t.Helper()
	deadline := time.Now().Add(shutdownGrace)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(raw), needle) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never contained %q", path, needle)
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

func TestDefaultTargetPrecedence(t *testing.T) {
	store := testTunnelStore(t)
	m := NewTunnelManager(store, TunnelManagerConfig{
		TargetURL:   "http://127.0.0.1:9001",
		HostBaseURL: "http://127.0.0.1:8330",
		DataDir:     store.DataDir(),
	})
	if got := m.DefaultTarget(); got != "http://127.0.0.1:9001" {
		t.Fatalf("yaml target = %q", got)
	}
	m.cfg.TargetURL = "   "
	if got := m.DefaultTarget(); got != "http://127.0.0.1:8330" {
		t.Fatalf("host base = %q", got)
	}
	m.cfg.HostBaseURL = ""
	if got := m.DefaultTarget(); got != "" {
		t.Fatalf("empty = %q", got)
	}
	var nilManager *TunnelManager
	if got := nilManager.DefaultTarget(); got != "" {
		t.Fatalf("nil manager = %q", got)
	}
}

func TestFixedArgsNoTLSVerifyOnlyForHTTPSLoopbackQuick(t *testing.T) {
	const base = "tunnel --no-autoupdate --metrics 127.0.0.1:0 --url "
	quick := []struct {
		target string
		want   string
	}{
		{"https://127.0.0.1:8317", base + "https://127.0.0.1:8317 --no-tls-verify"},
		{"https://[::1]:8317", base + "https://[::1]:8317 --no-tls-verify"},
		{"https://localhost:8317", base + "https://localhost:8317 --no-tls-verify"},
		{"http://127.0.0.1:8317", base + "http://127.0.0.1:8317"},
		{"https://app.example", base + "https://app.example"},
		{"https://10.0.0.5:8317", base + "https://10.0.0.5:8317"},
	}
	for _, tc := range quick {
		got := strings.Join(fixedArgs(&TunnelRow{Kind: "quick", TargetURL: tc.target}), " ")
		if got != tc.want {
			t.Fatalf("fixedArgs(%q) = %q, want %q", tc.target, got, tc.want)
		}
	}
	token := strings.Join(fixedArgs(&TunnelRow{Kind: "token", TargetURL: "https://127.0.0.1:8317"}), " ")
	if token != "tunnel --no-autoupdate --metrics 127.0.0.1:0 run" {
		t.Fatalf("token args = %q", token)
	}
}

func TestKeepAliveShutdownLeavesChildRunning(t *testing.T) {
	store := testTunnelStore(t)
	root := t.TempDir()
	script := writeFakeCloudflared(t, fakeKeepAlive)
	var spawns atomic.Int32
	m1 := newKeepAliveManager(t, store, script, root, &spawns)
	row, err := store.InsertTunnel(TunnelRow{Name: "keep", Kind: "quick", TargetURL: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m1.Start(row.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m1, row.ID, "running")
	pid := m1.View(row).PID
	if pid <= 0 {
		t.Fatalf("pid %d", pid)
	}
	waitOwned(t, root, pid, row.ID)
	waitURL(t, store, row.ID)

	m1.Shutdown()
	if !m1.isClosed() {
		t.Fatal("shutdown did not close the manager")
	}
	if !processAlive(pid) {
		t.Fatal("keep-alive shutdown stopped the child")
	}

	m2 := newKeepAliveManager(t, store, script, root, &spawns)
	m2.Reconcile()
	if got := spawns.Load(); got != 1 {
		t.Fatalf("%d children spawned, want the adopted one only", got)
	}
	waitStatus(t, m2, row.ID, "running")
	if got := m2.View(row).PID; got != pid {
		t.Fatalf("adopted pid %d, want %d", got, pid)
	}
	if err := m2.Stop(row.ID); err != nil {
		t.Fatal(err)
	}
	waitPidGone(t, pid)
	stopped, err := store.GetTunnel(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.PID != 0 || stopped.Desired != "stopped" {
		t.Fatalf("row after stop: %+v", stopped)
	}
	if got := spawns.Load(); got != 1 {
		t.Fatalf("stop spawned %d extra children", got-1)
	}
}

func TestKeepAliveAdoptedPidGoneSpawnsReplacement(t *testing.T) {
	store := testTunnelStore(t)
	root := t.TempDir()
	script := writeFakeCloudflared(t, fakeKeepAlive)
	var spawns atomic.Int32
	m1 := newKeepAliveManager(t, store, script, root, &spawns)
	row, err := store.InsertTunnel(TunnelRow{Name: "crashy", Kind: "quick", TargetURL: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m1.Start(row.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m1, row.ID, "running")
	pid := m1.View(row).PID
	waitOwned(t, root, pid, row.ID)
	m1.Shutdown()

	m2 := newKeepAliveManager(t, store, script, root, &spawns)
	m2.Reconcile()
	waitStatus(t, m2, row.ID, "running")
	if got := spawns.Load(); got != 1 {
		t.Fatalf("%d spawns before the crash, want 1", got)
	}
	// Simulate a real crash: the process is gone, so its marker is gone.
	if err := signalTestPID(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitPidGone(t, pid)
	if err := os.RemoveAll(filepath.Join(root, fmt.Sprint(pid))); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if spawns.Load() >= 2 && m2.View(row).Restarts >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := spawns.Load(); got < 2 {
		t.Fatalf("%d spawns, want a replacement", got)
	}
	if got := m2.View(row).Restarts; got < 1 {
		t.Fatalf("restarts %d, want at least 1", got)
	}
	if err := m2.Stop(row.ID); err != nil {
		t.Fatal(err)
	}
}

func TestKeepAliveLogFileURLAndRedaction(t *testing.T) {
	store := testTunnelStore(t)
	root := t.TempDir()
	script := writeFakeCloudflared(t, fakeKeepAlive)
	var spawns atomic.Int32
	m := newKeepAliveManager(t, store, script, root, &spawns)
	quick, err := store.InsertTunnel(TunnelRow{Name: "logfile", Kind: "quick", TargetURL: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(quick.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, quick.ID, "running")
	waitOwned(t, root, m.View(quick).PID, quick.ID)
	if got := waitURL(t, store, quick.ID); got != "https://abc-def.trycloudflare.com" {
		t.Fatalf("public url %q", got)
	}
	logPath := filepath.Join(store.DataDir(), "tunnels", quick.ID, "cloudflared.log")
	waitFileContains(t, logPath, "https://abc-def.trycloudflare.com")
	if logs := m.Logs(quick.ID, 50); len(logs) == 0 {
		t.Fatal("no lines from the log file")
	}

	token := strings.Repeat("k", 24)
	tunnel, err := store.InsertTunnel(TunnelRow{Name: "tokened", Kind: "token", Token: token})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(tunnel.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, tunnel.ID, "running")
	waitOwned(t, root, m.View(tunnel).PID, tunnel.ID)
	tokenLog := filepath.Join(store.DataDir(), "tunnels", tunnel.ID, "cloudflared.log")
	waitFileContains(t, tokenLog, "see "+token)
	joined := strings.Join(m.Logs(tunnel.ID, 50), "\n")
	if strings.Contains(joined, token) || !strings.Contains(joined, "[redacted]") {
		t.Fatalf("manager logs were not redacted: %q", joined)
	}
	fresh := NewTunnelManager(store, TunnelManagerConfig{DataDir: store.DataDir()})
	freshJoined := strings.Join(fresh.Logs(tunnel.ID, 50), "\n")
	if strings.Contains(freshJoined, token) || !strings.Contains(freshJoined, "[redacted]") {
		t.Fatalf("fresh manager logs were not redacted: %q", freshJoined)
	}
	if err := m.Stop(tunnel.ID); err != nil {
		t.Fatal(err)
	}
}

const fakeSystemdRun = `#!/bin/sh
printf '%s\n' "$*" > "$HOME/systemd-argv"
env > "$HOME/systemd-env"
while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do shift; done
[ "$#" -gt 0 ] && shift
exec "$@"
`

func writeFakeSystemdRun(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "systemd-run")
	if err := os.WriteFile(path, []byte(fakeSystemdRun), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSystemdRunWrapsKeptAliveChild(t *testing.T) {
	store := testTunnelStore(t)
	root := t.TempDir()
	script := writeFakeCloudflared(t, fakeKeepAlive)
	scope := writeFakeSystemdRun(t)
	enabled := true
	keepAlive := true
	var spawns atomic.Int32
	m := NewTunnelManager(store, TunnelManagerConfig{
		CloudflaredPath: script,
		Enabled:         &enabled,
		KeepAlive:       &keepAlive,
		DataDir:         store.DataDir(),
		ProcRoot:        root,
		SystemdRun:      scope,
		Grace:           500 * time.Millisecond,
		WatchInterval:   25 * time.Millisecond,
		Sleep:           func(time.Duration) {},
		Command: func(ctx context.Context, bin string, args []string, env []string, dir string) *exec.Cmd {
			spawns.Add(1)
			cmd := testCommand(ctx, bin, args, env, dir)
			cmd.Env = append(cmd.Env, "FAKE_PROCROOT="+root)
			return cmd
		},
	})
	t.Cleanup(func() {
		rows, _ := store.ListTunnels()
		for _, row := range rows {
			_ = m.Stop(row.ID)
		}
		m.Shutdown()
	})
	row, err := store.InsertTunnel(TunnelRow{Name: "scoped", Kind: "quick", TargetURL: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(row.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, row.ID, "running")
	home := filepath.Join(store.DataDir(), "tunnels", row.ID)
	waitFileContains(t, filepath.Join(home, "systemd-argv"), "--scope")
	raw, err := os.ReadFile(filepath.Join(home, "systemd-argv"))
	if err != nil {
		t.Fatal(err)
	}
	scopeArgv := strings.Fields(strings.TrimSpace(string(raw)))
	wantPrefix := []string{"--scope", "--quiet", "--collect", "--unit=keyshop-tunnel-" + row.ID + "-"}
	if len(scopeArgv) < len(wantPrefix)+1+len(fixedArgs(&row)) {
		t.Fatalf("scope argv too short: %q", scopeArgv)
	}
	for i, want := range wantPrefix {
		if i == len(wantPrefix)-1 {
			if !strings.HasPrefix(scopeArgv[i], want) {
				t.Fatalf("unit = %q, want prefix %q", scopeArgv[i], want)
			}
			continue
		}
		if scopeArgv[i] != want {
			t.Fatalf("argv[%d] = %q, want %q", i, scopeArgv[i], want)
		}
	}
	if scopeArgv[len(wantPrefix)] != "--" || scopeArgv[len(wantPrefix)+1] != script {
		t.Fatalf("scope argv does not exec the binary: %q", scopeArgv)
	}
	wantArgs := strings.Join(fixedArgs(&row), " ")
	waitFileContains(t, filepath.Join(home, "argv"), wantArgs)
	argv, err := os.ReadFile(filepath.Join(home, "argv"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(argv)); got != wantArgs {
		t.Fatalf("cloudflared argv %q, want %q", got, wantArgs)
	}
	envRaw, err := os.ReadFile(filepath.Join(home, "systemd-env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(envRaw), "KEYSHOP_TUNNEL_ID="+row.ID) {
		t.Fatalf("marker missing from the scope env: %s", envRaw)
	}
	waitOwned(t, root, m.View(row).PID, row.ID)
	if got := waitURL(t, store, row.ID); got != "https://abc-def.trycloudflare.com" {
		t.Fatalf("public url %q", got)
	}
	if got := spawns.Load(); got != 1 {
		t.Fatalf("%d spawns, want 1", got)
	}
}

func TestSystemdRunEmptyKeepsChildArgv(t *testing.T) {
	store := testTunnelStore(t)
	root := t.TempDir()
	script := writeFakeCloudflared(t, fakeKeepAlive)
	var spawns atomic.Int32
	m := newKeepAliveManager(t, store, script, root, &spawns)
	row, err := store.InsertTunnel(TunnelRow{Name: "plain", Kind: "quick", TargetURL: "http://127.0.0.1:8081"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(row.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, row.ID, "running")
	home := filepath.Join(store.DataDir(), "tunnels", row.ID)
	waitFileContains(t, filepath.Join(home, "argv"), "--url http://127.0.0.1:8081")
	argv, err := os.ReadFile(filepath.Join(home, "argv"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(argv)); got != strings.Join(fixedArgs(&row), " ") {
		t.Fatalf("cloudflared argv %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, "systemd-argv")); !os.IsNotExist(err) {
		t.Fatalf("systemd-run used without a configured path: %v", err)
	}
}

func TestKeepAliveDisabledShutdownStopsChild(t *testing.T) {
	store := testTunnelStore(t)
	m := newTestManager(t, store, writeFakeCloudflared(t, fakeStay))
	row, err := store.InsertTunnel(TunnelRow{Name: "killed", Kind: "quick", TargetURL: "http://127.0.0.1:8082"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(row.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, row.ID, "running")
	pid := m.View(row).PID
	if pid <= 0 {
		t.Fatalf("pid %d", pid)
	}
	m.Shutdown()
	waitPidGone(t, pid)
}

func TestReconcileDisabledStopsOwnedPidWithoutSpawn(t *testing.T) {
	store := testTunnelStore(t)
	root := t.TempDir()
	script := writeFakeCloudflared(t, fakeKeepAlive)
	row, err := store.InsertTunnel(TunnelRow{Name: "off", Kind: "quick", TargetURL: "http://127.0.0.1:8083", Desired: "running"})
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(store.DataDir(), "tunnels", row.ID)
	proc := spawnFakeTunnel(t, script, root, row.ID, home)
	waitOwned(t, root, proc.Pid, row.ID)
	if err := store.SetTunnelRuntime(row.ID, "running", proc.Pid, "", ""); err != nil {
		t.Fatal(err)
	}
	off := false
	spawns := 0
	m := NewTunnelManager(store, TunnelManagerConfig{
		Enabled:         &off,
		DataDir:         store.DataDir(),
		ProcRoot:        root,
		Grace:           500 * time.Millisecond,
		CloudflaredPath: filepath.Join(t.TempDir(), "missing"),
		Command: func(ctx context.Context, bin string, args []string, env []string, dir string) *exec.Cmd {
			spawns++
			return testCommand(ctx, bin, args, env, dir)
		},
	})
	t.Cleanup(m.Shutdown)
	m.Reconcile()
	waitPidGone(t, proc.Pid)
	got, err := store.GetTunnel(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != 0 {
		t.Fatalf("pid %d was not cleared", got.PID)
	}
	if got.Desired != "running" {
		t.Fatalf("desired %q, want the row kept", got.Desired)
	}
	if spawns != 0 {
		t.Fatalf("disabled reconcile spawned %d children", spawns)
	}
}

func TestStartAdoptsOwnedPidWithoutSpawn(t *testing.T) {
	store := testTunnelStore(t)
	root := t.TempDir()
	script := writeFakeCloudflared(t, fakeKeepAlive)
	row, err := store.InsertTunnel(TunnelRow{Name: "adopt", Kind: "quick", TargetURL: "http://127.0.0.1:8084"})
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(store.DataDir(), "tunnels", row.ID)
	proc := spawnFakeTunnel(t, script, root, row.ID, home)
	waitOwned(t, root, proc.Pid, row.ID)
	if err := store.SetTunnelRuntime(row.ID, "stopped", proc.Pid, "", ""); err != nil {
		t.Fatal(err)
	}
	var spawns atomic.Int32
	m := newKeepAliveManager(t, store, script, root, &spawns)
	if err := m.Start(row.ID); err != nil {
		t.Fatal(err)
	}
	if got := spawns.Load(); got != 0 {
		t.Fatalf("adopting start spawned %d children", got)
	}
	if got := m.View(row).Status; got != "running" {
		t.Fatalf("status %q, want running", got)
	}
	if got := m.View(row).PID; got != proc.Pid {
		t.Fatalf("pid %d, want %d", got, proc.Pid)
	}
	if err := m.Start(row.ID); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second start = %v, want ErrAlreadyRunning", err)
	}
	if err := m.Stop(row.ID); err != nil {
		t.Fatal(err)
	}
	waitPidGone(t, proc.Pid)
}

func TestTunnelStartStoreWriteFailureLeavesNoState(t *testing.T) {
	store := testTunnelStore(t)
	root := t.TempDir()
	var spawns atomic.Int32
	m := newKeepAliveManager(t, store, writeFakeCloudflared(t, fakeKeepAlive), root, &spawns)
	row, err := store.InsertTunnel(TunnelRow{Name: "nowrite", Kind: "quick", TargetURL: "http://127.0.0.1:8085"})
	if err != nil {
		t.Fatal(err)
	}
	// The runtime write fails while the row still reads back, which is the
	// concurrent Delete or DB-error shape Start has to survive.
	if _, err := store.db.Exec(`PRAGMA query_only = ON`); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(row.ID); err == nil {
		t.Fatal("start must fail when the runtime row cannot be written")
	}
	if got := spawns.Load(); got != 0 {
		t.Fatalf("%d children spawned for a failed start", got)
	}
	m.mu.Lock()
	pending := len(m.live)
	m.mu.Unlock()
	if pending != 0 {
		t.Fatalf("%d runtime states left behind by a failed start", pending)
	}
	// Keep-alive Shutdown waits on the done channel of every registered state.
	// The failed start must not register one, or Shutdown would wait forever.
	done := make(chan struct{})
	go func() {
		m.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownGrace):
		t.Fatal("keep-alive shutdown waited forever after a failed start")
	}
}

func waitForFileSize(t *testing.T, path string, size int64) {
	t.Helper()
	deadline := time.Now().Add(shutdownGrace)
	for time.Now().Before(deadline) {
		info, err := os.Stat(path)
		if err == nil && info.Size() == size {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never reached size %d", path, size)
}

func TestTunnelLogCapTruncates(t *testing.T) {
	store := testTunnelStore(t)
	enabled := true
	keepAlive := false
	m := NewTunnelManager(store, TunnelManagerConfig{
		CloudflaredPath: writeFakeCloudflared(t, fakeLogCap),
		Enabled:         &enabled,
		KeepAlive:       &keepAlive,
		DataDir:         store.DataDir(),
		LogMaxKB:        DefaultTunnelLogMaxKB / bytesPerKB,
		Command:         testCommand,
		Sleep:           func(time.Duration) {},
		Grace:           200 * time.Millisecond,
		WatchInterval:   25 * time.Millisecond,
	})
	t.Cleanup(m.Shutdown)
	row, err := store.InsertTunnel(TunnelRow{Name: "capped", Kind: "quick", TargetURL: "http://127.0.0.1:8086"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(row.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, row.ID, "running")
	home := filepath.Join(store.DataDir(), "tunnels", row.ID)
	logPath := filepath.Join(home, "cloudflared.log")
	waitFileContains(t, filepath.Join(home, "burst-done"), "")
	waitForFileSize(t, logPath, 0)
	if err := os.WriteFile(filepath.Join(home, "append-now"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFileContains(t, logPath, "AFTER-TRUNCATE")
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "AFTER-TRUNCATE" {
		t.Fatalf("log after the truncate = %q, want the append at offset 0", raw)
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		t.Fatal("log contains NUL padding after the truncate")
	}
	if err := m.Stop(row.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTunnelURLCaptureStopsAfterStored(t *testing.T) {
	// A spawned quick tunnel keeps the URL it captured even when a different
	// trycloudflare URL later appears in its log.
	store := testTunnelStore(t)
	m := newTestManager(t, store, writeFakeCloudflared(t, fakeKeepAlive))
	row, err := store.InsertTunnel(TunnelRow{Name: "captured", Kind: "quick", TargetURL: "http://127.0.0.1:8087"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(row.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, row.ID, "running")
	stored := waitURL(t, store, row.ID)
	logPath := filepath.Join(store.DataDir(), "tunnels", row.ID, "cloudflared.log")
	if err := os.WriteFile(logPath, []byte("https://other-name.trycloudflare.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		time.Sleep(m.WatchInterval())
		got, errGot := store.GetTunnel(row.ID)
		if errGot != nil {
			t.Fatal(errGot)
		}
		if got.PublicURL != stored {
			t.Fatalf("stored URL changed to %q after the capture, want %q", got.PublicURL, stored)
		}
	}
	if err := m.Stop(row.ID); err != nil {
		t.Fatal(err)
	}

	// An adopted quick tunnel with a stored URL never reads the log for it; one
	// with an empty URL reads it and keeps the result.
	store = testTunnelStore(t)
	root := t.TempDir()
	script := writeFakeCloudflared(t, fakeKeepAlive)
	const adoptedURL = "https://stored-url.trycloudflare.com"
	withURL, err := store.InsertTunnel(TunnelRow{
		Name: "adopted-stored", Kind: "quick", TargetURL: "http://127.0.0.1:8088",
		Desired: "running", PublicURL: adoptedURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	homeWithURL := filepath.Join(store.DataDir(), "tunnels", withURL.ID)
	withURLProc := spawnFakeTunnel(t, script, root, withURL.ID, homeWithURL)
	waitOwned(t, root, withURLProc.Pid, withURL.ID)
	if err := store.SetTunnelRuntime(withURL.ID, "running", withURLProc.Pid, adoptedURL, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeWithURL, "cloudflared.log"), []byte("https://other-name.trycloudflare.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	noURL, err := store.InsertTunnel(TunnelRow{
		Name: "adopted-empty", Kind: "quick", TargetURL: "http://127.0.0.1:8089", Desired: "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	homeNoURL := filepath.Join(store.DataDir(), "tunnels", noURL.ID)
	noURLProc := spawnFakeTunnel(t, script, root, noURL.ID, homeNoURL)
	waitOwned(t, root, noURLProc.Pid, noURL.ID)
	if err := store.SetTunnelRuntime(noURL.ID, "running", noURLProc.Pid, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeNoURL, "cloudflared.log"), []byte("https://picked-up.trycloudflare.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var spawns atomic.Int32
	adopting := newKeepAliveManager(t, store, script, root, &spawns)
	adopting.Reconcile()
	deadline := time.Now().Add(shutdownGrace)
	for {
		got, errGot := store.GetTunnel(noURL.ID)
		if errGot != nil {
			t.Fatal(errGot)
		}
		if got.PublicURL == "https://picked-up.trycloudflare.com" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("adopted empty URL was not captured: %q", got.PublicURL)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for i := 0; i < 5; i++ {
		time.Sleep(adopting.WatchInterval())
		got, errGot := store.GetTunnel(withURL.ID)
		if errGot != nil {
			t.Fatal(errGot)
		}
		if got.PublicURL != adoptedURL {
			t.Fatalf("adopted stored URL changed to %q, want %q", got.PublicURL, adoptedURL)
		}
	}
	if err := adopting.Stop(withURL.ID); err != nil {
		t.Fatal(err)
	}
	if err := adopting.Stop(noURL.ID); err != nil {
		t.Fatal(err)
	}
	waitPidGone(t, withURLProc.Pid)
	waitPidGone(t, noURLProc.Pid)
}

func TestTunnelStopUntrackedOwnedPid(t *testing.T) {
	store := testTunnelStore(t)
	root := t.TempDir()
	script := writeFakeCloudflared(t, fakeKeepAlive)

	owned, err := store.InsertTunnel(TunnelRow{
		Name: "owned-pid", Kind: "quick", TargetURL: "http://127.0.0.1:8090", Desired: "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	ownedProc := spawnFakeTunnel(t, script, root, owned.ID, filepath.Join(store.DataDir(), "tunnels", owned.ID))
	waitOwned(t, root, ownedProc.Pid, owned.ID)
	if err := store.SetTunnelRuntime(owned.ID, "running", ownedProc.Pid, "", ""); err != nil {
		t.Fatal(err)
	}

	foreign, err := store.InsertTunnel(TunnelRow{
		Name: "foreign-pid", Kind: "quick", TargetURL: "http://127.0.0.1:8091", Desired: "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	foreignProc := spawnFakeTunnel(t, script, root, foreign.ID, filepath.Join(store.DataDir(), "tunnels", foreign.ID))
	waitOwned(t, root, foreignProc.Pid, foreign.ID)
	// Keep the process alive but make the marker name another tunnel.
	if err := os.WriteFile(filepath.Join(root, fmt.Sprint(foreignProc.Pid), "environ"), []byte("KEYSHOP_TUNNEL_ID=tn_other"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTunnelRuntime(foreign.ID, "running", foreignProc.Pid, "", ""); err != nil {
		t.Fatal(err)
	}

	var spawns atomic.Int32
	m := newKeepAliveManager(t, store, script, root, &spawns)
	var mu sync.Mutex
	var signaled []os.Signal
	m.SetSignalHook(func(pid int, sig os.Signal) error {
		mu.Lock()
		signaled = append(signaled, sig)
		mu.Unlock()
		numeric, ok := sig.(syscall.Signal)
		if !ok {
			return nil
		}
		return signalTestPID(pid, numeric)
	})
	m.mu.Lock()
	tracked := len(m.live)
	m.mu.Unlock()
	if tracked != 0 {
		t.Fatalf("manager unexpectedly tracks %d tunnels", tracked)
	}

	if err := m.Stop(owned.ID); err != nil {
		t.Fatal(err)
	}
	waitPidGone(t, ownedProc.Pid)
	ownedRow, err := store.GetTunnel(owned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ownedRow.PID != 0 || ownedRow.Desired != "stopped" {
		t.Fatalf("owned row after stop: %+v", ownedRow)
	}
	mu.Lock()
	got := append([]os.Signal(nil), signaled...)
	mu.Unlock()
	if len(got) == 0 || got[0] != syscall.SIGTERM {
		t.Fatalf("owned pid signals = %v, want SIGTERM", got)
	}

	before := len(got)
	if err := m.Stop(foreign.ID); err != nil {
		t.Fatal(err)
	}
	foreignRow, err := store.GetTunnel(foreign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if foreignRow.PID != 0 || foreignRow.Desired != "stopped" {
		t.Fatalf("foreign row after stop: %+v", foreignRow)
	}
	if !processAlive(foreignProc.Pid) {
		t.Fatal("an unowned pid was stopped")
	}
	mu.Lock()
	after := len(signaled)
	mu.Unlock()
	if after != before {
		t.Fatalf("an unowned pid reached the signal hook: %v", signaled[before:])
	}
}

func TestTunnelLogCapCapturesURLFirst(t *testing.T) {
	store := testTunnelStore(t)
	enabled := true
	keepAlive := false
	m := NewTunnelManager(store, TunnelManagerConfig{
		Enabled:   &enabled,
		KeepAlive: &keepAlive,
		DataDir:   store.DataDir(),
		LogMaxKB:  DefaultTunnelLogMaxKB / bytesPerKB,
		Grace:     200 * time.Millisecond,
	})
	row, err := store.InsertTunnel(TunnelRow{Name: "cap-url", Kind: "quick", TargetURL: "http://127.0.0.1:8094"})
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(store.DataDir(), "tunnels", row.ID)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	const url = "https://cap-url.trycloudflare.com"
	logPath := filepath.Join(home, "cloudflared.log")
	// One child burst: the URL line plus enough padding to pass the cap.
	if err := os.WriteFile(logPath, []byte(url+"\n"+strings.Repeat("x", 4096)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := &runtimeState{id: row.ID, status: "running", pid: 4242, done: make(chan struct{}), stopWatch: make(chan struct{})}
	m.mu.Lock()
	m.live[row.ID] = st
	m.mu.Unlock()
	m.watchTick(st, &row)
	got, err := store.GetTunnel(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PublicURL != url {
		t.Fatalf("URL captured after the cap truncated the log: %q, want %q", got.PublicURL, url)
	}
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("log size %d after one capped tick, want 0", info.Size())
	}
}

func TestTunnelURLCaptureAfterSpawnClearsStale(t *testing.T) {
	store := testTunnelStore(t)
	root := t.TempDir()
	script := writeFakeCloudflared(t, fakeLateURL)
	var spawns atomic.Int32
	m1 := newKeepAliveManager(t, store, script, root, &spawns)
	stale := "https://stale-url.trycloudflare.com"
	row, err := store.InsertTunnel(TunnelRow{
		Name: "relaunch", Kind: "quick", TargetURL: "http://127.0.0.1:8095",
		Desired: "stopped", PublicURL: stale,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m1.Start(row.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m1, row.ID, "running")
	deadline := time.Now().Add(shutdownGrace)
	for {
		got, errGot := store.GetTunnel(row.ID)
		if errGot != nil {
			t.Fatal(errGot)
		}
		if got.PublicURL == "" && got.PID > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("previous child's URL survived the spawn: %q", got.PublicURL)
		}
		time.Sleep(10 * time.Millisecond)
	}
	pid := m1.View(row).PID
	waitOwned(t, root, pid, row.ID)
	m1.Shutdown()

	// A reload adopts the live child before its URL appears. The row must not
	// claim the previous child's URL, or the log would never be read.
	m2 := newKeepAliveManager(t, store, script, root, &spawns)
	m2.Reconcile()
	if got := m2.View(row).Status; got != "running" {
		t.Fatalf("status %q after adoption", got)
	}
	if err := os.WriteFile(filepath.Join(store.DataDir(), "tunnels", row.ID, "url-now"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(shutdownGrace)
	for {
		got, errGot := store.GetTunnel(row.ID)
		if errGot != nil {
			t.Fatal(errGot)
		}
		if got.PublicURL == "https://late-url.trycloudflare.com" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("adopted child's URL was not captured, stored %q", got.PublicURL)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := m2.Stop(row.ID); err != nil {
		t.Fatal(err)
	}
	waitPidGone(t, pid)
}
