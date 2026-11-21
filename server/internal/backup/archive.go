// Package backup writes and restores archives of the complete Postwerk
// system (database, mail blobs, certificates) to local disk, WebDAV or SMB.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Manifest is stored as manifest.json at the start of every archive.
type Manifest struct {
	Format    int       `json:"format"`
	Version   string    `json:"version"`
	Hostname  string    `json:"hostname"`
	CreatedAt time.Time `json:"createdAt"`
	Domains   int       `json:"domains"`
	Accounts  int       `json:"accounts"`
	Messages  int       `json:"messages"`
	Encrypted bool      `json:"encrypted"`
}

const (
	manifestName = "manifest.json"
	dataPrefix   = "data/"
	dbName       = "postwerk.db"
)

var ErrNeedPassword = errors.New("das Archiv ist verschlüsselt – Passwort angeben")

// Source describes what Create puts into an archive.
type Source struct {
	DataDir  string
	DB       *sql.DB
	Manifest Manifest
	// Exclude lists absolute paths inside DataDir that are skipped (for
	// example a local backup folder).
	Exclude  []string
	Progress func(done, total int64)
}

// skipTop reports whether a top-level entry of the data directory stays out
// of archives: the live database (a snapshot is added instead), backups,
// restore leftovers and temporary files.
func skipTop(name string) bool {
	switch name {
	case dbName, dbName + "-wal", dbName + "-shm", dbName + "-journal",
		"backups", "restore-staging", pendingFile, "setup-token.txt":
		return true
	}
	return strings.HasPrefix(name, "pre-restore-") || strings.HasPrefix(name, ".backup-") ||
		strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".part")
}

type fileEntry struct {
	rel  string // slash-separated, relative to DataDir
	abs  string
	size int64
	mode fs.FileMode
	mod  time.Time
	dir  bool
}

func (src *Source) collect() ([]fileEntry, int64, error) {
	var out []fileEntry
	var total int64
	root := filepath.Clean(src.DataDir)
	excluded := func(p string) bool {
		for _, e := range src.Exclude {
			if e == "" {
				continue
			}
			e = filepath.Clean(e)
			if p == e || strings.HasPrefix(p, e+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // removed while walking
			}
			return err
		}
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if (!strings.Contains(rel, "/") && skipTop(rel)) || excluded(p) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		switch {
		case d.IsDir():
			out = append(out, fileEntry{rel: rel, abs: p, mode: info.Mode().Perm(), mod: info.ModTime(), dir: true})
		case info.Mode().IsRegular():
			out = append(out, fileEntry{rel: rel, abs: p, size: info.Size(), mode: info.Mode().Perm(), mod: info.ModTime()})
			total += info.Size()
		}
		return nil
	})
	return out, total, err
}

type countingWriter struct {
	w     io.Writer
	n     int64
	total int64
	fn    func(done, total int64)
	last  time.Time
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	if c.fn != nil && time.Since(c.last) > 300*time.Millisecond {
		c.last = time.Now()
		c.fn(c.n, c.total)
	}
	return n, err
}

// Create writes a complete archive to w and returns the number of files.
// With a non-empty password the archive is encrypted.
func Create(ctx context.Context, w io.Writer, src Source, password string) (int, error) {
	snap := filepath.Join(src.DataDir, ".backup-"+randHex(6)+".db")
	defer os.Remove(snap)
	if _, err := src.DB.ExecContext(ctx, `VACUUM INTO ?`, snap); err != nil {
		return 0, fmt.Errorf("datenbank-snapshot: %w", err)
	}
	snapInfo, err := os.Stat(snap)
	if err != nil {
		return 0, err
	}
	files, total, err := src.collect()
	if err != nil {
		return 0, fmt.Errorf("dateien sammeln: %w", err)
	}
	total += snapInfo.Size()

	out := w
	var enc io.WriteCloser
	if password != "" {
		if enc, err = NewEncryptWriter(w, password); err != nil {
			return 0, err
		}
		out = enc
	}
	gz, _ := gzip.NewWriterLevel(out, gzip.DefaultCompression)
	tw := tar.NewWriter(gz)
	progress := func(done int64) {
		if src.Progress != nil {
			src.Progress(done, total)
		}
	}

	m := src.Manifest
	m.Format = 1
	m.Encrypted = password != ""
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := tw.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o640, Size: int64(len(mb)), ModTime: m.CreatedAt, Typeflag: tar.TypeReg}); err != nil {
		return 0, err
	}
	if _, err := tw.Write(mb); err != nil {
		return 0, err
	}

	var done int64
	addFile := func(name, abs string, mode fs.FileMode, mod time.Time) error {
		f, err := os.Open(abs)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: int64(mode), Size: info.Size(), ModTime: mod, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		cw := &countingWriter{w: tw, n: done, total: total, fn: src.Progress}
		if _, err := io.CopyN(cw, f, info.Size()); err != nil {
			return err
		}
		done = cw.n
		return nil
	}
	if err := addFile(dataPrefix+dbName, snap, 0o640, snapInfo.ModTime()); err != nil {
		return 0, fmt.Errorf("datenbank: %w", err)
	}
	n := 1
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		if f.dir {
			if err := tw.WriteHeader(&tar.Header{Name: dataPrefix + f.rel + "/", Mode: int64(f.mode), ModTime: f.mod, Typeflag: tar.TypeDir}); err != nil {
				return n, err
			}
			continue
		}
		if err := addFile(dataPrefix+f.rel, f.abs, f.mode, f.mod); err != nil {
			return n, fmt.Errorf("%s: %w", f.rel, err)
		}
		n++
	}
	progress(done)
	if err := tw.Close(); err != nil {
		return n, err
	}
	if err := gz.Close(); err != nil {
		return n, err
	}
	if enc != nil {
		if err := enc.Close(); err != nil {
			return n, err
		}
	}
	return n, nil
}

