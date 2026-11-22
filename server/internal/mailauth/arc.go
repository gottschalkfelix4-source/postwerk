package mailauth

// ARC (Authenticated Received Chain, RFC 8617): validation of the chain on
// inbound mail and sealing of forwarded mail. go-msgauth has no ARC support
// and does not export its canonicalization, so the DKIM building blocks
// (RFC 6376 canonicalization, tag lists, key records) are implemented here.

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MaxARCInstances is the highest ARC instance number allowed (RFC 8617 4.2.1).
const MaxARCInstances = 50

// Errors returned by SealARC.
var (
	ErrARCLimit       = errors.New("arc: chain has reached the maximum number of sets")
	ErrARCChainFailed = errors.New("arc: chain already failed upstream")
)

// ARCSet describes one validated ARC set.
type ARCSet struct {
	Instance int `json:"instance"`
	// SealDomain is the d= of the ARC-Seal (the sealing organization).
	SealDomain string `json:"sealDomain"`
	AMSDomain  string `json:"amsDomain"`
	CV         string `json:"cv"`
	// AuthResults is the payload of the ARC-Authentication-Results header
	// after the instance tag ("authserv-id; method=result ...").
	AuthResults string `json:"authResults"`
}

// ARCResult is the outcome of an ARC chain validation.
type ARCResult struct {
	Result string   `json:"result"` // none, pass, fail
	Reason string   `json:"reason,omitempty"`
	Sets   []ARCSet `json:"sets,omitempty"`
}

// ---- message and header handling ----

type headerField struct {
	raw string // complete field including folding and the trailing CRLF
	key string // lower-case field name
}

// normalizeCRLF converts bare LF line endings to CRLF.
func normalizeCRLF(raw []byte) []byte {
	bare := false
	for i, c := range raw {
		if c == '\n' && (i == 0 || raw[i-1] != '\r') {
			bare = true
			break
		}
	}
	if !bare {
		return raw
	}
	out := make([]byte, 0, len(raw)+64)
	for i, c := range raw {
		if c == '\n' && (i == 0 || raw[i-1] != '\r') {
			out = append(out, '\r')
		}
		out = append(out, c)
	}
	return out
}

// splitMessage splits a CRLF message into header fields and body.
func splitMessage(raw []byte) ([]headerField, []byte) {
	var fields []headerField
	rest := raw
	for len(rest) > 0 {
		end := bytes.Index(rest, []byte("\r\n"))
		var line []byte
		if end < 0 {
			line, rest = rest, nil
		} else {
			line, rest = rest[:end+2], rest[end+2:]
		}
		if len(line) == 2 && end == 0 {
			return fields, rest // empty line: end of header
		}
		if (line[0] == ' ' || line[0] == '\t') && len(fields) > 0 {
			fields[len(fields)-1].raw += string(line)
			continue
		}
		f := headerField{raw: string(line)}
		if i := strings.IndexByte(f.raw, ':'); i > 0 {
			f.key = strings.ToLower(strings.TrimRight(f.raw[:i], " \t"))
		}
		fields = append(fields, f)
	}
	return fields, nil
}

// fieldValue returns the value of a header field (after the colon, without
// the trailing CRLF, folding kept).
func fieldValue(raw string) string {
	i := strings.IndexByte(raw, ':')
	if i < 0 {
		return ""
	}
	return strings.TrimSuffix(raw[i+1:], "\r\n")
}

func isWSP(c byte) bool { return c == ' ' || c == '\t' }

// canonHeaderRelaxed implements the "relaxed" header canonicalization of
// RFC 6376 3.4.2. The result ends with CRLF.
func canonHeaderRelaxed(raw string) string {
	i := strings.IndexByte(raw, ':')
	if i < 0 {
		return ""
	}
	name := strings.ToLower(strings.TrimRight(raw[:i], " \t"))
	v := strings.ReplaceAll(raw[i+1:], "\r\n", "")
	var b strings.Builder
	b.Grow(len(name) + len(v) + 3)
	b.WriteString(name)
	b.WriteByte(':')
	val := make([]byte, 0, len(v))
	wsp := false
	for j := 0; j < len(v); j++ {
		if isWSP(v[j]) {
			wsp = true
			continue
		}
		if wsp && len(val) > 0 {
			val = append(val, ' ')
		}
		wsp = false
		val = append(val, v[j])
	}
	b.Write(val)
	b.WriteString("\r\n")
	return b.String()
}

