package auth

import (
	"testing"
	"time"
)

func TestTOTPKnownVector(t *testing.T) {
	// RFC 6238 test secret "12345678901234567890" in base32, T = 59 s -> counter 1.
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	if got := totpAt(secret, 1); got != "287082" {
		t.Fatalf("totp = %s, want 287082", got)
	}
}

func TestTOTPValidate(t *testing.T) {
	s := NewTOTPSecret()
	code := totpAt(s, uint64(time.Now().Unix()/30))
	if !ValidateTOTP(s, code) {
		t.Fatal("current code rejected")
	}
	if ValidateTOTP(s, "000000") && code != "000000" {
		t.Fatal("wrong code accepted")
	}
}

func TestAppPasswordNormalization(t *testing.T) {
	p := NewAppPassword()
	if NormalizeAppPassword(p) != NormalizeAppPassword(" "+stringsToUpper(p)+" ") {
		t.Fatal("normalization should ignore case and spaces")
	}
}

func stringsToUpper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}
