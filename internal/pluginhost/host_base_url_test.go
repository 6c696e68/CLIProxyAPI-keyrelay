package pluginhost

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

func TestHostBaseURLFromConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{name: "nil config", cfg: nil, want: ""},
		{name: "missing port", cfg: &config.Config{Host: "127.0.0.1"}, want: ""},
		{name: "negative port", cfg: &config.Config{Port: -1}, want: ""},
		{name: "wildcard v4", cfg: &config.Config{Port: 8330}, want: "http://127.0.0.1:8330"},
		{name: "zero address", cfg: &config.Config{Host: "0.0.0.0", Port: 8330}, want: "http://127.0.0.1:8330"},
		{name: "wildcard v6 with tls", cfg: &config.Config{Host: "::", Port: 8317, TLS: config.TLSConfig{Enable: true}}, want: "https://127.0.0.1:8317"},
		{name: "bracketed wildcard v6", cfg: &config.Config{Host: "[::]", Port: 8317}, want: "http://127.0.0.1:8317"},
		{name: "loopback v6", cfg: &config.Config{Host: "::1", Port: 9}, want: "http://[::1]:9"},
		{name: "loopback v4", cfg: &config.Config{Host: "127.0.0.1", Port: 8317}, want: "http://127.0.0.1:8317"},
		{name: "padded host", cfg: &config.Config{Host: "  10.0.0.5  ", Port: 9000}, want: "http://10.0.0.5:9000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostBaseURLFromConfig(tc.cfg); got != tc.want {
				t.Fatalf("hostBaseURLFromConfig() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCallRegisterSendsHostBaseURL(t *testing.T) {
	lookup := newTestSymbolLookup(&testPlugin{
		registerResult: validTestPlugin("base-url"),
	})
	h := New()
	h.runtimeConfig = &config.Config{Port: 8330}
	lp := &loadedPlugin{id: "base-url", client: lookup}

	if _, ok := h.callRegister(context.Background(), lp, runtimeItemConfig{Enabled: true}); !ok {
		t.Fatal("callRegister() failed")
	}
	if lookup.lastLifecycle.HostBaseURL != "http://127.0.0.1:8330" {
		t.Fatalf("host_base_url = %q, want http://127.0.0.1:8330", lookup.lastLifecycle.HostBaseURL)
	}
	if lookup.lastLifecycle.SchemaVersion != pluginabi.SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", lookup.lastLifecycle.SchemaVersion, pluginabi.SchemaVersion)
	}
}

func TestCallRegisterOmitsHostBaseURLWithoutPort(t *testing.T) {
	lookup := newTestSymbolLookup(&testPlugin{
		registerResult: validTestPlugin("base-url-empty"),
	})
	h := New()
	lp := &loadedPlugin{id: "base-url-empty", client: lookup}

	if _, ok := h.callRegister(context.Background(), lp, runtimeItemConfig{Enabled: true}); !ok {
		t.Fatal("callRegister() failed")
	}
	if lookup.lastLifecycle.HostBaseURL != "" {
		t.Fatalf("host_base_url = %q, want empty", lookup.lastLifecycle.HostBaseURL)
	}
}
