// Package message parses and builds RFC 5322 / MIME messages.
package message

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/quotedprintable"
	netmail "net/mail"
	"regexp"
	"strings"
	"time"

	_ "github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/mail"

	"postwerk/internal/store"
)

type Attachment struct {
	ID          int64  `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	Data        []byte `json:"-"`
}

type Parsed struct {
	Subject     string
	From        store.Address
	To, Cc      []store.Address
	ReplyTo     []store.Address
	MessageID   string
	InReplyTo   string
	References  []string
	Date        time.Time
	Text        string
	HTML        string
	Attachments []Attachment
	Header      mail.Header
	// FromAll holds every address of the From header; FromErr is set when
	// the header is missing, duplicated or cannot be parsed.
	FromAll []store.Address
	FromErr error
}

// Errors returned by FromAddresses.
var (
	ErrFromMissing   = errors.New("missing From header")
	ErrFromMultiple  = errors.New("multiple From headers")
	ErrFromMalformed = errors.New("malformed From header")
)

// FromAddresses returns the addresses of the single From header field. It
// fails when the field is missing, occurs more than once, cannot be parsed or
// contains no address.
func FromAddresses(h mail.Header) ([]store.Address, error) {
	return singleAddrField(h, "From", ErrFromMissing, ErrFromMultiple)
}

// SenderAddress returns the Sender header address, or nil when the message
// has no Sender header. A duplicated, unparseable or multi-address Sender
// header is an error.
func SenderAddress(h mail.Header) (*store.Address, error) {
	if !h.Has("Sender") {
		return nil, nil
	}
	l, err := singleAddrField(h, "Sender", errors.New("missing Sender header"), errors.New("multiple Sender headers"))
	if err != nil {
		return nil, err
	}
	if len(l) != 1 {
		return nil, errors.New("Sender header must contain exactly one address")
	}
	return &l[0], nil
}

func singleAddrField(h mail.Header, key string, errMissing, errMultiple error) ([]store.Address, error) {
	fields := h.FieldsByKey(key)
	n := 0
	for fields.Next() {
		n++
	}
	switch {
	case n == 0:
		return nil, errMissing
	case n > 1:
		return nil, errMultiple
	}
	list, err := h.AddressList(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFromMalformed, err)
	}
	out := make([]store.Address, 0, len(list))
	for _, a := range list {
		if a == nil || !strings.Contains(a.Address, "@") {
			return nil, ErrFromMalformed
		}
		out = append(out, store.Address{Name: a.Name, Email: strings.ToLower(a.Address)})
	}
	if len(out) == 0 {
		return nil, ErrFromMalformed
	}
	return out, nil
}

func addrs(h mail.Header, key string) []store.Address {
	list, err := h.AddressList(key)
	out := []store.Address{}
	if err != nil {
		return out
	}
	for _, a := range list {
		out = append(out, store.Address{Name: a.Name, Email: strings.ToLower(a.Address)})
	}
	return out
}

func trimID(s string) string { return CleanMessageID(s) }

// CleanMessageID strips angle brackets and every character that is not
// allowed in a msg-id (RFC 5322 dot-atom-text, "@" and domain literals), so a
// foreign Message-ID can never inject header content when it is reused in
// In-Reply-To or References.
func CleanMessageID(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "<")
	if i := strings.IndexByte(s, '>'); i >= 0 {
		s = s[:i]
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			strings.IndexByte("!#$%&'*+-/=?^_`{|}~.@[]", c) >= 0 {
			b.WriteByte(c)
		}
	}
	out := b.String()
	if len(out) > 250 {
		out = out[:250]
	}
	return out
}

// SanitizeFilename removes control characters, Unicode bidi controls and path
// separators from an attachment file name.
func SanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == '\\':
			b.WriteRune('_')
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			// C0 / C1 controls
		case (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0x200e || r == 0x200f || r == 0x061c:
			// bidi embeddings, overrides, isolates and direction marks
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if r := []rune(out); len(r) > 200 {
		out = string(r[:200])
	}
	return out
}

// MaxMultiparts bounds the multipart containers a message may declare. The
// MIME reader stacks one buffered reader per nesting level, so the cost of
// a deeply nested message grows with depth times size; real mail stays far
// below this.
const MaxMultiparts = 50

