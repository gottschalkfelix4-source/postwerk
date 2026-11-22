package jmap

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"postwerk/internal/store"
)

// ---- email rows ----

type emailRow struct {
	ID, MailboxID, ThreadID int64
	BlobHash                string
	Size                    int64
	ReceivedAt              time.Time
	Preview                 string
	HasAttachment           bool
	Flags                   store.IMAPFlags
}

const emailCols = `id, mailbox_id, thread_id, blob_hash, size, received_at, preview, has_attachments, seen, flagged, answered, draft, deleted, keywords_json`

func scanEmail(sc interface{ Scan(...any) error }) (*emailRow, error) {
	var e emailRow
	var received int64
	var kw string
	if err := sc.Scan(&e.ID, &e.MailboxID, &e.ThreadID, &e.BlobHash, &e.Size, &received, &e.Preview, &e.HasAttachment,
		&e.Flags.Seen, &e.Flags.Flagged, &e.Flags.Answered, &e.Flags.Draft, &e.Flags.Deleted, &kw); err != nil {
		return nil, err
	}
	e.ReceivedAt = time.Unix(received, 0).UTC()
	_ = json.Unmarshal([]byte(kw), &e.Flags.Keywords)
	return &e, nil
}

// loadEmails loads the given messages of the account, keyed by id.
func (c *call) loadEmails(ids []int64) (map[int64]*emailRow, error) {
	out := map[int64]*emailRow{}
	for start := 0; start < len(ids); start += 400 {
		end := min(start+400, len(ids))
		chunk := ids[start:end]
		args := []any{c.acct.ID}
		for _, id := range chunk {
			args = append(args, id)
		}
		rows, err := c.srv.Store.DB.QueryContext(c.ctx, `SELECT `+emailCols+` FROM messages WHERE account_id = ? AND id IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			e, err := scanEmail(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out[e.ID] = e
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (c *call) loadEmail(id int64) (*emailRow, error) {
	m, err := c.loadEmails([]int64{id})
	if err != nil {
		return nil, err
	}
	if e := m[id]; e != nil {
		return e, nil
	}
	return nil, store.ErrNotFound
}

// ---- keywords ----

// keywords maps IMAP flags to JMAP keywords (lower case). \Deleted has no
// JMAP equivalent and is left alone.
func keywords(f store.IMAPFlags) map[string]bool {
	out := map[string]bool{}
	if f.Seen {
		out["$seen"] = true
	}
	if f.Flagged {
		out["$flagged"] = true
	}
	if f.Answered {
		out["$answered"] = true
	}
	if f.Draft {
		out["$draft"] = true
	}
	for _, k := range f.Keywords {
		out[strings.ToLower(k)] = true
	}
	return out
}

// applyKeywords turns a keyword set back into IMAP flags.
func applyKeywords(kw map[string]bool, deleted bool) store.IMAPFlags {
	f := store.IMAPFlags{Deleted: deleted}
	for k, on := range kw {
		if !on {
			continue
		}
		switch k {
		case "$seen":
			f.Seen = true
		case "$flagged":
			f.Flagged = true
		case "$answered":
			f.Answered = true
		case "$draft":
			f.Draft = true
		default:
			f.Keywords = append(f.Keywords, k)
		}
	}
	return f
}

// validKeyword checks the JMAP keyword syntax (RFC 8621 section 4.1.1).
func validKeyword(k string) bool {
	if k == "" || len(k) > 255 {
		return false
	}
	for i := 0; i < len(k); i++ {
		ch := k[i]
		if ch < 0x21 || ch > 0x7e || strings.IndexByte(`()]{%*"\`, ch) >= 0 {
			return false
		}
	}
	return true
}

// ---- blobs ----

// parseBlobID splits "G<sha256>[_<part>_<part>...]".
func parseBlobID(id string) (hash string, path []string, ok bool) {
	rest, found := strings.CutPrefix(id, prefixBlob)
	if !found || len(rest) < 64 {
		return "", nil, false
	}
	hash = rest[:64]
	for i := 0; i < 64; i++ {
		ch := hash[i]
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return "", nil, false
		}
	}
	tail := rest[64:]
	if tail == "" {
		return hash, nil, true
	}
	if !strings.HasPrefix(tail, "_") {
		return "", nil, false
	}
	for _, seg := range strings.Split(tail[1:], "_") {
		if n, err := strconv.Atoi(seg); err != nil || n <= 0 || len(seg) > 6 {
			return "", nil, false
		}
		path = append(path, seg)
	}
	return hash, path, true
}

