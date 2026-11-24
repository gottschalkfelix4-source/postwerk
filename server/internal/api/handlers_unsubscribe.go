package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"postwerk/internal/message"
	"postwerk/internal/store"
)

// ---- newsletter list-unsubscribe ----

// unsubscribeInfo is what the message detail reports for the tooltip/button.
type unsubscribeInfo struct {
	// Header is the raw List-Unsubscribe header value (RFC 2369).
	Header string `json:"header"`
	// HTTPS holds a one-click URL (RFC 8058) if the list supports it.
	HTTPS string `json:"https"`
	// Mailto holds the mailto fallback address, if any.
	Mailto string `json:"mailto"`
}

// parseUnsubscribeHeader splits a List-Unsubscribe header into its
// <...> entries (POST/GET one-click URLs or mailto addresses).
func parseUnsubscribeHeader(v string) (info unsubscribeInfo) {
	info.Header = v
	for _, raw := range splitUnsubscribeEntries(v) {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		switch u.Scheme {
		case "https", "http":
			if info.HTTPS == "" {
				info.HTTPS = u.String()
			}
		case "mailto":
			if info.Mailto == "" && u.Opaque != "" {
				info.Mailto = u.Opaque
			} else if info.Mailto == "" && u.User == nil && u.Host != "" {
				// mailto:a@b handled through Opaque in most parsers; keep host fallback
				info.Mailto = u.Host
			}
		}
	}
	return info
}

// splitUnsubscribeEntries extracts the <...> entries of a comma separated
// List-Unsubscribe header (also tolerates bare URLs without angle brackets).
func splitUnsubscribeEntries(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		part = strings.TrimPrefix(part, "<")
		part = strings.TrimSuffix(part, ">")
		out = append(out, strings.TrimSpace(part))
	}
	return out
}

// safeUnsubscribeHost rejects SSRF targets: only public unicast addresses
// are allowed, no loopback/link-local/private/unspecified IPs.
func safeUnsubscribeHost(host string) error {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return errors.New("leere Adresse")
	}
	if strings.Contains(h, "@") || strings.ContainsAny(h, " \t") {
		return errors.New("ungültige Adresse")
	}
	hostOnly := h
	if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[i+1:], "]") {
		hostOnly = h[:i]
	}
	ips, err := net.LookupIP(hostOnly)
	if err != nil {
		return fmt.Errorf("Host %q nicht auflösbar", hostOnly)
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
			return fmt.Errorf("Adresse %q ist nicht erlaubt", hostOnly)
		}
	}
	return nil
}

// unsubscribe performs the actual list-unsubscribe: it posts the one-click
// request (RFC 8058) when possible, otherwise falls back to the mailto
// address which the client opens in the mail program.
func (s *Server) unsubscribe(ctx context.Context, m *store.Message) (unsubscribeInfo, error) {
	raw, err := s.Store.Blobs.Get(m.BlobHash)
	if err != nil {
		return unsubscribeInfo{}, err
	}
	p, err := message.Parse(raw, false)
	if err != nil {
		return unsubscribeInfo{}, invalid("Nachricht konnte nicht gelesen werden")
	}
	info := parseUnsubscribeHeader(p.Header.Get("List-Unsubscribe"))
	if strings.TrimSpace(info.Header) == "" {
		return info, invalid("Diese Nachricht hat keine Abmeldung (List-Unsubscribe)")
	}
	if info.HTTPS != "" {
		if err := s.oneClickUnsubscribe(ctx, info.HTTPS); err != nil {
			s.Log.Warn("list-unsubscribe one-click failed", "component", "webmail", "msg", m.ID, "err", err)
		} else {
			s.Log.Info("list-unsubscribe sent", "component", "webmail", "msg", m.ID, "url", info.HTTPS)
		}
	}
	// Remember the sender so future mail is filed into the Newsletter folder.
	if p.From.Email != "" {
		if err := s.Store.AddUnsubscriber(ctx, m.AccountID, p.From.Email); err != nil {
			s.Log.Warn("recording unsubscriber failed", "component", "webmail", "err", err)
		}
	}
	return info, nil
}

// oneClickUnsubscribe performs the RFC 8058 POST and tolerates lists that
// only support GET.
func (s *Server) oneClickUnsubscribe(ctx context.Context, target string) error {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("keine http(s)-URL")
	}
	if err := safeUnsubscribeHost(u.Host); err != nil {
		return err
	}
	body := strings.NewReader("List-Unsubscribe=One-Click")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("List-Unsubscribe", "One-Click")
	req.Header.Set("User-Agent", "Postwerk/1.0 (list-unsubscribe)")
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("zu viele Weiterleitungen")
		}
		if err := safeUnsubscribeHost(req.URL.Host); err != nil {
			return err
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// Fallback: older lists accept a GET on the same URL.
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req2.Header.Set("User-Agent", "Postwerk/1.0 (list-unsubscribe)")
	resp2, err := client.Do(req2)
	if err != nil {
		return err
	}
	defer resp2.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp2.Body, 1<<20))
	if resp2.StatusCode >= 200 && resp2.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("Liste antwortete mit HTTP %d", resp.StatusCode)
}

// handlers

func (s *Server) unsubscribeMessage(w http.ResponseWriter, r *http.Request, u *store.Account) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	m, err := s.Store.GetMessage(r.Context(), u.ID, id)
	if err != nil {
		return nil, errNotFound
	}
	info, err := s.unsubscribe(r.Context(), m)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "unsubscribe": info}, nil
}
