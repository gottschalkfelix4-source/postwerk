package jmap

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"postwerk/internal/message"
	"postwerk/internal/smtp"
	"postwerk/internal/store"
)

// ---- identities ----

type identity struct {
	ID    string
	Email string
}

// identities lists the addresses the account may send from: its own
// address and the aliases assigned to it.
func (c *call) identities() []identity {
	addrs := []string{c.acct.Email}
	seen := map[string]bool{c.acct.Email: true}
	aliases := append([]string{}, c.acct.Aliases...)
	sort.Strings(aliases)
	for _, a := range aliases {
		a = store.Normalize(a)
		if !seen[a] && c.srv.Store.SenderAllowed(c.ctx, c.acct, a) {
			seen[a] = true
			addrs = append(addrs, a)
		}
	}
	out := make([]identity, len(addrs))
	for i, a := range addrs {
		out[i] = identity{ID: "I" + base64.RawURLEncoding.EncodeToString([]byte(a)), Email: a}
	}
	return out
}

func (c *call) identityState() string {
	var b strings.Builder
	b.WriteString(c.acct.Name)
	for _, id := range c.identities() {
		b.WriteString("\x00" + id.Email)
	}
	h := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(h[:8])
}

func (c *call) identityGet(raw json.RawMessage) (any, error) {
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
	all := []string{"id", "name", "email", "replyTo", "bcc", "textSignature", "htmlSignature", "mayDelete"}
	valid := map[string]bool{}
	for _, p := range all {
		valid[p] = true
	}
	props, err := newPropFilter(a.Properties, all, valid)
	if err != nil {
		return nil, err
	}
	list := []any{}
	found := map[string]bool{}
	for _, id := range c.identities() {
		if a.IDs != nil {
			want := false
			for _, x := range *a.IDs {
				want = want || x == id.ID
			}
			if !want {
				continue
			}
		}
		found[id.ID] = true
		list = append(list, props.apply(map[string]any{
			"id": id.ID, "name": c.acct.Name, "email": id.Email, "replyTo": nil, "bcc": nil,
			"textSignature": "", "htmlSignature": "", "mayDelete": false,
		}))
	}
	notFound := []string{}
	if a.IDs != nil {
		for _, x := range *a.IDs {
			if !found[x] {
				notFound = append(notFound, x)
			}
		}
	}
	return map[string]any{"accountId": a.AccountID, "state": c.identityState(), "list": list, "notFound": notFound}, nil
}

func (c *call) identityChanges(raw json.RawMessage) (any, error) {
	var a changesArgs
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if err := c.checkAccount(a.AccountID); err != nil {
		return nil, err
	}
	state := c.identityState()
	if a.SinceState != state {
		return nil, methodErr("cannotCalculateChanges", "identities changed; fetch them again")
	}
	return &changesResponse{AccountID: a.AccountID, OldState: state, NewState: state, Created: []string{}, Updated: []string{}, Destroyed: []string{}}, nil
}

// ---- EmailSubmission ----

type envelopeAddr struct {
	Email      string         `json:"email"`
	Parameters map[string]any `json:"parameters"`
}

type envelope struct {
	MailFrom envelopeAddr   `json:"mailFrom"`
	RcptTo   []envelopeAddr `json:"rcptTo"`
}

func submissionObject(s *store.Submission) map[string]any {
	var env any
	if s.EnvelopeJSON != "" {
		var e envelope
		if json.Unmarshal([]byte(s.EnvelopeJSON), &e) == nil {
			env = e
		}
	}
	return map[string]any{
		"id": fmtID(prefixSubmission, s.ID), "identityId": s.IdentityID, "emailId": fmtID(prefixEmail, s.EmailID),
		"threadId": fmtID(prefixThread, s.ThreadID), "envelope": env, "sendAt": s.SendAt.Format(time.RFC3339),
		"undoStatus": "final", "deliveryStatus": nil, "dsnBlobIds": []string{}, "mdnBlobIds": []string{},
	}
}

