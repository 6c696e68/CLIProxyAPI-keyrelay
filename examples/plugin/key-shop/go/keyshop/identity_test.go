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

func TestBilledTokensIgnoresReasoningAndCache(t *testing.T) {
	if got := BilledTokens(3, 4); got != 7 {
		t.Fatalf("billed = %d", got)
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
