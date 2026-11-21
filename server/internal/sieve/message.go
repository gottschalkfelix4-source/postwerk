package sieve

import (
	"bytes"
	"mime"
	"net/mail"
	"strings"
	"time"

	"github.com/emersion/go-message/charset"
)

// maxHeaderFields bounds the number of header fields considered.
const maxHeaderFields = 2000

type field struct {
	name    string // lowercase
	raw     string // unfolded, undecoded value
	decoded string // RFC 2047 decoded value
}

// Message is the input of a script run.
type Message struct {
	fields []field
	// Size is the message size in octets.
	Size int64
	// EnvelopeFrom is the SMTP MAIL FROM (empty for the null sender);
	// EnvelopeTo the RCPT TO address that led to this delivery.
	EnvelopeFrom string
	EnvelopeTo   string
	// BodyText returns the decoded text content (body :text). Nil falls
	// back to the raw body.
	BodyText func() string
	rawBody  string
}

var wordDecoder = &mime.WordDecoder{CharsetReader: charset.Reader}

// ParseMessage reads the header fields and body of a raw message.
func ParseMessage(raw []byte) *Message {
	m := &Message{Size: int64(len(raw))}
	end, sep := bytes.Index(raw, []byte("\r\n\r\n")), 4
	if alt := bytes.Index(raw, []byte("\n\n")); alt >= 0 && (end < 0 || alt < end) {
		end, sep = alt, 2
	}
	head := raw
	if end >= 0 {
		head = raw[:end]
		m.rawBody = string(raw[end+sep:])
	}
	lines := strings.Split(strings.ReplaceAll(string(head), "\r\n", "\n"), "\n")
	var cur *field
	for _, line := range lines {
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if cur != nil {
				cur.raw += line
			}
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok || strings.ContainsAny(name, " \t") {
			cur = nil
			continue
		}
		if len(m.fields) >= maxHeaderFields {
			break
		}
		m.fields = append(m.fields, field{name: strings.ToLower(name), raw: value})
		cur = &m.fields[len(m.fields)-1]
	}
	for i := range m.fields {
		f := &m.fields[i]
		f.raw = strings.TrimSpace(f.raw)
		if d, err := wordDecoder.DecodeHeader(f.raw); err == nil {
			f.decoded = d
		} else {
			f.decoded = f.raw
		}
	}
	return m
}

// Header returns the decoded values of all fields with the given name.
func (m *Message) Header(name string) []string {
	name = strings.ToLower(name)
	var out []string
	for _, f := range m.fields {
		if f.name == name {
			out = append(out, f.decoded)
		}
	}
	return out
}

func (m *Message) rawHeader(name string) []string {
	name = strings.ToLower(name)
	var out []string
	for _, f := range m.fields {
		if f.name == name {
			out = append(out, f.raw)
		}
	}
	return out
}

func (m *Message) text() string {
	if m.BodyText != nil {
		return m.BodyText()
	}
	return m.rawBody
}

// headerDate returns the date of a header field (date test).
func (m *Message) headerDate(name string) (time.Time, bool) {
	vals := m.Header(name)
	if len(vals) == 0 {
		return time.Time{}, false
	}
	v := vals[0]
	if strings.EqualFold(name, "received") {
		// The date follows the last semicolon.
		if i := strings.LastIndexByte(v, ';'); i >= 0 {
			v = v[i+1:]
		}
	}
	t, err := mail.ParseDate(strings.TrimSpace(v))
	return t, err == nil
}

// addressList extracts the addr-specs of an address header value.
func addressList(raw string) []string {
	if list, err := mail.ParseAddressList(raw); err == nil {
		out := make([]string, 0, len(list))
		for _, a := range list {
			out = append(out, a.Address)
		}
		return out
	}
	// Lenient fallback for malformed headers.
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if i := strings.LastIndexByte(part, '<'); i >= 0 {
			if j := strings.IndexByte(part[i:], '>'); j > 0 {
				part = part[i+1 : i+j]
			}
		}
		if part = strings.Trim(part, " \t\"'"); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// addressPart returns the requested part of an address; ok is false when
// the part does not exist (e.g. :detail without separator).
func addressPart(addr, part string) (string, bool) {
	addr = strings.Trim(addr, "<> ")
	local, domain := addr, ""
	if i := strings.LastIndexByte(addr, '@'); i >= 0 {
		local, domain = addr[:i], addr[i+1:]
	}
	switch part {
	case "localpart":
		return local, true
	case "domain":
		return domain, true
	case "user":
		u, _, _ := strings.Cut(local, "+")
		return u, true
	case "detail":
		_, d, ok := strings.Cut(local, "+")
		return d, ok
	}
	return addr, true
}
