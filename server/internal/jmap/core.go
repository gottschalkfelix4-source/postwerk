// Package jmap implements a JMAP server (RFC 8620 core, RFC 8621 mail and
// submission, RFC 8620 section 7.3 EventSource push) on top of the store.
package jmap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"postwerk/internal/store"
)

// Capability URIs.
const (
	capCore       = "urn:ietf:params:jmap:core"
	capMail       = "urn:ietf:params:jmap:mail"
	capSubmission = "urn:ietf:params:jmap:submission"
)

// Limits announced in the session resource and enforced by the server.
const (
	maxSizeRequest        = 10 << 20
	maxConcurrentUpload   = 4
	maxConcurrentRequests = 8
	maxCallsInRequest     = 32
	maxObjectsInGet       = 500
	maxObjectsInSet       = 500
)

// ---- wire types ----

type request struct {
	Using       []string          `json:"using"`
	MethodCalls []invocation      `json:"methodCalls"`
	CreatedIDs  map[string]string `json:"createdIds,omitempty"`
}

type invocation struct {
	Name   string
	Args   json.RawMessage
	CallID string
}

func (inv *invocation) UnmarshalJSON(b []byte) error {
	var parts []json.RawMessage
	if err := json.Unmarshal(b, &parts); err != nil || len(parts) != 3 {
		return errors.New("invocation must be [name, arguments, callId]")
	}
	if err := json.Unmarshal(parts[0], &inv.Name); err != nil {
		return err
	}
	if err := json.Unmarshal(parts[2], &inv.CallID); err != nil {
		return err
	}
	if t := bytes.TrimSpace(parts[1]); len(t) == 0 || t[0] != '{' {
		return errors.New("arguments must be an object")
	}
	inv.Args = parts[1]
	return nil
}

type response struct {
	Name   string
	Args   any
	CallID string

	decoded any // generic form of Args for result references
}

func (r *response) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{r.Name, r.Args, r.CallID})
}

type responseBody struct {
	MethodResponses []*response       `json:"methodResponses"`
	CreatedIDs      map[string]string `json:"createdIds,omitempty"`
	SessionState    string            `json:"sessionState"`
}

// mErr is a method-level error (RFC 8620 section 3.6.2).
type mErr struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

func (e *mErr) Error() string { return e.Type + ": " + e.Description }

func methodErr(typ, format string, args ...any) *mErr {
	return &mErr{Type: typ, Description: fmt.Sprintf(format, args...)}
}

func invalidArgs(format string, args ...any) *mErr {
	return methodErr("invalidArguments", format, args...)
}

// setError is a per-object error in /set responses (RFC 8620 section 5.3).
type setError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
	ExistingID  string   `json:"existingId,omitempty"`
}

func setErr(typ, desc string, props ...string) *setError {
	return &setError{Type: typ, Description: desc, Properties: props}
}

// ---- ids ----

// Object ids carry a type prefix so they are never purely numeric.
const (
	prefixAccount    = "A"
	prefixMailbox    = "M"
	prefixEmail      = "E"
	prefixThread     = "T"
	prefixSubmission = "S"
	prefixBlob       = "G"
)

func fmtID(prefix string, id int64) string { return prefix + strconv.FormatInt(id, 10) }

