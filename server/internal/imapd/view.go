package imapd

import (
	"bufio"
	"bytes"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-message/textproto"

	"postwerk/internal/store"
)

// entry is one message as the client currently sees it; its sequence number
// is its index + 1.
type entry struct {
	id       int64
	uid      imap.UID
	size     int64
	received time.Time
	blob     string
	flags    store.IMAPFlags
	gone     bool // deleted elsewhere, EXPUNGE not yet sent
}

// view is the selected mailbox of one session. It keeps the client's
// snapshot and turns database changes into EXISTS/EXPUNGE/FETCH updates.
type view struct {
	ss        *session
	mb        *store.IMAPMailbox
	readOnly  bool
	msgs      []*entry
	searchRes imap.UIDSet
}

func newEntry(m *store.IMAPMessage) *entry {
	return &entry{id: m.ID, uid: imap.UID(m.UID), size: m.Size, received: m.ReceivedAt, blob: m.BlobHash, flags: m.Flags}
}

func openView(ss *session, mb *store.IMAPMailbox, readOnly bool) (*view, error) {
	list, err := ss.srv.Store.IMAPMessages(ss.ctx, mb.ID)
	if err != nil {
		return nil, err
	}
	v := &view{ss: ss, mb: mb, readOnly: readOnly}
	for i := range list {
		v.msgs = append(v.msgs, newEntry(&list[i]))
	}
	return v, nil
}

var systemFlags = []imap.Flag{imap.FlagSeen, imap.FlagAnswered, imap.FlagFlagged, imap.FlagDeleted, imap.FlagDraft}

