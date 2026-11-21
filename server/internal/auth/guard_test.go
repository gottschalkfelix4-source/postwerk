package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"postwerk/internal/store"
)

func testGuard(max int) (*Guard, *time.Time) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	g := NewGuard(func() Limits { return Limits{MaxAttempts: max, Window: 10 * time.Minute, BanFor: time.Hour} })
	g.now = func() time.Time { return now }
	return g, &now
}

func TestGuardOwnAccountSuccessDoesNotResetCounter(t *testing.T) {
	g, _ := testGuard(5)
	ip := "203.0.113.7"
	for i := 0; i < 4; i++ {
		g.FailAccount(ip, "victim@example.test", "web login")
	}
	// The attacker logs into an own account in between ...
	g.Success(ip, "attacker@example.test")
	// ... which must not forgive the failures against the victim.
	g.FailAccount(ip, "victim@example.test", "web login")
	if !g.Banned(ip) {
		t.Fatal("success on another account reset the failure counter")
	}
}

func TestGuardSuccessForgivesOwnTypos(t *testing.T) {
	g, _ := testGuard(5)
	ip := "198.51.100.20"
	for i := 0; i < 4; i++ {
		g.FailAccount(ip, "anna@example.test", "web login")
	}
	g.Success(ip, "ANNA@example.test")
	for i := 0; i < 4; i++ {
		g.FailAccount(ip, "anna@example.test", "web login")
	}
	if g.Banned(ip) {
		t.Fatal("typos followed by a successful login of the same account must be forgiven")
	}
	// Failures without account (setup token) are never forgiven by a login.
	g.Success(ip, "anna@example.test")
	for i := 0; i < 5; i++ {
		g.Fail(ip, "setup token")
	}
	if !g.Banned(ip) {
		t.Fatal("setup token failures must ban")
	}
}

func TestGuardIPv6Aggregation(t *testing.T) {
	g, _ := testGuard(3)
	for i := 1; i <= 3; i++ {
		g.FailAccount(fmt.Sprintf("2001:db8:1:2::%x", i), "x@example.test", "imap auth")
	}
	if !g.Banned("2001:db8:1:2:ffff:ffff:ffff:9") {
		t.Fatal("addresses of the same /64 must share one counter and ban")
	}
	if g.Banned("2001:db8:1:3::1") {
		t.Fatal("another /64 must not be banned")
	}
	list := g.List()
	if len(list) != 1 || list[0].IP != "2001:db8:1:2::/64" {
		t.Fatalf("ban list = %+v", list)
	}
	g.Unban(list[0].IP)
	if g.Banned("2001:db8:1:2::1") {
		t.Fatal("unban by key failed")
	}
	if IPKey("::ffff:192.0.2.1") != "192.0.2.1" || IPKey("192.0.2.1") != "192.0.2.1" {
		t.Fatal("IPv4 (also mapped) must stay a single address")
	}
}

func TestGuardAccountLock(t *testing.T) {
	g, now := testGuard(5)
	acct := "boss@example.test"
	home := "198.51.100.1"
	g.Success(home, acct)
	// A distributed attack: every address fails only once.
	for i := 0; i < 10; i++ {
		g.FailAccount(fmt.Sprintf("203.0.113.%d", i+10), acct, "web login")
	}
	if !g.AccountLocked("192.0.2.200", acct) {
		t.Fatal("account must be locked for unknown clients")
	}
	if g.AccountLocked(home, acct) {
		t.Fatal("a client with a recent successful login must not be locked out")
	}
	if g.AccountLocked("192.0.2.200", "other@example.test") {
		t.Fatal("other accounts are not affected")
	}
	*now = now.Add(2 * time.Minute)
	if g.AccountLocked("192.0.2.200", acct) {
		t.Fatal("the first lock is short")
	}
	// A second wave locks longer.
	for i := 0; i < 10; i++ {
		g.FailAccount(fmt.Sprintf("203.0.113.%d", i+50), acct, "web login")
	}
	*now = now.Add(90 * time.Second)
	if !g.AccountLocked("192.0.2.200", acct) {
		t.Fatal("repeated attacks must lock longer")
	}
}

func TestGuardSweepBoundsState(t *testing.T) {
	g, now := testGuard(5)
	for i := 0; i < 200; i++ {
		g.FailAccount(fmt.Sprintf("10.0.%d.%d", i/250, i%250), fmt.Sprintf("u%d@example.test", i), "web login")
	}
	*now = now.Add(11 * time.Minute)
	g.FailAccount("192.0.2.1", "a@example.test", "web login")
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.clients) != 1 || len(g.accounts) != 1 {
		t.Fatalf("expired failures not swept: clients=%d accounts=%d", len(g.clients), len(g.accounts))
	}
}

func TestGuardBansSurviveRestart(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	g, _ := testGuard(2)
	g.now = time.Now
	g.Store = st
	g.FailAccount("203.0.113.9", "a@example.test", "smtp auth")
	g.FailAccount("203.0.113.9", "a@example.test", "smtp auth")
	if !g.Banned("203.0.113.9") {
		t.Fatal("not banned")
	}

	restarted := NewGuard(func() Limits { return Limits{MaxAttempts: 2, Window: time.Minute, BanFor: time.Hour} })
	restarted.Store = st
	if err := restarted.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !restarted.Banned("203.0.113.9") {
		t.Fatal("ban lost after restart")
	}
	restarted.Unban("203.0.113.9")
	again := NewGuard(nil)
	again.Store = st
	_ = again.Load(context.Background())
	if again.Banned("203.0.113.9") {
		t.Fatal("unban must also remove the persisted ban")
	}
}

