package jmap

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"mime/quotedprintable"
	netmail "net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"postwerk/internal/message"
	"postwerk/internal/store"
)

// bodyPartIn is an EmailBodyPart in an Email/set create.
type bodyPartIn struct {
	PartID      *string       `json:"partId"`
	BlobID      *string       `json:"blobId"`
	Type        *string       `json:"type"`
	Charset     *string       `json:"charset"`
	Name        *string       `json:"name"`
	Disposition *string       `json:"disposition"`
	Cid         *string       `json:"cid"`
	Language    []string      `json:"language"`
	Location    *string       `json:"location"`
	Headers     []headerIn    `json:"headers"`
	SubParts    []*bodyPartIn `json:"subParts"`

	empty bool // generated empty text body
}

type headerIn struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type bodyValueIn struct {
	Value             string `json:"value"`
	IsEncodingProblem bool   `json:"isEncodingProblem"`
	IsTruncated       bool   `json:"isTruncated"`
}

type emailCreate struct {
	MailboxIDs    map[string]bool        `json:"mailboxIds"`
	Keywords      map[string]bool        `json:"keywords"`
	ReceivedAt    *string                `json:"receivedAt"`
	MessageID     []string               `json:"messageId"`
	InReplyTo     []string               `json:"inReplyTo"`
	References    []string               `json:"references"`
	Sender        []emailAddress         `json:"sender"`
	From          []emailAddress         `json:"from"`
	To            []emailAddress         `json:"to"`
	Cc            []emailAddress         `json:"cc"`
	Bcc           []emailAddress         `json:"bcc"`
	ReplyTo       []emailAddress         `json:"replyTo"`
	Subject       *string                `json:"subject"`
	SentAt        *string                `json:"sentAt"`
	Headers       []headerIn             `json:"headers"`
	BodyStructure *bodyPartIn            `json:"bodyStructure"`
	TextBody      []*bodyPartIn          `json:"textBody"`
	HTMLBody      []*bodyPartIn          `json:"htmlBody"`
	Attachments   []*bodyPartIn          `json:"attachments"`
	BodyValues    map[string]bodyValueIn `json:"bodyValues"`
}

var createKnown = map[string]bool{"mailboxIds": true, "keywords": true, "receivedAt": true, "messageId": true, "inReplyTo": true,
	"references": true, "sender": true, "from": true, "to": true, "cc": true, "bcc": true, "replyTo": true, "subject": true,
	"sentAt": true, "headers": true, "bodyStructure": true, "textBody": true, "htmlBody": true, "attachments": true, "bodyValues": true}

// convenience properties and the header field they set
var convenienceHeaders = map[string]string{"message-id": "messageId", "in-reply-to": "inReplyTo", "references": "references",
	"sender": "sender", "from": "from", "to": "to", "cc": "cc", "bcc": "bcc", "reply-to": "replyTo", "subject": "subject", "date": "sentAt"}

type buildErr struct {
	desc  string
	props []string
}

func (e *buildErr) Error() string { return e.desc }

func bErr(desc string, props ...string) *buildErr { return &buildErr{desc, props} }

func clean(v string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return ' '
		}
		return r
	}, v)
}

func validHeaderName(k string) bool {
	if k == "" || len(k) > 76 {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] <= ' ' || k[i] > '~' || k[i] == ':' {
			return false
		}
	}
	return true
}

func fmtAddresses(l []emailAddress) (string, error) {
	var out []string
	for _, a := range l {
		e := strings.TrimSpace(a.Email)
		if e == "" || strings.ContainsAny(e, " \t\r\n<>,;\"") {
			return "", fmt.Errorf("invalid address %q", a.Email)
		}
		name := ""
		if a.Name != nil {
			name = clean(*a.Name)
		}
		if name == "" {
			out = append(out, e)
		} else {
			out = append(out, (&netmail.Address{Name: name, Address: e}).String())
		}
	}
	return strings.Join(out, ", "), nil
}

func fmtMessageIDs(l []string) (string, error) {
	var out []string
	for _, id := range l {
		id = strings.Trim(strings.TrimSpace(id), "<>")
		if id == "" || strings.ContainsAny(id, " \t\r\n<>") {
			return "", fmt.Errorf("invalid message id %q", id)
		}
		out = append(out, "<"+id+">")
	}
	return strings.Join(out, " "), nil
}

func encodeText(s string) string { return mime.QEncoding.Encode("utf-8", clean(s)) }

func boundary() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "pwj-" + hex.EncodeToString(b)
}

func wrap76(s string) string {
	var b strings.Builder
	for len(s) > 76 {
		b.WriteString(s[:76] + "\r\n")
		s = s[76:]
	}
	b.WriteString(s)
	return b.String()
}

func crlf(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}

