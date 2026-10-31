package keyshop

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var partnersNow = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

type partnerClock struct{ now time.Time }

func (c *partnerClock) Now() time.Time      { return c.now }
func (c *partnerClock) Add(d time.Duration) { c.now = c.now.Add(d) }

type partnerFixture struct {
	store   *Store
	clock   *partnerClock
	group   Group
	plan    Plan
	partner Partner
	secret  string
}

func newPartnerFixture(t *testing.T) *partnerFixture {
	return newPartnerFixtureWith(t, Config{})
}

func newPartnerFixtureWith(t *testing.T, cfg Config) *partnerFixture {
	t.Helper()
	clock := &partnerClock{now: partnersNow}
	cfg.DataFile = filepath.Join(t.TempDir(), "key-shop.db")
	cfg.Now = clock.Now
	store, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f := &partnerFixture{store: store, clock: clock}
	f.group = mustGroup(t, store, "g", "m1", "m2")
	f.plan = mustPlan(t, store, planInput("plan", f.group.ID))
	f.partner, f.secret = mustPartner(t, store, PartnerInput{Name: "shop"})
	return f
}

func mustPartner(t *testing.T, s *Store, in PartnerInput) (Partner, string) {
	t.Helper()
	partner, secret, err := s.CreatePartner(in)
	if err != nil {
		t.Fatal(err)
	}
	return partner, secret
}