func (c *call) submissionGet(raw json.RawMessage) (any, error) {
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
	ids, err := getIDs(a.IDs)
	if err != nil {
		return nil, err
	}
	props, err := newPropFilter(a.Properties, []string{"id", "identityId", "emailId", "threadId", "envelope", "sendAt", "undoStatus",
		"deliveryStatus", "dsnBlobIds", "mdnBlobIds"}, nil)
	if err != nil {
		return nil, err
	}
	state, err := c.state(store.TypeEmailSubmission)
	if err != nil {
		return nil, err
	}
	subs, err := c.srv.Store.Submissions(c.ctx, c.acct.ID)
	if err != nil {
		return nil, err
	}
	byID := map[string]*store.Submission{}
	for _, s := range subs {
		byID[fmtID(prefixSubmission, s.ID)] = s
	}
	list := []any{}
	notFound := []string{}
	if ids == nil {
		for _, s := range subs {
			list = append(list, props.apply(submissionObject(s)))
		}
	}
	for _, sid := range ids {
		rid, _ := c.resolveCreated(sid)
		if s := byID[rid]; s != nil {
			list = append(list, props.apply(submissionObject(s)))
		} else {
			notFound = append(notFound, sid)
		}
	}
	return map[string]any{"accountId": a.AccountID, "state": state, "list": list, "notFound": notFound}, nil
}

func (c *call) submissionChanges(raw json.RawMessage) (any, error) {
	return c.changes(raw, store.TypeEmailSubmission, prefixSubmission)
}

func (c *call) submissionQuery(raw json.RawMessage) (any, error) {
	var a struct {
		AccountID string `json:"accountId"`
		Filter    *struct {
			IdentityIDs []string `json:"identityIds"`
			EmailIDs    []string `json:"emailIds"`
			ThreadIDs   []string `json:"threadIds"`
			UndoStatus  string   `json:"undoStatus"`
			Before      string   `json:"before"`
			After       string   `json:"after"`
		} `json:"filter"`
		queryWindow
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if err := c.checkAccount(a.AccountID); err != nil {
		return nil, err
	}
	state, err := c.state(store.TypeEmailSubmission)
	if err != nil {
		return nil, err
	}
	subs, err := c.srv.Store.Submissions(c.ctx, c.acct.ID)
	if err != nil {
		return nil, err
	}
	in := func(l []string, v string) bool {
		if l == nil {
			return true
		}
		for _, x := range l {
			if x == v {
				return true
			}
		}
		return false
	}
	var ids []string
	for _, s := range subs {
		if f := a.Filter; f != nil {
			if !in(f.IdentityIDs, s.IdentityID) || !in(f.EmailIDs, fmtID(prefixEmail, s.EmailID)) || !in(f.ThreadIDs, fmtID(prefixThread, s.ThreadID)) {
				continue
			}
			if f.UndoStatus != "" && f.UndoStatus != "final" {
				continue
			}
			if t, err := time.Parse(time.RFC3339, f.Before); err == nil && !s.SendAt.Before(t) {
				continue
			}
			if t, err := time.Parse(time.RFC3339, f.After); err == nil && s.SendAt.Before(t) {
				continue
			}
		}
		ids = append(ids, fmtID(prefixSubmission, s.ID))
	}
	page, pos, _, err := a.page(ids, maxQueryLimit)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"accountId": a.AccountID, "queryState": state, "canCalculateChanges": false, "position": pos, "ids": page}
	if a.CalculateTotal {
		out["total"] = len(ids)
	}
	return out, nil
}

