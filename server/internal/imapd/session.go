package imapd

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"

	"postwerk/internal/delivery"
	"postwerk/internal/message"
	"postwerk/internal/metrics"
	"postwerk/internal/store"
)

type session struct {
	srv  *Server
	conn *imapserver.Conn
	ip   string
	ctx  context.Context
	log  *slog.Logger
	acct *store.Account
	sel  *view
}

var (
	_ imapserver.SessionIMAP4rev2   = (*session)(nil)
	_ imapserver.SessionAppendLimit = (*session)(nil)
)

func errNo(code imap.ResponseCode, text string) error {
	return &imap.Error{Type: imap.StatusResponseTypeNo, Code: code, Text: text}
}

var (
	errNoMailbox = errNo(imap.ResponseCodeNonExistent, "Mailbox does not exist")
	errReadOnly  = errNo(imap.ResponseCodeCannot, "Mailbox is read-only")
)

func (ss *session) Close() error {
	if ss.sel != nil {
		ss.sel = nil
	}
	return nil
}

// ---- not authenticated ----

func (ss *session) Login(username, password string) error {
	acct, err := ss.srv.Auth(ss.ctx, ss.ip, strings.TrimSpace(username), password)
	if err != nil {
		metrics.AuthAttempts.WithLabelValues("imap", "failure").Inc()
		ss.log.Warn("authentication failed", "user", username)
		time.Sleep(time.Second)
		return imapserver.ErrAuthFailed
	}
	metrics.AuthAttempts.WithLabelValues("imap", "success").Inc()
	ss.acct = acct
	ss.log = ss.log.With("user", acct.Email)
	ss.log.Info("authenticated")
	return nil
}

// ---- mailbox helpers ----

func imapName(mb *store.IMAPMailbox) string {
	if mb.Role == "inbox" {
		return "INBOX"
	}
	return mb.Name
}

func specialUse(role string) imap.MailboxAttr {
	switch role {
	case "drafts":
		return imap.MailboxAttrDrafts
	case "sent":
		return imap.MailboxAttrSent
	case "archive":
		return imap.MailboxAttrArchive
	case "junk":
		return imap.MailboxAttrJunk
	case "trash":
		return imap.MailboxAttrTrash
	}
	return ""
}

func (ss *session) mailboxes() ([]*store.IMAPMailbox, error) {
	return ss.srv.Store.IMAPMailboxes(ss.ctx, ss.acct.ID)
}

func (ss *session) lookup(name string) (*store.IMAPMailbox, error) {
	list, err := ss.mailboxes()
	if err != nil {
		return nil, err
	}
	name = strings.TrimSuffix(name, string(Delim))
	for _, mb := range list {
		if mb.Role == "inbox" {
			if strings.EqualFold(name, "INBOX") {
				return mb, nil
			}
			continue
		}
		if mb.Name == name {
			return mb, nil
		}
	}
	return nil, errNoMailbox
}

func (ss *session) status(mb *store.IMAPMailbox, opt *imap.StatusOptions) (*imap.StatusData, error) {
	data := &imap.StatusData{Mailbox: imapName(mb)}
	total, unseen, deleted, size, err := ss.srv.Store.MailboxCounts(ss.ctx, mb.ID)
	if err != nil {
		return nil, err
	}
	u32 := func(n int) *uint32 { v := uint32(n); return &v }
	if opt.NumMessages {
		data.NumMessages = u32(total)
	}
	if opt.NumUnseen {
		data.NumUnseen = u32(unseen)
	}
	if opt.NumDeleted {
		data.NumDeleted = u32(deleted)
	}
	if opt.NumRecent {
		data.NumRecent = u32(0)
	}
	if opt.UIDNext {
		data.UIDNext = imap.UID(mb.UIDNext)
	}
	if opt.UIDValidity {
		data.UIDValidity = mb.UIDValidity
	}
	if opt.Size {
		data.Size = &size
	}
	if opt.AppendLimit {
		l := ss.AppendLimit()
		data.AppendLimit = &l
	}
	return data, nil
}

// ---- authenticated ----

func (ss *session) Select(name string, options *imap.SelectOptions) (*imap.SelectData, error) {
	ss.sel = nil
	mb, err := ss.lookup(name)
	if err != nil {
		return nil, err
	}
	v, err := openView(ss, mb, options != nil && options.ReadOnly)
	if err != nil {
		return nil, err
	}
	ss.sel = v
	return v.selectData(), nil
}