func canonHeader(raw string, relaxed bool) string {
	if relaxed {
		return canonHeaderRelaxed(raw)
	}
	if !strings.HasSuffix(raw, "\r\n") {
		raw += "\r\n"
	}
	return raw
}

// canonBody implements the "simple" and "relaxed" body canonicalizations of
// RFC 6376 3.4.3 and 3.4.4.
func canonBody(body []byte, relaxed bool) []byte {
	lines := strings.Split(string(body), "\r\n")
	if relaxed {
		for i, l := range lines {
			var b strings.Builder
			wsp := false
			for j := 0; j < len(l); j++ {
				if isWSP(l[j]) {
					wsp = true
					continue
				}
				if wsp {
					b.WriteByte(' ')
				}
				wsp = false
				b.WriteByte(l[j])
			}
			lines[i] = b.String()
		}
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		if relaxed {
			return nil
		}
		return []byte("\r\n")
	}
	return []byte(strings.Join(lines, "\r\n") + "\r\n")
}

func bodyHash(body []byte, relaxed bool, limit int64) []byte {
	c := canonBody(body, relaxed)
	if limit >= 0 && int64(len(c)) > limit {
		c = c[:limit]
	}
	h := sha256.Sum256(c)
	return h[:]
}

// parseTags parses a DKIM-style tag list (RFC 6376 3.2).
func parseTags(v string) (map[string]string, error) {
	m := map[string]string{}
	for _, part := range strings.Split(v, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, val, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("malformed tag %q", part)
		}
		k = strings.TrimSpace(k)
		if k == "" {
			return nil, errors.New("empty tag name")
		}
		if _, dup := m[k]; dup {
			return nil, fmt.Errorf("duplicate tag %q", k)
		}
		m[k] = strings.TrimSpace(val)
	}
	return m, nil
}

func stripWSP(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, s)
}

// stripSignature empties the value of the b= tag of a signature header
// field, keeping everything else as is (RFC 6376 3.7).
func stripSignature(raw string) string {
	i := strings.IndexByte(raw, ':')
	if i < 0 {
		return raw
	}
	parts := strings.Split(raw[i+1:], ";")
	for j, p := range parts {
		eq := strings.IndexByte(p, '=')
		if eq < 0 || strings.TrimSpace(p[:eq]) != "b" {
			continue
		}
		suffix := ""
		if strings.HasSuffix(p, "\r\n") {
			suffix = "\r\n"
		}
		parts[j] = p[:eq+1] + suffix
	}
	return raw[:i+1] + strings.Join(parts, ";")
}

// pickHeaders returns the canonicalized header fields named in h, selecting
// instances bottom-up as DKIM does (RFC 6376 5.4.2).
func pickHeaders(fields []headerField, names []string, relaxed bool) string {
	used := map[int]bool{}
	var b strings.Builder
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		for i := len(fields) - 1; i >= 0; i-- {
			if used[i] || fields[i].key != n {
				continue
			}
			used[i] = true
			b.WriteString(canonHeader(fields[i].raw, relaxed))
			break
		}
	}
	return b.String()
}

// ---- keys ----

