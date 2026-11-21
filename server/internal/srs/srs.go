// Package srs implements the Sender Rewriting Scheme (SRS0/SRS1) so that
// forwarded mail passes SPF at the receiver and bounces find their way back
// to the original sender.
//
// Address formats (separator "=", hash = truncated HMAC-SHA256, TT = day
// stamp in base32, valid for MaxAge days):
//
//	SRS0=HHHHHHHH=TT=orig-domain=orig-local@forward-domain
//	SRS1=HHHHHHHH=srs0-host==HHHHHHHH=TT=orig-domain=orig-local@forward-domain
package srs

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"strings"
	"time"
)

// Defaults used when the Rewriter fields are zero.
const (
	DefaultMaxAge  = 21 // days
	hashLen        = 8
	timePrecision  = 24 * time.Hour
	timeSlots      = 1024 // two base32 characters
	base32Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
)

var (
	ErrNotSRS      = errors.New("srs: not an SRS address")
	ErrMalformed   = errors.New("srs: malformed address")
	ErrBadHash     = errors.New("srs: invalid hash")
	ErrExpired     = errors.New("srs: address expired")
	ErrNoSecret    = errors.New("srs: no secret configured")
	ErrBadAddress  = errors.New("srs: invalid address")
	hashEncoding   = base32.StdEncoding.WithPadding(base32.NoPadding)
	errEmptyDomain = errors.New("srs: empty forward domain")
)

// Rewriter forwards and reverses SRS addresses.
type Rewriter struct {
	Secret []byte
	// MaxAge is the validity in days (default 21).
	MaxAge int
	// Now returns the current time (default time.Now).
	Now func() time.Time
}

func (r *Rewriter) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Rewriter) maxAge() int {
	if r.MaxAge > 0 {
		return r.MaxAge
	}
	return DefaultMaxAge
}

// IsSRS reports whether the local part of addr looks like an SRS address.
func IsSRS(addr string) bool {
	local := addr
	if i := strings.LastIndexByte(addr, '@'); i >= 0 {
		local = addr[:i]
	}
	if len(local) < 5 || !isSep(local[4]) {
		return false
	}
	l := strings.ToUpper(local[:4])
	return l == "SRS0" || l == "SRS1"
}

func split(addr string) (local, domain string, err error) {
	i := strings.LastIndexByte(addr, '@')
	if i <= 0 || i == len(addr)-1 {
		return "", "", ErrBadAddress
	}
	return addr[:i], addr[i+1:], nil
}

func (r *Rewriter) hash(parts ...string) string {
	m := hmac.New(sha256.New, r.Secret)
	for _, p := range parts {
		// Addresses are compared case-insensitively along the way.
		m.Write([]byte(strings.ToLower(p)))
		m.Write([]byte{0})
	}
	return hashEncoding.EncodeToString(m.Sum(nil))[:hashLen]
}

func (r *Rewriter) checkHash(got string, parts ...string) bool {
	want := r.hash(parts...)
	return hmac.Equal([]byte(strings.ToUpper(got)), []byte(want))
}

func (r *Rewriter) stamp() string {
	day := int(r.now().Unix()/int64(timePrecision/time.Second)) % timeSlots
	return string([]byte{base32Alphabet[day>>5], base32Alphabet[day&31]})
}

func (r *Rewriter) checkStamp(ts string) error {
	if len(ts) != 2 {
		return ErrMalformed
	}
	ts = strings.ToUpper(ts)
	hi := strings.IndexByte(base32Alphabet, ts[0])
	lo := strings.IndexByte(base32Alphabet, ts[1])
	if hi < 0 || lo < 0 {
		return ErrMalformed
	}
	then := hi<<5 | lo
	today := int(r.now().Unix()/int64(timePrecision/time.Second)) % timeSlots
	age := (today - then + timeSlots) % timeSlots
	if age > r.maxAge() {
		return ErrExpired
	}
	return nil
}

// Forward rewrites the envelope sender for a message forwarded by domain.
// Null senders are returned unchanged.
func (r *Rewriter) Forward(sender, domain string) (string, error) {
	if sender == "" || sender == "<>" {
		return sender, nil
	}
	if len(r.Secret) == 0 {
		return "", ErrNoSecret
	}
	if domain == "" {
		return "", errEmptyDomain
	}
	local, host, err := split(sender)
	if err != nil {
		return "", err
	}
	if strings.EqualFold(host, domain) {
		return sender, nil
	}
	upper := strings.ToUpper(local)
	switch {
	case len(local) > 5 && strings.HasPrefix(upper, "SRS0") && isSep(local[4]):
		// Already rewritten once: wrap as SRS1 pointing at the first forwarder.
		opaque := local[5:]
		h := r.hash(host, opaque)
		return "SRS1=" + h + "=" + host + "==" + opaque + "@" + domain, nil
	case len(local) > 5 && strings.HasPrefix(upper, "SRS1") && isSep(local[4]):
		// SRS1=hash=srs0host==opaque: keep srs0host and opaque, rehash.
		parts := strings.SplitN(local[5:], "=", 3)
		if len(parts) == 3 && parts[1] != "" {
			srs0host, opaque := parts[1], parts[2]
			if len(opaque) > 0 && isSep(opaque[0]) {
				opaque = opaque[1:]
			}
			h := r.hash(srs0host, opaque)
			return "SRS1=" + h + "=" + srs0host + "==" + opaque + "@" + domain, nil
		}
	}
	ts := r.stamp()
	h := r.hash(ts, host, local)
	return "SRS0=" + h + "=" + ts + "=" + host + "=" + local + "@" + domain, nil
}

func isSep(c byte) bool { return c == '=' || c == '-' || c == '+' }

// Reverse validates an SRS address and returns the address the bounce must
// be sent to: the original sender for SRS0, the first forwarder's SRS0
// address for SRS1.
func (r *Rewriter) Reverse(addr string) (string, error) {
	local, _, err := split(addr)
	if err != nil {
		return "", err
	}
	if !IsSRS(addr) {
		return "", ErrNotSRS
	}
	if len(r.Secret) == 0 {
		return "", ErrNoSecret
	}
	kind := strings.ToUpper(local[:4])
	rest := local[5:]
	switch kind {
	case "SRS0":
		// HHH=TT=domain=local
		parts := strings.SplitN(rest, "=", 4)
		if len(parts) != 4 || parts[0] == "" || parts[2] == "" || parts[3] == "" {
			return "", ErrMalformed
		}
		h, ts, host, orig := parts[0], parts[1], parts[2], parts[3]
		if !r.checkHash(h, ts, host, orig) {
			return "", ErrBadHash
		}
		if err := r.checkStamp(ts); err != nil {
			return "", err
		}
		return orig + "@" + host, nil
	case "SRS1":
		// HHH=srs0host==opaque
		parts := strings.SplitN(rest, "=", 3)
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || len(parts[2]) < 2 || !isSep(parts[2][0]) {
			return "", ErrMalformed
		}
		h, srs0host, opaque := parts[0], parts[1], parts[2][1:]
		if !r.checkHash(h, srs0host, opaque) {
			return "", ErrBadHash
		}
		return "SRS0=" + opaque + "@" + srs0host, nil
	}
	return "", ErrNotSRS
}
