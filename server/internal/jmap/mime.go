package jmap

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/quotedprintable"
	netmail "net/mail"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	gomessage "github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset"
	gomail "github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"
)

// maxMIMEDepth bounds multipart nesting.
const maxMIMEDepth = 30

// part is one node of a parsed MIME tree.
type part struct {
	path        string // "1", "1.2"; empty for a multipart root
	fields      []headerField
	mediaType   string
	params      map[string]string
	disposition string
	dispParams  map[string]string
	children    []*part
	body        []byte // transfer-decoded content of leaf parts
}

type headerField struct {
	name string
	raw  string // value octets after the colon, without the final CRLF
}

func (p *part) multipart() bool { return strings.HasPrefix(p.mediaType, "multipart/") }

// parseMessage parses raw RFC 5322 bytes into a MIME tree. It never fails:
// malformed input yields as much structure as could be read.
func parseMessage(raw []byte) *part {
	br := bufio.NewReader(bytes.NewReader(raw))
	h, err := textproto.ReadHeader(br)
	if err != nil {
		// Not even a header: treat everything as a plain text body.
		return &part{path: "1", mediaType: "text/plain", params: map[string]string{}, body: raw}
	}
	return parseBody(h, br, "", 0, "text/plain")
}

func parseBody(h textproto.Header, body io.Reader, path string, depth int, defaultType string) *part {
	p := &part{path: path, params: map[string]string{}}
	for f := h.Fields(); f.Next(); {
		b, _ := f.Raw()
		s := string(b)
		if i := strings.IndexByte(s, ':'); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(strings.TrimSuffix(s, "\n"), "\r")
		p.fields = append(p.fields, headerField{name: f.Key(), raw: s})
	}
	p.mediaType = defaultType
	if ct := h.Get("Content-Type"); ct != "" {
		if mt, params, err := mime.ParseMediaType(ct); err == nil && strings.Count(mt, "/") == 1 {
			p.mediaType, p.params = strings.ToLower(mt), params
		}
	}
	if cd := h.Get("Content-Disposition"); cd != "" {
		if d, params, err := mime.ParseMediaType(cd); err == nil {
			p.disposition, p.dispParams = strings.ToLower(d), params
		}
	}
	if p.multipart() && p.params["boundary"] != "" && depth < maxMIMEDepth {
		childType := "text/plain"
		if p.mediaType == "multipart/digest" {
			childType = "message/rfc822"
		}
		mr := textproto.NewMultipartReader(body, p.params["boundary"])
		for i := 1; ; i++ {
			cp, err := mr.NextPart()
			if err != nil {
				break
			}
			childPath := strconv.Itoa(i)
			if path != "" {
				childPath = path + "." + childPath
			}
			p.children = append(p.children, parseBody(cp.Header, cp, childPath, depth+1, childType))
		}
		return p
	}
	if p.multipart() {
		// A multipart without boundary cannot be split; expose it as data.
		p.mediaType = "application/octet-stream"
	}
	if p.path == "" {
		p.path = "1"
	}
	data, _ := io.ReadAll(body)
	p.body = decodeTransfer(h.Get("Content-Transfer-Encoding"), data)
	return p
}

func decodeTransfer(enc string, data []byte) []byte {
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "base64":
		clean := make([]byte, 0, len(data))
		for _, c := range data {
			if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' {
				clean = append(clean, c)
			}
		}
		// Padding was dropped above; a dangling single character cannot be
		// decoded and is ignored.
		if len(clean)%4 == 1 {
			clean = clean[:len(clean)-1]
		}
		out := make([]byte, base64.RawStdEncoding.DecodedLen(len(clean)))
		n, _ := base64.RawStdEncoding.Decode(out, clean)
		return out[:n]
	case "quoted-printable":
		out, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(data)))
		if err != nil && len(out) == 0 {
			return data
		}
		return out
	}
	return data
}