// extractPart walks a part path; message/rfc822 parts are descended into,
// so parts of attached messages are addressable too.
func extractPart(raw []byte, path []string, depth int) *part {
	if depth > 8 {
		return nil
	}
	tree := parseMessage(raw)
	for k := 1; k <= len(path); k++ {
		p := tree.find(strings.Join(path[:k], "."))
		if p == nil {
			continue
		}
		if k == len(path) {
			return p
		}
		if p.mediaType == "message/rfc822" || p.mediaType == "message/global" {
			return extractPart(p.body, path[k:], depth+1)
		}
		return nil
	}
	return nil
}

var errBlobNotFound = errors.New("blob not found")

// blobData returns the content and media type of a blob the account may read.
func (c *call) blobData(blobID string) ([]byte, string, error) {
	return c.srv.blobData(c.ctx, c.acct.ID, blobID)
}

// ---- Email/get ----

var defaultEmailProps = []string{"id", "blobId", "threadId", "mailboxIds", "keywords", "size", "receivedAt", "messageId", "inReplyTo",
	"references", "sender", "from", "to", "cc", "bcc", "replyTo", "subject", "sentAt", "hasAttachment", "preview", "bodyValues",
	"textBody", "htmlBody", "attachments"}

var emailProps = map[string]bool{"id": true, "blobId": true, "threadId": true, "mailboxIds": true, "keywords": true, "size": true,
	"receivedAt": true, "messageId": true, "inReplyTo": true, "references": true, "sender": true, "from": true, "to": true, "cc": true,
	"bcc": true, "replyTo": true, "subject": true, "sentAt": true, "hasAttachment": true, "preview": true, "bodyValues": true,
	"textBody": true, "htmlBody": true, "attachments": true, "headers": true, "bodyStructure": true}

// metaProps can be answered from the database without reading the message.
var metaProps = map[string]bool{"id": true, "blobId": true, "threadId": true, "mailboxIds": true, "keywords": true, "size": true,
	"receivedAt": true, "hasAttachment": true, "preview": true}

var bodyPartProps = map[string]bool{"partId": true, "blobId": true, "size": true, "headers": true, "name": true, "type": true,
	"charset": true, "disposition": true, "cid": true, "language": true, "location": true, "subParts": true}

type bodyOpts struct {
	FetchTextBodyValues bool `json:"fetchTextBodyValues"`
	FetchHTMLBodyValues bool `json:"fetchHTMLBodyValues"`
	FetchAllBodyValues  bool `json:"fetchAllBodyValues"`
	MaxBodyValueBytes   int  `json:"maxBodyValueBytes"`
}

type emailGetArgs struct {
	AccountID      string    `json:"accountId"`
	IDs            *[]string `json:"ids"`
	Properties     *[]string `json:"properties"`
	BodyProperties *[]string `json:"bodyProperties"`
	bodyOpts
}

func checkProps(list []string, valid map[string]bool) error {
	for _, p := range list {
		if !valid[p] && !strings.HasPrefix(p, "header:") {
			return invalidArgs("unknown property %q", p)
		}
		if strings.HasPrefix(p, "header:") {
			if _, ok := (&part{}).headerProp(p); !ok {
				return invalidArgs("invalid header property %q", p)
			}
		}
	}
	return nil
}

func (a *emailGetArgs) props() ([]string, []string, error) {
	props := defaultEmailProps
	if a.Properties != nil {
		props = *a.Properties
	}
	if err := checkProps(props, emailProps); err != nil {
		return nil, nil, err
	}
	body := defaultBodyProps
	if a.BodyProperties != nil {
		body = *a.BodyProperties
	}
	if err := checkProps(body, bodyPartProps); err != nil {
		return nil, nil, err
	}
	if a.MaxBodyValueBytes < 0 {
		return nil, nil, invalidArgs("maxBodyValueBytes must not be negative")
	}
	return props, body, nil
}