func (ss *session) Create(name string, _ *imap.CreateOptions) error {
	name = strings.Trim(strings.TrimSpace(name), string(Delim))
	if name == "" || strings.EqualFold(name, "INBOX") {
		return errNo(imap.ResponseCodeAlreadyExists, "Mailbox already exists")
	}
	if _, err := ss.lookup(name); err == nil {
		return errNo(imap.ResponseCodeAlreadyExists, "Mailbox already exists")
	}
	_, err := ss.srv.Store.CreateMailbox(ss.ctx, ss.acct.ID, name)
	return err
}

func (ss *session) Delete(name string) error {
	mb, err := ss.lookup(name)
	if err != nil {
		return err
	}
	if mb.Role != "" {
		return errNo(imap.ResponseCodeCannot, "System mailboxes cannot be deleted")
	}
	return ss.srv.Store.DeleteMailbox(ss.ctx, ss.acct.ID, mb.ID)
}

func (ss *session) Rename(oldName, newName string, _ *imap.RenameOptions) error {
	mb, err := ss.lookup(oldName)
	if err != nil {
		return err
	}
	newName = strings.Trim(strings.TrimSpace(newName), string(Delim))
	if mb.Role == "inbox" || newName == "" || strings.EqualFold(newName, "INBOX") {
		return errNo(imap.ResponseCodeCannot, "Cannot rename this mailbox")
	}
	if _, err := ss.lookup(newName); err == nil {
		return errNo(imap.ResponseCodeAlreadyExists, "Mailbox already exists")
	}
	list, err := ss.mailboxes()
	if err != nil {
		return err
	}
	if err := ss.srv.Store.RenameMailbox(ss.ctx, ss.acct.ID, mb.ID, newName); err != nil {
		return err
	}
	// Move children along ("Old/Sub" -> "New/Sub").
	prefix := mb.Name + string(Delim)
	for _, child := range list {
		if child.Role == "" && strings.HasPrefix(child.Name, prefix) {
			_ = ss.srv.Store.RenameMailbox(ss.ctx, ss.acct.ID, child.ID, newName+string(Delim)+strings.TrimPrefix(child.Name, prefix))
		}
	}
	return nil
}

func (ss *session) Subscribe(name string) error {
	mb, err := ss.lookup(name)
	if err != nil {
		return err
	}
	return ss.srv.Store.SetMailboxSubscribed(ss.ctx, ss.acct.ID, mb.ID, true)
}

func (ss *session) Unsubscribe(name string) error {
	mb, err := ss.lookup(name)
	if err != nil {
		return err
	}
	return ss.srv.Store.SetMailboxSubscribed(ss.ctx, ss.acct.ID, mb.ID, false)
}