// find returns the part with the given path.
func (p *part) find(path string) *part {
	if p.path == path && !p.multipart() {
		return p
	}
	for _, c := range p.children {
		if c.path == path || strings.HasPrefix(path, c.path+".") {
			return c.find(path)
		}
	}
	return nil
}

// header returns the raw values of all fields with the name.
func (p *part) header(name string) []string {
	var out []string
	for _, f := range p.fields {
		if strings.EqualFold(f.name, name) {
			out = append(out, f.raw)
		}
	}
	return out
}

func (p *part) lastHeader(name string) (string, bool) {
	v := p.header(name)
	if len(v) == 0 {
		return "", false
	}
	return v[len(v)-1], true
}

// ---- header forms (RFC 8621 section 4.1.2) ----

var wordDecoder = &mime.WordDecoder{CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
	if gomessage.CharsetReader == nil {
		return input, nil
	}
	return gomessage.CharsetReader(strings.ToLower(charset), input)
}}

func unfold(s string) string {
	return strings.NewReplacer("\r\n", "", "\n", "").Replace(s)
}

func asText(raw string) string {
	s := unfold(raw)
	if d, err := wordDecoder.DecodeHeader(s); err == nil {
		s = d
	}
	return strings.TrimSpace(s)
}

type emailAddress struct {
	Name  *string `json:"name"`
	Email string  `json:"email"`
}

type addressGroup struct {
	Name      *string        `json:"name"`
	Addresses []emailAddress `json:"addresses"`
}

func asAddresses(raw string) []emailAddress {
	s := strings.TrimSpace(unfold(raw))
	out := []emailAddress{}
	if s == "" {
		return out
	}
	list, err := gomail.ParseAddressList(s)
	if err != nil {
		// Lenient fallback for slightly broken headers.
		for _, item := range strings.Split(s, ",") {
			item = strings.TrimSpace(item)
			if a, err := netmail.ParseAddress(item); err == nil {
				list = append(list, a)
			} else if i, j := strings.LastIndexByte(item, '<'), strings.LastIndexByte(item, '>'); i >= 0 && j > i {
				list = append(list, &netmail.Address{Name: strings.Trim(strings.TrimSpace(item[:i]), `"`), Address: item[i+1 : j]})
			} else if strings.Contains(item, "@") {
				list = append(list, &netmail.Address{Address: item})
			}
		}
	}
	for _, a := range list {
		e := emailAddress{Email: a.Address}
		if a.Name != "" {
			n := asText(a.Name)
			e.Name = &n
		}
		out = append(out, e)
	}
	return out
}

func asMessageIDs(raw string) []string {
	s := unfold(raw)
	var out []string
	for {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			break
		}
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			break
		}
		if id := strings.TrimSpace(s[i+1 : i+j]); id != "" {
			out = append(out, id)
		}
		s = s[i+j+1:]
	}
	if len(out) == 0 {
		// Some senders omit the angle brackets.
		if f := strings.Fields(s); len(f) == 1 && strings.Contains(f[0], "@") {
			return f
		}
		return nil
	}
	return out
}

func asDate(raw string) *string {
	t, err := netmail.ParseDate(strings.TrimSpace(unfold(raw)))
	if err != nil {
		return nil
	}
	s := t.Format(time.RFC3339)
	return &s
}

func asURLs(raw string) []string {
	s := unfold(raw)
	var out []string
	for {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			break
		}
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			break
		}
		out = append(out, strings.TrimSpace(s[i+1:i+j]))
		s = s[i+j+1:]
	}
	return out
}

