package backup

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"postwerk/internal/httpx"
)

// webdavTarget talks plain WebDAV (Nextcloud, ownCloud, Synology, QNAP,
// Hetzner Storage Box, ...): PUT, GET, DELETE, PROPFIND and MKCOL.
type webdavTarget struct {
	base       *url.URL // collection URL, always ending in "/"
	user, pass string
	client     *http.Client
}

func newWebDAV(raw, user, pass string, skipVerify bool) (*webdavTarget, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("WebDAV-URL muss mit http:// oder https:// beginnen")
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if skipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	// No overall timeout: uploads of large archives take a while. Stalled
	// connections are caught by the response header timeout.
	tr.ResponseHeaderTimeout = 2 * time.Minute
	return &webdavTarget{base: u, user: user, pass: pass, client: &http.Client{Transport: tr, CheckRedirect: httpx.SameHostRedirect}}, nil
}

func (w *webdavTarget) Describe() string {
	u := *w.base
	u.User = nil
	return u.String()
}

// Uploads need a Content-Length: many WebDAV servers reject chunked bodies.
func (w *webdavTarget) Streaming() bool { return false }

func (w *webdavTarget) fileURL(name string) string {
	u := *w.base
	u.Path = path.Join(u.Path, name)
	return u.String()
}

func (w *webdavTarget) do(ctx context.Context, method, target string, body io.Reader, size int64, hdr map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	if size >= 0 && body != nil {
		req.ContentLength = size
	}
	if w.user != "" || w.pass != "" {
		req.SetBasicAuth(w.user, w.pass)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		res.Body.Close()
		return nil, fmt.Errorf("WebDAV: Anmeldung abgelehnt (HTTP %d) – Benutzer/Passwort prüfen", res.StatusCode)
	}
	return res, nil
}

func statusErr(op string, res *http.Response) error {
	// The response body is not echoed: it may come from any host the admin
	// entered and must not turn the target test into a way to read it.
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if txt := http.StatusText(res.StatusCode); txt != "" {
		return fmt.Errorf("WebDAV %s: HTTP %d %s", op, res.StatusCode, txt)
	}
	return fmt.Errorf("WebDAV %s: HTTP %d", op, res.StatusCode)
}

// ensureCollection creates the target folder (and parents) if missing.
func (w *webdavTarget) ensureCollection(ctx context.Context) error {
	res, err := w.do(ctx, "PROPFIND", w.base.String(), nil, -1, map[string]string{"Depth": "0"})
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode == http.StatusMultiStatus || res.StatusCode == http.StatusOK {
		return nil
	}
	if res.StatusCode != http.StatusNotFound {
		return statusErr("Ordner prüfen", res)
	}
	parts := strings.Split(strings.Trim(w.base.Path, "/"), "/")
	u := *w.base
	cur := ""
	for _, p := range parts {
		cur += "/" + p
		u.Path = cur + "/"
		res, err := w.do(ctx, "MKCOL", u.String(), nil, -1, nil)
		if err != nil {
			return err
		}
		res.Body.Close()
		// 405: exists already; 301/302 on some servers for existing folders.
		if res.StatusCode >= 400 && res.StatusCode != http.StatusMethodNotAllowed && res.StatusCode != http.StatusConflict {
			return statusErr("Ordner anlegen", res)
		}
	}
	return nil
}

func (w *webdavTarget) Put(ctx context.Context, name string, r io.Reader, size int64) error {
	if err := w.ensureCollection(ctx); err != nil {
		return err
	}
	res, err := w.do(ctx, http.MethodPut, w.fileURL(name), r, size, map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return statusErr("Hochladen", res)
	}
	return nil
}

func (w *webdavTarget) Open(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	res, err := w.do(ctx, http.MethodGet, w.fileURL(name), nil, -1, nil)
	if err != nil {
		return nil, 0, err
	}
	if res.StatusCode == http.StatusNotFound {
		res.Body.Close()
		return nil, 0, ErrNotFound
	}
	if res.StatusCode != http.StatusOK {
		defer res.Body.Close()
		return nil, 0, statusErr("Herunterladen", res)
	}
	return res.Body, res.ContentLength, nil
}

func (w *webdavTarget) Delete(ctx context.Context, name string) error {
	res, err := w.do(ctx, http.MethodDelete, w.fileURL(name), nil, -1, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if res.StatusCode >= 300 {
		return statusErr("Löschen", res)
	}
	return nil
}

type multistatus struct {
	Responses []struct {
		Href  string `xml:"href"`
		Props []struct {
			Status string `xml:"status"`
			Prop   struct {
				Length       string `xml:"getcontentlength"`
				LastModified string `xml:"getlastmodified"`
				ResourceType struct {
					Collection *struct{} `xml:"collection"`
				} `xml:"resourcetype"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

const propfindBody = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:"><d:prop><d:getcontentlength/><d:getlastmodified/><d:resourcetype/></d:prop></d:propfind>`

func (w *webdavTarget) List(ctx context.Context) ([]Entry, error) {
	res, err := w.do(ctx, "PROPFIND", w.base.String(), strings.NewReader(propfindBody), int64(len(propfindBody)),
		map[string]string{"Depth": "1", "Content-Type": "application/xml; charset=utf-8"})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return []Entry{}, nil
	}
	if res.StatusCode != http.StatusMultiStatus {
		return nil, statusErr("Auflisten", res)
	}
	var ms multistatus
	if err := xml.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(&ms); err != nil {
		return nil, fmt.Errorf("WebDAV-Antwort unlesbar: %w", err)
	}
	var out []Entry
	for _, r := range ms.Responses {
		href, err := url.PathUnescape(r.Href)
		if err != nil {
			href = r.Href
		}
		if u, err := url.Parse(href); err == nil && u.Path != "" {
			href = u.Path
		}
		name := path.Base(strings.TrimSuffix(href, "/"))
		e := Entry{Name: name}
		isDir := strings.HasSuffix(href, "/")
		for _, ps := range r.Props {
			if ps.Status != "" && !strings.Contains(ps.Status, " 200 ") {
				continue
			}
			if ps.Prop.ResourceType.Collection != nil {
				isDir = true
			}
			if n, err := strconv.ParseInt(ps.Prop.Length, 10, 64); err == nil {
				e.Size = n
			}
			if t, err := http.ParseTime(ps.Prop.LastModified); err == nil {
				e.Modified = t.UTC()
			}
		}
		if !isDir {
			out = append(out, e)
		}
	}
	return filterArchives(out), nil
}