func (ss *session) List(w *imapserver.ListWriter, ref string, patterns []string, options *imap.ListOptions) error {
	if len(patterns) == 0 {
		return w.WriteList(&imap.ListData{Attrs: []imap.MailboxAttr{imap.MailboxAttrNoSelect}, Delim: Delim})
	}
	list, err := ss.mailboxes()
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for _, mb := range list {
		names[imapName(mb)] = true
	}
	hasChildren := func(name string) bool {
		for n := range names {
			if strings.HasPrefix(n, name+string(Delim)) {
				return true
			}
		}
		return false
	}
	for _, mb := range list {
		name := imapName(mb)
		match := false
		for _, p := range patterns {
			if imapserver.MatchList(name, Delim, ref, p) {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		if options.SelectSubscribed && !mb.Subscribed {
			continue
		}
		use := specialUse(mb.Role)
		if options.SelectSpecialUse && use == "" {
			continue
		}
		data := &imap.ListData{Mailbox: name, Delim: Delim}
		if hasChildren(name) {
			data.Attrs = append(data.Attrs, imap.MailboxAttrHasChildren)
		} else {
			data.Attrs = append(data.Attrs, imap.MailboxAttrHasNoChildren)
		}
		if use != "" {
			data.Attrs = append(data.Attrs, use)
		}
		if mb.Subscribed && (options.SelectSubscribed || options.ReturnSubscribed) {
			data.Attrs = append(data.Attrs, imap.MailboxAttrSubscribed)
		}
		if options.ReturnStatus != nil {
			if data.Status, err = ss.status(mb, options.ReturnStatus); err != nil {
				return err
			}
		}
		if err := w.WriteList(data); err != nil {
			return err
		}
	}
	return nil
}

func (ss *session) Status(name string, options *imap.StatusOptions) (*imap.StatusData, error) {
	mb, err := ss.lookup(name)
	if err != nil {
		return nil, err
	}
	return ss.status(mb, options)
}

func (ss *session) AppendLimit() uint32 {
	if n := ss.maxSize(); n > 0 && n < 1<<31 {
		return uint32(n)
	}
	return 1 << 31
}

func (ss *session) Append(name string, r imap.LiteralReader, options *imap.AppendOptions) (*imap.AppendData, error) {
	mb, err := ss.lookup(name)
	if err != nil {
		return nil, errNo(imap.ResponseCodeTryCreate, "Mailbox does not exist")
	}
	if n := ss.maxSize(); n > 0 && r.Size() > n {
		return nil, errNo(imap.ResponseCodeTooBig, "Message too big")
	}
	raw, err := io.ReadAll(io.LimitReader(r, r.Size()+1))
	if err != nil {
		return nil, err
	}
	f := flagsFromIMAP(options.Flags)
	// Mail clients store a copy of every sent message; skip exact duplicates.
	if mb.Role == "sent" || mb.Role == "drafts" {
		if id := strings.Trim(message.HeaderValue(raw, "Message-ID"), "<> 	"); id != "" {
			if uid, ok := ss.srv.Store.UIDByMessageID(ss.ctx, mb.ID, id, int64(len(raw))); ok {
				return &imap.AppendData{UID: imap.UID(uid), UIDValidity: mb.UIDValidity}, nil
			}
		}
	}
	m, err := ss.srv.Delivery.ImportMessage(ss.ctx, ss.acct, mb.ID, raw, delivery.ImportOptions{
		ReceivedAt: options.Time, Seen: f.Seen, Flagged: f.Flagged, Answered: f.Answered, SkipAI: mb.Role != "inbox",
	})
	if err != nil {
		if strings.Contains(err.Error(), "Quota") {
			return nil, errNo(imap.ResponseCodeOverQuota, "Quota exceeded")
		}
		return nil, err
	}
	if f.Draft || f.Deleted || len(f.Keywords) > 0 {
		_ = ss.srv.Store.SetIMAPFlags(ss.ctx, ss.acct.ID, m.ID, f)
	}
	return &imap.AppendData{UID: imap.UID(m.UID), UIDValidity: mb.UIDValidity}, nil
}

func (ss *session) Namespace() (*imap.NamespaceData, error) {
	return &imap.NamespaceData{Personal: []imap.NamespaceDescriptor{{Prefix: "", Delim: Delim}}}, nil
}

func (ss *session) Poll(w *imapserver.UpdateWriter, allowExpunge bool) error {
	if ss.sel == nil {
		return nil
	}
	return ss.sel.poll(w, allowExpunge)
}

func (ss *session) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	if ss.sel == nil {
		<-stop
		return nil
	}
	changes, cancel := ss.srv.Store.SubscribeChanges(ss.acct.ID)
	defer cancel()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	if err := ss.sel.poll(w, true); err != nil {
		return err
	}
	for {
		select {
		case <-stop:
			return nil
		case <-ss.ctx.Done():
			return nil
		case <-changes:
		case <-tick.C:
		}
		if err := ss.sel.poll(w, true); err != nil {
			return err
		}
	}
}

// ---- selected ----

func (ss *session) Unselect() error {
	ss.sel = nil
	return nil
}

func (ss *session) Expunge(w *imapserver.ExpungeWriter, uids *imap.UIDSet) error {
	return ss.sel.expunge(w, uids)
}

func (ss *session) Search(kind imapserver.NumKind, criteria *imap.SearchCriteria, options *imap.SearchOptions) (*imap.SearchData, error) {
	return ss.sel.search(kind, criteria, options)
}

func (ss *session) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	return ss.sel.fetch(w, numSet, options)
}

func (ss *session) Store(w *imapserver.FetchWriter, numSet imap.NumSet, flags *imap.StoreFlags, options *imap.StoreOptions) error {
	return ss.sel.store(w, numSet, flags)
}

func (ss *session) Copy(numSet imap.NumSet, dest string) (*imap.CopyData, error) {
	return ss.sel.copy(numSet, dest)
}

func (ss *session) Move(w *imapserver.MoveWriter, numSet imap.NumSet, dest string) error {
	return ss.sel.move(w, numSet, dest)
}

func (ss *session) maxSize() int64 {
	if ss.srv.MaxSize == nil {
		return 0
	}
	return ss.srv.MaxSize()
}

func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
