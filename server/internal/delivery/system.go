package delivery

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// SendSystemMail delivers a message the server generated itself (alerts and
// similar). It is DKIM-signed for the domain of from; local recipients get it
// directly, remote ones through the queue with the null envelope sender, so a
// failed delivery never produces a bounce that could start a loop. System
// mail does not count against any account's send limits.
func (s *Service) SendSystemMail(ctx context.Context, from string, rcpts []string, raw []byte) error {
	signed := s.sign(ctx, from, raw)
	var remote []string
	seen := map[string]bool{}
	var errs []string
	delivered := 0
	for _, r := range rcpts {
		local, external, err := s.Store.ResolveRecipient(ctx, r)
		if err != nil {
			errs = append(errs, r+": "+err.Error())
			continue
		}
		if len(local) == 0 && len(external) == 0 {
			errs = append(errs, r+": unbekannte Adresse")
			continue
		}
		for _, l := range local {
			if seen[l] {
				continue
			}
			seen[l] = true
			if err := s.deliverLocal(ctx, l, signed, nil, deliveryMeta{Rcpt: r}, ""); err != nil {
				errs = append(errs, l+": "+err.Error())
				continue
			}
			delivered++
		}
		for _, e := range external {
			if !seen[e] {
				seen[e] = true
				remote = append(remote, e)
			}
		}
	}
	if len(remote) > 0 {
		if err := s.enqueue(ctx, "", remote, signed); err != nil {
			errs = append(errs, "Warteschlange: "+err.Error())
		} else {
			delivered += len(remote)
		}
	}
	if delivered == 0 {
		if len(errs) == 0 {
			return errors.New("keine Empfänger")
		}
		return fmt.Errorf("zustellung fehlgeschlagen: %s", strings.Join(errs, "; "))
	}
	if len(errs) > 0 {
		s.Log.Warn("system mail partially delivered", "errors", strings.Join(errs, "; "))
	}
	return nil
}

// SystemSender returns the address the server uses for its own messages:
// postmaster@ of the local domain that matches the hostname best, otherwise
// of the first domain, otherwise of the hostname itself.
func (s *Service) SystemSender(ctx context.Context) string {
	host := strings.ToLower(strings.TrimSuffix(s.hostname(), "."))
	for d := host; strings.Contains(d, "."); {
		if s.isLocalDomain(ctx, d) {
			return "postmaster@" + d
		}
		_, rest, _ := strings.Cut(d, ".")
		d = rest
	}
	if domains, err := s.Store.ListDomains(ctx); err == nil && len(domains) > 0 {
		return "postmaster@" + domains[0].Name
	}
	if host == "" {
		host = "localhost"
	}
	return "postmaster@" + host
}