// TooComplex reports whether raw declares more multipart boundaries than
// MaxMultiparts. It is a linear scan that runs before any MIME parsing.
func TooComplex(raw []byte) bool {
	const needle = "boundary"
	n := 0
	for i := 0; i+len(needle) <= len(raw); i++ {
		c := raw[i] | 0x20
		if c != 'b' || !bytes.EqualFold(raw[i:i+len(needle)], []byte(needle)) {
			continue
		}
		j := i + len(needle)
		for j < len(raw) && (raw[j] == ' ' || raw[j] == '\t' || raw[j] == '*') {
			j++
		}
		if j < len(raw) && raw[j] == '=' {
			if n++; n > MaxMultiparts {
				return true
			}
		}
		i = j
	}
	return false
}

// Parse parses raw message bytes. With withData, attachment content is kept.
// Of messages that are TooComplex only the header is parsed: the reader
// descends through every level before it returns the first part.
func Parse(raw []byte, withData bool) (*Parsed, error) {
	headerOnly := TooComplex(raw)
	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil && mr == nil {
		return nil, err
	}
	p := &Parsed{Header: mr.Header}
	p.Subject, _ = mr.Header.Subject()
	p.FromAll, p.FromErr = FromAddresses(mr.Header)
	if len(p.FromAll) > 0 {
		p.From = p.FromAll[0]
	} else if from := addrs(mr.Header, "From"); len(from) > 0 {
		p.From = from[0]
	}
	p.To = addrs(mr.Header, "To")
	p.Cc = addrs(mr.Header, "Cc")
	p.ReplyTo = addrs(mr.Header, "Reply-To")
	p.MessageID = trimID(mr.Header.Get("Message-Id"))
	if f := strings.Fields(mr.Header.Get("In-Reply-To")); len(f) > 0 {
		p.InReplyTo = trimID(f[0])
	}
	for _, r := range strings.Fields(mr.Header.Get("References")) {
		if id := trimID(r); id != "" {
			p.References = append(p.References, id)
		}
	}
	p.Date, _ = mr.Header.Date()

	var id int64
	for !headerOnly {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Keep what we have; malformed tails are common.
			break
		}
		id++
		switch h := part.Header.(type) {
		case *mail.InlineHeader:
			ct, _, _ := h.ContentType()
			body, _ := io.ReadAll(io.LimitReader(part.Body, 20<<20))
			switch {
			case ct == "text/plain" && p.Text == "":
				p.Text = string(body)
			case ct == "text/html" && p.HTML == "":
				p.HTML = string(body)
			case strings.HasPrefix(ct, "text/"):
				// additional text parts are ignored
			default:
				// inline non-text (e.g. images) are listed as attachments
				p.Attachments = append(p.Attachments, att(id, "", ct, body, withData))
			}
		case *mail.AttachmentHeader:
			ct, _, _ := h.ContentType()
			name, _ := h.Filename()
			body, _ := io.ReadAll(io.LimitReader(part.Body, 100<<20))
			p.Attachments = append(p.Attachments, att(id, name, ct, body, withData))
		}
	}
	if p.Text == "" && p.HTML != "" {
		p.Text = HTMLToText(p.HTML)
	}
	return p, nil
}

