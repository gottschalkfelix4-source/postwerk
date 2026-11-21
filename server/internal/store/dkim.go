package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DKIM key states. A pending key is published in DNS but not used yet, the
// active keys sign outgoing mail (at most one per algorithm) and retired
// keys stay published for a while so that mail signed shortly before the
// rotation still verifies.
const (
	DKIMPending = "pending"
	DKIMActive  = "active"
	DKIMRetired = "retired"
)

// DKIM key algorithms (see mailauth.AlgRSA/AlgEd25519).
const (
	DKIMAlgRSA     = "rsa"
	DKIMAlgEd25519 = "ed25519"
)

// DKIMRetireGrace is how long a retired key should stay published in DNS.
const DKIMRetireGrace = 7 * 24 * time.Hour

// DKIMKey is one DKIM key of a domain.
type DKIMKey struct {
	ID          int64      `json:"id"`
	Domain      string     `json:"domain"`
	Selector    string     `json:"selector"`
	Algorithm   string     `json:"algorithm"`
	PrivateKey  string     `json:"-"`
	State       string     `json:"state"`
	CreatedAt   time.Time  `json:"createdAt"`
	ActivatedAt *time.Time `json:"activatedAt"`
	RetiredAt   *time.Time `json:"retiredAt"`
}

// RemovableAt returns when the DNS record of a retired key may be removed.
func (k DKIMKey) RemovableAt() *time.Time {
	if k.State != DKIMRetired || k.RetiredAt == nil {
		return nil
	}
	t := k.RetiredAt.Add(DKIMRetireGrace)
	return &t
}

// DKIMStateError reports a transition that is not allowed; its message is
// meant for the user.
type DKIMStateError struct{ Msg string }

func (e *DKIMStateError) Error() string { return e.Msg }

func stateErr(format string, args ...any) error {
	return &DKIMStateError{Msg: fmt.Sprintf(format, args...)}
}

// ---- state machine (pure) ----

// checkNewDKIMKey validates that a new pending key of alg may be created.
func checkNewDKIMKey(keys []DKIMKey, alg string) error {
	if alg != DKIMAlgRSA && alg != DKIMAlgEd25519 {
		return stateErr("Unbekannter Schlüsseltyp: %s", alg)
	}
	for _, k := range keys {
		if k.Algorithm == alg && k.State == DKIMPending {
			return stateErr("Es gibt bereits einen neuen %s-Schlüssel (%s), der noch nicht aktiviert ist", algLabel(alg), k.Selector)
		}
	}
	return nil
}

// planDKIMActivation returns the keys whose state changes when key id is
// activated: the key itself becomes active and the active key of the same
// algorithm is retired.
func planDKIMActivation(keys []DKIMKey, id int64, now time.Time) ([]DKIMKey, error) {
	var target *DKIMKey
	for i := range keys {
		if keys[i].ID == id {
			target = &keys[i]
		}
	}
	if target == nil {
		return nil, ErrNotFound
	}
	if target.State != DKIMPending {
		return nil, stateErr("Nur neue (noch nicht aktive) Schlüssel können aktiviert werden")
	}
	t := now.UTC()
	changed := []DKIMKey{}
	for _, k := range keys {
		if k.ID != id && k.Algorithm == target.Algorithm && k.State == DKIMActive {
			k.State = DKIMRetired
			k.RetiredAt = &t
			changed = append(changed, k)
		}
	}
	k := *target
	k.State = DKIMActive
	k.ActivatedAt = &t
	return append(changed, k), nil
}

// checkDKIMDelete validates that a key may be removed.
func checkDKIMDelete(k DKIMKey, now time.Time) error {
	switch k.State {
	case DKIMActive:
		return stateErr("Aktive Schlüssel können nicht gelöscht werden – erst einen neuen Schlüssel aktivieren")
	case DKIMRetired:
		if r := k.RemovableAt(); r != nil && now.Before(*r) {
			return stateErr("Der alte Schlüssel bleibt bis %s veröffentlicht, damit kürzlich signierte Mails gültig bleiben", r.Local().Format("02.01.2006 15:04"))
		}
	}
	return nil
}

// NextDKIMSelector picks an unused selector "pw<YYYYMM>" (Ed25519 with
// suffix "e"), adding a counter if needed.
func NextDKIMSelector(keys []DKIMKey, alg string, now time.Time) string {
	base := "pw" + now.Format("200601")
	if alg == DKIMAlgEd25519 {
		base += "e"
	}
	used := map[string]bool{}
	for _, k := range keys {
		used[strings.ToLower(k.Selector)] = true
	}
	sel := base
	for n := 2; used[sel]; n++ {
		sel = fmt.Sprintf("%s-%d", base, n)
	}
	return sel
}

func algLabel(alg string) string {
	if alg == DKIMAlgEd25519 {
		return "Ed25519"
	}
	return "RSA"
}

// ---- persistence ----

const dkimCols = `id, domain, selector, algorithm, private_key, state, created_at, activated_at, retired_at`

func scanDKIMKey(sc interface{ Scan(...any) error }) (DKIMKey, error) {
	var k DKIMKey
	var created int64
	var act, ret sql.NullInt64
	if err := sc.Scan(&k.ID, &k.Domain, &k.Selector, &k.Algorithm, &k.PrivateKey, &k.State, &created, &act, &ret); err != nil {
		return k, err
	}
	k.CreatedAt = ts(created)
	k.ActivatedAt = nullTime(act)
	k.RetiredAt = nullTime(ret)
	return k, nil
}