func parseID(prefix, s string) (int64, bool) {
	rest, ok := strings.CutPrefix(s, prefix)
	if !ok || rest == "" || rest[0] == '0' || len(rest) > 18 {
		return 0, false
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	return n, err == nil && n > 0
}

// ---- method dispatch ----

type call struct {
	srv       *Server
	ctx       context.Context
	acct      *store.Account
	using     map[string]bool
	created   map[string]string // creation id -> object id, for "#id" references
	responses []*response
	current   string      // callId of the method being processed
	extra     []*response // implicit responses (e.g. Email/set after EmailSubmission/set)
}

type method struct {
	capability string
	fn         func(c *call, args json.RawMessage) (any, error)
}

var methods map[string]method

func init() {
	methods = map[string]method{
		"Core/echo": {capCore, func(c *call, args json.RawMessage) (any, error) { return args, nil }},

		"Mailbox/get":          {capMail, (*call).mailboxGet},
		"Mailbox/changes":      {capMail, (*call).mailboxChanges},
		"Mailbox/query":        {capMail, (*call).mailboxQuery},
		"Mailbox/queryChanges": {capMail, queryChangesUnsupported},
		"Mailbox/set":          {capMail, (*call).mailboxSet},

		"Thread/get":     {capMail, (*call).threadGet},
		"Thread/changes": {capMail, (*call).threadChanges},

		"Email/get":          {capMail, (*call).emailGet},
		"Email/changes":      {capMail, (*call).emailChanges},
		"Email/query":        {capMail, (*call).emailQuery},
		"Email/queryChanges": {capMail, queryChangesUnsupported},
		"Email/set":          {capMail, (*call).emailSet},
		"Email/import":       {capMail, (*call).emailImport},
		"Email/parse":        {capMail, (*call).emailParse},

		"SearchSnippet/get": {capMail, (*call).searchSnippetGet},

		"Identity/get":     {capSubmission, (*call).identityGet},
		"Identity/changes": {capSubmission, (*call).identityChanges},

		"EmailSubmission/get":     {capSubmission, (*call).submissionGet},
		"EmailSubmission/changes": {capSubmission, (*call).submissionChanges},
		"EmailSubmission/query":   {capSubmission, (*call).submissionQuery},
		"EmailSubmission/set":     {capSubmission, (*call).submissionSet},
	}
}

// run processes all method calls of a request in order.
func (c *call) run(req *request) []*response {
	for _, inv := range req.MethodCalls {
		c.current = inv.CallID
		c.extra = nil
		out, err := c.invoke(inv)
		if err != nil {
			var me *mErr
			if !errors.As(err, &me) {
				id := randomID()
				c.srv.Log.Error("jmap method failed", "method", inv.Name, "user", c.acct.Email, "request_id", id, "err", err)
				me = methodErr("serverFail", "internal error (reference %s)", id)
			}
			c.responses = append(c.responses, &response{Name: "error", Args: me, CallID: inv.CallID})
			continue
		}
		c.responses = append(c.responses, &response{Name: inv.Name, Args: out, CallID: inv.CallID})
		c.responses = append(c.responses, c.extra...)
	}
	return c.responses
}

func (c *call) invoke(inv invocation) (any, error) {
	m, ok := methods[inv.Name]
	if !ok || !c.using[m.capability] {
		return nil, methodErr("unknownMethod", "unknown method %q", inv.Name)
	}
	args, err := c.resolveRefs(inv.Args)
	if err != nil {
		return nil, err
	}
	return m.fn(c, args)
}

// resolveRefs replaces "#name" result references (RFC 8620 section 3.7).
func (c *call) resolveRefs(raw json.RawMessage) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, invalidArgs("arguments must be a JSON object")
	}
	var refs []string
	for k := range obj {
		if strings.HasPrefix(k, "#") {
			refs = append(refs, k)
		}
	}
	if len(refs) == 0 {
		return raw, nil
	}
	for _, k := range refs {
		name := k[1:]
		if _, dup := obj[name]; dup {
			return nil, invalidArgs("argument %q given both directly and as a reference", name)
		}
		var ref struct {
			ResultOf string `json:"resultOf"`
			Name     string `json:"name"`
			Path     string `json:"path"`
		}
		if err := json.Unmarshal(obj[k], &ref); err != nil {
			return nil, methodErr("invalidResultReference", "malformed result reference %q", k)
		}
		val, err := c.evalRef(ref.ResultOf, ref.Name, ref.Path)
		if err != nil {
			return nil, methodErr("invalidResultReference", "%s: %v", k, err)
		}
		b, err := json.Marshal(val)
		if err != nil {
			return nil, err
		}
		obj[name] = b
		delete(obj, k)
	}
	return json.Marshal(obj)
}