func att(id int64, name, ct string, body []byte, withData bool) Attachment {
	name = SanitizeFilename(name)
	if name == "" {
		name = fmt.Sprintf("anhang-%d", id)
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	a := Attachment{ID: id, Filename: name, ContentType: ct, Size: int64(len(body))}
	if withData {
		a.Data = body
	}
	return a
}

var (
	reScript = regexp.MustCompile(`(?is)<(script|style|head)[^>]*>.*?</(script|style|head)>`)
	reBreak  = regexp.MustCompile(`(?i)<(br|/p|/div|/tr|/h[1-6]|/li)[^>]*>`)
	reTag    = regexp.MustCompile(`(?s)<[^>]+>`)
	reSpace  = regexp.MustCompile(`[ \t]+`)
	reLines  = regexp.MustCompile(`\n{3,}`)
)

func HTMLToText(s string) string {
	s = reScript.ReplaceAllString(s, "")
	s = reBreak.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = reSpace.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return strings.TrimSpace(reLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

func Preview(text string, n int) string {
	t := strings.Join(strings.Fields(text), " ")
	r := []rune(t)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return t
}

func NewMessageID(domain string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s.%d@%s", hex.EncodeToString(b), time.Now().Unix(), domain)
}

// ---- building ----

type OutAttachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

type Draft struct {
	From       store.Address
	To, Cc     []store.Address
	Subject    string
	Text       string
	HTML       string
	InReplyTo  string
	References []string
	MessageID  string
	Extra      map[string]string
	Attach     []OutAttachment
}

func fmtAddr(a store.Address) string {
	if a.Email == "" {
		return ""
	}
	if a.Name == "" {
		return a.Email
	}
	// net/mail quotes or RFC 2047-encodes the display name as needed, so
	// specials like '<' or ',' in a name cannot add further addresses.
	return (&netmail.Address{Name: a.Name, Address: a.Email}).String()
}

// cleanHeaderValue replaces CR, LF and NUL so a value can never start a new
// header line.
func cleanHeaderValue(v string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', 0:
			return ' '
		}
		return r
	}, v)
}

func validHeaderName(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] <= ' ' || k[i] > '~' || k[i] == ':' {
			return false
		}
	}
	return true
}

// attachmentHeaders returns safe Content-Type and Content-Disposition values
// for an outgoing attachment.
func attachmentHeaders(contentType, filename string) (ct, disp string) {
	name := SanitizeFilename(filename)
	if name == "" {
		name = "anhang"
	}
	mt := "application/octet-stream"
	if parsed, _, err := mime.ParseMediaType(contentType); err == nil && strings.Count(parsed, "/") == 1 {
		mt = parsed
	}
	ct = mime.FormatMediaType(mt, map[string]string{"name": name})
	if ct == "" {
		ct = mime.FormatMediaType("application/octet-stream", map[string]string{"name": name})
	}
	disp = mime.FormatMediaType("attachment", map[string]string{"filename": name})
	return ct, disp
}

func fmtList(l []store.Address) string {
	s := make([]string, len(l))
	for i, a := range l {
		s[i] = fmtAddr(a)
	}
	return strings.Join(s, ", ")
}

func qp(s string) string {
	var b bytes.Buffer
	w := quotedprintable.NewWriter(&b)
	w.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")))
	w.Close()
	return b.String()
}

func boundary() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "pw-" + hex.EncodeToString(b)
}

func wrap76(s string) string {
	var b strings.Builder
	for len(s) > 76 {
		b.WriteString(s[:76])
		b.WriteString("\r\n")
		s = s[76:]
	}
	b.WriteString(s)
	return b.String()
}

// Build renders the draft as RFC 5322 bytes with CRLF line endings.
func Build(d Draft) []byte {
	var h strings.Builder
	w := func(k, v string) {
		v = strings.TrimSpace(cleanHeaderValue(v))
		if v != "" && validHeaderName(k) {
			h.WriteString(k + ": " + v + "\r\n")
		}
	}
	id := func(s string) string {
		if s = CleanMessageID(s); s == "" {
			return ""
		}
		return "<" + s + ">"
	}
	w("From", fmtAddr(d.From))
	w("To", fmtList(d.To))
	w("Cc", fmtList(d.Cc))
	w("Subject", mime.QEncoding.Encode("utf-8", d.Subject))
	w("Date", time.Now().Format(time.RFC1123Z))
	w("Message-ID", id(d.MessageID))
	w("In-Reply-To", id(d.InReplyTo))
	var refs []string
	for _, r := range d.References {
		if r := id(r); r != "" {
			refs = append(refs, r)
		}
	}
	w("References", strings.Join(refs, " "))
	for k, v := range d.Extra {
		w(k, v)
	}
	w("MIME-Version", "1.0")

	textPart := "Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" + qp(d.Text) + "\r\n"
	body := textPart
	if d.HTML != "" {
		bAlt := boundary()
		body = "Content-Type: multipart/alternative; boundary=\"" + bAlt + "\"\r\n\r\n" +
			"--" + bAlt + "\r\n" + textPart +
			"--" + bAlt + "\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" + qp(d.HTML) + "\r\n" +
			"--" + bAlt + "--\r\n"
	}
	if len(d.Attach) > 0 {
		bMix := boundary()
		var b strings.Builder
		b.WriteString("Content-Type: multipart/mixed; boundary=\"" + bMix + "\"\r\n\r\n")
		b.WriteString("--" + bMix + "\r\n" + body)
		for _, a := range d.Attach {
			ct, disp := attachmentHeaders(a.ContentType, a.Filename)
			b.WriteString("--" + bMix + "\r\n")
			b.WriteString("Content-Type: " + ct + "\r\n")
			b.WriteString("Content-Disposition: " + disp + "\r\n")
			b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
			b.WriteString(wrap76(base64.StdEncoding.EncodeToString(a.Data)) + "\r\n")
		}
		b.WriteString("--" + bMix + "--\r\n")
		body = b.String()
	}
	// body already starts with its own Content-Type header line(s)
	return []byte(h.String() + body)
}

