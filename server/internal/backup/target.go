package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"postwerk/internal/store"
)

// Entry is an archive stored on a target.
type Entry struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	Modified  time.Time `json:"modified"`
	Encrypted bool      `json:"encrypted"`
}

// Target stores archives. Implementations open a fresh connection per call.
type Target interface {
	Put(ctx context.Context, name string, r io.Reader, size int64) error
	Open(ctx context.Context, name string) (io.ReadCloser, int64, error)
	List(ctx context.Context) ([]Entry, error)
	Delete(ctx context.Context, name string) error
	// Describe is a short human-readable location, without credentials.
	Describe() string
	// Streaming reports whether Put accepts an unknown size (-1).
	Streaming() bool
}

var archiveName = regexp.MustCompile(`^postwerk-\d{8}-\d{6}(-[a-z0-9-]{1,40})?\.tar\.gz(\.enc)?$`)

// ValidName reports whether name looks like a Postwerk archive file name.
func ValidName(name string) bool { return archiveName.MatchString(name) }

// NewName returns the archive file name for a backup started at t.
func NewName(t time.Time, encrypted bool) string {
	n := "postwerk-" + t.Format("20060102-150405") + ".tar.gz"
	if encrypted {
		n += ".enc"
	}
	return n
}

func filterArchives(in []Entry) []Entry {
	out := []Entry{}
	for _, e := range in {
		if ValidName(e.Name) {
			e.Encrypted = strings.HasSuffix(e.Name, ".enc")
			out = append(out, e)
		}
	}
	// Newest first; names carry the timestamp.
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out
}

// NewTarget builds the target configured in the settings. dataDir is the
// fallback location for local backups.
func NewTarget(t store.BackupTarget, dataDir string) (Target, error) {
	switch t.Type {
	case "", "local":
		p := strings.TrimSpace(t.LocalPath)
		if p == "" {
			p = filepath.Join(dataDir, "backups")
		}
		return &localTarget{dir: p}, nil
	case "webdav":
		return newWebDAV(t.WebDAV.URL, t.WebDAV.Username, t.WebDAV.Password, t.WebDAV.SkipTLSVerify)
	case "smb":
		return newSMB(t.SMB.Host, t.SMB.Port, t.SMB.Share, t.SMB.Path, t.SMB.Username, t.SMB.Password, t.SMB.Domain)
	}
	return nil, fmt.Errorf("unbekanntes Backup-Ziel %q", t.Type)
}

// LocalDir returns the folder of a local target ("" for remote targets).
func LocalDir(t Target) string {
	if l, ok := t.(*localTarget); ok {
		return l.dir
	}
	return ""
}

type localTarget struct{ dir string }

func (l *localTarget) Describe() string { return l.dir }
func (l *localTarget) Streaming() bool  { return true }

func (l *localTarget) Put(ctx context.Context, name string, r io.Reader, size int64) error {
	if err := os.MkdirAll(l.dir, 0o750); err != nil {
		return err
	}
	tmp := filepath.Join(l.dir, name+".part")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, filepath.Join(l.dir, name))
}

func (l *localTarget) Open(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	f, err := os.Open(filepath.Join(l.dir, name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, info.Size(), nil
}

func (l *localTarget) List(ctx context.Context) ([]Entry, error) {
	entries, err := os.ReadDir(l.dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			out = append(out, Entry{Name: e.Name(), Size: info.Size(), Modified: info.ModTime().UTC()})
		}
	}
	return filterArchives(out), nil
}

func (l *localTarget) Delete(ctx context.Context, name string) error {
	err := os.Remove(filepath.Join(l.dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

var ErrNotFound = errors.New("archiv nicht gefunden")