// builder renders an Email/set create into RFC 5322 bytes.
type builder struct {
	c      *call
	values map[string]bodyValueIn
	used   map[string]bool
}

// buildEmail validates the create object and produces the message bytes.
func (c *call) buildEmail(raw json.RawMessage, o *emailCreate) ([]byte, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, bErr("email must be an object")
	}
	var hdr strings.Builder
	write := func(k, v string) { hdr.WriteString(k + ": " + v + "\r\n") }
	set := map[string]bool{}

	// Custom header:Name[:form] properties.
	for k, v := range all {
		if !strings.HasPrefix(k, "header:") {
			if !createKnown[k] {
				return nil, bErr("property cannot be set", k)
			}
			continue
		}
		segs := strings.Split(strings.TrimPrefix(k, "header:"), ":")
		name, form := segs[0], "asRaw"
		if len(segs) > 2 || !validHeaderName(name) {
			return nil, bErr("invalid header property", k)
		}
		if len(segs) == 2 {
			form = segs[1]
		}
		lname := strings.ToLower(name)
		if strings.HasPrefix(lname, "content-") || lname == "mime-version" {
			return nil, bErr("MIME headers are set from the body structure", k)
		}
		if prop, ok := convenienceHeaders[lname]; ok && all[prop] != nil {
			return nil, bErr("header given twice", k, prop)
		}
		var value string
		switch form {
		case "asRaw":
			var s string
			if json.Unmarshal(v, &s) != nil {
				return nil, bErr("invalid value", k)
			}
			value = strings.TrimSpace(clean(s))
		case "asText":
			var s string
			if json.Unmarshal(v, &s) != nil {
				return nil, bErr("invalid value", k)
			}
			value = encodeText(s)
		case "asAddresses":
			var l []emailAddress
			if json.Unmarshal(v, &l) != nil {
				return nil, bErr("invalid value", k)
			}
			s, err := fmtAddresses(l)
			if err != nil {
				return nil, bErr(err.Error(), k)
			}
			value = s
		case "asMessageIds":
			var l []string
			if json.Unmarshal(v, &l) != nil {
				return nil, bErr("invalid value", k)
			}
			s, err := fmtMessageIDs(l)
			if err != nil {
				return nil, bErr(err.Error(), k)
			}
			value = s
		case "asDate":
			var s string
			t, err := time.Parse(time.RFC3339, func() string { _ = json.Unmarshal(v, &s); return s }())
			if err != nil {
				return nil, bErr("invalid date", k)
			}
			value = t.Format(time.RFC1123Z)
		case "asURLs":
			var l []string
			if json.Unmarshal(v, &l) != nil {
				return nil, bErr("invalid value", k)
			}
			for i, u := range l {
				l[i] = "<" + clean(u) + ">"
			}
			value = strings.Join(l, ", ")
		default:
			return nil, bErr("unsupported header form", k)
		}
		write(name, value)
		set[lname] = true
	}
	for _, h := range o.Headers {
		if !validHeaderName(h.Name) {
			return nil, bErr("invalid header name", "headers")
		}
		lname := strings.ToLower(h.Name)
		if strings.HasPrefix(lname, "content-") || lname == "mime-version" {
			return nil, bErr("MIME headers are set from the body structure", "headers")
		}
		if _, ok := convenienceHeaders[lname]; ok && set[lname] {
			return nil, bErr("header given twice", "headers")
		}
		write(h.Name, strings.TrimSpace(clean(h.Value)))
		set[lname] = true
	}
	addrHeader := func(name, prop string, l []emailAddress) error {
		if l == nil {
			return nil
		}
		s, err := fmtAddresses(l)
		if err != nil {
			return bErr(err.Error(), prop)
		}
		if s != "" {
			write(name, s)
			set[strings.ToLower(name)] = true
		}
		return nil
	}
	for _, a := range []struct {
		name, prop string
		l          []emailAddress
	}{{"From", "from", o.From}, {"Sender", "sender", o.Sender}, {"To", "to", o.To}, {"Cc", "cc", o.Cc}, {"Bcc", "bcc", o.Bcc}, {"Reply-To", "replyTo", o.ReplyTo}} {
		if err := addrHeader(a.name, a.prop, a.l); err != nil {
			return nil, err
		}
	}
	if o.Subject != nil {
		write("Subject", encodeText(*o.Subject))
		set["subject"] = true
	}
	if !set["date"] {
		date := time.Now()
		if o.SentAt != nil {
			t, err := time.Parse(time.RFC3339, *o.SentAt)
			if err != nil {
				return nil, bErr("invalid sentAt", "sentAt")
			}
			date = t
		}
		write("Date", date.Format(time.RFC1123Z))
	}
	for _, m := range []struct {
		name, prop string
		l          []string
	}{{"Message-ID", "messageId", o.MessageID}, {"In-Reply-To", "inReplyTo", o.InReplyTo}, {"References", "references", o.References}} {
		if m.l == nil {
			continue
		}
		s, err := fmtMessageIDs(m.l)
		if err != nil {
			return nil, bErr(err.Error(), m.prop)
		}
		if s != "" {
			write(m.name, s)
			set[strings.ToLower(m.name)] = true
		}
	}
	if !set["message-id"] {
		domain := c.acct.Email
		if len(o.From) > 0 {
			domain = o.From[0].Email
		}
		write("Message-ID", "<"+message.NewMessageID(store.DomainOf(domain))+">")
	}
	write("MIME-Version", "1.0")

	b := &builder{c: c, values: o.BodyValues, used: map[string]bool{}}
	for id, v := range o.BodyValues {
		if v.IsTruncated || v.IsEncodingProblem {
			return nil, bErr("bodyValues must not be truncated or have encoding problems", "bodyValues/"+id)
		}
	}
	root, err := b.structure(o)
	if err != nil {
		return nil, err
	}
	entity, err := b.entity(root)
	if err != nil {
		return nil, err
	}
	return []byte(hdr.String() + entity), nil
}