// ListDKIMKeys returns all keys of a domain: active first, then pending,
// then retired; newest first within a state.
func (s *Store) ListDKIMKeys(ctx context.Context, domain string) ([]DKIMKey, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+dkimCols+` FROM dkim_keys WHERE domain = ?
		ORDER BY CASE state WHEN 'active' THEN 0 WHEN 'pending' THEN 1 ELSE 2 END, algorithm DESC, created_at DESC, id DESC`, Normalize(domain))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DKIMKey{}
	for rows.Next() {
		k, err := scanDKIMKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// ActiveDKIMKeys returns the signing keys of a domain (RSA first).
func (s *Store) ActiveDKIMKeys(ctx context.Context, domain string) ([]DKIMKey, error) {
	all, err := s.ListDKIMKeys(ctx, domain)
	if err != nil {
		return nil, err
	}
	out := []DKIMKey{}
	for _, k := range all {
		if k.State == DKIMActive {
			out = append(out, k)
		}
	}
	return out, nil
}

// GetDKIMKey returns one key of a domain.
func (s *Store) GetDKIMKey(ctx context.Context, domain string, id int64) (*DKIMKey, error) {
	k, err := scanDKIMKey(s.DB.QueryRowContext(ctx, `SELECT `+dkimCols+` FROM dkim_keys WHERE domain = ? AND id = ?`, Normalize(domain), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// AddDKIMKey stores a new key. A pending key may only be added when there
// is no other pending key of the same algorithm.
func (s *Store) AddDKIMKey(ctx context.Context, domain, selector, alg, privPEM, state string) (*DKIMKey, error) {
	domain = Normalize(domain)
	var out *DKIMKey
	err := s.tx(ctx, func(tx *sql.Tx) error {
		keys, err := listDKIMTx(ctx, tx, domain)
		if err != nil {
			return err
		}
		if state == DKIMPending {
			if err := checkNewDKIMKey(keys, alg); err != nil {
				return err
			}
		}
		t := now()
		var act any
		if state == DKIMActive {
			act = t
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO dkim_keys(domain, selector, algorithm, private_key, state, created_at, activated_at) VALUES (?,?,?,?,?,?,?)`,
			domain, selector, alg, privPEM, state, t, act)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return ErrConflict
			}
			return err
		}
		id, _ := res.LastInsertId()
		k := DKIMKey{ID: id, Domain: domain, Selector: selector, Algorithm: alg, PrivateKey: privPEM, State: state, CreatedAt: ts(t)}
		if state == DKIMActive {
			k.ActivatedAt = &k.CreatedAt
		}
		out = &k
		return nil
	})
	return out, err
}

func listDKIMTx(ctx context.Context, tx *sql.Tx, domain string) ([]DKIMKey, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+dkimCols+` FROM dkim_keys WHERE domain = ?`, domain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DKIMKey
	for rows.Next() {
		k, err := scanDKIMKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// ActivateDKIMKey switches signing to a pending key and retires the active
// key of the same algorithm. For RSA the domain's primary selector follows.
func (s *Store) ActivateDKIMKey(ctx context.Context, domain string, id int64) error {
	domain = Normalize(domain)
	return s.tx(ctx, func(tx *sql.Tx) error {
		keys, err := listDKIMTx(ctx, tx, domain)
		if err != nil {
			return err
		}
		changes, err := planDKIMActivation(keys, id, time.Now())
		if err != nil {
			return err
		}
		for _, k := range changes {
			var act, ret any
			if k.ActivatedAt != nil {
				act = k.ActivatedAt.Unix()
			}
			if k.RetiredAt != nil {
				ret = k.RetiredAt.Unix()
			}
			if _, err := tx.ExecContext(ctx, `UPDATE dkim_keys SET state = ?, activated_at = ?, retired_at = ? WHERE id = ?`, k.State, act, ret, k.ID); err != nil {
				return err
			}
			if k.State == DKIMActive && k.Algorithm == DKIMAlgRSA {
				// Keep the legacy columns pointing at the active RSA key.
				if _, err := tx.ExecContext(ctx, `UPDATE domains SET dkim_selector = ?, dkim_private_key = ? WHERE name = ?`, k.Selector, k.PrivateKey, domain); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// DeleteDKIMKey removes a pending key or a retired key after its grace
// period.
func (s *Store) DeleteDKIMKey(ctx context.Context, domain string, id int64) error {
	k, err := s.GetDKIMKey(ctx, domain, id)
	if err != nil {
		return err
	}
	if err := checkDKIMDelete(*k, time.Now()); err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `DELETE FROM dkim_keys WHERE id = ?`, id)
	return err
}

// SigningKeys returns the active keys of a domain, falling back to the key
// stored with the domain when the key table has none.
func (s *Store) SigningKeys(ctx context.Context, domain string) ([]DKIMKey, error) {
	keys, err := s.ActiveDKIMKeys(ctx, domain)
	if err != nil {
		return nil, err
	}
	if len(keys) > 0 {
		return keys, nil
	}
	d, err := s.GetDomain(ctx, domain)
	if err != nil {
		return nil, err
	}
	if d.DKIMPrivateKey == "" {
		return nil, nil
	}
	return []DKIMKey{{Domain: d.Name, Selector: d.DKIMSelector, Algorithm: DKIMAlgRSA, PrivateKey: d.DKIMPrivateKey, State: DKIMActive, CreatedAt: d.CreatedAt}}, nil
}