func (c *call) submissionSet(raw json.RawMessage) (any, error) {
	var a struct {
		setArgs
		OnSuccessUpdateEmail  map[string]json.RawMessage `json:"onSuccessUpdateEmail"`
		OnSuccessDestroyEmail []string                   `json:"onSuccessDestroyEmail"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	old, err := a.check(c, store.TypeEmailSubmission)
	if err != nil {
		return nil, err
	}
	emailOld, err := c.state(store.TypeEmail)
	if err != nil {
		return nil, err
	}
	resp := newSetResponse(a.AccountID, old)
	// submission id -> email id of submissions handled in this call
	emailOf := map[string]string{}
	for cid, objRaw := range a.Create {
		sub, serr := c.createSubmission(objRaw)
		if serr != nil {
			resp.NotCreated[cid] = serr
			continue
		}
		sid := fmtID(prefixSubmission, sub.ID)
		c.created[cid] = sid
		emailOf[sid] = fmtID(prefixEmail, sub.EmailID)
		resp.Created[cid] = map[string]any{"id": sid, "threadId": fmtID(prefixThread, sub.ThreadID),
			"sendAt": sub.SendAt.Format(time.RFC3339), "undoStatus": "final"}
	}
	for sid, patchRaw := range a.Update {
		var patch map[string]json.RawMessage
		if err := json.Unmarshal(patchRaw, &patch); err != nil {
			resp.NotUpdated[sid] = setErr("invalidPatch", "patch must be an object")
			continue
		}
		if v, ok := patch["undoStatus"]; ok && len(patch) == 1 {
			var s string
			_ = json.Unmarshal(v, &s)
			if s == "canceled" {
				resp.NotUpdated[sid] = setErr("cannotUnsend", "the email was already sent")
				continue
			}
		}
		resp.NotUpdated[sid] = setErr("invalidProperties", "submissions cannot be changed")
	}
	for _, sid := range a.Destroy {
		rid, _ := c.resolveCreated(sid)
		id, ok := parseID(prefixSubmission, rid)
		if !ok || c.srv.Store.DeleteSubmission(c.ctx, c.acct.ID, id) != nil {
			resp.NotDestroyed[sid] = setErr("notFound", "submission not found")
			continue
		}
		resp.Destroyed = append(resp.Destroyed, sid)
	}
	if resp.NewState, err = c.state(store.TypeEmailSubmission); err != nil {
		return nil, err
	}

	if len(a.OnSuccessUpdateEmail) == 0 && len(a.OnSuccessDestroyEmail) == 0 {
		return resp, nil
	}
	// Implicit Email/set for the successfully sent emails.
	emailFor := func(ref string) (string, bool) {
		rid, ok := c.resolveCreated(ref)
		if !ok {
			return "", false
		}
		e, ok := emailOf[rid]
		return e, ok
	}
	er := newSetResponse(a.AccountID, emailOld)
	for ref, patch := range a.OnSuccessUpdateEmail {
		eid, ok := emailFor(ref)
		if !ok {
			continue
		}
		if serr := c.updateEmail(eid, patch); serr != nil {
			er.NotUpdated[eid] = serr
		} else {
			er.Updated[eid] = nil
		}
	}
	for _, ref := range a.OnSuccessDestroyEmail {
		eid, ok := emailFor(ref)
		if !ok {
			continue
		}
		if serr := c.destroyEmail(eid); serr != nil {
			er.NotDestroyed[eid] = serr
		} else {
			er.Destroyed = append(er.Destroyed, eid)
		}
	}
	if er.NewState, err = c.state(store.TypeEmail); err != nil {
		return nil, err
	}
	c.extra = append(c.extra, &response{Name: "Email/set", Args: er, CallID: c.current})
	return resp, nil
}

func (c *call) createSubmission(objRaw json.RawMessage) (*store.Submission, *setError) {
	var o struct {
		IdentityID string    `json:"identityId"`
		EmailID    string    `json:"emailId"`
		Envelope   *envelope `json:"envelope"`
	}
	if err := json.Unmarshal(objRaw, &o); err != nil {
		return nil, setErr("invalidProperties", err.Error())
	}
	var ident *identity
	for _, id := range c.identities() {
		if id.ID == o.IdentityID {
			ident = &id
			break
		}
	}
	if ident == nil {
		return nil, setErr("invalidProperties", "unknown identity", "identityId")
	}
	rid, ok := c.resolveCreated(o.EmailID)
	eid, ok2 := parseID(prefixEmail, rid)
	if !ok || !ok2 {
		return nil, setErr("invalidProperties", "unknown email", "emailId")
	}
	e, err := c.loadEmail(eid)
	if err != nil {
		return nil, setErr("invalidProperties", "unknown email", "emailId")
	}
	raw, err := c.srv.Store.Blobs.Get(e.BlobHash)
	if err != nil {
		return nil, setErr("invalidEmail", "the email content is missing")
	}
	if int64(len(raw)) > c.srv.maxSize() {
		return nil, setErr("tooLarge", "the email exceeds the maximum message size")
	}
	tree := parseMessage(raw)
	fromHdr, ok := tree.lastHeader("From")
	if !ok || len(asAddresses(fromHdr)) == 0 {
		return nil, setErr("invalidEmail", "the email has no From address")
	}
	for _, a := range asAddresses(fromHdr) {
		if !c.srv.Store.SenderAllowed(c.ctx, c.acct, a.Email) {
			return nil, setErr("forbiddenFrom", "the From address "+a.Email+" does not belong to this account")
		}
	}
	env := envelope{MailFrom: envelopeAddr{Email: ident.Email}}
	if o.Envelope != nil {
		env = *o.Envelope
		if !c.srv.Store.SenderAllowed(c.ctx, c.acct, env.MailFrom.Email) {
			return nil, setErr("forbiddenMailFrom", "the envelope sender does not belong to this account")
		}
	} else {
		seen := map[string]bool{}
		for _, h := range []string{"To", "Cc", "Bcc"} {
			for _, v := range tree.header(h) {
				for _, a := range asAddresses(v) {
					addr := store.Normalize(a.Email)
					if addr != "" && !seen[addr] {
						seen[addr] = true
						env.RcptTo = append(env.RcptTo, envelopeAddr{Email: addr})
					}
				}
			}
		}
	}
	var rcpts []string
	for _, r := range env.RcptTo {
		addr := store.Normalize(r.Email)
		if addr == "" || !strings.Contains(addr, "@") || strings.ContainsAny(addr, " <>\r\n") {
			return nil, &setError{Type: "invalidRecipients", Description: "invalid recipient " + r.Email}
		}
		rcpts = append(rcpts, addr)
	}
	if len(rcpts) == 0 {
		return nil, setErr("noRecipients", "the email has no recipients")
	}
	if len(rcpts) > 100 {
		return nil, setErr("tooManyRecipients", "at most 100 recipients per submission")
	}
	if c.srv.Delivery == nil {
		return nil, setErr("forbiddenToSend", "sending is not available")
	}
	// Bcc recipients must not see each other.
	data := message.StripHeaders(raw, func(name, _ string) bool { return strings.EqualFold(name, "Bcc") })
	if _, err := c.srv.Delivery.Submit(c.ctx, c.acct, store.Normalize(env.MailFrom.Email), rcpts, data, false); err != nil {
		var rep *smtp.Reply
		if errors.As(err, &rep) {
			if strings.Contains(rep.Text, "From") || strings.Contains(rep.Text, "Sender") {
				return nil, setErr("forbiddenFrom", rep.Text)
			}
			return nil, setErr("forbiddenToSend", rep.Text)
		}
		c.srv.Log.Error("jmap submission failed", "component", "jmap", "user", c.acct.Email, "err", err)
		return nil, setErr("forbiddenToSend", "the email could not be sent")
	}
	envJSON, _ := json.Marshal(env)
	sub := &store.Submission{IdentityID: ident.ID, EmailID: e.ID, ThreadID: e.ThreadID, EnvelopeJSON: string(envJSON), SendAt: time.Now().UTC()}
	if err := c.srv.Store.CreateSubmission(c.ctx, c.acct.ID, sub); err != nil {
		c.srv.Log.Error("jmap: recording submission failed", "component", "jmap", "err", err)
	}
	return sub, nil
}
