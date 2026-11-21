package srs

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func fixed(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestForwardReverseSRS0(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	r := &Rewriter{Secret: []byte("geheim"), Now: fixed(now)}
	got, err := r.Forward("alice@sender.test", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "SRS0=") || !strings.HasSuffix(got, "=sender.test=alice@example.test") {
		t.Fatalf("forward = %q", got)
	}
	if !IsSRS(got) {
		t.Fatal("IsSRS false")
	}
	back, err := r.Reverse(got)
	if err != nil || back != "alice@sender.test" {
		t.Fatalf("reverse = %q, %v", back, err)
	}
	// Case changes by intermediate MTAs must not break validation.
	if back, err := r.Reverse(strings.ToLower(got)); err != nil || back != "alice@sender.test" {
		t.Fatalf("reverse lowercase = %q, %v", back, err)
	}
}

func TestReverseExpiry(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	r := &Rewriter{Secret: []byte("geheim"), Now: fixed(now)}
	addr, _ := r.Forward("bob@sender.test", "example.test")

	r.Now = fixed(now.Add(20 * 24 * time.Hour))
	if _, err := r.Reverse(addr); err != nil {
		t.Fatalf("20 days old should be valid: %v", err)
	}
	r.Now = fixed(now.Add(22 * 24 * time.Hour))
	if _, err := r.Reverse(addr); !errors.Is(err, ErrExpired) {
		t.Fatalf("22 days old: %v", err)
	}
}

func TestReverseRejectsForgery(t *testing.T) {
	r := &Rewriter{Secret: []byte("geheim")}
	addr, _ := r.Forward("bob@sender.test", "example.test")
	// Tamper with the original local part.
	forged := strings.Replace(addr, "=bob@", "=mallory@", 1)
	if _, err := r.Reverse(forged); !errors.Is(err, ErrBadHash) {
		t.Fatalf("forged: %v", err)
	}
	other := &Rewriter{Secret: []byte("anderes")}
	if _, err := other.Reverse(addr); !errors.Is(err, ErrBadHash) {
		t.Fatalf("wrong secret: %v", err)
	}
	for _, bad := range []string{"SRS0=abc@example.test", "SRS0==XX=d=l@example.test", "SRS1=x@example.test", "nobody@example.test"} {
		if _, err := r.Reverse(bad); err == nil {
			t.Errorf("Reverse(%q) should fail", bad)
		}
	}
	if _, err := (&Rewriter{}).Forward("a@b.test", "c.test"); !errors.Is(err, ErrNoSecret) {
		t.Fatalf("no secret: %v", err)
	}
}

func TestSRS1Chain(t *testing.T) {
	first := &Rewriter{Secret: []byte("erster")}
	srs0, _ := first.Forward("carol@origin.test", "hop1.test")

	second := &Rewriter{Secret: []byte("zweiter")}
	srs1, err := second.Forward(srs0, "hop2.test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(srs1, "SRS1=") || !strings.Contains(srs1, "=hop1.test==") || !strings.HasSuffix(srs1, "@hop2.test") {
		t.Fatalf("srs1 = %q", srs1)
	}
	back, err := second.Reverse(srs1)
	if err != nil || back != srs0 {
		t.Fatalf("reverse srs1 = %q, %v (want %q)", back, err, srs0)
	}
	if orig, err := first.Reverse(back); err != nil || orig != "carol@origin.test" {
		t.Fatalf("reverse srs0 = %q, %v", orig, err)
	}

	// A third hop rewrites SRS1 again, still pointing at hop1.
	third := &Rewriter{Secret: []byte("dritter")}
	again, err := third.Forward(srs1, "hop3.test")
	if err != nil || !strings.Contains(again, "=hop1.test==") || !strings.HasSuffix(again, "@hop3.test") {
		t.Fatalf("third hop = %q, %v", again, err)
	}
	if back, err := third.Reverse(again); err != nil || back != srs0 {
		t.Fatalf("third reverse = %q, %v", back, err)
	}
}

func TestForwardSpecialCases(t *testing.T) {
	r := &Rewriter{Secret: []byte("geheim")}
	if got, _ := r.Forward("", "example.test"); got != "" {
		t.Fatalf("null sender rewritten: %q", got)
	}
	if got, _ := r.Forward("<>", "example.test"); got != "<>" {
		t.Fatalf("null sender rewritten: %q", got)
	}
	if got, _ := r.Forward("me@example.test", "example.test"); got != "me@example.test" {
		t.Fatalf("same domain rewritten: %q", got)
	}
	if _, err := r.Forward("kaputt", "example.test"); err == nil {
		t.Fatal("invalid sender accepted")
	}
}