func newMintSecret(t *testing.T) string {
	t.Helper()
	secret, _, _, _, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func newMintSecrets(t *testing.T, n int) []string {
	t.Helper()
	secrets := make([]string, 0, n)
	for range n {
		secrets = append(secrets, newMintSecret(t))
	}
	return secrets
}

func (f *partnerFixture) mintInput(ref string, secrets []string) PartnerMintInput {
	return PartnerMintInput{
		Ref: ref, PlanID: f.plan.ID, PlanRevision: f.plan.Revision,
		Quantity: int64(len(secrets)), Secrets: secrets,
	}
}

func (f *partnerFixture) mint(t *testing.T, ref string, secrets []string) PartnerMintResult {
	t.Helper()
	result, err := f.store.PartnerMint(f.partner.ID, f.mintInput(ref, secrets))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func keyIDs(keys []PartnerKey) map[string]bool {
	out := make(map[string]bool, len(keys))
	for _, key := range keys {
		out[key.ID] = true
	}
	return out
}

func TestPartnerConfigDefaults(t *testing.T) {
	f := newPartnerFixture(t)
	cfg := f.store.cfg
	if cfg.PartnerAPIEnabled {
		t.Fatal("the partner API must default to disabled")
	}
	if cfg.PublicBaseURL != "" || cfg.PublicDashboardPath != "/client" {
		t.Fatalf("public urls: base=%q dashboard=%q", cfg.PublicBaseURL, cfg.PublicDashboardPath)
	}
	if cfg.PartnerMaxQuantity != 50 || cfg.PartnerMaxBodyKB != 64 {
		t.Fatalf("quantity=%d body_kb=%d", cfg.PartnerMaxQuantity, cfg.PartnerMaxBodyKB)
	}
	if cfg.PartnerDailyKeyCap == nil || *cfg.PartnerDailyKeyCap != 300 {
		t.Fatalf("daily cap=%v", cfg.PartnerDailyKeyCap)
	}
	if cfg.PartnerUnconfirmedAlertMin != 10 || cfg.PartnerLogRetentionDays != 30 {
		t.Fatalf("alert=%d retention=%d", cfg.PartnerUnconfirmedAlertMin, cfg.PartnerLogRetentionDays)
	}
	if cfg.PartnerKeyLabelTemplate != "{plan}-{ref}-{i}" || cfg.PartnerKeyNoteTemplate != "partner:{partner} ref:{ref}" {
		t.Fatalf("label=%q note=%q", cfg.PartnerKeyLabelTemplate, cfg.PartnerKeyNoteTemplate)
	}
}

func TestPartnerSecretAndAuth(t *testing.T) {
	f := newPartnerFixture(t)
	if !strings.HasPrefix(f.secret, "ksp_") || len(f.secret) != 47 {
		t.Fatalf("secret=%q", f.secret)
	}
	if f.partner.SecretPrefix != f.secret[4:8] || f.partner.SecretLast4 != f.secret[len(f.secret)-4:] {
		t.Fatalf("partner=%+v secret=%q", f.partner, f.secret)
	}
	var stored []byte
	if err := f.store.db.QueryRow(`SELECT secret_hash FROM partners WHERE id=?`, f.partner.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !SecretMatches(f.secret, stored) || string(stored) == f.secret {
		t.Fatal("the partner secret must be stored as a hash")
	}
	partner, err := f.store.AuthenticatePartner(f.secret)
	if err != nil || partner.ID != f.partner.ID || partner.LastUsedAt == nil || !partner.LastUsedAt.Equal(partnersNow) {
		t.Fatalf("partner=%+v err=%v", partner, err)
	}
	f.clock.Add(time.Minute)
	partner, err = f.store.AuthenticatePartner(f.secret)
	if err != nil || partner.LastUsedAt == nil || !partner.LastUsedAt.Equal(partnersNow.Add(time.Minute)) {
		t.Fatalf("last used=%v err=%v", partner.LastUsedAt, err)
	}
	for _, token := range []string{"", "ksp_" + strings.Repeat("A", 43), f.secret + "x"} {
		if _, err = f.store.AuthenticatePartner(token); !errors.Is(err, ErrPartnerUnauthorized) {
			t.Fatalf("token %q err=%v", token, err)
		}
	}
	if _, err = f.store.UpdatePartner(f.partner.ID, PartnerUpdate{Status: "disabled", StatusSet: true}); err != nil {
		t.Fatal(err)
	}
	f.clock.Add(time.Minute)
	disabled, err := f.store.AuthenticatePartner(f.secret)
	if !errors.Is(err, ErrPartnerDisabled) || disabled.LastUsedAt == nil || !disabled.LastUsedAt.Equal(partnersNow.Add(2*time.Minute)) {
		t.Fatalf("disabled last used=%v err=%v", disabled.LastUsedAt, err)
	}
	if err = f.store.DeletePartner(f.partner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.AuthenticatePartner(f.secret); !errors.Is(err, ErrPartnerUnauthorized) {
		t.Fatalf("deleted err=%v", err)
	}
	if partners, err := f.store.ListPartners(); err != nil || len(partners) != 0 {
		t.Fatalf("partners=%+v err=%v", partners, err)
	}
}

func TestRotatePartnerKillsOldSecret(t *testing.T) {
	f := newPartnerFixture(t)
	rotated, secret, err := f.store.RotatePartner(f.partner.ID)
	if err != nil || secret == f.secret || !strings.HasPrefix(secret, "ksp_") {
		t.Fatalf("rotate err=%v secret=%q", err, secret)
	}
	if rotated.SecretPrefix != secret[4:8] || rotated.SecretLast4 != secret[len(secret)-4:] {
		t.Fatalf("rotated=%+v", rotated)
	}
	if _, err = f.store.AuthenticatePartner(f.secret); !errors.Is(err, ErrPartnerUnauthorized) {
		t.Fatalf("old secret err=%v", err)
	}
	if partner, err := f.store.AuthenticatePartner(secret); err != nil || partner.ID != f.partner.ID {
		t.Fatalf("new secret err=%v", err)
	}
	if _, _, err = f.store.RotatePartner("ghost"); !errors.Is(err, ErrPartnerNotFound) {
		t.Fatalf("missing partner err=%v", err)
	}
}

func TestUpdatePartnerFields(t *testing.T) {
	f := newPartnerFixture(t)
	other := mustPlan(t, f.store, planInput("other", f.group.ID))
	daily, max := int64(3), int64(2)
	updated, err := f.store.UpdatePartner(f.partner.ID, PartnerUpdate{
		Name:            "Renamed",
		NameSet:         true,
		AllowedPlanIDs:  []string{f.plan.ID, other.ID, f.plan.ID},
		AllowedPlansSet: true,
		DailyKeyCap:     &daily,
		DailyCapSet:     true,
		MaxQuantity:     &max,
		MaxQuantitySet:  true,
	})
	if err != nil || updated.Name != "Renamed" || updated.Status != "active" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if len(updated.AllowedPlanIDs) != 2 || *updated.DailyKeyCap != 3 || *updated.MaxQuantity != 2 {
		t.Fatalf("updated=%+v", updated)
	}
	cleared, err := f.store.UpdatePartner(f.partner.ID, PartnerUpdate{DailyCapSet: true, MaxQuantitySet: true})
	if err != nil || cleared.DailyKeyCap != nil || cleared.MaxQuantity != nil {
		t.Fatalf("cleared=%+v err=%v", cleared, err)
	}
	negative, zero := int64(-1), int64(0)
	for _, tc := range []PartnerUpdate{
		{Status: "paused", StatusSet: true},
		{Name: "  ", NameSet: true},
		{DailyKeyCap: &negative, DailyCapSet: true},
		{MaxQuantity: &zero, MaxQuantitySet: true},
	} {
		if _, err = f.store.UpdatePartner(f.partner.ID, tc); !errors.Is(err, ErrPartnerInvalid) {
			t.Fatalf("update %+v err=%v", tc, err)
		}
	}
	if _, err = f.store.UpdatePartner(f.partner.ID, PartnerUpdate{AllowedPlanIDs: []string{"ghost"}, AllowedPlansSet: true}); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("unknown plan err=%v", err)
	}
	if _, _, err = f.store.CreatePartner(PartnerInput{Name: "renamed"}); !errors.Is(err, ErrPartnerInvalid) {
		t.Fatalf("duplicate create err=%v", err)
	}
	second, _, err := f.store.CreatePartner(PartnerInput{Name: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.UpdatePartner(second.ID, PartnerUpdate{Name: "RENAMED", NameSet: true}); !errors.Is(err, ErrPartnerInvalid) {
		t.Fatalf("duplicate rename err=%v", err)
	}
	if _, err = f.store.UpdatePartner("ghost", PartnerUpdate{Name: "x", NameSet: true}); !errors.Is(err, ErrPartnerNotFound) {
		t.Fatalf("missing partner err=%v", err)
	}
}

func TestPartnerMintSuccess(t *testing.T) {
	f := newPartnerFixture(t)
	secrets := newMintSecrets(t, 2)
	result, err := f.store.PartnerMint(f.partner.ID, PartnerMintInput{
		Ref: "order-0001", PlanID: f.plan.ID, PlanRevision: f.plan.Revision,
		Quantity: 2, Secrets: secrets, Customer: "buyer@example.com", Note: "first order",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Ref != "order-0001" || result.State != "minted" || result.Replayed || result.Plan.ID != f.plan.ID || len(result.Keys) != 2 {
		t.Fatalf("result=%+v", result)
	}
	for i, secret := range secrets {
		key := result.Keys[i]
		if key.ID == "" || !strings.HasPrefix(key.Display, "ck_") || key.Status != "" {
			t.Fatalf("key=%+v", key)
		}
		if key.ExpiresAt == nil || !key.ExpiresAt.Equal(partnersNow.Add(time.Hour)) {
			t.Fatalf("expires=%v", key.ExpiresAt)
		}
		body := strings.TrimPrefix(secret, "ck_")
		if want := Display(body[:4], body[len(body)-4:]); key.Display != want {
			t.Fatalf("display=%q want %q", key.Display, want)
		}
		looked, err := f.store.Lookup(secret)
		if err != nil {
			t.Fatal(err)
		}
		if looked.ID != key.ID || looked.Display != key.Display || looked.Status != "active" {
			t.Fatalf("looked=%+v key=%+v", looked, key)
		}
		if looked.GroupID != f.group.ID || looked.TokenLimit != f.plan.TokenLimit || looked.RequestLimit != f.plan.RequestLimit {
			t.Fatalf("looked=%+v", looked)
		}
		if looked.Customer != "buyer@example.com" || looked.PartnerID != f.partner.ID || looked.PartnerRef != "order-0001" {
			t.Fatalf("looked=%+v", looked)
		}
		if looked.Label != fmt.Sprintf("plan-der-0001-%d", i+1) {
			t.Fatalf("label=%q", looked.Label)
		}
		if looked.Note != "partner:shop ref:der-0001" {
			t.Fatalf("note=%q", looked.Note)
		}
	}
	orders, _, err := f.store.ListPartnerOrders(PartnerOrderQuery{Ref: "order-0001"})
	if err != nil || len(orders) != 1 {
		t.Fatalf("orders=%+v err=%v", orders, err)
	}
	order := orders[0]
	if order.State != "minted" || order.Quantity != 2 || order.PlanID != f.plan.ID || order.Customer != "buyer@example.com" || order.OrderID != "" {
		t.Fatalf("order=%+v", order)
	}
}

func TestPartnerMintNeverExpires(t *testing.T) {
	f := newPartnerFixture(t)
	lifetime := PlanInput{Name: "lifetime", GroupID: f.group.ID, NeverExpires: true, Active: true}
	var err error
	if f.plan, err = f.store.CreatePlan(lifetime); err != nil {
		t.Fatal(err)
	}
	secrets := newMintSecrets(t, 1)
	result := f.mint(t, "order-0001", secrets)
	if len(result.Keys) != 1 || result.Keys[0].ExpiresAt != nil {
		t.Fatalf("result=%+v", result)
	}
	key, err := f.store.Lookup(secrets[0])
	if err != nil || key.ExpiresAt != nil {
		t.Fatalf("key=%+v err=%v", key, err)
	}
}

func TestPartnerMintReplayAndConflict(t *testing.T) {
	f := newPartnerFixture(t)
	secrets := newMintSecrets(t, 2)
	first := f.mint(t, "order-0001", secrets)
	second := f.mint(t, "order-0001", secrets)
	if !second.Replayed || second.State != "minted" {
		t.Fatalf("replay=%+v", second)
	}
	firstIDs, secondIDs := keyIDs(first.Keys), keyIDs(second.Keys)
	if len(firstIDs) != len(secondIDs) {
		t.Fatalf("replay must return the same keys: first=%+v second=%+v", first.Keys, second.Keys)
	}
	for id := range firstIDs {
		if !secondIDs[id] {
			t.Fatalf("replay must return the same keys: first=%+v second=%+v", first.Keys, second.Keys)
		}
	}
	orders, _, err := f.store.ListPartnerOrders(PartnerOrderQuery{Ref: "order-0001"})
	if err != nil || len(orders) != 1 {
		t.Fatalf("orders=%+v err=%v", orders, err)
	}
	keys, err := f.store.List("")
	if err != nil || len(keys) != 2 {
		t.Fatalf("keys=%d err=%v", len(keys), err)
	}
	if _, err = f.store.PartnerMint(f.partner.ID, f.mintInput("order-0001", secrets[:1])); !errors.Is(err, ErrRefConflict) {
		t.Fatalf("quantity conflict err=%v", err)
	}
	if _, err = f.store.PartnerMint(f.partner.ID, f.mintInput("order-0001", newMintSecrets(t, 2))); !errors.Is(err, ErrRefConflict) {
		t.Fatalf("secret conflict err=%v", err)
	}
	// A plan revision change updates the fingerprint, so a body naming the new
	// revision conflicts while the original body still replays.
	original := f.mintInput("order-0001", secrets)
	next := planInput("plan", f.group.ID)
	next.TTLSeconds = 7200
	f.plan, err = f.store.UpdatePlan(f.plan.ID, next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.PartnerMint(f.partner.ID, f.mintInput("order-0001", secrets)); !errors.Is(err, ErrRefConflict) {
		t.Fatalf("revision conflict err=%v", err)
	}
	// Replay after confirm reports the stored state.
	if _, err = f.store.PartnerConfirm(f.partner.ID, "order-0001", "shop-1", ""); err != nil {
		t.Fatal(err)
	}
	replayed, err := f.store.PartnerMint(f.partner.ID, original)
	if err != nil || !replayed.Replayed || replayed.State != "confirmed" {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
}

func TestPartnerMintValidation(t *testing.T) {
	f := newPartnerFixture(t)
	valid := newMintSecret(t)
	many := newMintSecrets(t, 51)
	cases := []struct {
		name string
		in   PartnerMintInput
	}{
		{"short ref", f.mintInput("short", []string{valid})},
		{"bad ref characters", f.mintInput("ref with space", []string{valid})},
		{"long ref", f.mintInput(strings.Repeat("a", 81), []string{valid})},
		{"zero quantity", f.mintInput("order-0001", nil)},
		{"quantity over config max", f.mintInput("order-0001", many)},
		{"secrets mismatch", PartnerMintInput{Ref: "order-0001", PlanID: f.plan.ID, PlanRevision: f.plan.Revision, Quantity: 2, Secrets: []string{valid}}},
		{"bad secret", PartnerMintInput{Ref: "order-0001", PlanID: f.plan.ID, PlanRevision: f.plan.Revision, Quantity: 1, Secrets: []string{"ck_short"}}},
		{"duplicate secret", PartnerMintInput{Ref: "order-0001", PlanID: f.plan.ID, PlanRevision: f.plan.Revision, Quantity: 2, Secrets: []string{valid, valid}}},
		{"customer too long", PartnerMintInput{Ref: "order-0001", PlanID: f.plan.ID, PlanRevision: f.plan.Revision, Quantity: 1, Secrets: []string{valid}, Customer: strings.Repeat("a", 201)}},
		{"note too long", PartnerMintInput{Ref: "order-0001", PlanID: f.plan.ID, PlanRevision: f.plan.Revision, Quantity: 1, Secrets: []string{valid}, Note: strings.Repeat("a", 201)}},
	}
	for _, tc := range cases {
		if _, err := f.store.PartnerMint(f.partner.ID, tc.in); !errors.Is(err, ErrPartnerBadRequest) {
			t.Fatalf("%s: err=%v", tc.name, err)
		}
	}
	if orders, _, err := f.store.ListPartnerOrders(PartnerOrderQuery{}); err != nil || len(orders) != 0 {
		t.Fatalf("a rejected mint must not write an order: %+v err=%v", orders, err)
	}
}

func TestPartnerMintPlanErrors(t *testing.T) {
	f := newPartnerFixture(t)
	secrets := newMintSecrets(t, 1)
	unknown := f.mintInput("order-0001", secrets)
	unknown.PlanID = "ghost"
	if _, err := f.store.PartnerMint(f.partner.ID, unknown); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("unknown plan err=%v", err)
	}
	other := mustPlan(t, f.store, planInput("other", f.group.ID))
	if _, err := f.store.UpdatePartner(f.partner.ID, PartnerUpdate{AllowedPlanIDs: []string{other.ID}, AllowedPlansSet: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.PartnerMint(f.partner.ID, f.mintInput("order-0001", secrets)); !errors.Is(err, ErrPlanNotAllowed) {
		t.Fatalf("not allowed err=%v", err)
	}
	if _, err := f.store.UpdatePartner(f.partner.ID, PartnerUpdate{AllowedPlanIDs: []string{f.plan.ID}, AllowedPlansSet: true}); err != nil {
		t.Fatal(err)
	}
	inactive := planInput("plan", f.group.ID)
	inactive.Active = false
	var err error
	if f.plan, err = f.store.UpdatePlan(f.plan.ID, inactive); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.PartnerMint(f.partner.ID, f.mintInput("order-0001", secrets)); !errors.Is(err, ErrPlanInactive) {
		t.Fatalf("inactive err=%v", err)
	}
	reactivate := planInput("plan", f.group.ID)
	if f.plan, err = f.store.UpdatePlan(f.plan.ID, reactivate); err != nil {
		t.Fatal(err)
	}
	stale := f.mintInput("order-0001", secrets)
	stale.PlanRevision = 1
	var changed *PlanChangedError
	if _, err = f.store.PartnerMint(f.partner.ID, stale); !errors.As(err, &changed) || changed.Plan.Revision != f.plan.Revision {
		t.Fatalf("plan changed err=%v", err)
	}
	f.mint(t, "order-0001", secrets)
}

func TestPartnerQuantityOverride(t *testing.T) {
	f := newPartnerFixture(t)
	max := int64(2)
	if _, err := f.store.UpdatePartner(f.partner.ID, PartnerUpdate{MaxQuantity: &max, MaxQuantitySet: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.PartnerMint(f.partner.ID, f.mintInput("order-0001", newMintSecrets(t, 3))); !errors.Is(err, ErrPartnerBadRequest) {
		t.Fatalf("quantity over partner max err=%v", err)
	}
	f.mint(t, "order-0001", newMintSecrets(t, 2))
}

func TestPartnerDailyCap(t *testing.T) {
	f := newPartnerFixture(t)
	cap := int64(2)
	if _, err := f.store.UpdatePartner(f.partner.ID, PartnerUpdate{DailyKeyCap: &cap, DailyCapSet: true}); err != nil {
		t.Fatal(err)
	}
	f.mint(t, "order-0001", newMintSecrets(t, 2))
	if _, err := f.store.PartnerMint(f.partner.ID, f.mintInput("order-0002", newMintSecrets(t, 1))); !errors.Is(err, ErrDailyCapReached) {
		t.Fatalf("cap err=%v", err)
	}
	// A voided order stops counting toward the day.
	if _, err := f.store.PartnerVoid(f.partner.ID, "order-0001", "test"); err != nil {
		t.Fatal(err)
	}
	f.mint(t, "order-0002", newMintSecrets(t, 2))
	// The next UTC day starts a new sum.
	f.clock.Add(24 * time.Hour)
	f.mint(t, "order-0003", newMintSecrets(t, 2))
	// A non-null zero override means unlimited.
	zero := int64(0)
	if _, err := f.store.UpdatePartner(f.partner.ID, PartnerUpdate{DailyKeyCap: &zero, DailyCapSet: true}); err != nil {
		t.Fatal(err)
	}
	f.mint(t, "order-0004", newMintSecrets(t, 3))
}

func TestPartnerConfigDailyCap(t *testing.T) {
	cap := int64(2)
	f := newPartnerFixtureWith(t, Config{PartnerDailyKeyCap: &cap})
	f.mint(t, "order-0001", newMintSecrets(t, 2))
	if _, err := f.store.PartnerMint(f.partner.ID, f.mintInput("order-0002", newMintSecrets(t, 1))); !errors.Is(err, ErrDailyCapReached) {
		t.Fatalf("config cap err=%v", err)
	}
	f.clock.Add(24 * time.Hour)
	f.mint(t, "order-0002", newMintSecrets(t, 1))
}

func TestPartnerMintSecretConflictRollsBack(t *testing.T) {
	f := newPartnerFixture(t)
	existing, existingSecret, err := f.store.Create(CreateInput{Label: "existing"})
	if err != nil {
		t.Fatal(err)
	}
	secrets := []string{newMintSecret(t), existingSecret}
	if _, err = f.store.PartnerMint(f.partner.ID, f.mintInput("order-0001", secrets)); !errors.Is(err, ErrSecretConflict) {
		t.Fatalf("secret conflict err=%v", err)
	}
	if orders, _, err := f.store.ListPartnerOrders(PartnerOrderQuery{Ref: "order-0001"}); err != nil || len(orders) != 0 {
		t.Fatalf("the rollback must drop the order row: %+v err=%v", orders, err)
	}
	keys, err := f.store.List("")
	if err != nil || len(keys) != 1 || keys[0].ID != existing.ID {
		t.Fatalf("the rollback must drop the minted keys: %+v err=%v", keys, err)
	}
}

func TestPartnerVoidMintedDisablesKeys(t *testing.T) {
	f := newPartnerFixture(t)
	secrets := newMintSecrets(t, 2)
	f.mint(t, "order-0001", secrets)
	result, err := f.store.PartnerVoid(f.partner.ID, "order-0001", "buyer cancelled")
	if err != nil || result.State != "voided" || result.KeysDisabled != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, secret := range secrets {
		key, err := f.store.Lookup(secret)
		if err != nil || key.Status != "disabled" {
			t.Fatalf("key=%+v err=%v", key, err)
		}
		admission, err := f.store.Admit(AdmitInput{Secret: secret, RequestID: "req-" + key.ID, Path: "/v1/chat/completions"})
		if err != nil || admission.Allowed || admission.Code != "key_disabled" {
			t.Fatalf("admission=%+v err=%v", admission, err)
		}
	}
	again, err := f.store.PartnerVoid(f.partner.ID, "order-0001", "buyer cancelled")
	if err != nil || again.KeysDisabled != 0 {
		t.Fatalf("idempotent void=%+v err=%v", again, err)
	}
	status, err := f.store.PartnerStatus(f.partner.ID, "order-0001")
	if err != nil || status.State != "voided" || len(status.Keys) != 2 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	for _, key := range status.Keys {
		if key.Status != "disabled" {
			t.Fatalf("key=%+v", key)
		}
	}
	if _, err = f.store.PartnerConfirm(f.partner.ID, "order-0001", "shop-1", ""); !errors.Is(err, ErrRefVoided) {
		t.Fatalf("confirm voided err=%v", err)
	}
	if _, err = f.store.PartnerMint(f.partner.ID, f.mintInput("order-0001", newMintSecrets(t, 1))); !errors.Is(err, ErrRefVoided) {
		t.Fatalf("mint voided err=%v", err)
	}
}

func TestPartnerVoidTombstone(t *testing.T) {
	f := newPartnerFixture(t)
	result, err := f.store.PartnerVoid(f.partner.ID, "never-minted", "shop failed")
	if err != nil || result.State != "voided" || result.KeysDisabled != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	status, err := f.store.PartnerStatus(f.partner.ID, "never-minted")
	if err != nil || status.State != "voided" || status.OrderID != "" || len(status.Keys) != 0 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	orders, _, err := f.store.ListPartnerOrders(PartnerOrderQuery{Ref: "never-minted"})
	if err != nil || len(orders) != 1 {
		t.Fatalf("orders=%+v err=%v", orders, err)
	}
	order := orders[0]
	if order.State != "voided" || order.Quantity != 0 || order.PlanID != "" || order.PlanRevision != 0 ||
		order.Fingerprint != "" || order.VoidReason != "shop failed" || order.VoidedAt == nil {
		t.Fatalf("tombstone=%+v", order)
	}
	if _, err = f.store.PartnerMint(f.partner.ID, f.mintInput("never-minted", newMintSecrets(t, 1))); !errors.Is(err, ErrRefVoided) {
		t.Fatalf("mint after tombstone err=%v", err)
	}
	again, err := f.store.PartnerVoid(f.partner.ID, "never-minted", "shop failed")
	if err != nil || again.KeysDisabled != 0 {
		t.Fatalf("idempotent tombstone=%+v err=%v", again, err)
	}
}

func TestPartnerConfirm(t *testing.T) {
	f := newPartnerFixture(t)
	secrets := newMintSecrets(t, 1)
	f.mint(t, "order-0001", secrets)
	if _, err := f.store.PartnerConfirm(f.partner.ID, "missing-ref", "shop-1", ""); !errors.Is(err, ErrRefNotFound) {
		t.Fatalf("missing err=%v", err)
	}
	state, err := f.store.PartnerConfirm(f.partner.ID, "order-0001", "shop-1", "buyer")
	if err != nil || state.State != "confirmed" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	orders, _, err := f.store.ListPartnerOrders(PartnerOrderQuery{Ref: "order-0001"})
	if err != nil || len(orders) != 1 {
		t.Fatalf("orders=%+v err=%v", orders, err)
	}
	order := orders[0]
	if order.State != "confirmed" || order.OrderID != "shop-1" || order.Buyer != "buyer" || order.ConfirmedAt == nil {
		t.Fatalf("order=%+v", order)
	}
	key, err := f.store.Lookup(secrets[0])
	if err != nil || key.Customer != "buyer" {
		t.Fatalf("key=%+v err=%v", key, err)
	}
	if state, err = f.store.PartnerConfirm(f.partner.ID, "order-0001", "shop-1", "buyer"); err != nil || state.State != "confirmed" {
		t.Fatalf("idempotent confirm=%+v err=%v", state, err)
	}
	if _, err = f.store.PartnerConfirm(f.partner.ID, "order-0001", "shop-2", "buyer"); !errors.Is(err, ErrOrderConflict) {
		t.Fatalf("order conflict err=%v", err)
	}
	if _, err = f.store.PartnerVoid(f.partner.ID, "order-0001", "test"); !errors.Is(err, ErrRefConfirmed) {
		t.Fatalf("void confirmed err=%v", err)
	}
	f.mint(t, "order-0002", newMintSecrets(t, 1))
	if _, err = f.store.PartnerVoid(f.partner.ID, "order-0002", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.PartnerConfirm(f.partner.ID, "order-0002", "shop-3", ""); !errors.Is(err, ErrRefVoided) {
		t.Fatalf("confirm voided err=%v", err)
	}
	// An empty buyer keeps the customer the mint wrote.
	keptSecrets := newMintSecrets(t, 1)
	if _, err = f.store.PartnerMint(f.partner.ID, PartnerMintInput{
		Ref: "order-0003", PlanID: f.plan.ID, PlanRevision: f.plan.Revision,
		Quantity: 1, Secrets: keptSecrets, Customer: "keep-me",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.PartnerConfirm(f.partner.ID, "order-0003", "shop-4", ""); err != nil {
		t.Fatal(err)
	}
	kept, err := f.store.Lookup(keptSecrets[0])
	if err != nil || kept.Customer != "keep-me" {
		t.Fatalf("key=%+v err=%v", kept, err)
	}
}

func TestPartnerStatus(t *testing.T) {
	f := newPartnerFixture(t)
	if _, err := f.store.PartnerStatus(f.partner.ID, "missing-ref"); !errors.Is(err, ErrRefNotFound) {
		t.Fatalf("missing err=%v", err)
	}
	f.mint(t, "order-0001", newMintSecrets(t, 2))
	status, err := f.store.PartnerStatus(f.partner.ID, "order-0001")
	if err != nil || status.State != "minted" || status.Ref != "order-0001" || len(status.Keys) != 2 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	for _, key := range status.Keys {
		if key.Status != "active" || key.ExpiresAt == nil || !key.ExpiresAt.Equal(partnersNow.Add(time.Hour)) {
			t.Fatalf("key=%+v", key)
		}
	}
	f.clock.Add(2 * time.Hour)
	status, err = f.store.PartnerStatus(f.partner.ID, "order-0001")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range status.Keys {
		if key.Status != "expired" {
			t.Fatalf("key=%+v", key)
		}
	}
}

func TestPartnerRefsPaging(t *testing.T) {
	f := newPartnerFixture(t)
	for i := range 5 {
		f.clock.Add(time.Minute)
		f.mint(t, fmt.Sprintf("ref-%04d", i), newMintSecrets(t, 1))
	}
	all, err := f.store.PartnerRefs(f.partner.ID, PartnerRefsQuery{})
	if err != nil || len(all.Refs) != 5 || all.NextCursor != "" {
		t.Fatalf("all=%+v err=%v", all, err)
	}
	for i, item := range all.Refs {
		if item.Ref != fmt.Sprintf("ref-%04d", i) {
			t.Fatalf("refs must be ordered by created_at: %+v", all.Refs)
		}
		if item.State != "minted" || item.Quantity != 1 || item.PlanID != f.plan.ID {
			t.Fatalf("item=%+v", item)
		}
	}
	since := all.Refs[2].CreatedAt
	window, err := f.store.PartnerRefs(f.partner.ID, PartnerRefsQuery{Since: since})
	if err != nil || len(window.Refs) != 3 {
		t.Fatalf("since window=%+v err=%v", window, err)
	}
	until := all.Refs[1].CreatedAt
	early, err := f.store.PartnerRefs(f.partner.ID, PartnerRefsQuery{Until: until})
	if err != nil || len(early.Refs) != 2 {
		t.Fatalf("until window=%+v err=%v", early, err)
	}
	page, err := f.store.PartnerRefs(f.partner.ID, PartnerRefsQuery{Limit: 2})
	if err != nil || len(page.Refs) != 2 || page.NextCursor == "" {
		t.Fatalf("page1=%+v err=%v", page, err)
	}
	next, err := f.store.PartnerRefs(f.partner.ID, PartnerRefsQuery{Limit: 2, Cursor: page.NextCursor})
	if err != nil || len(next.Refs) != 2 || next.Refs[0].Ref != "ref-0002" || next.NextCursor == "" {
		t.Fatalf("page2=%+v err=%v", next, err)
	}
	last, err := f.store.PartnerRefs(f.partner.ID, PartnerRefsQuery{Limit: 2, Cursor: next.NextCursor})
	if err != nil || len(last.Refs) != 1 || last.NextCursor != "" {
		t.Fatalf("page3=%+v err=%v", last, err)
	}
	exact, err := f.store.PartnerRefs(f.partner.ID, PartnerRefsQuery{Limit: 5})
	if err != nil || len(exact.Refs) != 5 || exact.NextCursor != "" {
		t.Fatalf("exact page=%+v err=%v", exact, err)
	}
	if _, err = f.store.PartnerRefs(f.partner.ID, PartnerRefsQuery{Cursor: "garbage"}); !errors.Is(err, ErrPartnerBadRequest) {
		t.Fatalf("invalid cursor err=%v", err)
	}
	other, _ := mustPartner(t, f.store, PartnerInput{Name: "other"})
	if isolated, err := f.store.PartnerRefs(other.ID, PartnerRefsQuery{}); err != nil || len(isolated.Refs) != 0 {
		t.Fatalf("partner isolation=%+v err=%v", isolated, err)
	}
}

func TestPartnerRefsLimits(t *testing.T) {
	f := newPartnerFixture(t)
	// Tombstones go through the store write path, so this also covers bulk refs.
	for i := range 505 {
		if _, err := f.store.PartnerVoid(f.partner.ID, fmt.Sprintf("bulk-%04d", i), "bulk"); err != nil {
			t.Fatal(err)
		}
	}
	defaulted, err := f.store.PartnerRefs(f.partner.ID, PartnerRefsQuery{})
	if err != nil || len(defaulted.Refs) != defaultPartnerListLimit || defaulted.NextCursor == "" {
		t.Fatalf("default limit: got %d refs err=%v", len(defaulted.Refs), err)
	}
	capped, err := f.store.PartnerRefs(f.partner.ID, PartnerRefsQuery{Limit: 10000})
	if err != nil || len(capped.Refs) != maxPartnerListLimit || capped.NextCursor == "" {
		t.Fatalf("max limit: got %d refs err=%v", len(capped.Refs), err)
	}
}

func TestPartnerCallLog(t *testing.T) {
	f := newPartnerFixture(t)
	record := func(op string, status int, code string) {
		t.Helper()
		if err := f.store.RecordPartnerCall(PartnerCall{
			PartnerID: f.partner.ID, Op: op, HTTPStatus: status, Code: code,
			Ref: "order-0001", LatencyMS: 7, ClientIP: "203.0.113.5",
		}); err != nil {
			t.Fatal(err)
		}
	}
	record("mint", 200, "")
	f.clock.Add(time.Minute)
	record("mint", 401, "unauthorized")
	f.clock.Add(time.Minute)
	record("void", 500, "internal")
	all, next, err := f.store.ListPartnerCalls(PartnerCallQuery{})
	if err != nil || len(all) != 3 || next != "" {
		t.Fatalf("all=%+v next=%q err=%v", all, next, err)
	}
	if all[0].Op != "void" || all[1].HTTPStatus != 401 || all[2].Op != "mint" {
		t.Fatalf("calls must be newest first: %+v", all)
	}
	if all[0].At.IsZero() || all[0].LatencyMS != 7 || all[0].ClientIP != "203.0.113.5" || all[0].Ref != "order-0001" {
		t.Fatalf("call=%+v", all[0])
	}
	if ok, _, err := f.store.ListPartnerCalls(PartnerCallQuery{Status: "ok"}); err != nil || len(ok) != 1 || ok[0].HTTPStatus != 200 {
		t.Fatalf("ok=%+v err=%v", ok, err)
	}
	if failed, _, err := f.store.ListPartnerCalls(PartnerCallQuery{Status: "error"}); err != nil || len(failed) != 2 {
		t.Fatalf("error=%+v err=%v", failed, err)
	}
	if minted, _, err := f.store.ListPartnerCalls(PartnerCallQuery{Op: "mint"}); err != nil || len(minted) != 2 {
		t.Fatalf("op filter=%+v err=%v", minted, err)
	}
	windowed, _, err := f.store.ListPartnerCalls(PartnerCallQuery{From: partnersNow.Add(90 * time.Second)})
	if err != nil || len(windowed) != 1 || windowed[0].Op != "void" {
		t.Fatalf("from filter=%+v err=%v", windowed, err)
	}
	page, next, err := f.store.ListPartnerCalls(PartnerCallQuery{Limit: 2})
	if err != nil || len(page) != 2 || next == "" {
		t.Fatalf("page1=%+v next=%q err=%v", page, next, err)
	}
	rest, next2, err := f.store.ListPartnerCalls(PartnerCallQuery{Limit: 2, Cursor: next})
	if err != nil || len(rest) != 1 || next2 != "" {
		t.Fatalf("page2=%+v next=%q err=%v", rest, next2, err)
	}
	if calls, _, err := f.store.ListPartnerCalls(PartnerCallQuery{PartnerID: "ghost"}); err != nil || len(calls) != 0 {
		t.Fatalf("partner filter=%+v err=%v", calls, err)
	}
	f.clock.Add(31 * 24 * time.Hour)
	if err := f.store.Sweep(); err != nil {
		t.Fatal(err)
	}
	if after, _, err := f.store.ListPartnerCalls(PartnerCallQuery{}); err != nil || len(after) != 0 {
		t.Fatalf("retention must delete old calls: %+v err=%v", after, err)
	}
}

func TestListPartnersStats(t *testing.T) {
	f := newPartnerFixture(t)
	f.mint(t, "order-0001", newMintSecrets(t, 1))
	f.clock.Add(5 * time.Minute)
	if _, err := f.store.PartnerConfirm(f.partner.ID, "order-0001", "shop-1", ""); err != nil {
		t.Fatal(err)
	}
	f.mint(t, "order-0002", newMintSecrets(t, 1))
	if _, err := f.store.PartnerVoid(f.partner.ID, "order-0002", "test"); err != nil {
		t.Fatal(err)
	}
	f.clock.Add(time.Minute)
	f.mint(t, "order-0003", newMintSecrets(t, 2))
	f.clock.Add(20 * time.Minute)
	for _, status := range []int{200, 403, 500} {
		if err := f.store.RecordPartnerCall(PartnerCall{PartnerID: f.partner.ID, Op: "mint", HTTPStatus: status}); err != nil {
			t.Fatal(err)
		}
	}
	cap := int64(7)
	if _, _, err := f.store.CreatePartner(PartnerInput{Name: "limited", DailyKeyCap: &cap}); err != nil {
		t.Fatal(err)
	}
	partners, err := f.store.ListPartners()
	if err != nil || len(partners) != 2 {
		t.Fatalf("partners=%+v err=%v", partners, err)
	}
	var shop *Partner
	for i := range partners {
		if partners[i].ID == f.partner.ID {
			shop = &partners[i]
		}
	}
	if shop == nil || shop.Stats == nil {
		t.Fatalf("stats missing: %+v", partners)
	}
	stats := shop.Stats
	if stats.CallsOK != 1 || stats.CallsClientError != 1 || stats.CallsServerError != 1 {
		t.Fatalf("calls=%+v", stats)
	}
	if stats.Minted != 1 || stats.Confirmed != 1 || stats.Voided != 1 || stats.UnconfirmedOverdue != 1 {
		t.Fatalf("orders=%+v", stats)
	}
	if stats.DailyUsed != 3 || stats.DailyCap != 300 {
		t.Fatalf("daily=%+v", stats)
	}
	for _, partner := range partners {
		if partner.Name == "limited" && (partner.Stats == nil || partner.Stats.DailyCap != 7) {
			t.Fatalf("per-partner cap=%+v", partner.Stats)
		}
	}
}

func TestListPartnerOrders(t *testing.T) {
	f := newPartnerFixture(t)
	f.mint(t, "order-0001", newMintSecrets(t, 1))
	f.clock.Add(time.Minute)
	f.mint(t, "order-0002", newMintSecrets(t, 1))
	if _, err := f.store.PartnerConfirm(f.partner.ID, "order-0001", "shop-1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.PartnerVoid(f.partner.ID, "order-0002", "test"); err != nil {
		t.Fatal(err)
	}
	all, next, err := f.store.ListPartnerOrders(PartnerOrderQuery{})
	if err != nil || len(all) != 2 || next != "" {
		t.Fatalf("all=%+v next=%q err=%v", all, next, err)
	}
	if all[0].Ref != "order-0002" || all[1].Ref != "order-0001" {
		t.Fatalf("orders must be newest first: %+v", all)
	}
	if confirmed, _, err := f.store.ListPartnerOrders(PartnerOrderQuery{State: "confirmed"}); err != nil || len(confirmed) != 1 || confirmed[0].Ref != "order-0001" {
		t.Fatalf("confirmed=%+v err=%v", confirmed, err)
	}
	if voided, _, err := f.store.ListPartnerOrders(PartnerOrderQuery{State: "voided"}); err != nil || len(voided) != 1 || voided[0].Ref != "order-0002" {
		t.Fatalf("voided=%+v err=%v", voided, err)
	}
	if byRef, _, err := f.store.ListPartnerOrders(PartnerOrderQuery{Ref: "order-0001"}); err != nil || len(byRef) != 1 {
		t.Fatalf("ref filter=%+v err=%v", byRef, err)
	}
	if windowed, _, err := f.store.ListPartnerOrders(PartnerOrderQuery{From: partnersNow.Add(30 * time.Second)}); err != nil || len(windowed) != 1 || windowed[0].Ref != "order-0002" {
		t.Fatalf("from filter=%+v err=%v", windowed, err)
	}
	if other, _, err := f.store.ListPartnerOrders(PartnerOrderQuery{PartnerID: "ghost"}); err != nil || len(other) != 0 {
		t.Fatalf("partner filter=%+v err=%v", other, err)
	}
	page, next, err := f.store.ListPartnerOrders(PartnerOrderQuery{Limit: 1})
	if err != nil || len(page) != 1 || next == "" {
		t.Fatalf("page1=%+v next=%q err=%v", page, next, err)
	}
	rest, next2, err := f.store.ListPartnerOrders(PartnerOrderQuery{Limit: 1, Cursor: next})
	if err != nil || len(rest) != 1 || rest[0].Ref != "order-0001" || next2 != "" {
		t.Fatalf("page2=%+v next=%q err=%v", rest, next2, err)
	}
}

func TestPartnerSchemaIsAdditive(t *testing.T) {
	f := newPartnerFixture(t)
	for _, table := range []string{"plans", "partners", "partner_orders", "partner_calls"} {
		var name string
		if err := f.store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
	}
	for _, index := range []string{"idx_partners_name_live", "idx_partner_orders_created", "idx_partner_orders_state", "idx_partner_calls_at", "idx_keys_partner"} {
		var name string
		if err := f.store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&name); err != nil {
			t.Fatalf("%s: %v", index, err)
		}
	}
	columns, err := tableColumns(f.store.db, "keys")
	if err != nil || !columns["partner_id"] || !columns["partner_ref"] {
		t.Fatalf("columns=%v err=%v", columns, err)
	}
	legacy, secret, err := f.store.Create(CreateInput{Label: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.PartnerID != "" || legacy.PartnerRef != "" {
		t.Fatalf("legacy=%+v", legacy)
	}
	// Simulate a database created before the partner columns existed.
	if _, err = f.store.db.Exec(`DROP INDEX IF EXISTS idx_keys_partner`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.Exec(`ALTER TABLE keys DROP COLUMN partner_id`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.Exec(`ALTER TABLE keys DROP COLUMN partner_ref`); err != nil {
		t.Fatal(err)
	}
	columns, err = tableColumns(f.store.db, "keys")
	if err != nil || columns["partner_id"] || columns["partner_ref"] {
		t.Fatalf("columns should be gone for the migration test: %v err=%v", columns, err)
	}
	path := f.store.cfg.DataFile
	if err = f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(Config{DataFile: path, Now: f.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	columns, err = tableColumns(reopened.db, "keys")
	if err != nil || !columns["partner_id"] || !columns["partner_ref"] {
		t.Fatalf("ensureColumns must restore the partner columns: %v err=%v", columns, err)
	}
	key, err := reopened.Lookup(secret)
	if err != nil || key.ID != legacy.ID || key.PartnerID != "" {
		t.Fatalf("key=%+v err=%v", key, err)
	}
	if _, _, err = reopened.Create(CreateInput{Label: "after"}); err != nil {
		t.Fatalf("the existing insert path must still work: %v", err)
	}
	if _, err = reopened.CreatePlan(planInput("after", f.group.ID)); err != nil {
		t.Fatalf("plans must survive the reopen: %v", err)
	}
}