func (c *call) evalRef(resultOf, name, path string) (any, error) {
	var r *response
	for i := len(c.responses) - 1; i >= 0; i-- {
		if c.responses[i].CallID == resultOf {
			r = c.responses[i]
			if r.Name == name {
				break
			}
			r = nil
		}
	}
	if r == nil {
		return nil, fmt.Errorf("no %s response for call %q", name, resultOf)
	}
	if r.decoded == nil {
		b, err := json.Marshal(r.Args)
		if err != nil {
			return nil, err
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		if err := dec.Decode(&r.decoded); err != nil {
			return nil, err
		}
	}
	return evalPointer(r.decoded, path)
}

// evalPointer evaluates a JSON pointer with the JMAP "*" extension: on an
// array, "*" applies the rest of the path to every item and flattens
// resulting arrays.
func evalPointer(v any, path string) (any, error) {
	if path == "" {
		return v, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("path must start with /")
	}
	toks := strings.Split(path[1:], "/")
	for i, t := range toks {
		toks[i] = strings.ReplaceAll(strings.ReplaceAll(t, "~1", "/"), "~0", "~")
	}
	return walkPointer(v, toks)
}

func walkPointer(v any, toks []string) (any, error) {
	if len(toks) == 0 {
		return v, nil
	}
	t := toks[0]
	switch x := v.(type) {
	case []any:
		if t == "*" {
			out := []any{}
			for _, item := range x {
				r, err := walkPointer(item, toks[1:])
				if err != nil {
					return nil, err
				}
				if arr, ok := r.([]any); ok {
					out = append(out, arr...)
				} else {
					out = append(out, r)
				}
			}
			return out, nil
		}
		i, err := strconv.Atoi(t)
		if err != nil || i < 0 || i >= len(x) {
			return nil, fmt.Errorf("index %q out of range", t)
		}
		return walkPointer(x[i], toks[1:])
	case map[string]any:
		val, ok := x[t]
		if !ok {
			return nil, fmt.Errorf("property %q not found", t)
		}
		return walkPointer(val, toks[1:])
	}
	return nil, fmt.Errorf("cannot descend into %q", t)
}

// decodeArgs unmarshals method arguments into dst.
func decodeArgs(raw json.RawMessage, dst any) error {
	if err := json.Unmarshal(raw, dst); err != nil {
		return invalidArgs("%v", err)
	}
	return nil
}

// checkAccount verifies the accountId argument.
func (c *call) checkAccount(id string) error {
	if id != fmtID(prefixAccount, c.acct.ID) {
		return methodErr("accountNotFound", "unknown account %q", id)
	}
	return nil
}

// resolveCreated maps "#creationId" to the id created earlier in the request.
func (c *call) resolveCreated(id string) (string, bool) {
	if cid, ok := strings.CutPrefix(id, "#"); ok {
		v, ok := c.created[cid]
		return v, ok
	}
	return id, true
}

func (c *call) state(typ string) (string, error) {
	v, err := c.srv.Store.ChangeState(c.ctx, c.acct.ID, typ)
	return strconv.FormatInt(v, 10), err
}

// ---- generic /changes ----

type changesArgs struct {
	AccountID  string `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges *int   `json:"maxChanges"`
}

type changesResponse struct {
	AccountID      string   `json:"accountId"`
	OldState       string   `json:"oldState"`
	NewState       string   `json:"newState"`
	HasMoreChanges bool     `json:"hasMoreChanges"`
	Created        []string `json:"created"`
	Updated        []string `json:"updated"`
	Destroyed      []string `json:"destroyed"`

	UpdatedProperties []string `json:"updatedProperties,omitempty"`
}

func (c *call) changes(raw json.RawMessage, typ, prefix string) (*changesResponse, error) {
	var a changesArgs
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if err := c.checkAccount(a.AccountID); err != nil {
		return nil, err
	}
	max := 0
	if a.MaxChanges != nil {
		if *a.MaxChanges <= 0 {
			return nil, invalidArgs("maxChanges must be positive")
		}
		max = *a.MaxChanges
	}
	since, err := strconv.ParseInt(a.SinceState, 10, 64)
	if err != nil {
		return nil, methodErr("cannotCalculateChanges", "unknown state %q", a.SinceState)
	}
	cs, err := c.srv.Store.Changes(c.ctx, c.acct.ID, typ, since, max)
	if errors.Is(err, store.ErrCannotCalculateChanges) {
		return nil, methodErr("cannotCalculateChanges", "state %q is too old or unknown", a.SinceState)
	}
	if err != nil {
		return nil, err
	}
	ids := func(l []int64) []string {
		out := make([]string, len(l))
		for i, v := range l {
			out[i] = fmtID(prefix, v)
		}
		return out
	}
	return &changesResponse{
		AccountID: a.AccountID, OldState: a.SinceState, NewState: strconv.FormatInt(cs.NewState, 10),
		HasMoreChanges: cs.HasMore, Created: ids(cs.Created), Updated: ids(cs.Updated), Destroyed: ids(cs.Destroyed),
	}, nil
}

func queryChangesUnsupported(c *call, raw json.RawMessage) (any, error) {
	var a struct {
		AccountID string `json:"accountId"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if err := c.checkAccount(a.AccountID); err != nil {
		return nil, err
	}
	return nil, methodErr("cannotCalculateChanges", "query changes are not tracked; run the query again")
}

// ---- generic helpers ----

// getIDs parses the ids argument of a /get call (nil means "all").
func getIDs(ids *[]string) ([]string, error) {
	if ids == nil {
		return nil, nil
	}
	if len(*ids) > maxObjectsInGet {
		return nil, methodErr("requestTooLarge", "at most %d ids per call", maxObjectsInGet)
	}
	return dedupe(*ids), nil
}

// dedupe drops repeated ids (RFC 8620 §5.1: each id is answered once), so a
// request cannot multiply the work for one large object.
func dedupe(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// maxBodyFetchBytes caps how much message data one /get or /parse call may
// decode into body values; larger requests must be split by the client.
const maxBodyFetchBytes = 64 << 20

func (o bodyOpts) wantsValues() bool {
	return o.FetchTextBodyValues || o.FetchHTMLBodyValues || o.FetchAllBodyValues
}

// propFilter decides which properties of an object are returned.
type propFilter map[string]bool

func newPropFilter(props *[]string, defaults []string, valid map[string]bool) (propFilter, error) {
	list := defaults
	if props != nil {
		list = *props
	}
	f := propFilter{"id": true}
	for _, p := range list {
		if valid != nil && !valid[p] {
			return nil, invalidArgs("unknown property %q", p)
		}
		f[p] = true
	}
	return f, nil
}

func (f propFilter) apply(obj map[string]any) map[string]any {
	for k := range obj {
		if !f[k] {
			delete(obj, k)
		}
	}
	return obj
}