func (v *view) selectData() *imap.SelectData {
	flags := append([]imap.Flag{}, systemFlags...)
	kw := map[string]bool{}
	var first uint32
	for i, e := range v.msgs {
		for _, k := range e.flags.Keywords {
			kw[k] = true
		}
		if first == 0 && !e.flags.Seen {
			first = uint32(i + 1)
		}
	}
	for _, k := range sortedKeys(kw) {
		flags = append(flags, imap.Flag(k))
	}
	permanent := append(append([]imap.Flag{}, flags...), imap.FlagWildcard)
	if v.readOnly {
		permanent = nil
	}
	uidNext := v.mb.UIDNext
	if n := len(v.msgs); n > 0 && uint32(v.msgs[n-1].uid) >= uidNext {
		uidNext = uint32(v.msgs[n-1].uid) + 1
	}
	return &imap.SelectData{
		Flags: flags, PermanentFlags: permanent, NumMessages: uint32(len(v.msgs)), FirstUnseenSeqNum: first,
		UIDNext: imap.UID(uidNext), UIDValidity: v.mb.UIDValidity,
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- flags ----

func flagList(f store.IMAPFlags) []imap.Flag {
	out := []imap.Flag{}
	if f.Seen {
		out = append(out, imap.FlagSeen)
	}
	if f.Answered {
		out = append(out, imap.FlagAnswered)
	}
	if f.Flagged {
		out = append(out, imap.FlagFlagged)
	}
	if f.Deleted {
		out = append(out, imap.FlagDeleted)
	}
	if f.Draft {
		out = append(out, imap.FlagDraft)
	}
	for _, k := range f.Keywords {
		out = append(out, imap.Flag(k))
	}
	return out
}

func flagKey(f store.IMAPFlags) string {
	var b strings.Builder
	for _, fl := range flagList(f) {
		b.WriteString(strings.ToLower(string(fl)))
		b.WriteByte(' ')
	}
	return b.String()
}

// setFlag turns one IMAP flag on or off; \Recent and unknown system flags
// are ignored.
func setFlag(f *store.IMAPFlags, flag imap.Flag, on bool) {
	switch strings.ToLower(string(flag)) {
	case `\seen`:
		f.Seen = on
	case `\answered`:
		f.Answered = on
	case `\flagged`:
		f.Flagged = on
	case `\deleted`:
		f.Deleted = on
	case `\draft`:
		f.Draft = on
	case `\recent`, `\*`:
	default:
		if strings.HasPrefix(string(flag), `\`) {
			return
		}
		kept := f.Keywords[:0:0]
		for _, k := range f.Keywords {
			if !strings.EqualFold(k, string(flag)) {
				kept = append(kept, k)
			}
		}
		if on {
			kept = append(kept, string(flag))
		}
		sort.Strings(kept)
		f.Keywords = kept
	}
}

func flagsFromIMAP(flags []imap.Flag) store.IMAPFlags {
	var f store.IMAPFlags
	for _, fl := range flags {
		setFlag(&f, fl, true)
	}
	return f
}

func hasFlag(f store.IMAPFlags, flag imap.Flag) bool {
	want := strings.ToLower(string(flag))
	for _, fl := range flagList(f) {
		if strings.ToLower(string(fl)) == want {
			return true
		}
	}
	return false
}

// ---- updates ----

// poll compares the snapshot with the database and reports the differences.
func (v *view) poll(w *imapserver.UpdateWriter, allowExpunge bool) error {
	cur, err := v.ss.srv.Store.IMAPMessages(v.ss.ctx, v.mb.ID)
	if err != nil {
		return err
	}
	byUID := make(map[imap.UID]*store.IMAPMessage, len(cur))
	for i := range cur {
		byUID[imap.UID(cur[i].UID)] = &cur[i]
	}
	// Expunged messages, highest sequence number first.
	for i := len(v.msgs) - 1; i >= 0; i-- {
		e := v.msgs[i]
		if _, ok := byUID[e.uid]; ok {
			continue
		}
		if !allowExpunge {
			e.gone = true
			continue
		}
		if err := w.WriteExpunge(uint32(i + 1)); err != nil {
			return err
		}
		v.msgs = append(v.msgs[:i], v.msgs[i+1:]...)
	}
	// Flag changes.
	var maxUID imap.UID
	for i, e := range v.msgs {
		if e.uid > maxUID {
			maxUID = e.uid
		}
		m, ok := byUID[e.uid]
		if !ok || flagKey(m.Flags) == flagKey(e.flags) {
			continue
		}
		e.flags = m.Flags
		if err := w.WriteMessageFlags(uint32(i+1), e.uid, flagList(e.flags)); err != nil {
			return err
		}
	}
	// New messages.
	added := false
	for i := range cur {
		if imap.UID(cur[i].UID) > maxUID {
			v.msgs = append(v.msgs, newEntry(&cur[i]))
			added = true
		}
	}
	if added {
		return w.WriteNumMessages(uint32(len(v.msgs)))
	}
	return nil
}

// ---- number sets ----

// resolve returns the snapshot indexes addressed by a sequence or UID set.
func (v *view) resolve(numSet imap.NumSet) []int {
	if imap.IsSearchRes(numSet) {
		numSet = v.searchRes
	}
	var out []int
	switch set := numSet.(type) {
	case imap.SeqSet:
		max := uint32(len(v.msgs))
		set = staticSeq(set, max)
		for i := range v.msgs {
			if set.Contains(uint32(i + 1)) {
				out = append(out, i)
			}
		}
	case imap.UIDSet:
		var max imap.UID
		if n := len(v.msgs); n > 0 {
			max = v.msgs[n-1].uid
		}
		set = staticUID(set, max)
		for i, e := range v.msgs {
			if set.Contains(e.uid) {
				out = append(out, i)
			}
		}
	}
	return out
}

func staticSeq(set imap.SeqSet, max uint32) imap.SeqSet {
	out := make(imap.SeqSet, len(set))
	for i, r := range set {
		out[i] = imap.SeqRange{Start: r.Start, Stop: r.Stop}
		staticRange(&out[i].Start, &out[i].Stop, max)
	}
	return out
}

func staticUID(set imap.UIDSet, max imap.UID) imap.UIDSet {
	out := make(imap.UIDSet, len(set))
	for i, r := range set {
		start, stop := uint32(r.Start), uint32(r.Stop)
		staticRange(&start, &stop, uint32(max))
		out[i] = imap.UIDRange{Start: imap.UID(start), Stop: imap.UID(stop)}
	}
	return out
}

// staticRange replaces "*" (0) by the maximum and orders the range.
func staticRange(start, stop *uint32, max uint32) {
	if *start == 0 {
		*start = max
	}
	if *stop == 0 {
		*stop = max
	}
	if *start > *stop {
		*start, *stop = *stop, *start
	}
}

// ---- FETCH ----

func (v *view) load(e *entry) []byte {
	raw, err := v.ss.srv.Store.Blobs.Get(e.blob)
	if err != nil {
		return nil
	}
	return raw
}

func (v *view) fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	markSeen := false
	if !v.readOnly {
		for _, bs := range options.BodySection {
			if !bs.Peek {
				markSeen = true
			}
		}
		for _, bs := range options.BinarySection {
			if !bs.Peek {
				markSeen = true
			}
		}
	}
	needBody := options.Envelope || options.BodyStructure != nil || len(options.BodySection) > 0 ||
		len(options.BinarySection) > 0 || len(options.BinarySectionSize) > 0
	for _, i := range v.resolve(numSet) {
		e := v.msgs[i]
		if e.gone {
			continue
		}
		var raw []byte
		if needBody {
			if raw = v.load(e); raw == nil {
				continue
			}
		}
		writeFlags := options.Flags
		if markSeen && !e.flags.Seen {
			f := e.flags
			f.Seen = true
			if err := v.ss.srv.Store.SetIMAPFlags(v.ss.ctx, v.ss.acct.ID, e.id, f); err == nil {
				e.flags = f
				writeFlags = true
			}
		}
		rw := w.CreateMessage(uint32(i + 1))
		rw.WriteUID(e.uid)
		if writeFlags {
			rw.WriteFlags(flagList(e.flags))
		}
		if options.InternalDate {
			rw.WriteInternalDate(e.received)
		}
		if options.RFC822Size {
			rw.WriteRFC822Size(e.size)
		}
		if options.Envelope {
			if h, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(raw))); err == nil {
				rw.WriteEnvelope(imapserver.ExtractEnvelope(h))
			}
		}
		if options.BodyStructure != nil {
			rw.WriteBodyStructure(imapserver.ExtractBodyStructure(bytes.NewReader(raw)))
		}
		for _, bs := range options.BodySection {
			buf := imapserver.ExtractBodySection(bytes.NewReader(raw), bs)
			wc := rw.WriteBodySection(bs, int64(len(buf)))
			_, werr := wc.Write(buf)
			if cerr := wc.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				return werr
			}
		}
		for _, bs := range options.BinarySection {
			buf := imapserver.ExtractBinarySection(bytes.NewReader(raw), bs)
			wc := rw.WriteBinarySection(bs, int64(len(buf)))
			_, werr := wc.Write(buf)
			if cerr := wc.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				return werr
			}
		}
		for _, bss := range options.BinarySectionSize {
			rw.WriteBinarySectionSize(bss, imapserver.ExtractBinarySectionSize(bytes.NewReader(raw), bss))
		}
		if err := rw.Close(); err != nil {
			return err
		}
	}
	return nil
}

// ---- STORE ----

func (v *view) store(w *imapserver.FetchWriter, numSet imap.NumSet, sf *imap.StoreFlags) error {
	if v.readOnly {
		return errReadOnly
	}
	idx := v.resolve(numSet)
	for _, i := range idx {
		e := v.msgs[i]
		if e.gone {
			continue
		}
		f := e.flags
		f.Keywords = append([]string{}, f.Keywords...)
		switch sf.Op {
		case imap.StoreFlagsSet:
			f = store.IMAPFlags{}
			for _, fl := range sf.Flags {
				setFlag(&f, fl, true)
			}
		case imap.StoreFlagsAdd:
			for _, fl := range sf.Flags {
				setFlag(&f, fl, true)
			}
		case imap.StoreFlagsDel:
			for _, fl := range sf.Flags {
				setFlag(&f, fl, false)
			}
		}
		if flagKey(f) != flagKey(e.flags) {
			if err := v.ss.srv.Store.SetIMAPFlags(v.ss.ctx, v.ss.acct.ID, e.id, f); err != nil && !isNotFound(err) {
				return err
			}
			e.flags = f
		}
	}
	if sf.Silent {
		return nil
	}
	for _, i := range idx {
		e := v.msgs[i]
		if e.gone {
			continue
		}
		rw := w.CreateMessage(uint32(i + 1))
		rw.WriteUID(e.uid)
		rw.WriteFlags(flagList(e.flags))
		if err := rw.Close(); err != nil {
			return err
		}
	}
	return nil
}

// ---- EXPUNGE ----

func (v *view) expunge(w *imapserver.ExpungeWriter, uids *imap.UIDSet) error {
	if v.readOnly {
		return errReadOnly
	}
	// Use the current \Deleted state: another client may have set it.
	cur, err := v.ss.srv.Store.IMAPMessages(v.ss.ctx, v.mb.ID)
	if err != nil {
		return err
	}
	deleted := map[imap.UID]bool{}
	for _, m := range cur {
		if m.Flags.Deleted {
			deleted[imap.UID(m.UID)] = true
		}
	}
	for i := len(v.msgs) - 1; i >= 0; i-- {
		e := v.msgs[i]
		if !deleted[e.uid] || (uids != nil && !uids.Contains(e.uid)) {
			continue
		}
		if err := v.ss.srv.Store.DeleteMessage(v.ss.ctx, v.ss.acct.ID, e.id); err != nil && !isNotFound(err) {
			return err
		}
		if err := w.WriteExpunge(uint32(i + 1)); err != nil {
			return err
		}
		v.msgs = append(v.msgs[:i], v.msgs[i+1:]...)
	}
	return nil
}

// ---- COPY / MOVE ----

func (v *view) target(dest string) (*store.IMAPMailbox, error) {
	mb, err := v.ss.lookup(dest)
	if err != nil {
		return nil, errNo(imap.ResponseCodeTryCreate, "Mailbox does not exist")
	}
	return mb, nil
}

func (v *view) copy(numSet imap.NumSet, dest string) (*imap.CopyData, error) {
	mb, err := v.target(dest)
	if err != nil {
		return nil, err
	}
	data := &imap.CopyData{UIDValidity: mb.UIDValidity}
	for _, i := range v.resolve(numSet) {
		e := v.msgs[i]
		if e.gone {
			continue
		}
		cp, err := v.ss.srv.Store.CopyMessage(v.ss.ctx, v.ss.acct.ID, e.id, mb.ID)
		if err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, err
		}
		data.SourceUIDs.AddNum(e.uid)
		data.DestUIDs.AddNum(imap.UID(cp.UID))
	}
	return data, nil
}

func (v *view) move(w *imapserver.MoveWriter, numSet imap.NumSet, dest string) error {
	if v.readOnly {
		return errReadOnly
	}
	mb, err := v.target(dest)
	if err != nil {
		return err
	}
	idx := v.resolve(numSet)
	data := &imap.CopyData{UIDValidity: mb.UIDValidity}
	var moved []int
	for _, i := range idx {
		e := v.msgs[i]
		if e.gone {
			continue
		}
		if err := v.ss.srv.Store.UpdateMessage(v.ss.ctx, v.ss.acct.ID, e.id, store.MessagePatch{MailboxID: &mb.ID}); err != nil {
			if isNotFound(err) {
				continue
			}
			return err
		}
		uid, err := v.ss.srv.Store.MessageUID(v.ss.ctx, e.id)
		if err != nil {
			return err
		}
		data.SourceUIDs.AddNum(e.uid)
		data.DestUIDs.AddNum(imap.UID(uid))
		moved = append(moved, i)
	}
	if len(moved) > 0 {
		if err := w.WriteCopyData(data); err != nil {
			return err
		}
	}
	for j := len(moved) - 1; j >= 0; j-- {
		i := moved[j]
		if err := w.WriteExpunge(uint32(i + 1)); err != nil {
			return err
		}
		v.msgs = append(v.msgs[:i], v.msgs[i+1:]...)
	}
	return nil
}