// headerProp evaluates a "header:Name[:asForm][:all]" property.
func (p *part) headerProp(prop string) (any, bool) {
	spec := strings.TrimPrefix(prop, "header:")
	segs := strings.Split(spec, ":")
	if len(segs) == 0 || segs[0] == "" || len(segs) > 3 {
		return nil, false
	}
	name, form, all := segs[0], "asRaw", false
	for _, s := range segs[1:] {
		switch {
		case s == "all":
			all = true
		case strings.HasPrefix(s, "as") && form == "asRaw":
			form = s
		default:
			return nil, false
		}
	}
	conv := func(raw string) (any, bool) {
		switch form {
		case "asRaw":
			return raw, true
		case "asText":
			return asText(raw), true
		case "asAddresses":
			return asAddresses(raw), true
		case "asGroupedAddresses":
			return []addressGroup{{Addresses: asAddresses(raw)}}, true
		case "asMessageIds":
			return asMessageIDs(raw), true
		case "asDate":
			return asDate(raw), true
		case "asURLs":
			return asURLs(raw), true
		}
		return nil, false
	}
	vals := p.header(name)
	if all {
		out := []any{}
		for _, v := range vals {
			c, ok := conv(v)
			if !ok {
				return nil, false
			}
			out = append(out, c)
		}
		return out, true
	}
	if len(vals) == 0 {
		_, ok := conv("")
		return nil, ok
	}
	return conv(vals[len(vals)-1])
}

func (p *part) headersList() []map[string]string {
	out := make([]map[string]string, 0, len(p.fields))
	for _, f := range p.fields {
		out = append(out, map[string]string{"name": f.name, "value": f.raw})
	}
	return out
}

// ---- body parts ----

func (p *part) name() *string {
	n := p.dispParams["filename"]
	if n == "" {
		n = p.params["name"]
	}
	if n == "" {
		return nil
	}
	n = asText(n)
	return &n
}

func (p *part) charset() *string {
	cs := p.params["charset"]
	if cs == "" && strings.HasPrefix(p.mediaType, "text/") {
		cs = "us-ascii"
	}
	if cs == "" {
		return nil
	}
	return &cs
}

func blobIDFor(hash, path string) string {
	return prefixBlob + hash + "_" + strings.ReplaceAll(path, ".", "_")
}

var defaultBodyProps = []string{"partId", "blobId", "size", "name", "type", "charset", "disposition", "cid", "language", "location"}

// bodyPart renders an EmailBodyPart with the requested properties.
func (p *part) bodyPart(hash string, props []string) map[string]any {
	out := map[string]any{}
	for _, prop := range props {
		switch prop {
		case "partId":
			if p.multipart() {
				out[prop] = nil
			} else {
				out[prop] = p.path
			}
		case "blobId":
			if p.multipart() || hash == "" {
				out[prop] = nil
			} else {
				out[prop] = blobIDFor(hash, p.path)
			}
		case "size":
			out[prop] = len(p.body)
		case "headers":
			out[prop] = p.headersList()
		case "name":
			out[prop] = p.name()
		case "type":
			out[prop] = p.mediaType
		case "charset":
			if p.multipart() {
				out[prop] = nil
			} else {
				out[prop] = p.charset()
			}
		case "disposition":
			if p.disposition == "" {
				out[prop] = nil
			} else {
				out[prop] = p.disposition
			}
		case "cid":
			if v, ok := p.lastHeader("Content-Id"); ok {
				out[prop] = strings.Trim(strings.TrimSpace(unfold(v)), "<>")
			} else {
				out[prop] = nil
			}
		case "language":
			if v, ok := p.lastHeader("Content-Language"); ok {
				var langs []string
				for _, l := range strings.Split(unfold(v), ",") {
					if l = strings.TrimSpace(l); l != "" {
						langs = append(langs, l)
					}
				}
				out[prop] = langs
			} else {
				out[prop] = nil
			}
		case "location":
			if v, ok := p.lastHeader("Content-Location"); ok {
				out[prop] = strings.TrimSpace(unfold(v))
			} else {
				out[prop] = nil
			}
		case "subParts":
			if p.multipart() {
				subs := []any{}
				for _, c := range p.children {
					subs = append(subs, c.bodyPart(hash, props))
				}
				out[prop] = subs
			} else {
				out[prop] = nil
			}
		default:
			if strings.HasPrefix(prop, "header:") {
				if v, ok := p.headerProp(prop); ok {
					out[prop] = v
				}
			}
		}
	}
	return out
}