// Open returns the plain tar.gz stream of an archive, decrypting if needed.
func Open(r io.Reader, password string) (io.Reader, bool, error) {
	format, br, err := Sniff(r)
	if err != nil {
		return nil, false, err
	}
	switch format {
	case FormatGzip:
		return br, false, nil
	case FormatEncrypted:
		if password == "" {
			return nil, true, ErrNeedPassword
		}
		dr, err := NewDecryptReader(br, password)
		return dr, true, err
	}
	return nil, false, errors.New("keine Postwerk-Sicherung (unbekanntes Format)")
}

// ExtractLimits bounds what a restore may write to disk.
type ExtractLimits struct {
	// MaxBytes caps the total size of extracted files. 0 means the free
	// space at the destination minus a safety margin (or DefaultMaxRestoreBytes
	// where free space cannot be determined).
	MaxBytes int64
	// MaxFiles caps the number of extracted entries. 0 means DefaultMaxRestoreFiles.
	MaxFiles int
}

const (
	DefaultMaxRestoreBytes = 1 << 40
	DefaultMaxRestoreFiles = 20_000_000
	restoreDiskReserve     = 512 << 20
)

var errRestoreTooLarge = errors.New("archiv ist größer als der freie Speicherplatz oder das Limit für Wiederherstellungen")

func (l ExtractLimits) resolve(dest string) ExtractLimits {
	if l.MaxFiles <= 0 {
		l.MaxFiles = DefaultMaxRestoreFiles
	}
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultMaxRestoreBytes
		if free, ok := freeDiskSpace(dest); ok {
			l.MaxBytes = max(int64(free)-restoreDiskReserve, 0)
		}
	}
	return l
}

// Extract unpacks an archive into dest (dest/data/... and the manifest) and
// checks that the database inside is intact.
func Extract(ctx context.Context, r io.Reader, password, dest string) (*Manifest, error) {
	return ExtractWithLimits(ctx, r, password, dest, ExtractLimits{})
}

// ExtractWithLimits is Extract with explicit size and file count limits.
// File modes from the archive are masked to 0640 (files) and 0750 (dirs).
func ExtractWithLimits(ctx context.Context, r io.Reader, password, dest string, lim ExtractLimits) (*Manifest, error) {
	lim = lim.resolve(dest)
	remaining := lim.MaxBytes
	files := 0
	plain, _, err := Open(r, password)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(plain)
	if err != nil {
		if errors.Is(err, ErrPassword) {
			return nil, err
		}
		return nil, fmt.Errorf("archiv lesen: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var man *Manifest
	haveDB := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if errors.Is(err, ErrPassword) {
				return nil, err
			}
			return nil, fmt.Errorf("archiv beschädigt: %w", err)
		}
		name := path.Clean(h.Name)
		if name == manifestName {
			var m Manifest
			if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&m); err != nil {
				return nil, fmt.Errorf("manifest: %w", err)
			}
			man = &m
			continue
		}
		if !strings.HasPrefix(name, dataPrefix) || strings.Contains(name, "..") || path.IsAbs(name) {
			return nil, fmt.Errorf("ungültiger Pfad im Archiv: %q", h.Name)
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if files++; files > lim.MaxFiles {
			return nil, fmt.Errorf("archiv enthält mehr als %d Dateien", lim.MaxFiles)
		}
		if h.Typeflag == tar.TypeReg && h.Size > remaining {
			return nil, errRestoreTooLarge
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return nil, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return nil, err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(h.Mode)&0o640|0o600)
			if err != nil {
				return nil, err
			}
			n, err := io.Copy(f, io.LimitReader(tr, remaining+1))
			remaining -= n
			if err == nil && remaining < 0 {
				err = errRestoreTooLarge
			}
			cerr := f.Close()
			if errors.Is(err, errRestoreTooLarge) {
				return nil, err
			}
			if err != nil {
				if errors.Is(err, ErrPassword) {
					return nil, err
				}
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			if cerr != nil {
				return nil, cerr
			}
			_ = os.Chtimes(target, h.ModTime, h.ModTime)
			if name == dataPrefix+dbName {
				haveDB = true
			}
		}
	}
	if man == nil || !haveDB {
		return nil, errors.New("archiv unvollständig: Manifest oder Datenbank fehlt")
	}
	if err := checkDB(ctx, filepath.Join(dest, "data", dbName)); err != nil {
		return nil, err
	}
	return man, nil
}

func checkDB(ctx context.Context, p string) error {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(p)+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var res string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&res); err != nil {
		return fmt.Errorf("datenbank im archiv nicht lesbar: %w", err)
	}
	if res != "ok" {
		return fmt.Errorf("datenbank im archiv beschädigt: %s", res)
	}
	return nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
