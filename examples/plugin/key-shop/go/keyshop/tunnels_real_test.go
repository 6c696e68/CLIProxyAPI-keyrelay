//go:build realcloudflared

package keyshop

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealQuickTunnel(t *testing.T) {
	bin := "/opt/homebrew/bin/cloudflared"
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("real cloudflared missing: %v", err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "key-shop-tunnel-ok")
	}))
	t.Cleanup(upstream.Close)

	parentHome := t.TempDir()
	t.Setenv("HOME", parentHome)
	t.Setenv("TUNNEL_DEVICE", "do-not-inherit")
	t.Setenv("CLOUDFLARED_CONFIG", filepath.Join(parentHome, "nope.yml"))

	store := testTunnelStore(t)
	wrapper := filepath.Join(t.TempDir(), "cloudflared-wrapper")
	script := "#!/bin/sh\nenv > \"$HOME/env\"\nexec " + bin + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	enabled := true
	m := NewTunnelManager(store, TunnelManagerConfig{
		CloudflaredPath: wrapper,
		Enabled:         &enabled,
		DataDir:         store.DataDir(),
	})
	t.Cleanup(m.Shutdown)

	row, err := store.InsertTunnel(TunnelRow{
		Name: "real-quick", Kind: "quick", TargetURL: upstream.URL, Desired: "stopped",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(row.ID); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(90 * time.Second)
	var got TunnelRow
	for time.Now().Before(deadline) {
		got, err = store.GetTunnel(row.ID)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(got.PublicURL, "https://") && strings.HasSuffix(got.PublicURL, ".trycloudflare.com") {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !strings.HasPrefix(got.PublicURL, "https://") || !strings.HasSuffix(got.PublicURL, ".trycloudflare.com") {
		t.Fatalf("public_url %q logs %v err %q", got.PublicURL, m.Logs(row.ID, 40), got.LastError)
	}
	home := filepath.Join(store.DataDir(), "tunnels", row.ID)
	if _, err := os.Stat(home); err != nil {
		t.Fatal(err)
	}
	envRaw, err := os.ReadFile(filepath.Join(home, "env"))
	if err != nil {
		t.Fatal(err)
	}
	envText := string(envRaw)
	if strings.Contains(envText, "TUNNEL_DEVICE") || strings.Contains(envText, "CLOUDFLARED_CONFIG") || strings.Contains(envText, "do-not-inherit") {
		t.Fatalf("inherited env:\n%s", envText)
	}
	if !strings.Contains(envText, "HOME="+home) || !strings.Contains(envText, "KEYSHOP_TUNNEL_ID="+row.ID) {
		t.Fatalf("scrubbed env missing marker:\n%s", envText)
	}
	if strings.Contains(envText, "HOME="+parentHome) {
		t.Fatalf("parent HOME inherited:\n%s", envText)
	}

	if err := m.Stop(row.ID); err != nil {
		t.Fatal(err)
	}
	stopped := waitStatus(t, m, row.ID, "stopped")
	if stopped.Desired != "stopped" {
		t.Fatalf("desired %s", stopped.Desired)
	}

	if err := m.Start(row.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, row.ID, "running")
	if err := m.Stop(row.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, row.ID, "stopped")

	if err := m.Delete(row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("home remains: %v", err)
	}
	if _, err := store.GetTunnel(row.ID); err != ErrTunnelNotFound {
		t.Fatalf("row remains: %v", err)
	}
}