// structure determines the MIME tree from bodyStructure or the
// textBody/htmlBody/attachments shorthand.
func (b *builder) structure(o *emailCreate) (*bodyPartIn, error) {
	if o.BodyStructure != nil {
		if o.TextBody != nil || o.HTMLBody != nil || o.Attachments != nil {
			return nil, bErr("bodyStructure excludes textBody, htmlBody and attachments", "bodyStructure")
		}
		return o.BodyStructure, nil
	}
	if len(o.TextBody) > 1 || len(o.HTMLBody) > 1 {
		return nil, bErr("at most one textBody and one htmlBody part", "textBody", "htmlBody")
	}
	var text, html *bodyPartIn
	if len(o.TextBody) == 1 {
		text = o.TextBody[0]
		if text.Type != nil && !strings.EqualFold(*text.Type, "text/plain") {
			return nil, bErr("textBody must be text/plain", "textBody")
		}
		t := "text/plain"
		text.Type = &t
	}
	if len(o.HTMLBody) == 1 {
		html = o.HTMLBody[0]
		if html.Type != nil && !strings.EqualFold(*html.Type, "text/html") {
			return nil, bErr("htmlBody must be text/html", "htmlBody")
		}
		t := "text/html"
		html.Type = &t
	}
	var body *bodyPartIn
	switch {
	case text != nil && html != nil:
		body = &bodyPartIn{Type: strPtr("multipart/alternative"), SubParts: []*bodyPartIn{text, html}}
	case text != nil:
		body = text
	case html != nil:
		body = html
	default:
		body = &bodyPartIn{Type: strPtr("text/plain"), empty: true}
	}
	var inline, other []*bodyPartIn
	for _, a := range o.Attachments {
		if html != nil && a.Cid != nil && a.Disposition != nil && strings.EqualFold(*a.Disposition, "inline") {
			inline = append(inline, a)
		} else {
			other = append(other, a)
		}
	}
	if len(inline) > 0 {
		body = &bodyPartIn{Type: strPtr("multipart/related"), SubParts: append([]*bodyPartIn{body}, inline...)}
	}
	if len(other) > 0 {
		body = &bodyPartIn{Type: strPtr("multipart/mixed"), SubParts: append([]*bodyPartIn{body}, other...)}
	}
	return body, nil
}

func strPtr(s string) *string { return &s }

