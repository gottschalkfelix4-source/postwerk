package store

import (
	"context"
	"strings"
)

// SystemLocalParts are the role addresses every hosted domain must accept
// (RFC 5321 4.5.1 for postmaster, RFC 2142 for abuse). Unless an account or
// alias with that address exists they are routed to the postmaster target.
var SystemLocalParts = []string{"postmaster", "abuse"}

// IsSystemAddress reports whether addr is postmaster@ or abuse@ of a domain.
func IsSystemAddress(addr string) bool {
	local, _, ok := strings.Cut(Normalize(addr), "@")
	if !ok {
		return false
	}
	for _, p := range SystemLocalParts {
		if local == p {
			return true
		}
	}
	return false
}

// PostmasterTarget returns where implicit postmaster@/abuse@ mail goes: the
// configured address or, by default, the first enabled admin account.
func (s *Store) PostmasterTarget(ctx context.Context) string {
	if s.settings != nil {
		if a := Normalize(s.settings.postmasterAddress()); a != "" {
			return a
		}
	}
	var email string
	_ = s.DB.QueryRowContext(ctx, `SELECT email FROM accounts WHERE role = 'admin' AND disabled = 0 ORDER BY id LIMIT 1`).Scan(&email)
	return email
}

// SystemAlias is an implicit postmaster@/abuse@ alias of a domain.
type SystemAlias struct {
	Address string   `json:"address"`
	Targets []string `json:"targets"`
	System  bool     `json:"system"`
	// Configured is true when the target comes from the settings rather
	// than the first admin account.
	Configured bool `json:"configured"`
}

// ListSystemAliases returns the implicit role addresses of all domains that
// have no account or alias of their own.
func (s *Store) ListSystemAliases(ctx context.Context) ([]SystemAlias, error) {
	domains, err := s.ListDomains(ctx)
	if err != nil {
		return nil, err
	}
	target := s.PostmasterTarget(ctx)
	configured := s.settings != nil && strings.TrimSpace(s.settings.postmasterAddress()) != ""
	taken := map[string]bool{}
	rows, err := s.DB.QueryContext(ctx, `SELECT email FROM accounts UNION SELECT address FROM aliases`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err == nil {
			taken[Normalize(a)] = true
		}
	}
	rows.Close()
	out := []SystemAlias{}
	for _, d := range domains {
		for _, p := range SystemLocalParts {
			addr := p + "@" + d.Name
			if taken[addr] {
				continue
			}
			sa := SystemAlias{Address: addr, Targets: []string{}, System: true, Configured: configured}
			if target != "" && target != addr {
				sa.Targets = append(sa.Targets, target)
			}
			out = append(out, sa)
		}
	}
	return out, nil
}