// lookupPublicKey fetches the public key of a DKIM/ARC selector from DNS.
func lookupPublicKey(ctx context.Context, domain, selector string) (crypto.PublicKey, error) {
	name := selector + "._domainkey." + strings.TrimSuffix(domain, ".")
	txts, err := lookupTXT(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("key lookup %s: %w", name, err)
	}
	if len(txts) == 0 {
		return nil, fmt.Errorf("no key record at %s", name)
	}
	tags, err := parseTags(strings.Join(txts, ""))
	if len(txts) > 1 {
		// Several records: use the first parseable one.
		for _, t := range txts {
			if tg, e := parseTags(t); e == nil && tg["p"] != "" {
				tags, err = tg, nil
				break
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("key record %s: %w", name, err)
	}
	if v, ok := tags["v"]; ok && v != "DKIM1" {
		return nil, fmt.Errorf("key record %s: unsupported version", name)
	}
	p := stripWSP(tags["p"])
	if p == "" {
		return nil, fmt.Errorf("key %s revoked", name)
	}
	data, err := base64.StdEncoding.DecodeString(p)
	if err != nil {
		return nil, fmt.Errorf("key record %s: %w", name, err)
	}
	switch k := strings.ToLower(tags["k"]); k {
	case "", "rsa":
		pub, err := x509.ParsePKIXPublicKey(data)
		if err != nil {
			if rk, e := x509.ParsePKCS1PublicKey(data); e == nil {
				pub, err = rk, nil
			}
		}
		if err != nil {
			return nil, fmt.Errorf("key record %s: %w", name, err)
		}
		rk, ok := pub.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("key record %s: not an RSA key", name)
		}
		if rk.N.BitLen() < 1024 {
			return nil, fmt.Errorf("key %s too short", name)
		}
		return rk, nil
	case "ed25519":
		if len(data) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("key record %s: invalid ed25519 key", name)
		}
		return ed25519.PublicKey(data), nil
	default:
		return nil, fmt.Errorf("key record %s: unsupported key type %q", name, k)
	}
}

func verifyDigest(pub crypto.PublicKey, alg string, digest, sig []byte) error {
	switch k := pub.(type) {
	case *rsa.PublicKey:
		if alg != "rsa-sha256" {
			return fmt.Errorf("algorithm %s does not match rsa key", alg)
		}
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, digest, sig)
	case ed25519.PublicKey:
		if alg != "ed25519-sha256" {
			return fmt.Errorf("algorithm %s does not match ed25519 key", alg)
		}
		if !ed25519.Verify(k, digest, sig) {
			return errors.New("ed25519 signature mismatch")
		}
		return nil
	}
	return errors.New("unsupported key")
}

func signDigest(signer crypto.Signer, digest []byte) ([]byte, string, error) {
	if _, ok := signer.(ed25519.PrivateKey); ok {
		sig, err := signer.Sign(rand.Reader, digest, crypto.Hash(0))
		return sig, "ed25519-sha256", err
	}
	sig, err := signer.Sign(rand.Reader, digest, crypto.SHA256)
	return sig, "rsa-sha256", err
}

// ---- validation ----

type arcRawSet struct {
	aar, ams, as string // raw header fields
	asTags       map[string]string
	amsTags      map[string]string
}

// arcInstance extracts the instance number of an ARC header field.
func arcInstance(f headerField) (int, error) {
	v := strings.TrimSpace(fieldValue(f.raw))
	if f.key == "arc-authentication-results" {
		head, _, _ := strings.Cut(v, ";")
		k, n, ok := strings.Cut(head, "=")
		if !ok || strings.TrimSpace(k) != "i" {
			return 0, errors.New("missing instance")
		}
		return strconv.Atoi(strings.TrimSpace(n))
	}
	tags, err := parseTags(v)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(tags["i"])
}

// aarPayload returns the ARC-Authentication-Results value after "i=N;".
func aarPayload(raw string) string {
	v := strings.Join(strings.Fields(strings.ReplaceAll(fieldValue(raw), "\r\n", " ")), " ")
	if _, rest, ok := strings.Cut(v, ";"); ok {
		return strings.TrimSpace(rest)
	}
	return ""
}