// ---- authenticator ----

func testAuthenticator(t *testing.T) (*Authenticator, *store.Account, string) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	pw := "richtig-gutes-passwort"
	h, _ := HashPassword(pw)
	acct := &store.Account{Email: "anna@example.test", PasswordHash: h, Role: "user"}
	if err := st.CreateAccount(context.Background(), acct); err != nil {
		t.Fatal(err)
	}
	g := NewGuard(func() Limits { return Limits{MaxAttempts: 50, Window: time.Minute, BanFor: time.Minute} })
	return &Authenticator{Store: st, Guard: g}, acct, pw
}

func enableTOTP(t *testing.T, a *Authenticator, acct *store.Account) string {
	t.Helper()
	secret := NewTOTPSecret()
	on := true
	if err := a.Store.UpdateAccount(context.Background(), acct.ID, store.AccountPatch{TOTPSecret: &secret, TOTPEnabled: &on}); err != nil {
		t.Fatal(err)
	}
	return secret
}

func TestTOTPReplayRejected(t *testing.T) {
	a, acct, pw := testAuthenticator(t)
	secret := enableTOTP(t, a, acct)
	ctx := context.Background()
	code := totpAt(secret, uint64(time.Now().Unix()/30))
	if _, err := a.Interactive(ctx, "198.51.100.1", acct.Email, pw, code); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if _, err := a.Interactive(ctx, "198.51.100.2", acct.Email, pw, code); !errors.Is(err, ErrInvalid) {
		t.Fatalf("replayed code accepted: %v", err)
	}
	// An older step (previous code) is not accepted either once a newer one was used.
	prev := totpAt(secret, uint64(time.Now().Unix()/30-1))
	fresh, _ := a.Store.GetAccount(ctx, acct.ID)
	if ok, _ := a.VerifyTOTP(ctx, fresh, prev); ok {
		t.Fatal("older code accepted after a newer one")
	}
	// Re-authentication reports the replay explicitly.
	if err := a.Reauth(ctx, "198.51.100.1", fresh, pw, code, true); !errors.Is(err, ErrTOTPReused) {
		t.Fatalf("reauth with reused code: %v", err)
	}
}

func TestRecoveryCodesAreOneTime(t *testing.T) {
	a, acct, pw := testAuthenticator(t)
	enableTOTP(t, a, acct)
	ctx := context.Background()
	codes, hashes := NewRecoveryCodes()
	if len(codes) != RecoveryCodeCount {
		t.Fatalf("got %d codes", len(codes))
	}
	if err := a.Store.ReplaceRecoveryCodes(ctx, acct.ID, hashes); err != nil {
		t.Fatal(err)
	}
	// Case and dashes do not matter.
	if _, err := a.Interactive(ctx, "198.51.100.1", acct.Email, pw, " "+stringsToUpper(codes[3])+" "); err != nil {
		t.Fatalf("recovery code rejected: %v", err)
	}
	if _, err := a.Interactive(ctx, "198.51.100.1", acct.Email, pw, codes[3]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("recovery code reused: %v", err)
	}
	if n, _ := a.Store.CountRecoveryCodes(ctx, acct.ID); n != RecoveryCodeCount-1 {
		t.Fatalf("remaining = %d", n)
	}
	// Regenerating invalidates the old codes.
	_, newHashes := NewRecoveryCodes()
	_ = a.Store.ReplaceRecoveryCodes(ctx, acct.ID, newHashes)
	if _, err := a.Interactive(ctx, "198.51.100.1", acct.Email, pw, codes[4]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("old recovery code still valid: %v", err)
	}
}

func TestProtocolDisabledAccountCountsFailures(t *testing.T) {
	a, acct, pw := testAuthenticator(t)
	a.Guard = NewGuard(func() Limits { return Limits{MaxAttempts: 2, Window: time.Minute, BanFor: time.Minute} })
	ctx := context.Background()
	off := true
	_ = a.Store.UpdateAccount(ctx, acct.ID, store.AccountPatch{Disabled: &off})
	// The correct password reveals the disabled state ...
	if _, err := a.Protocol(ctx, "203.0.113.1", acct.Email, pw, "imap"); !errors.Is(err, ErrAccountLocked) {
		t.Fatalf("correct password on disabled account: %v", err)
	}
	// ... a wrong one looks like any other failure and counts.
	for i := 0; i < 2; i++ {
		if _, err := a.Protocol(ctx, "203.0.113.2", acct.Email, "falsch", "imap"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("wrong password on disabled account: %v", err)
		}
	}
	if !a.Guard.Banned("203.0.113.2") {
		t.Fatal("failures on disabled accounts must count")
	}
	// Unknown users count as well.
	for i := 0; i < 2; i++ {
		_, _ = a.Protocol(ctx, "203.0.113.3", "nobody@example.test", "x", "smtp")
	}
	if !a.Guard.Banned("203.0.113.3") {
		t.Fatal("failures for unknown users must count")
	}
}