// HeaderValue returns the first value of a header in raw message bytes.
func HeaderValue(raw []byte, key string) string {
	end := bytes.Index(raw, []byte("\r\n\r\n"))
	if end < 0 {
		end = bytes.Index(raw, []byte("\n\n"))
	}
	if end < 0 {
		end = len(raw)
	}
	lines := strings.Split(strings.ReplaceAll(string(raw[:end]), "\r\n", "\n"), "\n")
	prefix := strings.ToLower(key) + ":"
	for i, l := range lines {
		if strings.HasPrefix(strings.ToLower(l), prefix) {
			v := strings.TrimSpace(l[len(prefix):])
			for j := i + 1; j < len(lines) && (strings.HasPrefix(lines[j], " ") || strings.HasPrefix(lines[j], "\t")); j++ {
				v += " " + strings.TrimSpace(lines[j])
			}
			return v
		}
	}
	return ""
}

// headerBlock returns the header section of raw including the line ending of
// its last line (but not the blank separator line).
func headerBlock(raw []byte) []byte {
	crlf := bytes.Index(raw, []byte("\r\n\r\n"))
	lf := bytes.Index(raw, []byte("\n\n"))
	switch {
	case crlf >= 0 && (lf < 0 || crlf+1 <= lf):
		return raw[:crlf+2]
	case lf >= 0:
		return raw[:lf+1]
	}
	return raw
}

// headerFields splits a header block into raw fields, each including its
// continuation lines and line endings.
func headerFields(block []byte) [][]byte {
	var fields [][]byte
	for len(block) > 0 {
		end := 0
		for {
			nl := bytes.IndexByte(block[end:], '\n')
			if nl < 0 {
				end = len(block)
				break
			}
			end += nl + 1
			if end >= len(block) || (block[end] != ' ' && block[end] != '\t') {
				break
			}
		}
		fields = append(fields, block[:end])
		block = block[end:]
	}
	return fields
}

func fieldNameValue(f []byte) (string, string) {
	i := bytes.IndexByte(f, ':')
	if i < 0 {
		return "", ""
	}
	return strings.TrimSpace(string(f[:i])), strings.Join(strings.Fields(string(f[i+1:])), " ")
}

// StripHeaders removes every top-level header field for which drop returns
// true. The body is left untouched.
func StripHeaders(raw []byte, drop func(name, value string) bool) []byte {
	block := headerBlock(raw)
	out := make([]byte, 0, len(raw))
	removed := false
	for _, f := range headerFields(block) {
		if name, value := fieldNameValue(f); name != "" && drop(name, value) {
			removed = true
			continue
		}
		out = append(out, f...)
	}
	if !removed {
		return raw
	}
	return append(out, raw[len(block):]...)
}

// CountHeader returns how often a header field occurs in the header section.
func CountHeader(raw []byte, key string) int {
	n := 0
	for _, f := range headerFields(headerBlock(raw)) {
		if name, _ := fieldNameValue(f); strings.EqualFold(name, key) {
			n++
		}
	}
	return n
}