// collectARC groups the ARC header fields by instance. It returns the sets
// (index 0 = instance 1) or an error describing a structural problem.
func collectARC(fields []headerField) ([]*arcRawSet, error) {
	byInst := map[int]*arcRawSet{}
	max := 0
	for _, f := range fields {
		switch f.key {
		case "arc-seal", "arc-message-signature", "arc-authentication-results":
		default:
			continue
		}
		i, err := arcInstance(f)
		if err != nil || i < 1 || i > MaxARCInstances {
			return nil, fmt.Errorf("invalid instance in %s", f.key)
		}
		set := byInst[i]
		if set == nil {
			set = &arcRawSet{}
			byInst[i] = set
		}
		var slot *string
		switch f.key {
		case "arc-seal":
			slot = &set.as
		case "arc-message-signature":
			slot = &set.ams
		default:
			slot = &set.aar
		}
		if *slot != "" {
			return nil, fmt.Errorf("duplicate %s for i=%d", f.key, i)
		}
		*slot = f.raw
		if i > max {
			max = i
		}
	}
	if max == 0 {
		return nil, nil
	}
	sets := make([]*arcRawSet, max)
	for i := 1; i <= max; i++ {
		set := byInst[i]
		if set == nil || set.aar == "" || set.ams == "" || set.as == "" {
			return nil, fmt.Errorf("incomplete ARC set i=%d", i)
		}
		var err error
		if set.asTags, err = parseTags(fieldValue(set.as)); err != nil {
			return nil, fmt.Errorf("arc-seal i=%d: %v", i, err)
		}
		if set.amsTags, err = parseTags(fieldValue(set.ams)); err != nil {
			return nil, fmt.Errorf("arc-message-signature i=%d: %v", i, err)
		}
		sets[i-1] = set
	}
	return sets, nil
}

// VerifyARC validates the ARC chain of a message (RFC 8617 5.2).
func VerifyARC(ctx context.Context, raw []byte) ARCResult {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	fields, body := splitMessage(normalizeCRLF(raw))
	sets, err := collectARC(fields)
	if err != nil {
		return ARCResult{Result: "fail", Reason: err.Error()}
	}
	if len(sets) == 0 {
		return ARCResult{Result: "none"}
	}
	res := ARCResult{Result: "fail"}
	for i, s := range sets {
		res.Sets = append(res.Sets, ARCSet{
			Instance:    i + 1,
			SealDomain:  strings.ToLower(s.asTags["d"]),
			AMSDomain:   strings.ToLower(s.amsTags["d"]),
			CV:          strings.ToLower(s.asTags["cv"]),
			AuthResults: aarPayload(s.aar),
		})
	}
	n := len(sets)
	if res.Sets[n-1].CV == "fail" {
		res.Reason = fmt.Sprintf("i=%d: chain marked as failed", n)
		return res
	}
	for i, s := range res.Sets {
		want := "pass"
		if i == 0 {
			want = "none"
		}
		if s.CV != want {
			res.Reason = fmt.Sprintf("i=%d: unexpected cv=%s", i+1, s.CV)
			return res
		}
	}
	// The most recent AMS has to validate (older ones may legitimately be
	// broken by intermediaries that changed the message).
	if err := verifyAMS(ctx, fields, body, sets[n-1]); err != nil {
		res.Reason = fmt.Sprintf("ams i=%d: %v", n, err)
		return res
	}
	for i := n; i >= 1; i-- {
		if err := verifySeal(ctx, sets, i); err != nil {
			res.Reason = fmt.Sprintf("seal i=%d: %v", i, err)
			return res
		}
	}
	res.Result = "pass"
	return res
}

