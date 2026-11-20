package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/hirochachacha/go-smb2"
)

// smbTarget writes to a Windows/Samba share (NAS, Unraid, Synology,
// Fritzbox, ...). SMB2/3 with NTLM authentication.
type smbTarget struct {
	host, share, dir   string
	port               int
	user, pass, domain string
}

func newSMB(host string, port int, share, dir, user, pass, domain string) (*smbTarget, error) {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(strings.TrimPrefix(host, "smb://"), `\\`)
	share = strings.Trim(strings.TrimSpace(share), `/\`)
	if host == "" || share == "" {
		return nil, errors.New("SMB: Server und Freigabe angeben")
	}
	if user == "" {
		return nil, errors.New("SMB: Benutzername angeben (für Gastzugriff z. B. „guest“)")
	}
	if port == 0 {
		port = 445
	}
	dir = strings.Trim(strings.ReplaceAll(strings.TrimSpace(dir), `\`, "/"), "/")
	return &smbTarget{host: host, port: port, share: share, dir: dir, user: user, pass: pass, domain: domain}, nil
}

func (s *smbTarget) Describe() string {
	d := `\\` + s.host + `\` + s.share
	if s.dir != "" {
		d += `\` + strings.ReplaceAll(s.dir, "/", `\`)
	}
	return d
}

func (s *smbTarget) Streaming() bool { return true }

type smbConn struct {
	conn  net.Conn
	sess  *smb2.Session
	share *smb2.Share
}

func (c *smbConn) Close() {
	if c.share != nil {
		_ = c.share.Umount()
	}
	if c.sess != nil {
		_ = c.sess.Logoff()
	}
	_ = c.conn.Close()
}

func (s *smbTarget) connect(ctx context.Context) (*smbConn, error) {
	d := net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(s.host, strconv.Itoa(s.port)))
	if err != nil {
		return nil, fmt.Errorf("SMB: %s nicht erreichbar: %w", s.host, err)
	}
	dialer := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: s.user, Password: s.pass, Domain: s.domain}}
	sess, err := dialer.DialContext(ctx, conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SMB: Anmeldung fehlgeschlagen: %w", err)
	}
	share, err := sess.WithContext(ctx).Mount(s.share)
	if err != nil {
		_ = sess.Logoff()
		conn.Close()
		return nil, fmt.Errorf("SMB: Freigabe %q nicht verfügbar: %w", s.share, err)
	}
	return &smbConn{conn: conn, sess: sess, share: share.WithContext(ctx)}, nil
}

func (s *smbTarget) file(name string) string {
	if s.dir == "" {
		return name
	}
	return path.Join(s.dir, name)
}

func (s *smbTarget) Put(ctx context.Context, name string, r io.Reader, size int64) error {
	c, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if s.dir != "" {
		if err := c.share.MkdirAll(s.dir, 0o755); err != nil {
			return fmt.Errorf("SMB: Ordner anlegen: %w", err)
		}
	}
	tmp := s.file(name + ".part")
	f, err := c.share.Create(tmp)
	if err != nil {
		return fmt.Errorf("SMB: Datei anlegen: %w", err)
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = c.share.Remove(tmp)
		return fmt.Errorf("SMB: schreiben: %w", err)
	}
	final := s.file(name)
	_ = c.share.Remove(final)
	if err := c.share.Rename(tmp, final); err != nil {
		return fmt.Errorf("SMB: umbenennen: %w", err)
	}
	return nil
}

type smbReader struct {
	*smb2.File
	c *smbConn
}

func (r *smbReader) Close() error {
	err := r.File.Close()
	r.c.Close()
	return err
}

func (s *smbTarget) Open(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	c, err := s.connect(ctx)
	if err != nil {
		return nil, 0, err
	}
	f, err := c.share.Open(s.file(name))
	if err != nil {
		c.Close()
		if errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, fmt.Errorf("SMB: öffnen: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		c.Close()
		return nil, 0, err
	}
	return &smbReader{File: f, c: c}, info.Size(), nil
}

func (s *smbTarget) List(ctx context.Context) ([]Entry, error) {
	c, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	dir := s.dir
	if dir == "" {
		dir = "."
	}
	infos, err := c.share.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
			return []Entry{}, nil
		}
		return nil, fmt.Errorf("SMB: auflisten: %w", err)
	}
	var out []Entry
	for _, fi := range infos {
		if fi.Mode().IsRegular() {
			out = append(out, Entry{Name: fi.Name(), Size: fi.Size(), Modified: fi.ModTime().UTC()})
		}
	}
	return filterArchives(out), nil
}

func (s *smbTarget) Delete(ctx context.Context, name string) error {
	c, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.share.Remove(s.file(name)); err != nil {
		if errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
			return ErrNotFound
		}
		return fmt.Errorf("SMB: löschen: %w", err)
	}
	return nil
}
