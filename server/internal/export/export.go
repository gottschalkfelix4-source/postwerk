// Package export builds GDPR data exports: one ZIP per account with all
// mail folders as mbox files, calendar and tasks as iCalendar, account
// information, app password names and sessions (never secrets). Exports are
// written in the background to <data dir>/exports and can be downloaded for
// a limited time.
package export

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"postwerk/internal/store"
)

// Section adds extra files to an export. Features that keep their own data
// per account (for example contacts as contacts.vcf) register one with
// RegisterSection; add creates a file inside the ZIP.
type Section struct {
	Name  string
	Write func(ctx context.Context, st *store.Store, accountID int64, add func(name string) (io.Writer, error)) error
}

var (
	sectionsMu sync.Mutex
	sections   []Section
)

// RegisterSection adds a section to every future export.
func RegisterSection(s Section) {
	sectionsMu.Lock()
	defer sectionsMu.Unlock()
	sections = append(sections, s)
}

func registered() []Section {
	sectionsMu.Lock()
	defer sectionsMu.Unlock()
	return append([]Section(nil), sections...)
}

// Progress reports the current phase and how many messages were written.
type Progress func(phase string, done, total int64)

// Build writes the complete export of an account as ZIP to w.
func Build(ctx context.Context, st *store.Store, accountID int64, w io.Writer, progress Progress) error {
	if progress == nil {
		progress = func(string, int64, int64) {}
	}
	acct, err := st.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	boxes, err := st.ListMailboxes(ctx, accountID)
	if err != nil {
		return err
	}
	var total int64
	for _, b := range boxes {
		total += int64(b.Total)
	}
	zw := zip.NewWriter(w)
	created := time.Now()
	add := func(name string) (io.Writer, error) {
		return zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: created})
	}
	writeJSON := func(name string, v any) error {
		f, err := add(name)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}

	progress("Kontodaten", 0, total)
	if err := writeAccount(ctx, st, acct, writeJSON); err != nil {
		return err
	}

	var done int64
	names := map[string]bool{}
	var folders []folderInfo
	for _, b := range boxes {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := uniqueName(names, "mail/"+safeName(b.Name), ".mbox")
		progress("Ordner "+b.Name, done, total)
		f, err := add(name)
		if err != nil {
			return err
		}
		msgs, err := st.ListMailboxMessages(ctx, b.ID)
		if err != nil {
			return err
		}
		info := folderInfo{Folder: b.Name, File: name, Messages: len(msgs)}
		for _, m := range msgs {
			if err := ctx.Err(); err != nil {
				return err
			}
			raw, err := st.Blobs.Get(m.BlobHash)
			if err != nil {
				info.Missing++
			} else if err := writeMbox(f, m, raw); err != nil {
				return err
			}
			done++
			if done%50 == 0 {
				progress("Ordner "+b.Name, done, total)
			}
		}
		folders = append(folders, info)
	}
	if err := writeJSON("mail/ordner.json", folders); err != nil {
		return err
	}

	progress("Kalender und Aufgaben", done, total)
	events, err := st.ListEvents(ctx, accountID, "", "")
	if err != nil {
		return err
	}
	tasks, err := st.ListTasks(ctx, accountID)
	if err != nil {
		return err
	}
	if len(events) > 0 || len(tasks) > 0 {
		f, err := add("kalender.ics")
		if err != nil {
			return err
		}
		if _, err := io.WriteString(f, ICalendar(acct.Email, events, tasks, created)); err != nil {
			return err
		}
	}

	for _, s := range registered() {
		progress(s.Name, done, total)
		if err := s.Write(ctx, st, accountID, add); err != nil {
			return fmt.Errorf("%s: %w", s.Name, err)
		}
	}

	f, err := add("LIESMICH.txt")
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, readme(acct, created, done)); err != nil {
		return err
	}
	progress("Abschluss", done, total)
	return zw.Close()
}

type folderInfo struct {
	Folder   string `json:"folder"`
	File     string `json:"file"`
	Messages int    `json:"messages"`
	Missing  int    `json:"missing,omitempty"`
}