// bodyLists computes textBody, htmlBody and attachments following the
// algorithm of RFC 8621 section 4.1.4.
func (p *part) bodyLists() (text, html, attachments []*part) {
	text, html, attachments = []*part{}, []*part{}, []*part{}
	parseStructure([]*part{p}, "mixed", false, &html, &text, &attachments)
	return
}

func isInlineMediaType(t string) bool {
	return strings.HasPrefix(t, "image/") || strings.HasPrefix(t, "audio/") || strings.HasPrefix(t, "video/")
}

func parseStructure(parts []*part, multipartType string, inAlternative bool, htmlBody, textBody, attachments *[]*part) {
	textLength, htmlLength := -1, -1
	if textBody != nil {
		textLength = len(*textBody)
	}
	if htmlBody != nil {
		htmlLength = len(*htmlBody)
	}
	for i, pt := range parts {
		isInline := pt.disposition != "attachment" &&
			(pt.mediaType == "text/plain" || pt.mediaType == "text/html" || isInlineMediaType(pt.mediaType)) &&
			(i == 0 || (multipartType != "related" && (isInlineMediaType(pt.mediaType) || pt.name() == nil)))
		switch {
		case pt.multipart():
			sub := strings.TrimPrefix(pt.mediaType, "multipart/")
			parseStructure(pt.children, sub, inAlternative || sub == "alternative", htmlBody, textBody, attachments)
		case isInline:
			if multipartType == "alternative" {
				switch pt.mediaType {
				case "text/plain":
					if textBody != nil {
						*textBody = append(*textBody, pt)
					}
				case "text/html":
					if htmlBody != nil {
						*htmlBody = append(*htmlBody, pt)
					}
				default:
					*attachments = append(*attachments, pt)
				}
				continue
			} else if inAlternative {
				if pt.mediaType == "text/plain" {
					htmlBody = nil
				}
				if pt.mediaType == "text/html" {
					textBody = nil
				}
			}
			if textBody != nil {
				*textBody = append(*textBody, pt)
			}
			if htmlBody != nil {
				*htmlBody = append(*htmlBody, pt)
			}
			if (textBody == nil || htmlBody == nil) && isInlineMediaType(pt.mediaType) {
				*attachments = append(*attachments, pt)
			}
		default:
			*attachments = append(*attachments, pt)
		}
	}
	if multipartType == "alternative" && textBody != nil && htmlBody != nil {
		// Only an HTML part was found: use it for the text body too.
		if textLength == len(*textBody) && htmlLength != len(*htmlBody) {
			*textBody = append(*textBody, (*htmlBody)[htmlLength:]...)
		}
		// Only a plain text part was found: use it for the HTML body too.
		if htmlLength == len(*htmlBody) && textLength != len(*textBody) {
			*htmlBody = append(*htmlBody, (*textBody)[textLength:]...)
		}
	}
}

// bodyValue decodes a text part to UTF-8, truncated to max bytes (0: no limit).
func (p *part) bodyValue(max int) map[string]any {
	problem := false
	data := p.body
	if cs := p.charset(); cs != nil {
		l := strings.ToLower(*cs)
		if l != "utf-8" && l != "us-ascii" && l != "utf8" && gomessage.CharsetReader != nil {
			if r, err := gomessage.CharsetReader(l, bytes.NewReader(data)); err == nil && r != nil {
				if conv, err := io.ReadAll(r); err == nil {
					data = conv
				} else {
					problem = true
				}
			} else {
				problem = true
			}
		}
	}
	s := string(data)
	if !utf8.ValidString(s) {
		problem = true
		s = strings.ToValidUTF8(s, "�")
	}
	truncated := false
	if max > 0 && len(s) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s, truncated = s[:cut], true
	}
	return map[string]any{"value": s, "isEncodingProblem": problem, "isTruncated": truncated}
}
