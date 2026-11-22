package jmap

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"postwerk/internal/delivery"
	"postwerk/internal/store"
)

// ---- Email/set ----

func (c *call) emailSet(raw json.RawMessage) (any, error) {
	var a setArgs
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	old, err := a.check(c, store.TypeEmail)
	if err != nil {
		return nil, err
	}
	resp := newSetResponse(a.AccountID, old)
	for cid, objRaw := range a.Create {
		obj, serr := c.createEmail(objRaw)
		if serr != nil {
			resp.NotCreated[cid] = serr
			continue
		}
		c.created[cid] = obj["id"].(string)
		resp.Created[cid] = obj
	}
	for sid, patch := range a.Update {
		if serr := c.updateEmail(sid, patch); serr != nil {
			resp.NotUpdated[sid] = serr
			continue
		}
		resp.Updated[sid] = nil
	}
	for _, sid := range a.Destroy {
		if serr := c.destroyEmail(sid); serr != nil {
			resp.NotDestroyed[sid] = serr
			continue
		}
		resp.Destroyed = append(resp.Destroyed, sid)
	}
	if resp.NewState, err = c.state(store.TypeEmail); err != nil {
		return nil, err
	}
	return resp, nil
}

// singleMailbox resolves a mailboxIds set; Postwerk stores every email in
// exactly one mailbox.
func (c *call) singleMailbox(ids map[string]bool) (int64, *setError) {
	var out []int64
	for sid, on := range ids {
		if !on {
			return 0, setErr("invalidProperties", "mailboxIds values must be true", "mailboxIds")
		}
		rid, ok := c.resolveCreated(sid)
		id, ok2 := parseID(prefixMailbox, rid)
		if !ok || !ok2 {
			return 0, setErr("invalidProperties", "unknown mailbox "+sid, "mailboxIds")
		}
		out = append(out, id)
	}
	if len(out) != 1 {
		return 0, setErr("invalidProperties", "an email must be in exactly one mailbox on this server", "mailboxIds")
	}
	if _, err := c.srv.Store.MailboxOwned(c.ctx, c.acct.ID, out[0]); err != nil {
		return 0, setErr("invalidProperties", "unknown mailbox", "mailboxIds")
	}
	return out[0], nil
}

func checkKeywords(kw map[string]bool) (map[string]bool, *setError) {
	out := map[string]bool{}
	for k, on := range kw {
		if !on || !validKeyword(k) {
			return nil, setErr("invalidProperties", "invalid keyword "+k, "keywords")
		}
		out[strings.ToLower(k)] = true
	}
	return out, nil
}

// storeEmail saves raw bytes as a new message with keywords.
func (c *call) storeEmail(raw []byte, mailbox int64, kw map[string]bool, received time.Time) (map[string]any, *setError) {
	if int64(len(raw)) > c.srv.maxSize() {
		return nil, setErr("tooLarge", "the email exceeds the maximum message size")
	}
	if c.srv.Delivery == nil {
		return nil, setErr("forbidden", "storing emails is not available")
	}
	f := applyKeywords(kw, false)
	m, err := c.srv.Delivery.ImportMessage(c.ctx, c.acct, mailbox, raw, delivery.ImportOptions{
		ReceivedAt: received, Seen: f.Seen, Flagged: f.Flagged, Answered: f.Answered, SkipAI: true,
	})
	if err != nil {
		if errors.Is(err, store.ErrQuota) {
			return nil, setErr("overQuota", "the account quota is exhausted")
		}
		c.srv.Log.Error("jmap: storing email failed", "component", "jmap", "user", c.acct.Email, "err", err)
		return nil, setErr("serverFail", "the email could not be stored")
	}
	if f.Draft || len(f.Keywords) > 0 {
		_ = c.srv.Store.SetIMAPFlags(c.ctx, c.acct.ID, m.ID, f)
	}
	return map[string]any{
		"id": fmtID(prefixEmail, m.ID), "blobId": prefixBlob + m.BlobHash, "threadId": fmtID(prefixThread, m.ThreadID), "size": m.Size,
	}, nil
}