func (c *call) emailGet(raw json.RawMessage) (any, error) {
	var a emailGetArgs
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if err := c.checkAccount(a.AccountID); err != nil {
		return nil, err
	}
	props, bodyProps, err := a.props()
	if err != nil {
		return nil, err
	}
	if !slices.Contains(props, "id") {
		props = append([]string{"id"}, props...)
	}
	ids, err := getIDs(a.IDs)
	if err != nil {
		return nil, err
	}
	state, err := c.state(store.TypeEmail)
	if err != nil {
		return nil, err
	}
	if a.IDs == nil {
		all, err := c.allEmailIDs()
		if err != nil {
			return nil, err
		}
		if len(all) > maxObjectsInGet {
			return nil, methodErr("requestTooLarge", "too many emails; pass ids (at most %d)", maxObjectsInGet)
		}
		ids = all
	}
	var nums []int64
	for _, sid := range ids {
		rid, _ := c.resolveCreated(sid)
		if id, ok := parseID(prefixEmail, rid); ok {
			nums = append(nums, id)
		}
	}
	rows, err := c.loadEmails(nums)
	if err != nil {
		return nil, err
	}
	if a.wantsValues() {
		var total int64
		for _, e := range rows {
			total += e.Size
		}
		if total > maxBodyFetchBytes {
			return nil, methodErr("requestTooLarge", "body values of these emails exceed %d bytes; request fewer ids", maxBodyFetchBytes)
		}
	}
	list := []any{}
	notFound := []string{}
	for _, sid := range ids {
		rid, _ := c.resolveCreated(sid)
		id, _ := parseID(prefixEmail, rid)
		e := rows[id]
		if e == nil {
			notFound = append(notFound, sid)
			continue
		}
		obj, err := c.srv.emailObject(e, nil, prefixBlob+e.BlobHash, props, bodyProps, a.bodyOpts)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				notFound = append(notFound, sid)
				continue
			}
			return nil, err
		}
		list = append(list, obj)
	}
	return map[string]any{"accountId": a.AccountID, "state": state, "list": list, "notFound": notFound}, nil
}

func (c *call) allEmailIDs() ([]string, error) {
	rows, err := c.srv.Store.DB.QueryContext(c.ctx, `SELECT id FROM messages WHERE account_id = ? ORDER BY received_at DESC, id DESC LIMIT ?`, c.acct.ID, maxObjectsInGet+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, fmtID(prefixEmail, id))
	}
	return out, rows.Err()
}

// emailObject renders an Email. For stored messages e is set; for parsed
// blobs (Email/parse) raw is given instead. blobBase is the blobId of the
// whole message, part blobIds are derived from it.
func (s *Server) emailObject(e *emailRow, raw []byte, blobBase string, props, bodyProps []string, opts bodyOpts) (map[string]any, error) {
	obj := map[string]any{}
	var tree *part
	needParse := false
	for _, p := range props {
		if !metaProps[p] || e == nil {
			needParse = true
		}
	}
	if needParse {
		if raw == nil {
			data, err := s.Store.Blobs.Get(e.BlobHash)
			if err != nil {
				return nil, err
			}
			raw = data
		}
		tree = parseMessage(raw)
	}
	partBlobBase := strings.TrimPrefix(blobBase, prefixBlob)
	var text, html, atts []*part
	lists := func() {
		if text == nil {
			text, html, atts = tree.bodyLists()
		}
	}
	renderList := func(l []*part) []any {
		out := []any{}
		for _, p := range l {
			out = append(out, p.bodyPart(partBlobBase, bodyProps))
		}
		return out
	}
	addrs := func(name string) any {
		if v, ok := tree.lastHeader(name); ok {
			return asAddresses(v)
		}
		return nil
	}
	ids := func(name string) any {
		if v, ok := tree.lastHeader(name); ok {
			if l := asMessageIDs(v); l != nil {
				return l
			}
		}
		return nil
	}
	for _, p := range props {
		switch p {
		case "id":
			if e != nil {
				obj[p] = fmtID(prefixEmail, e.ID)
			} else {
				obj[p] = nil
			}
		case "blobId":
			obj[p] = blobBase
		case "threadId":
			if e != nil {
				obj[p] = fmtID(prefixThread, e.ThreadID)
			} else {
				obj[p] = nil
			}
		case "mailboxIds":
			if e != nil {
				obj[p] = map[string]bool{fmtID(prefixMailbox, e.MailboxID): true}
			} else {
				obj[p] = nil
			}
		case "keywords":
			if e != nil {
				obj[p] = keywords(e.Flags)
			} else {
				obj[p] = nil
			}
		case "size":
			if e != nil {
				obj[p] = e.Size
			} else {
				obj[p] = len(raw)
			}
		case "receivedAt":
			if e != nil {
				obj[p] = e.ReceivedAt.Format(time.RFC3339)
			} else {
				obj[p] = nil
			}
		case "hasAttachment":
			if e != nil {
				obj[p] = e.HasAttachment
			} else {
				lists()
				obj[p] = len(atts) > 0
			}
		case "preview":
			if e != nil {
				obj[p] = e.Preview
			} else {
				lists()
				obj[p] = previewOf(text)
			}
		case "messageId":
			obj[p] = ids("Message-Id")
		case "inReplyTo":
			obj[p] = ids("In-Reply-To")
		case "references":
			obj[p] = ids("References")
		case "sender":
			obj[p] = addrs("Sender")
		case "from":
			obj[p] = addrs("From")
		case "to":
			obj[p] = addrs("To")
		case "cc":
			obj[p] = addrs("Cc")
		case "bcc":
			obj[p] = addrs("Bcc")
		case "replyTo":
			obj[p] = addrs("Reply-To")
		case "subject":
			if v, ok := tree.lastHeader("Subject"); ok {
				obj[p] = asText(v)
			} else {
				obj[p] = nil
			}
		case "sentAt":
			if v, ok := tree.lastHeader("Date"); ok {
				obj[p] = asDate(v)
			} else {
				obj[p] = nil
			}
		case "headers":
			obj[p] = tree.headersList()
		case "bodyStructure":
			obj[p] = tree.bodyPart(partBlobBase, append(append([]string{}, bodyProps...), "subParts"))
		case "textBody":
			lists()
			obj[p] = renderList(text)
		case "htmlBody":
			lists()
			obj[p] = renderList(html)
		case "attachments":
			lists()
			obj[p] = renderList(atts)
		case "bodyValues":
			lists()
			values := map[string]any{}
			add := func(l []*part) {
				for _, pt := range l {
					if strings.HasPrefix(pt.mediaType, "text/") {
						values[pt.path] = pt.bodyValue(opts.MaxBodyValueBytes)
					}
				}
			}
			if opts.FetchTextBodyValues || opts.FetchAllBodyValues {
				add(text)
			}
			if opts.FetchHTMLBodyValues || opts.FetchAllBodyValues {
				add(html)
			}
			if opts.FetchAllBodyValues {
				var walk func(*part)
				walk = func(pt *part) {
					if pt.multipart() {
						for _, ch := range pt.children {
							walk(ch)
						}
						return
					}
					add([]*part{pt})
				}
				walk(tree)
			}
			obj[p] = values
		default:
			if strings.HasPrefix(p, "header:") {
				v, _ := tree.headerProp(p)
				obj[p] = v
			}
		}
	}
	return obj, nil
}

