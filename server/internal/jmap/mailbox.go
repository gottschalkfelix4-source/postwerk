package jmap

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode"

	"postwerk/internal/store"
)

// Mailbox names use "/" as hierarchy delimiter (as in IMAP); JMAP sees the
// last path segment as name and the mailbox of the prefix as parent.
const mailboxDelim = "/"

const maxMailboxName = 255

var roleOrder = map[string]int{"inbox": 1, "drafts": 2, "sent": 3, "archive": 4, "junk": 5, "trash": 6}

type mailboxRow struct {
	ID            int64
	FullName      string
	Role          string
	Subscribed    bool
	Total, Unread int
	Threads       int
	UnreadThreads int

	parent *mailboxRow
	name   string
}

func (c *call) loadMailboxes() ([]*mailboxRow, error) {
	rows, err := c.srv.Store.DB.QueryContext(c.ctx, `SELECT b.id, b.name, COALESCE(b.role, ''), b.subscribed,
		COUNT(m.id), COALESCE(SUM(m.seen = 0), 0), COUNT(DISTINCT m.thread_id), COUNT(DISTINCT CASE WHEN m.seen = 0 THEN m.thread_id END)
		FROM mailboxes b LEFT JOIN messages m ON m.mailbox_id = b.id
		WHERE b.account_id = ? GROUP BY b.id ORDER BY b.id`, c.acct.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*mailboxRow
	byName := map[string]*mailboxRow{}
	for rows.Next() {
		var m mailboxRow
		if err := rows.Scan(&m.ID, &m.FullName, &m.Role, &m.Subscribed, &m.Total, &m.Unread, &m.Threads, &m.UnreadThreads); err != nil {
			return nil, err
		}
		out = append(out, &m)
		byName[m.FullName] = &m
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, m := range out {
		m.name = m.FullName
		// The longest existing prefix is the parent.
		for i := strings.LastIndex(m.FullName, mailboxDelim); i > 0; i = strings.LastIndex(m.FullName[:i], mailboxDelim) {
			if p, ok := byName[m.FullName[:i]]; ok && p != m {
				m.parent = p
				m.name = m.FullName[i+1:]
				break
			}
		}
	}
	return out, nil
}

func (m *mailboxRow) object() map[string]any {
	var role any
	if m.Role != "" {
		role = m.Role
	}
	var parent any
	if m.parent != nil {
		parent = fmtID(prefixMailbox, m.parent.ID)
	}
	order := 10
	if o, ok := roleOrder[m.Role]; ok {
		order = o
	}
	return map[string]any{
		"id":            fmtID(prefixMailbox, m.ID),
		"name":          m.name,
		"parentId":      parent,
		"role":          role,
		"sortOrder":     order,
		"totalEmails":   m.Total,
		"unreadEmails":  m.Unread,
		"totalThreads":  m.Threads,
		"unreadThreads": m.UnreadThreads,
		"myRights":      m.rights(),
		"isSubscribed":  m.Subscribed,
	}
}

func (m *mailboxRow) rights() map[string]bool {
	return map[string]bool{
		"mayReadItems": true, "mayAddItems": true, "mayRemoveItems": true, "maySetSeen": true, "maySetKeywords": true,
		"mayCreateChild": true, "mayRename": m.Role != "inbox", "mayDelete": m.Role == "", "maySubmit": true,
	}
}

var mailboxProps = map[string]bool{"id": true, "name": true, "parentId": true, "role": true, "sortOrder": true, "totalEmails": true,
	"unreadEmails": true, "totalThreads": true, "unreadThreads": true, "myRights": true, "isSubscribed": true}

func (c *call) mailboxGet(raw json.RawMessage) (any, error) {
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
	props, err := newPropFilter(a.Properties, nil, mailboxProps)
	if err != nil {
		return nil, err
	}
	if a.Properties == nil {
		props = propFilter{}
		for k := range mailboxProps {
			props[k] = true
		}
	}
	state, err := c.state(store.TypeMailbox)
	if err != nil {
		return nil, err
	}
	all, err := c.loadMailboxes()
	if err != nil {
		return nil, err
	}
	list := []any{}
	notFound := []string{}
	if ids == nil {
		for _, m := range all {
			list = append(list, props.apply(m.object()))
		}
	} else {
		byID := map[int64]*mailboxRow{}
		for _, m := range all {
			byID[m.ID] = m
		}
		for _, sid := range ids {
			rid, _ := c.resolveCreated(sid)
			id, ok := parseID(prefixMailbox, rid)
			if m := byID[id]; ok && m != nil {
				list = append(list, props.apply(m.object()))
			} else {
				notFound = append(notFound, sid)
			}
		}
	}
	return map[string]any{"accountId": a.AccountID, "state": state, "list": list, "notFound": notFound}, nil
}

func (c *call) mailboxChanges(raw json.RawMessage) (any, error) {
	return c.changes(raw, store.TypeMailbox, prefixMailbox)
}

func (c *call) threadChanges(raw json.RawMessage) (any, error) {
	return c.changes(raw, store.TypeThread, prefixThread)
}

// ---- Mailbox/query ----

type queryWindow struct {
	Position       int     `json:"position"`
	Anchor         *string `json:"anchor"`
	AnchorOffset   int     `json:"anchorOffset"`
	Limit          *int    `json:"limit"`
	CalculateTotal bool    `json:"calculateTotal"`
}

// page applies position/anchor/limit to a sorted id list.
func (q queryWindow) page(ids []string, maxLimit int) (out []string, position int, limit int, err error) {
	limit = maxLimit
	if q.Limit != nil {
		if *q.Limit < 0 {
			return nil, 0, 0, invalidArgs("limit must not be negative")
		}
		if *q.Limit < limit {
			limit = *q.Limit
		}
	}
	if q.Anchor != nil {
		idx := -1
		for i, id := range ids {
			if id == *q.Anchor {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, 0, 0, methodErr("anchorNotFound", "anchor %q is not in the results", *q.Anchor)
		}
		position = idx + q.AnchorOffset
		if position < 0 {
			position = 0
		}
	} else {
		position = q.Position
		if position < 0 {
			position += len(ids)
			if position < 0 {
				position = 0
			}
		}
	}
	if position > len(ids) {
		position = len(ids)
	}
	end := position + limit
	if end > len(ids) {
		end = len(ids)
	}
	return append([]string{}, ids[position:end]...), position, limit, nil
}

type comparator struct {
	Property    string `json:"property"`
	IsAscending *bool  `json:"isAscending"`
	Collation   string `json:"collation"`
	Keyword     string `json:"keyword"`
}

func (s comparator) ascending() bool { return s.IsAscending == nil || *s.IsAscending }

func (c *call) mailboxQuery(raw json.RawMessage) (any, error) {
	var a struct {
		AccountID string          `json:"accountId"`
		Filter    json.RawMessage `json:"filter"`
		Sort      []comparator    `json:"sort"`
		queryWindow
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if err := c.checkAccount(a.AccountID); err != nil {
		return nil, err
	}
	state, err := c.state(store.TypeMailbox)
	if err != nil {
		return nil, err
	}
	all, err := c.loadMailboxes()
	if err != nil {
		return nil, err
	}
	var match func(m *mailboxRow) bool
	if len(a.Filter) > 0 && string(a.Filter) != "null" {
		if match, err = c.mailboxFilter(a.Filter); err != nil {
			return nil, err
		}
	}
	var sel []*mailboxRow
	for _, m := range all {
		if match == nil || match(m) {
			sel = append(sel, m)
		}
	}
	for _, s := range a.Sort {
		if s.Property != "sortOrder" && s.Property != "name" {
			return nil, methodErr("unsupportedSort", "cannot sort by %q", s.Property)
		}
	}
	sort.SliceStable(sel, func(i, j int) bool {
		for _, s := range a.Sort {
			var cmp int
			switch s.Property {
			case "sortOrder":
				cmp = sel[i].object()["sortOrder"].(int) - sel[j].object()["sortOrder"].(int)
			case "name":
				cmp = strings.Compare(strings.ToLower(sel[i].name), strings.ToLower(sel[j].name))
			}
			if cmp != 0 {
				return (cmp < 0) == s.ascending()
			}
		}
		return false
	})
	ids := make([]string, len(sel))
	for i, m := range sel {
		ids[i] = fmtID(prefixMailbox, m.ID)
	}
	page, pos, _, err := a.page(ids, len(ids))
	if err != nil {
		return nil, err
	}
	out := map[string]any{"accountId": a.AccountID, "queryState": state, "canCalculateChanges": false, "position": pos, "ids": page}
	if a.CalculateTotal {
		out["total"] = len(ids)
	}
	return out, nil
}

func (c *call) mailboxFilter(raw json.RawMessage) (func(*mailboxRow) bool, error) {
	var f map[string]json.RawMessage
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, invalidArgs("filter must be an object")
	}
	if op, ok := f["operator"]; ok {
		var o struct {
			Operator   string            `json:"operator"`
			Conditions []json.RawMessage `json:"conditions"`
		}
		_ = json.Unmarshal(op, &o.Operator)
		_ = json.Unmarshal(f["conditions"], &o.Conditions)
		var subs []func(*mailboxRow) bool
		for _, cond := range o.Conditions {
			fn, err := c.mailboxFilter(cond)
			if err != nil {
				return nil, err
			}
			subs = append(subs, fn)
		}
		switch o.Operator {
		case "AND":
			return func(m *mailboxRow) bool {
				for _, s := range subs {
					if !s(m) {
						return false
					}
				}
				return true
			}, nil
		case "OR":
			return func(m *mailboxRow) bool {
				for _, s := range subs {
					if s(m) {
						return true
					}
				}
				return false
			}, nil
		case "NOT":
			return func(m *mailboxRow) bool {
				for _, s := range subs {
					if s(m) {
						return false
					}
				}
				return true
			}, nil
		}
		return nil, methodErr("unsupportedFilter", "unknown operator %q", o.Operator)
	}
	var conds []func(*mailboxRow) bool
	for k, v := range f {
		switch k {
		case "parentId":
			var p *string
			_ = json.Unmarshal(v, &p)
			conds = append(conds, func(m *mailboxRow) bool {
				if p == nil {
					return m.parent == nil
				}
				id, _ := parseID(prefixMailbox, *p)
				return m.parent != nil && m.parent.ID == id
			})
		case "name":
			var s string
			_ = json.Unmarshal(v, &s)
			s = strings.ToLower(s)
			conds = append(conds, func(m *mailboxRow) bool { return strings.Contains(strings.ToLower(m.name), s) })
		case "role":
			var r *string
			_ = json.Unmarshal(v, &r)
			conds = append(conds, func(m *mailboxRow) bool {
				if r == nil {
					return m.Role == ""
				}
				return m.Role == *r
			})
		case "hasAnyRole":
			var b bool
			_ = json.Unmarshal(v, &b)
			conds = append(conds, func(m *mailboxRow) bool { return (m.Role != "") == b })
		case "isSubscribed":
			var b bool
			_ = json.Unmarshal(v, &b)
			conds = append(conds, func(m *mailboxRow) bool { return m.Subscribed == b })
		default:
			return nil, methodErr("unsupportedFilter", "unknown filter property %q", k)
		}
	}
	return func(m *mailboxRow) bool {
		for _, c := range conds {
			if !c(m) {
				return false
			}
		}
		return true
	}, nil
}

// ---- Mailbox/set ----

// createOrder sorts creation ids so that a mailbox whose parentId refers to
// another creation id comes after that one.
func createOrder(create map[string]json.RawMessage) []string {
	parent := map[string]string{}
	var ids []string
	for cid, raw := range create {
		ids = append(ids, cid)
		var o struct {
			ParentID *string `json:"parentId"`
		}
		if json.Unmarshal(raw, &o) == nil && o.ParentID != nil && strings.HasPrefix(*o.ParentID, "#") {
			parent[cid] = strings.TrimPrefix(*o.ParentID, "#")
		}
	}
	sort.Strings(ids)
	depth := func(cid string) int {
		n := 0
		for p, ok := parent[cid]; ok && n <= len(ids); p, ok = parent[p] {
			n++
		}
		return n
	}
	sort.SliceStable(ids, func(i, j int) bool { return depth(ids[i]) < depth(ids[j]) })
	return ids
}

func validMailboxName(n string) bool {
	if n == "" || len(n) > maxMailboxName || strings.Contains(n, mailboxDelim) || strings.TrimSpace(n) != n {
		return false
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

type setArgs struct {
	AccountID string                     `json:"accountId"`
	IfInState *string                    `json:"ifInState"`
	Create    map[string]json.RawMessage `json:"create"`
	Update    map[string]json.RawMessage `json:"update"`
	Destroy   []string                   `json:"destroy"`
}

func (a *setArgs) check(c *call, typ string) (string, error) {
	if err := c.checkAccount(a.AccountID); err != nil {
		return "", err
	}
	if len(a.Create)+len(a.Update)+len(a.Destroy) > maxObjectsInSet {
		return "", methodErr("requestTooLarge", "at most %d objects per call", maxObjectsInSet)
	}
	state, err := c.state(typ)
	if err != nil {
		return "", err
	}
	if a.IfInState != nil && *a.IfInState != state {
		return "", methodErr("stateMismatch", "state is %s", state)
	}
	return state, nil
}

type setResponse struct {
	AccountID    string               `json:"accountId"`
	OldState     string               `json:"oldState"`
	NewState     string               `json:"newState"`
	Created      map[string]any       `json:"created"`
	Updated      map[string]any       `json:"updated"`
	Destroyed    []string             `json:"destroyed"`
	NotCreated   map[string]*setError `json:"notCreated"`
	NotUpdated   map[string]*setError `json:"notUpdated"`
	NotDestroyed map[string]*setError `json:"notDestroyed"`
}

func newSetResponse(accountID, oldState string) *setResponse {
	return &setResponse{AccountID: accountID, OldState: oldState, Created: map[string]any{}, Updated: map[string]any{}, Destroyed: []string{},
		NotCreated: map[string]*setError{}, NotUpdated: map[string]*setError{}, NotDestroyed: map[string]*setError{}}
}

func (c *call) mailboxSet(raw json.RawMessage) (any, error) {
	var a struct {
		setArgs
		OnDestroyRemoveEmails bool `json:"onDestroyRemoveEmails"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	old, err := a.check(c, store.TypeMailbox)
	if err != nil {
		return nil, err
	}
	resp := newSetResponse(a.AccountID, old)
	st := c.srv.Store

	// lookup reloads the mailbox list (names change during the call).
	lookup := func(sid string) (*mailboxRow, []*mailboxRow, error) {
		all, err := c.loadMailboxes()
		if err != nil {
			return nil, nil, err
		}
		rid, ok := c.resolveCreated(sid)
		if !ok {
			return nil, all, nil
		}
		id, _ := parseID(prefixMailbox, rid)
		for _, m := range all {
			if m.ID == id {
				return m, all, nil
			}
		}
		return nil, all, nil
	}

	// Parents referenced by creation id ("#p") are created first.
	order := createOrder(a.Create)
	for _, cid := range order {
		objRaw := a.Create[cid]
		var o struct {
			Name         *string `json:"name"`
			ParentID     *string `json:"parentId"`
			Role         *string `json:"role"`
			IsSubscribed *bool   `json:"isSubscribed"`
			SortOrder    *int    `json:"sortOrder"`
		}
		if err := json.Unmarshal(objRaw, &o); err != nil {
			resp.NotCreated[cid] = setErr("invalidProperties", err.Error())
			continue
		}
		if o.Name == nil || !validMailboxName(*o.Name) {
			resp.NotCreated[cid] = setErr("invalidProperties", "invalid name (no \"/\", no control characters)", "name")
			continue
		}
		if o.Role != nil {
			resp.NotCreated[cid] = setErr("invalidProperties", "roles cannot be assigned", "role")
			continue
		}
		full := *o.Name
		if o.ParentID != nil {
			p, _, err := lookup(*o.ParentID)
			if err != nil {
				return nil, err
			}
			if p == nil {
				resp.NotCreated[cid] = setErr("invalidProperties", "parent not found", "parentId")
				continue
			}
			full = p.FullName + mailboxDelim + *o.Name
		}
		mb, err := st.CreateMailbox(c.ctx, c.acct.ID, full)
		if errors.Is(err, store.ErrConflict) {
			resp.NotCreated[cid] = setErr("invalidProperties", "a mailbox with this name already exists", "name")
			continue
		}
		if err != nil {
			return nil, err
		}
		if o.IsSubscribed != nil && !*o.IsSubscribed {
			_ = st.SetMailboxSubscribed(c.ctx, c.acct.ID, mb.ID, false)
		}
		id := fmtID(prefixMailbox, mb.ID)
		c.created[cid] = id
		row := &mailboxRow{ID: mb.ID, Subscribed: o.IsSubscribed == nil || *o.IsSubscribed}
		obj := row.object()
		resp.Created[cid] = map[string]any{"id": id, "role": nil, "sortOrder": obj["sortOrder"], "totalEmails": 0, "unreadEmails": 0,
			"totalThreads": 0, "unreadThreads": 0, "myRights": obj["myRights"], "isSubscribed": obj["isSubscribed"]}
	}

	for sid, patchRaw := range a.Update {
		m, all, err := lookup(sid)
		if err != nil {
			return nil, err
		}
		if m == nil {
			resp.NotUpdated[sid] = setErr("notFound", "mailbox not found")
			continue
		}
		var patch map[string]json.RawMessage
		if err := json.Unmarshal(patchRaw, &patch); err != nil {
			resp.NotUpdated[sid] = setErr("invalidPatch", "patch must be an object")
			continue
		}
		name, parent := m.name, m.parent
		var subscribed *bool
		var serr *setError
		for k, v := range patch {
			switch k {
			case "name":
				var n string
				if json.Unmarshal(v, &n) != nil || !validMailboxName(n) {
					serr = setErr("invalidProperties", "invalid name", "name")
				}
				name = n
			case "parentId":
				var p *string
				if json.Unmarshal(v, &p) != nil {
					serr = setErr("invalidProperties", "invalid parentId", "parentId")
					break
				}
				parent = nil
				if p != nil {
					np, _, err := lookup(*p)
					if err != nil {
						return nil, err
					}
					if np == nil {
						serr = setErr("invalidProperties", "parent not found", "parentId")
						break
					}
					// Refuse cycles: the new parent must not be the mailbox or below it.
					for x := np; x != nil; x = x.parent {
						if x.ID == m.ID {
							serr = setErr("invalidProperties", "a mailbox cannot be moved below itself", "parentId")
						}
					}
					parent = np
				}
			case "isSubscribed":
				var b bool
				if json.Unmarshal(v, &b) != nil {
					serr = setErr("invalidProperties", "invalid isSubscribed", "isSubscribed")
				}
				subscribed = &b
			case "sortOrder":
				// accepted but not stored
			case "role":
				var r *string
				_ = json.Unmarshal(v, &r)
				if (r == nil && m.Role != "") || (r != nil && *r != m.Role) {
					serr = setErr("invalidProperties", "roles cannot be changed", "role")
				}
			default:
				serr = setErr("invalidProperties", "property cannot be changed", k)
			}
		}
		if serr != nil {
			resp.NotUpdated[sid] = serr
			continue
		}
		full := name
		if parent != nil {
			full = parent.FullName + mailboxDelim + name
		}
		if full != m.FullName {
			if m.Role == "inbox" {
				resp.NotUpdated[sid] = setErr("forbidden", "the inbox cannot be renamed", "name")
				continue
			}
			if err := st.RenameMailbox(c.ctx, c.acct.ID, m.ID, full); errors.Is(err, store.ErrConflict) {
				resp.NotUpdated[sid] = setErr("invalidProperties", "a mailbox with this name already exists", "name")
				continue
			} else if err != nil {
				return nil, err
			}
			// Children move along ("Old/Sub" -> "New/Sub").
			prefix := m.FullName + mailboxDelim
			for _, child := range all {
				if child.Role == "" && strings.HasPrefix(child.FullName, prefix) {
					_ = st.RenameMailbox(c.ctx, c.acct.ID, child.ID, full+mailboxDelim+strings.TrimPrefix(child.FullName, prefix))
				}
			}
		}
		if subscribed != nil && *subscribed != m.Subscribed {
			if err := st.SetMailboxSubscribed(c.ctx, c.acct.ID, m.ID, *subscribed); err != nil {
				return nil, err
			}
		}
		resp.Updated[sid] = nil
	}

	for _, sid := range a.Destroy {
		m, all, err := lookup(sid)
		if err != nil {
			return nil, err
		}
		if m == nil {
			resp.NotDestroyed[sid] = setErr("notFound", "mailbox not found")
			continue
		}
		if m.Role != "" {
			resp.NotDestroyed[sid] = setErr("forbidden", "mailboxes with a role cannot be destroyed")
			continue
		}
		hasChild := false
		for _, x := range all {
			if x.parent == m {
				hasChild = true
			}
		}
		if hasChild {
			resp.NotDestroyed[sid] = setErr("mailboxHasChild", "the mailbox has child mailboxes")
			continue
		}
		if m.Total > 0 && !a.OnDestroyRemoveEmails {
			resp.NotDestroyed[sid] = setErr("mailboxHasEmail", "the mailbox is not empty")
			continue
		}
		if err := st.DeleteMailbox(c.ctx, c.acct.ID, m.ID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				resp.NotDestroyed[sid] = setErr("notFound", "mailbox not found")
				continue
			}
			return nil, err
		}
		resp.Destroyed = append(resp.Destroyed, sid)
	}
	if resp.NewState, err = c.state(store.TypeMailbox); err != nil {
		return nil, err
	}
	return resp, nil
}