func (c *call) createEmail(objRaw json.RawMessage) (map[string]any, *setError) {
	var o emailCreate
	if err := json.Unmarshal(objRaw, &o); err != nil {
		return nil, setErr("invalidProperties", err.Error())
	}
	mailbox, serr := c.singleMailbox(o.MailboxIDs)
	if serr != nil {
		return nil, serr
	}
	kw, serr := checkKeywords(o.Keywords)
	if serr != nil {
		return nil, serr
	}
	received := time.Now()
	if o.ReceivedAt != nil {
		t, err := time.Parse(time.RFC3339, *o.ReceivedAt)
		if err != nil {
			return nil, setErr("invalidProperties", "invalid receivedAt", "receivedAt")
		}
		received = t
	}
	raw, err := c.buildEmail(objRaw, &o)
	if err != nil {
		var be *buildErr
		if errors.As(err, &be) {
			return nil, setErr("invalidProperties", be.desc, be.props...)
		}
		return nil, setErr("invalidProperties", err.Error())
	}
	return c.storeEmail(raw, mailbox, kw, received)
}

// patchKey decodes a JSON pointer segment.
func patchKey(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
}

func (c *call) updateEmail(sid string, patchRaw json.RawMessage) *setError {
	rid, ok := c.resolveCreated(sid)
	id, ok2 := parseID(prefixEmail, rid)
	if !ok || !ok2 {
		return setErr("notFound", "email not found")
	}
	e, err := c.loadEmail(id)
	if err != nil {
		return setErr("notFound", "email not found")
	}
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(patchRaw, &patch); err != nil {
		return setErr("invalidPatch", "patch must be an object")
	}
	kw := keywords(e.Flags)
	mbs := map[string]bool{fmtID(prefixMailbox, e.MailboxID): true}
	kwChanged, mbChanged := false, false
	for k, v := range patch {
		switch {
		case k == "keywords":
			var m map[string]bool
			if err := json.Unmarshal(v, &m); err != nil {
				return setErr("invalidProperties", "keywords must be an object", "keywords")
			}
			checked, serr := checkKeywords(m)
			if serr != nil {
				return serr
			}
			kw, kwChanged = checked, true
		case strings.HasPrefix(k, "keywords/"):
			name := strings.ToLower(patchKey(strings.TrimPrefix(k, "keywords/")))
			if !validKeyword(name) {
				return setErr("invalidPatch", "invalid keyword "+name, k)
			}
			var on *bool
			if err := json.Unmarshal(v, &on); err != nil || (on != nil && !*on) {
				return setErr("invalidPatch", "keyword patches take true or null", k)
			}
			if on == nil {
				delete(kw, name)
			} else {
				kw[name] = true
			}
			kwChanged = true
		case k == "mailboxIds":
			var m map[string]bool
			if err := json.Unmarshal(v, &m); err != nil {
				return setErr("invalidProperties", "mailboxIds must be an object", "mailboxIds")
			}
			mbs, mbChanged = m, true
		case strings.HasPrefix(k, "mailboxIds/"):
			mid := patchKey(strings.TrimPrefix(k, "mailboxIds/"))
			var on *bool
			if err := json.Unmarshal(v, &on); err != nil || (on != nil && !*on) {
				return setErr("invalidPatch", "mailboxIds patches take true or null", k)
			}
			if on == nil {
				// A mailbox may be referenced by creation id as well.
				delete(mbs, mid)
				if rid, ok := c.resolveCreated(mid); ok {
					delete(mbs, rid)
				}
			} else {
				mbs[mid] = true
			}
			mbChanged = true
		default:
			return setErr("invalidProperties", "property cannot be changed", k)
		}
	}
	var target int64
	if mbChanged {
		// Normalize creation ids before counting.
		norm := map[string]bool{}
		for k, v := range mbs {
			rid, _ := c.resolveCreated(k)
			norm[rid] = v
		}
		var serr *setError
		if target, serr = c.singleMailbox(norm); serr != nil {
			return serr
		}
	}
	if kwChanged {
		if err := c.srv.Store.SetIMAPFlags(c.ctx, c.acct.ID, e.ID, applyKeywords(kw, e.Flags.Deleted)); err != nil {
			return setErr("notFound", "email not found")
		}
	}
	if mbChanged && target != e.MailboxID {
		if err := c.srv.Store.UpdateMessage(c.ctx, c.acct.ID, e.ID, store.MessagePatch{MailboxID: &target}); err != nil {
			return setErr("notFound", "email not found")
		}
	}
	return nil
}

func (c *call) destroyEmail(sid string) *setError {
	rid, ok := c.resolveCreated(sid)
	id, ok2 := parseID(prefixEmail, rid)
	if !ok || !ok2 {
		return setErr("notFound", "email not found")
	}
	if err := c.srv.Store.DeleteMessage(c.ctx, c.acct.ID, id); err != nil {
		return setErr("notFound", "email not found")
	}
	return nil
}

