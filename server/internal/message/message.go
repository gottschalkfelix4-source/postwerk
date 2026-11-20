// Package message parses and builds RFC 5322 / MIME messages.
package message

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/quotedprintable"
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

func trimID(s string) string { return strings.Trim(strings.TrimSpace(s), "<>") }

// Parse parses raw message bytes. With withData, attachment content is kept.
func Parse(raw []byte, withData bool) (*Parsed, error) {
	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil && mr == nil {
		return nil, err
	}
	p := &Parsed{Header: mr.Header}
	p.Subject, _ = mr.Header.Subject()
	if from := addrs(mr.Header, "From"); len(from) > 0 {
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
		p.References = append(p.References, trimID(r))
	}
	p.Date, _ = mr.Header.Date()

	var id int64
	for {
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
	if a.Name == "" {
		return a.Email
	}
	return mime.QEncoding.Encode("utf-8", a.Name) + " <" + a.Email + ">"
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
		if v != "" {
			h.WriteString(k + ": " + v + "\r\n")
		}
	}
	w("From", fmtAddr(d.From))
	w("To", fmtList(d.To))
	w("Cc", fmtList(d.Cc))
	w("Subject", mime.QEncoding.Encode("utf-8", d.Subject))
	w("Date", time.Now().Format(time.RFC1123Z))
	w("Message-ID", "<"+d.MessageID+">")
	if d.InReplyTo != "" {
		w("In-Reply-To", "<"+d.InReplyTo+">")
	}
	if len(d.References) > 0 {
		refs := make([]string, len(d.References))
		for i, r := range d.References {
			refs[i] = "<" + r + ">"
		}
		w("References", strings.Join(refs, " "))
	}
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
			ct := a.ContentType
			if ct == "" {
				ct = "application/octet-stream"
			}
			name := mime.QEncoding.Encode("utf-8", a.Filename)
			b.WriteString("--" + bMix + "\r\n")
			b.WriteString("Content-Type: " + ct + "; name=\"" + name + "\"\r\n")
			b.WriteString("Content-Disposition: attachment; filename=\"" + name + "\"\r\n")
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