// entity renders one MIME entity: its content headers, a blank line and
// the encoded body.
func (b *builder) entity(p *bodyPartIn) (string, error) {
	var h strings.Builder
	for _, x := range p.Headers {
		lname := strings.ToLower(x.Name)
		if !validHeaderName(x.Name) || lname == "content-transfer-encoding" || lname == "content-type" || lname == "content-disposition" {
			return "", bErr("invalid part header "+x.Name, "headers")
		}
		h.WriteString(x.Name + ": " + strings.TrimSpace(clean(x.Value)) + "\r\n")
	}
	typ := ""
	if p.Type != nil {
		typ = strings.ToLower(strings.TrimSpace(*p.Type))
	}
	if len(p.SubParts) > 0 || strings.HasPrefix(typ, "multipart/") {
		if typ == "" {
			typ = "multipart/mixed"
		}
		if !strings.HasPrefix(typ, "multipart/") || p.PartID != nil || p.BlobID != nil {
			return "", bErr("multipart parts carry subParts only", "bodyStructure")
		}
		bd := boundary()
		var body strings.Builder
		for _, sp := range p.SubParts {
			e, err := b.entity(sp)
			if err != nil {
				return "", err
			}
			body.WriteString("--" + bd + "\r\n" + e + "\r\n")
		}
		body.WriteString("--" + bd + "--\r\n")
		h.WriteString("Content-Type: " + mime.FormatMediaType(typ, map[string]string{"boundary": bd}) + "\r\n")
		return h.String() + "\r\n" + body.String(), nil
	}

	var data []byte
	isText := false
	switch {
	case p.PartID != nil && p.BlobID != nil:
		return "", bErr("a part has either partId or blobId", "partId", "blobId")
	case p.PartID != nil:
		v, ok := b.values[*p.PartID]
		if !ok {
			return "", bErr("partId "+*p.PartID+" not found in bodyValues", "bodyValues")
		}
		b.used[*p.PartID] = true
		data, isText = []byte(v.Value), true
		if typ == "" {
			typ = "text/plain"
		}
		if !strings.HasPrefix(typ, "text/") {
			return "", bErr("bodyValues can only be used for text parts", "partId")
		}
	case p.BlobID != nil:
		blobID, ok := b.c.resolveCreated(*p.BlobID)
		if !ok {
			return "", bErr("blob "+*p.BlobID+" not found", "blobId")
		}
		d, partType, err := b.c.blobData(blobID)
		if err != nil {
			return "", bErr("blob "+*p.BlobID+" not found", "blobId")
		}
		data = d
		if typ == "" {
			typ = partType
		}
		if typ == "" {
			typ = b.c.uploadType(blobID)
		}
	case p.empty:
		isText = true
	default:
		return "", bErr("a part needs partId or blobId", "partId", "blobId")
	}
	if mt, _, err := mime.ParseMediaType(typ); err != nil || strings.Count(mt, "/") != 1 {
		typ = "application/octet-stream"
	} else {
		typ = mt
	}
	params := map[string]string{}
	if strings.HasPrefix(typ, "text/") {
		switch {
		case isText:
			params["charset"] = "utf-8"
		case p.Charset != nil:
			params["charset"] = *p.Charset
		case utf8.Valid(data):
			params["charset"] = "utf-8"
		}
	}
	name := ""
	if p.Name != nil {
		name = message.SanitizeFilename(*p.Name)
		if name != "" {
			params["name"] = name
		}
	}
	ct := mime.FormatMediaType(typ, params)
	if ct == "" {
		ct = "application/octet-stream"
	}
	h.WriteString("Content-Type: " + ct + "\r\n")
	disp := ""
	if p.Disposition != nil {
		disp = strings.ToLower(strings.TrimSpace(*p.Disposition))
	}
	if disp == "" && name != "" {
		disp = "attachment"
	}
	if disp != "" {
		dp := map[string]string{}
		if name != "" {
			dp["filename"] = name
		}
		if v := mime.FormatMediaType(disp, dp); v != "" {
			h.WriteString("Content-Disposition: " + v + "\r\n")
		}
	}
	if p.Cid != nil && *p.Cid != "" {
		h.WriteString("Content-ID: <" + strings.Trim(clean(*p.Cid), "<> ") + ">\r\n")
	}
	if len(p.Language) > 0 {
		h.WriteString("Content-Language: " + clean(strings.Join(p.Language, ", ")) + "\r\n")
	}
	if p.Location != nil && *p.Location != "" {
		h.WriteString("Content-Location: " + clean(*p.Location) + "\r\n")
	}
	var body string
	switch {
	case isText:
		var qb strings.Builder
		w := quotedprintable.NewWriter(&qb)
		_, _ = w.Write([]byte(crlf(string(data))))
		_ = w.Close()
		h.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
		body = qb.String()
	case typ == "message/rfc822" || typ == "message/global":
		// RFC 2046 forbids encoding embedded messages.
		h.WriteString("Content-Transfer-Encoding: 8bit\r\n")
		body = crlf(string(data))
	default:
		h.WriteString("Content-Transfer-Encoding: base64\r\n")
		body = wrap76(base64.StdEncoding.EncodeToString(data))
	}
	return h.String() + "\r\n" + body + "\r\n", nil
}

// uploadType returns the media type an upload was stored with.
func (c *call) uploadType(blobID string) string {
	hash, path, ok := parseBlobID(blobID)
	if !ok || len(path) > 0 {
		return "application/octet-stream"
	}
	var t string
	if err := c.srv.Store.DB.QueryRowContext(c.ctx, `SELECT type FROM jmap_uploads WHERE account_id = ? AND blob_hash = ?`, c.acct.ID, hash).Scan(&t); err != nil || t == "" {
		return "application/octet-stream"
	}
	if mt, _, err := mime.ParseMediaType(t); err == nil {
		return mt
	}
	return "application/octet-stream"
}