func verifyAMS(ctx context.Context, fields []headerField, body []byte, set *arcRawSet) error {
	t := set.amsTags
	alg := strings.ToLower(t["a"])
	if alg != "rsa-sha256" && alg != "ed25519-sha256" {
		return fmt.Errorf("unsupported algorithm %q", t["a"])
	}
	hc, bc, _ := strings.Cut(strings.ToLower(t["c"]), "/")
	if hc == "" {
		hc = "simple"
	}
	if bc == "" {
		bc = "simple"
	}
	if (hc != "simple" && hc != "relaxed") || (bc != "simple" && bc != "relaxed") {
		return fmt.Errorf("unsupported canonicalization %q", t["c"])
	}
	if t["d"] == "" || t["s"] == "" || t["h"] == "" || t["bh"] == "" || t["b"] == "" {
		return errors.New("missing required tag")
	}
	names := strings.Split(t["h"], ":")
	for _, n := range names {
		if strings.EqualFold(strings.TrimSpace(n), "arc-seal") {
			return errors.New("h= must not include ARC-Seal")
		}
	}
	limit := int64(-1)
	if l, ok := t["l"]; ok {
		v, err := strconv.ParseInt(l, 10, 64)
		if err != nil || v < 0 {
			return errors.New("invalid l= tag")
		}
		limit = v
	}
	wantBH, err := base64.StdEncoding.DecodeString(stripWSP(t["bh"]))
	if err != nil {
		return errors.New("invalid bh= tag")
	}
	if !bytes.Equal(wantBH, bodyHash(body, bc == "relaxed", limit)) {
		return errors.New("body hash mismatch")
	}
	sig, err := base64.StdEncoding.DecodeString(stripWSP(t["b"]))
	if err != nil {
		return errors.New("invalid b= tag")
	}
	relaxed := hc == "relaxed"
	data := pickHeaders(fields, names, relaxed) +
		strings.TrimSuffix(canonHeader(stripSignature(set.ams), relaxed), "\r\n")
	digest := sha256.Sum256([]byte(data))
	pub, err := lookupPublicKey(ctx, t["d"], t["s"])
	if err != nil {
		return err
	}
	return verifyDigest(pub, alg, digest[:], sig)
}

// sealData builds the input of the ARC-Seal signature for instance i: all
// sets up to i in increasing order, each as AAR, AMS, AS, with the b= value
// of the final ARC-Seal removed (RFC 8617 5.1.1). Only relaxed header
// canonicalization is used.
func sealData(aars, amss, ass []string, i int) string {
	var b strings.Builder
	for j := 0; j < i; j++ {
		b.WriteString(canonHeaderRelaxed(aars[j]))
		b.WriteString(canonHeaderRelaxed(amss[j]))
		if j == i-1 {
			b.WriteString(strings.TrimSuffix(canonHeaderRelaxed(stripSignature(ass[j])), "\r\n"))
		} else {
			b.WriteString(canonHeaderRelaxed(ass[j]))
		}
	}
	return b.String()
}

func verifySeal(ctx context.Context, sets []*arcRawSet, i int) error {
	set := sets[i-1]
	t := set.asTags
	alg := strings.ToLower(t["a"])
	if alg != "rsa-sha256" && alg != "ed25519-sha256" {
		return fmt.Errorf("unsupported algorithm %q", t["a"])
	}
	if _, ok := t["h"]; ok {
		return errors.New("h= is not allowed in ARC-Seal")
	}
	if t["d"] == "" || t["s"] == "" || t["b"] == "" {
		return errors.New("missing required tag")
	}
	sig, err := base64.StdEncoding.DecodeString(stripWSP(t["b"]))
	if err != nil {
		return errors.New("invalid b= tag")
	}
	aars, amss, ass := make([]string, i), make([]string, i), make([]string, i)
	for j := 0; j < i; j++ {
		aars[j], amss[j], ass[j] = sets[j].aar, sets[j].ams, sets[j].as
	}
	digest := sha256.Sum256([]byte(sealData(aars, amss, ass, i)))
	pub, err := lookupPublicKey(ctx, t["d"], t["s"])
	if err != nil {
		return err
	}
	return verifyDigest(pub, alg, digest[:], sig)
}

// ---- sealing ----

// ARCSealOptions configure SealARC.
type ARCSealOptions struct {
	Domain     string
	Selector   string
	PrivateKey string // PEM; RSA keys are recommended for ARC
	AuthServID string
	// AuthResults are the authentication results of this hop
	// ("spf=pass smtp.mailfrom=...; dkim=...; dmarc=...").
	AuthResults string
	// ChainValidation is the result of validating the incoming chain:
	// none, pass or fail.
	ChainValidation string
	// Headers overrides the header fields signed by the
	// ARC-Message-Signature (default: ARCSignedHeaders that are present).
	Headers []string
	Time    time.Time
}

