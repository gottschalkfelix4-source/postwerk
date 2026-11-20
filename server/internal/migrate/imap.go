// Package migrate imports mail from other servers (IMAP) and from files
// (mbox, eml, zip).
package migrate

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

type Source struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"` // tls | starttls | none
}

func (s Source) addr() string { return net.JoinHostPort(s.Host, strconv.Itoa(s.Port)) }

func (s Source) validate() error {
	if strings.TrimSpace(s.Host) == "" {
		return errors.New("Server fehlt")
	}
	if s.Port <= 0 || s.Port > 65535 {
		return errors.New("ungültiger Port")
	}
	switch s.Security {
	case "tls", "starttls", "none":
	default:
		return errors.New("ungültige Verschlüsselung")
	}
	return nil
}

// dial connects and logs in to the source server.
func dial(ctx context.Context, src Source, user, pass string) (*client.Client, error) {
	if err := src.validate(); err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: 20 * time.Second}
	cfg := &tls.Config{ServerName: src.Host, MinVersion: tls.VersionTLS12}
	var c *client.Client
	var err error
	switch src.Security {
	case "tls":
		c, err = client.DialWithDialerTLS(d, src.addr(), cfg)
	default:
		c, err = client.DialWithDialer(d, src.addr())
	}
	if err != nil {
		return nil, friendlyErr(err)
	}
	c.Timeout = 2 * time.Minute
	if src.Security == "starttls" {
		if err := c.StartTLS(cfg); err != nil {
			c.Logout()
			return nil, friendlyErr(err)
		}
	}
	if err := c.Login(user, pass); err != nil {
		c.Logout()
		return nil, fmt.Errorf("Anmeldung fehlgeschlagen: %v", err)
	}
	go func() {
		<-ctx.Done()
		c.Terminate()
	}()
	return c, nil
}

func friendlyErr(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "certificate"):
		return fmt.Errorf("TLS-Zertifikat der Quelle ungültig: %v", err)
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("Verbindung abgelehnt (Server/Port prüfen): %v", err)
	case strings.Contains(msg, "no such host"):
		return fmt.Errorf("Server nicht gefunden: %v", err)
	case strings.Contains(msg, "timeout"):
		return fmt.Errorf("Keine Verbindung zum Server (Zeitüberschreitung) – das liegt nicht an den Zugangsdaten, sondern am Netzwerk: %v", err)
	}
	return err
}

type Folder struct {
	Name       string  `json:"name"`
	Messages   int     `json:"messages"`
	SpecialUse *string `json:"specialUse"`
	Target     string  `json:"target"`
	delimiter  string
}

var specialUse = map[string]string{
	imap.SentAttr: "sent", imap.DraftsAttr: "drafts", imap.JunkAttr: "junk",
	imap.TrashAttr: "trash", imap.ArchiveAttr: "archive",
}

// nameRoles maps common folder names (lowercase, last path element) to roles.
var nameRoles = map[string]string{
	"inbox": "inbox", "posteingang": "inbox",
	"sent": "sent", "sent items": "sent", "sent messages": "sent", "sent mail": "sent", "gesendet": "sent",
	"gesendete objekte": "sent", "gesendete elemente": "sent", "gesendete nachrichten": "sent",
	"drafts": "drafts", "draft": "drafts", "entwürfe": "drafts", "entwurf": "drafts",
	"junk": "junk", "spam": "junk", "junk e-mail": "junk", "junk-e-mail": "junk", "bulk mail": "junk",
	"trash": "trash", "deleted items": "trash", "deleted messages": "trash", "papierkorb": "trash", "gelöschte elemente": "trash", "gelöschte objekte": "trash", "bin": "trash",
	"archive": "archive", "archiv": "archive", "archives": "archive",
}

