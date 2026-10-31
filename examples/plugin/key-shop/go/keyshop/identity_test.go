package keyshop

import (
	"strings"
	"testing"
)

func TestNewSecretIsURLSafeAndHashMatches(t *testing.T) {
	secret, hash, prefix, last4, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, "ck_") || strings.ContainsAny(secret, "+/=") {
		t.Fatal("secret format is not ck_ raw url-safe base64")
	}
	if !SecretMatches(secret, hash) || SecretMatches(secret+"x", hash) {
		t.Fatal("hash match failed")
	}
	if Display(prefix, last4) != "ck_"+prefix+"…"+last4 {
		t.Fatalf("display = %s", Display(prefix, last4))
	}
}

func TestBilledTokensUsesUpstreamTotal(t *testing.T) {
	// Codex reports a total that already equals input plus output.
	if got := BilledTokens(100, 50, 150); got != 150 {
		t.Fatalf("codex billed = %d", got)
	}
	// Claude adds cache read and cache creation tokens to the total.
	if got := BilledTokens(10, 50, 1060); got != 1060 {
		t.Fatalf("claude billed = %d", got)
	}
	// A missing total falls back to input plus output.
	if got := BilledTokens(3, 4, 0); got != 7 {
		t.Fatalf("fallback billed = %d", got)
	}
	// Negative counters clamp to zero instead of crediting usage.
	if got := BilledTokens(-5, -6, -7); got != 0 {
		t.Fatalf("clamped billed = %d", got)
	}
	if got := BilledTokens(4, -9, 0); got != 4 {
		t.Fatalf("partial clamp billed = %d", got)
	}
}

func TestExtractSecretIgnoresQueryStyleValues(t *testing.T) {
	headers := map[string]string{"X-Api-Key": "ck_abc"}
	got, ok := ExtractSecret(func(name string) string { return headers[name] })
	if !ok || got != "ck_abc" {
		t.Fatal("x-api-key was not extracted")
	}
	headers = map[string]string{"Authorization": "Bearer ck_bearer"}
	got, ok = ExtractSecret(func(name string) string { return headers[name] })
	if !ok || got != "ck_bearer" {
		t.Fatal("bearer secret was not extracted")
	}
}