// ARCSignedHeaders are the header fields covered by our
// ARC-Message-Signature when present in the message.
var ARCSignedHeaders = []string{
	"From", "To", "Cc", "Subject", "Date", "Message-ID", "Reply-To", "In-Reply-To", "References",
	"MIME-Version", "Content-Type", "Content-Transfer-Encoding", "DKIM-Signature",
}

// foldBase64 splits a base64 value into folded lines.
func foldBase64(s string) string {
	var b strings.Builder
	for len(s) > 64 {
		b.WriteString(s[:64])
		b.WriteString("\r\n\t ")
		s = s[64:]
	}
	b.WriteString(s)
	return b.String()
}

// SealARC adds an ARC set (ARC-Authentication-Results, ARC-Message-Signature
// and ARC-Seal) with instance n+1 to raw.
func SealARC(raw []byte, o ARCSealOptions) ([]byte, error) {
	signer, _, err := parseSigner(o.PrivateKey)
	if err != nil {
		return nil, err
	}
	if o.Domain == "" || o.Selector == "" {
		return nil, errors.New("arc: domain and selector required")
	}
	raw = normalizeCRLF(raw)
	fields, body := splitMessage(raw)

	// Existing chain. A structurally broken chain counts as failed.
	sets, structErr := collectARC(fields)
	i := len(sets) + 1
	if structErr != nil {
		i = 0
		for _, f := range fields {
			if f.key == "arc-seal" {
				if n, err := arcInstance(f); err == nil && n > i-1 {
					i = n + 1
				}
			}
		}
		if i == 0 {
			i = 1
		}
	}
	if i > MaxARCInstances {
		return nil, ErrARCLimit
	}
	cv := "none"
	if i > 1 {
		cv = "fail"
		if structErr == nil && o.ChainValidation == "pass" {
			cv = "pass"
		}
		if structErr == nil && strings.EqualFold(sets[len(sets)-1].asTags["cv"], "fail") {
			return nil, ErrARCChainFailed
		}
	}
	ts := o.Time
	if ts.IsZero() {
		ts = time.Now()
	}
	authserv := o.AuthServID
	if authserv == "" {
		authserv = o.Domain
	}
	results := strings.TrimSpace(o.AuthResults)
	if results == "" {
		results = "none"
	}
	aar := fmt.Sprintf("ARC-Authentication-Results: i=%d; %s;\r\n\t%s\r\n", i, authserv, results)

	// ARC-Message-Signature over the message.
	names := o.Headers
	if len(names) == 0 {
		present := map[string]bool{}
		for _, f := range fields {
			present[f.key] = true
		}
		for _, h := range ARCSignedHeaders {
			if present[strings.ToLower(h)] {
				names = append(names, h)
			}
		}
	}
	bh := base64.StdEncoding.EncodeToString(bodyHash(body, true, -1))
	alg := "rsa-sha256"
	if _, ok := signer.(ed25519.PrivateKey); ok {
		alg = "ed25519-sha256"
	}
	amsHead := fmt.Sprintf("ARC-Message-Signature: i=%d; a=%s; c=relaxed/relaxed; d=%s;\r\n\ts=%s; t=%d;\r\n\th=%s;\r\n\tbh=%s;\r\n\tb=",
		i, alg, o.Domain, o.Selector, ts.Unix(), strings.Join(names, ":"), bh)
	data := pickHeaders(fields, names, true) + strings.TrimSuffix(canonHeaderRelaxed(amsHead), "\r\n")
	digest := sha256.Sum256([]byte(data))
	sig, _, err := signDigest(signer, digest[:])
	if err != nil {
		return nil, err
	}
	ams := amsHead + foldBase64(base64.StdEncoding.EncodeToString(sig)) + "\r\n"

	// ARC-Seal over all sets including the new one.
	asHead := fmt.Sprintf("ARC-Seal: i=%d; a=%s; t=%d; cv=%s;\r\n\td=%s; s=%s;\r\n\tb=", i, alg, ts.Unix(), cv, o.Domain, o.Selector)
	aars, amss, ass := make([]string, i), make([]string, i), make([]string, i)
	if structErr == nil {
		for j, s := range sets {
			aars[j], amss[j], ass[j] = s.aar, s.ams, s.as
		}
	}
	aars[i-1], amss[i-1], ass[i-1] = aar, ams, asHead
	var sealInput string
	if structErr == nil {
		sealInput = sealData(aars, amss, ass, i)
	} else {
		// The broken chain cannot be serialized; the seal covers only the
		// new set (the chain is failed for every verifier anyway).
		sealInput = sealData(aars[i-1:], amss[i-1:], ass[i-1:], 1)
	}
	digest = sha256.Sum256([]byte(sealInput))
	sig, _, err = signDigest(signer, digest[:])
	if err != nil {
		return nil, err
	}
	as := asHead + foldBase64(base64.StdEncoding.EncodeToString(sig)) + "\r\n"

	out := make([]byte, 0, len(as)+len(ams)+len(aar)+len(raw))
	out = append(out, as...)
	out = append(out, ams...)
	out = append(out, aar...)
	return append(out, raw...), nil
}