func writeAccount(ctx context.Context, st *store.Store, a *store.Account, writeJSON func(string, any) error) error {
	type encryptionInfo struct {
		Kind        string     `json:"kind"`
		Fingerprint string     `json:"fingerprint"`
		Description string     `json:"description"`
		Expires     *time.Time `json:"expires"`
		HideSubject bool       `json:"hideSubject"`
		PublicKey   string     `json:"publicKey"`
	}
	info := map[string]any{
		"email":       a.Email,
		"name":        a.Name,
		"role":        a.Role,
		"quotaBytes":  a.QuotaBytes,
		"usedBytes":   a.UsedBytes,
		"disabled":    a.Disabled,
		"totpEnabled": a.TOTPEnabled,
		"aliases":     a.Aliases,
		"createdAt":   a.CreatedAt,
	}
	settings := map[string]any{}
	if v, err := st.GetVacation(ctx, a.ID); err == nil {
		settings["vacation"] = v
	}
	if e, err := st.GetMailEncryption(ctx, a.ID); err == nil {
		settings["encryptionAtRest"] = encryptionInfo{Kind: e.Kind, Fingerprint: e.Fingerprint, Description: e.Description,
			Expires: e.Expires, HideSubject: e.HideSubject, PublicKey: e.PublicKey}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if n, err := st.CountRecoveryCodes(ctx, a.ID); err == nil {
		settings["recoveryCodesLeft"] = n
	}
	info["settings"] = settings
	if err := writeJSON("konto.json", info); err != nil {
		return err
	}
	apps, err := st.ListAppPasswords(ctx, a.ID)
	if err != nil {
		return err
	}
	if err := writeJSON("app-passwoerter.json", apps); err != nil {
		return err
	}
	sessions, err := st.ListSessions(ctx, a.ID)
	if err != nil {
		return err
	}
	return writeJSON("sitzungen.json", sessions)
}

// writeMbox appends one message in mboxrd format (LF line endings, ">From "
// quoting) to w.
func writeMbox(w io.Writer, m *store.Message, raw []byte) error {
	sender := m.From.Email
	if sender == "" || strings.ContainsAny(sender, " \t") {
		sender = "MAILER-DAEMON"
	}
	var b bytes.Buffer
	b.WriteString("From " + sender + " " + m.ReceivedAt.UTC().Format(time.ANSIC) + "\n")
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, ">"), "From ") {
			b.WriteByte('>')
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	_, err := w.Write(b.Bytes())
	return err
}

// safeName turns a folder name into a portable file name.
func safeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 32 || strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.Trim(strings.TrimSpace(b.String()), ".")
	if s == "" {
		s = "Ordner"
	}
	if len(s) > 100 {
		s = s[:100]
	}
	return s
}

func uniqueName(used map[string]bool, base, ext string) string {
	name := base + ext
	for i := 2; used[strings.ToLower(name)]; i++ {
		name = fmt.Sprintf("%s-%d%s", base, i, ext)
	}
	used[strings.ToLower(name)] = true
	return name
}

func readme(a *store.Account, created time.Time, messages int64) string {
	extra := []string{}
	for _, s := range registered() {
		extra = append(extra, s.Name)
	}
	sort.Strings(extra)
	more := ""
	if len(extra) > 0 {
		more = "\nWeitere Bereiche: " + strings.Join(extra, ", ") + "\n"
	}
	return fmt.Sprintf(`Postwerk – Datenexport (Art. 15/20 DSGVO)
==========================================

Konto:     %s
Erstellt:  %s
Nachrichten: %d

Inhalt
------
konto.json            Kontodaten und persönliche Einstellungen (ohne Passwörter und Geheimnisse)
app-passwoerter.json  Namen und Nutzung der App-Passwörter (die Passwörter selbst sind nicht gespeichert)
sitzungen.json        Aktive Anmeldungen an der Weboberfläche
mail/*.mbox           Ein mbox-Archiv (mboxrd) pro Ordner, z. B. mit Thunderbird
                      (Add-on „ImportExportTools NG“) oder mutt lesbar
mail/ordner.json      Zuordnung der Ordner zu den Dateien
kalender.ics          Termine und Aufgaben im iCalendar-Format (falls vorhanden)
%s
Verschlüsselt gespeicherte Nachrichten („Verschlüsselung im Ruhezustand“)
sind so exportiert, wie sie gespeichert sind – zum Lesen wird der eigene
private Schlüssel benötigt.
`, a.Email, created.Format("02.01.2006 15:04 MST"), messages, more)
}
