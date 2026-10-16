package keyshop

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strings"
)

func NewSecret() (secret string, hash []byte, prefix string, last4 string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", nil, "", "", err
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	secret = "ck_" + body
	sum := sha256.Sum256([]byte(secret))
	hash = append([]byte(nil), sum[:]...)
	prefix = body[:4]
	last4 = body[len(body)-4:]
	return secret, hash, prefix, last4, nil
}

func HashSecret(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

func SecretMatches(secret string, hash []byte) bool {
	got := HashSecret(secret)
	if len(got) != len(hash) {
		return false
	}
	return subtle.ConstantTimeCompare(got, hash) == 1
}

func Display(prefix, last4 string) string {
	return "ck_" + prefix + "…" + last4
}

func BilledTokens(input, output int64) int64 {
	if input < 0 {
		input = 0
	}
	if output < 0 {
		output = 0
	}
	return input + output
}

func ExtractSecret(header func(string) string) (string, bool) {
	if header == nil {
		return "", false
	}
	if secret, ok := bearer(header("Authorization")); ok {
		return secret, true
	}
	if secret := strings.TrimSpace(header("X-Api-Key")); secret != "" {
		return secret, true
	}
	if secret := strings.TrimSpace(header("X-Goog-Api-Key")); secret != "" {
		return secret, true
	}
	return "", false
}

func bearer(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	parts := strings.SplitN(value, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
		secret := strings.TrimSpace(parts[1])
		return secret, secret != ""
	}
	return value, true
}