// ---- local policy ----

// aarResults parses the method results of an (ARC-)Authentication-Results
// payload ("authserv-id; method=result prop=value ...") into
// method -> result plus the properties of each method.
func aarResults(payload string) []aarEntry {
	var out []aarEntry
	parts := strings.Split(payload, ";")
	for idx, p := range parts {
		if idx == 0 {
			continue // authserv-id
		}
		p = stripComments(p)
		f := strings.Fields(p)
		if len(f) == 0 {
			continue
		}
		m, r, ok := strings.Cut(f[0], "=")
		if !ok {
			continue
		}
		e := aarEntry{method: strings.ToLower(m), result: strings.ToLower(r), props: map[string]string{}}
		for _, kv := range f[1:] {
			if k, v, ok := strings.Cut(kv, "="); ok {
				e.props[strings.ToLower(k)] = strings.ToLower(strings.Trim(v, `"`))
			}
		}
		out = append(out, e)
	}
	return out
}

type aarEntry struct {
	method, result string
	props          map[string]string
}

func stripComments(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '(':
			depth++
		case r == ')' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// aarVouchesFor reports whether an ARC-Authentication-Results payload
// shows that the message was authenticated for fromDomain at that hop:
// DMARC passed for the domain, or an aligned DKIM signature validated.
func aarVouchesFor(payload, fromDomain string) bool {
	for _, e := range aarResults(payload) {
		if e.result != "pass" {
			continue
		}
		switch e.method {
		case "dmarc":
			// A dmarc=pass without header.from vouches for nothing.
			if hf := e.props["header.from"]; hf != "" && strings.EqualFold(hf, fromDomain) {
				return true
			}
		case "dkim":
			if d := e.props["header.d"]; d != "" && orgDomain(d) == orgDomain(fromDomain) {
				return true
			}
		}
	}
	return false
}

// ARCOverride decides whether a DMARC failure may be relaxed because of a
// valid ARC chain sealed by a trusted forwarder (RFC 7489 local policy,
// RFC 8617 7.2). It returns:
//   - "accept" when a trusted sealer recorded a passing authentication for
//     the From domain,
//   - "quarantine" when the chain was sealed by a trusted forwarder but its
//     results do not vouch for the domain (a reject becomes quarantine),
//   - "" when no override applies;
//
// plus the trusted sealing domain.
func (r Result) ARCOverride(trusted []string) (string, string) {
	if r.DMARC != "fail" || r.ARC != "pass" || len(trusted) == 0 {
		return "", ""
	}
	sealer := ""
	for i := len(r.ARCChain.Sets) - 1; i >= 0; i-- {
		s := r.ARCChain.Sets[i]
		ok := false
		for _, t := range trusted {
			if within(s.SealDomain, strings.TrimSpace(t)) {
				ok = true
				break
			}
		}
		if !ok {
			continue
		}
		if aarVouchesFor(s.AuthResults, r.FromDomain) {
			return "accept", s.SealDomain
		}
		if sealer == "" {
			sealer = s.SealDomain
		}
	}
	if sealer != "" {
		return "quarantine", sealer
	}
	return "", ""
}