func previewOf(text []*part) string {
	for _, p := range text {
		if p.mediaType == "text/plain" {
			v := p.bodyValue(4096)["value"].(string)
			return truncateRunes(strings.Join(strings.Fields(v), " "), 256)
		}
	}
	return ""
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func (c *call) emailChanges(raw json.RawMessage) (any, error) {
	return c.changes(raw, store.TypeEmail, prefixEmail)
}

// ---- Thread/get ----

func (c *call) threadGet(raw json.RawMessage) (any, error) {
	var a struct {
		AccountID  string    `json:"accountId"`
		IDs        *[]string `json:"ids"`
		Properties *[]string `json:"properties"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if err := c.checkAccount(a.AccountID); err != nil {
		return nil, err
	}
	if a.IDs == nil {
		return nil, methodErr("requestTooLarge", "pass the thread ids to fetch")
	}
	ids, err := getIDs(a.IDs)
	if err != nil {
		return nil, err
	}
	props, err := newPropFilter(a.Properties, []string{"id", "emailIds"}, map[string]bool{"id": true, "emailIds": true})
	if err != nil {
		return nil, err
	}
	state, err := c.state(store.TypeThread)
	if err != nil {
		return nil, err
	}
	members := map[int64][]string{}
	var nums []any
	for _, sid := range ids {
		if id, ok := parseID(prefixThread, sid); ok {
			nums = append(nums, id)
		}
	}
	for start := 0; start < len(nums); start += 400 {
		chunk := nums[start:min(start+400, len(nums))]
		rows, err := c.srv.Store.DB.QueryContext(c.ctx, `SELECT thread_id, id FROM messages WHERE account_id = ? AND thread_id IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")+`) ORDER BY received_at, id`, append([]any{c.acct.ID}, chunk...)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var t, id int64
			if err := rows.Scan(&t, &id); err != nil {
				rows.Close()
				return nil, err
			}
			members[t] = append(members[t], fmtID(prefixEmail, id))
		}
		rows.Close()
	}
	list := []any{}
	notFound := []string{}
	for _, sid := range ids {
		id, _ := parseID(prefixThread, sid)
		if m := members[id]; len(m) > 0 {
			list = append(list, props.apply(map[string]any{"id": sid, "emailIds": m}))
		} else {
			notFound = append(notFound, sid)
		}
	}
	return map[string]any{"accountId": a.AccountID, "state": state, "list": list, "notFound": notFound}, nil
}