// ---- Email/import ----

func (c *call) emailImport(raw json.RawMessage) (any, error) {
	var a struct {
		AccountID string `json:"accountId"`
		IfInState *string
		Emails    map[string]struct {
			BlobID     string          `json:"blobId"`
			MailboxIDs map[string]bool `json:"mailboxIds"`
			Keywords   map[string]bool `json:"keywords"`
			ReceivedAt *string         `json:"receivedAt"`
		} `json:"emails"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if err := c.checkAccount(a.AccountID); err != nil {
		return nil, err
	}
	if len(a.Emails) > maxObjectsInSet {
		return nil, methodErr("requestTooLarge", "at most %d emails per call", maxObjectsInSet)
	}
	old, err := c.state(store.TypeEmail)
	if err != nil {
		return nil, err
	}
	if a.IfInState != nil && *a.IfInState != old {
		return nil, methodErr("stateMismatch", "state is %s", old)
	}
	created := map[string]any{}
	notCreated := map[string]*setError{}
	for cid, e := range a.Emails {
		mailbox, serr := c.singleMailbox(e.MailboxIDs)
		if serr != nil {
			notCreated[cid] = serr
			continue
		}
		kw, serr := checkKeywords(e.Keywords)
		if serr != nil {
			notCreated[cid] = serr
			continue
		}
		received := time.Now()
		if e.ReceivedAt != nil {
			t, err := time.Parse(time.RFC3339, *e.ReceivedAt)
			if err != nil {
				notCreated[cid] = setErr("invalidProperties", "invalid receivedAt", "receivedAt")
				continue
			}
			received = t
		}
		blobID, _ := c.resolveCreated(e.BlobID)
		data, _, err := c.blobData(blobID)
		if err != nil {
			notCreated[cid] = &setError{Type: "blobNotFound", Description: "blob " + e.BlobID + " not found"}
			continue
		}
		obj, serr := c.storeEmail(data, mailbox, kw, received)
		if serr != nil {
			notCreated[cid] = serr
			continue
		}
		c.created[cid] = obj["id"].(string)
		created[cid] = obj
	}
	newState, err := c.state(store.TypeEmail)
	if err != nil {
		return nil, err
	}
	return map[string]any{"accountId": a.AccountID, "oldState": old, "newState": newState, "created": created, "notCreated": notCreated}, nil
}

// ---- Email/parse ----

var defaultParseProps = []string{"messageId", "inReplyTo", "references", "sender", "from", "to", "cc", "bcc", "replyTo", "subject",
	"sentAt", "hasAttachment", "preview", "bodyValues", "textBody", "htmlBody", "attachments"}

func (c *call) emailParse(raw json.RawMessage) (any, error) {
	var a struct {
		emailGetArgs
		BlobIDs []string `json:"blobIds"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if err := c.checkAccount(a.AccountID); err != nil {
		return nil, err
	}
	if a.Properties == nil {
		a.Properties = &defaultParseProps
	}
	props, bodyProps, err := a.props()
	if err != nil {
		return nil, err
	}
	if len(a.BlobIDs) > maxObjectsInGet {
		return nil, methodErr("requestTooLarge", "at most %d blobs per call", maxObjectsInGet)
	}
	parsed := map[string]any{}
	notParsable, notFound := []string{}, []string{}
	var total int64
	for _, bid := range dedupe(a.BlobIDs) {
		rid, _ := c.resolveCreated(bid)
		data, _, err := c.blobData(rid)
		if err != nil {
			notFound = append(notFound, bid)
			continue
		}
		if total += int64(len(data)); total > maxBodyFetchBytes {
			return nil, methodErr("requestTooLarge", "blobs exceed %d bytes; request fewer blobIds", maxBodyFetchBytes)
		}
		if len(strings.TrimSpace(string(data[:min(len(data), 4096)]))) == 0 {
			notParsable = append(notParsable, bid)
			continue
		}
		obj, err := c.srv.emailObject(nil, data, rid, props, bodyProps, a.bodyOpts)
		if err != nil {
			notParsable = append(notParsable, bid)
			continue
		}
		parsed[bid] = obj
	}
	return map[string]any{"accountId": a.AccountID, "parsed": parsed, "notParsable": notParsable, "notFound": notFound}, nil
}