// mapFolder decides where a source folder goes: a role ("inbox", "sent", …)
// or a folder name (hierarchy separators become "/").
func mapFolder(name, delim string, attrs []string) (string, *string) {
	for _, a := range attrs {
		if role, ok := specialUse[a]; ok {
			au := a
			return role, &au
		}
	}
	if strings.EqualFold(name, "INBOX") {
		return "inbox", nil
	}
	parts := []string{name}
	if delim != "" {
		parts = strings.Split(name, delim)
	}
	last := parts[len(parts)-1]
	parent := strings.ToLower(strings.Join(parts[:len(parts)-1], "/"))
	// Well-known names count only at top level or directly below INBOX / [Gmail].
	if role, ok := nameRoles[strings.ToLower(last)]; ok && (parent == "" || parent == "inbox" || parent == "[gmail]" || parent == "[google mail]") {
		return role, nil
	}
	target := name
	if delim != "" && delim != "/" {
		target = strings.ReplaceAll(name, delim, "/")
	}
	if strings.HasPrefix(strings.ToUpper(target), "INBOX/") {
		target = target[len("INBOX/"):]
	}
	return target, nil
}

// skipFolder reports folders that never hold real mail (e.g. Gmail's virtual "All Mail").
func skipFolder(attrs []string) bool {
	for _, a := range attrs {
		switch a {
		case imap.NoSelectAttr, imap.AllAttr, imap.FlaggedAttr, "\\NonExistent", "\\Important":
			return true
		}
	}
	return false
}

// ListFolders returns the selectable folders of the source mailbox.
func ListFolders(ctx context.Context, src Source, user, pass string) ([]Folder, error) {
	c, err := dial(ctx, src, user, pass)
	if err != nil {
		return nil, err
	}
	defer c.Logout()
	return listFolders(c)
}

func listFolders(c *client.Client) ([]Folder, error) {
	ch := make(chan *imap.MailboxInfo, 50)
	done := make(chan error, 1)
	go func() { done <- c.List("", "*", ch) }()
	var infos []*imap.MailboxInfo
	for mi := range ch {
		infos = append(infos, mi)
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("Ordnerliste: %v", err)
	}
	var out []Folder
	for _, mi := range infos {
		if skipFolder(mi.Attributes) {
			continue
		}
		target, su := mapFolder(mi.Name, mi.Delimiter, mi.Attributes)
		f := Folder{Name: mi.Name, SpecialUse: su, Target: target, delimiter: mi.Delimiter}
		if st, err := c.Status(mi.Name, []imap.StatusItem{imap.StatusMessages}); err == nil {
			f.Messages = int(st.Messages)
		}
		out = append(out, f)
	}
	order := map[string]int{"inbox": 0, "sent": 1, "drafts": 2, "archive": 3, "junk": 8, "trash": 9}
	sort.SliceStable(out, func(i, j int) bool {
		oi, ok := order[out[i].Target]
		if !ok {
			oi = 5
		}
		oj, ok := order[out[j].Target]
		if !ok {
			oj = 5
		}
		if oi != oj {
			return oi < oj
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

type fetched struct {
	uid      uint32
	raw      []byte
	date     time.Time
	seen     bool
	flagged  bool
	answered bool
	deleted  bool
	size     uint32
}

// folderUIDs returns all UIDs in the selected folder (optionally since a date).
func folderUIDs(c *client.Client, since time.Time) ([]uint32, error) {
	crit := imap.NewSearchCriteria()
	if !since.IsZero() {
		crit.Since = since
	}
	uids, err := c.UidSearch(crit)
	if err != nil {
		return nil, err
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	return uids, nil
}

// fetchBatch downloads full messages for the given UIDs.
func fetchBatch(c *client.Client, uids []uint32) ([]fetched, error) {
	set := new(imap.SeqSet)
	set.AddNum(uids...)
	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{imap.FetchUid, imap.FetchFlags, imap.FetchInternalDate, imap.FetchRFC822Size, section.FetchItem()}
	ch := make(chan *imap.Message, len(uids))
	done := make(chan error, 1)
	go func() { done <- c.UidFetch(set, items, ch) }()
	var out []fetched
	for msg := range ch {
		f := fetched{uid: msg.Uid, date: msg.InternalDate, size: msg.Size}
		for _, fl := range msg.Flags {
			switch fl {
			case imap.SeenFlag:
				f.seen = true
			case imap.FlaggedFlag:
				f.flagged = true
			case imap.AnsweredFlag:
				f.answered = true
			case imap.DeletedFlag:
				f.deleted = true
			}
		}
		if body := msg.GetBody(section); body != nil {
			b, err := io.ReadAll(body)
			if err == nil {
				f.raw = b
			}
		}
		out = append(out, f)
	}
	return out, <-done
}
