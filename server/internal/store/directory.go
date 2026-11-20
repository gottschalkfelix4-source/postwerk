package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Domain struct {
	Name           string    `json:"name"`
	CatchAll       *string   `json:"catchAll"`
	DKIMSelector   string    `json:"dkimSelector"`
	DKIMPrivateKey string    `json:"-"`
	AccountCount   int       `json:"accountCount"`
	AliasCount     int       `json:"aliasCount"`
	CreatedAt      time.Time `json:"createdAt"`
}

type Account struct {
	ID           int64     `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"`
	QuotaBytes   int64     `json:"quotaBytes"`
	UsedBytes    int64     `json:"usedBytes"`
	Disabled     bool      `json:"disabled"`
	TOTPSecret   string    `json:"-"`
	TOTPEnabled  bool      `json:"totpEnabled"`
	Aliases      []string  `json:"aliases"`
	CreatedAt    time.Time `json:"createdAt"`
}

type Alias struct {
	ID        int64     `json:"id"`
	Address   string    `json:"address"`
	Targets   []string  `json:"targets"`
	CreatedAt time.Time `json:"createdAt"`
}

type AppPassword struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Hash       string     `json:"-"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

func Normalize(addr string) string { return strings.ToLower(strings.TrimSpace(addr)) }

func DomainOf(addr string) string {
	if i := strings.LastIndexByte(addr, '@'); i >= 0 {
		return strings.ToLower(addr[i+1:])
	}
	return ""
}

// ---- domains ----

func (s *Store) ListDomains(ctx context.Context) ([]Domain, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT d.name, d.catch_all, d.dkim_selector, d.dkim_private_key, d.created_at,
		(SELECT COUNT(*) FROM accounts a WHERE a.email LIKE '%@' || d.name),
		(SELECT COUNT(*) FROM aliases x WHERE x.address LIKE '%@' || d.name)
		FROM domains d ORDER BY d.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Domain
	for rows.Next() {
		var d Domain
		var ca sql.NullString
		var created int64
		if err := rows.Scan(&d.Name, &ca, &d.DKIMSelector, &d.DKIMPrivateKey, &created, &d.AccountCount, &d.AliasCount); err != nil {
			return nil, err
		}
		d.CatchAll = nullStr(ca)
		d.CreatedAt = ts(created)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDomain(ctx context.Context, name string) (*Domain, error) {
	all, err := s.ListDomains(ctx)
	if err != nil {
		return nil, err
	}
	name = Normalize(name)
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
	}
	return nil, ErrNotFound
}

func (s *Store) DomainExists(ctx context.Context, name string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM domains WHERE name = ?`, Normalize(name)).Scan(&n)
	return n > 0, err
}

func (s *Store) CreateDomain(ctx context.Context, name, selector, privKeyPEM string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO domains(name, dkim_selector, dkim_private_key, created_at) VALUES (?,?,?,?)`,
		Normalize(name), selector, privKeyPEM, now())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrConflict
	}
	return err
}

func (s *Store) SetCatchAll(ctx context.Context, name string, target *string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE domains SET catch_all = ? WHERE name = ?`, toNull(target), Normalize(name))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteDomain(ctx context.Context, name string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM domains WHERE name = ?`, Normalize(name))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- accounts ----

const accountCols = `a.id, a.email, a.name, a.password_hash, a.role, a.quota_bytes, a.disabled, a.totp_secret, a.totp_enabled, a.created_at,
	(SELECT COALESCE(SUM(m.size),0) FROM messages m WHERE m.account_id = a.id)`

func scanAccount(sc interface{ Scan(...any) error }) (*Account, error) {
	var a Account
	var created int64
	if err := sc.Scan(&a.ID, &a.Email, &a.Name, &a.PasswordHash, &a.Role, &a.QuotaBytes, &a.Disabled, &a.TOTPSecret, &a.TOTPEnabled, &created, &a.UsedBytes); err != nil {
		return nil, err
	}
	a.CreatedAt = ts(created)
	a.Aliases = []string{}
	return &a, nil
}

func (s *Store) fillAliases(ctx context.Context, accts []*Account) error {
	if len(accts) == 0 {
		return nil
	}
	aliases, err := s.ListAliases(ctx)
	if err != nil {
		return err
	}
	for _, a := range accts {
		for _, al := range aliases {
			for _, t := range al.Targets {
				if t == a.Email {
					a.Aliases = append(a.Aliases, al.Address)
					break
				}
			}
		}
	}
	return nil
}

func (s *Store) ListAccounts(ctx context.Context, q string, limit, offset int) ([]*Account, int, error) {
	like := "%" + strings.ToLower(q) + "%"
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE lower(email) LIKE ? OR lower(name) LIKE ?`, like, like).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+accountCols+` FROM accounts a WHERE lower(a.email) LIKE ? OR lower(a.name) LIKE ? ORDER BY a.email LIMIT ? OFFSET ?`, like, like, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	var out []*Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			rows.Close()
			return nil, 0, err
		}
		out = append(out, a)
	}
	rows.Close()
	if err := s.fillAliases(ctx, out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (s *Store) GetAccount(ctx context.Context, id int64) (*Account, error) {
	a, err := scanAccount(s.DB.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts a WHERE a.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return a, s.fillAliases(ctx, []*Account{a})
}

func (s *Store) GetAccountByEmail(ctx context.Context, email string) (*Account, error) {
	a, err := scanAccount(s.DB.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts a WHERE a.email = ?`, Normalize(email)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return a, s.fillAliases(ctx, []*Account{a})
}

func (s *Store) CountAccounts(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts`).Scan(&n)
	return n, err
}

// CreateAccount inserts an account and its default mailboxes.
func (s *Store) CreateAccount(ctx context.Context, a *Account) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO accounts(email, name, password_hash, role, quota_bytes, created_at) VALUES (?,?,?,?,?,?)`,
			Normalize(a.Email), a.Name, a.PasswordHash, a.Role, a.QuotaBytes, now())
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return ErrConflict
			}
			return err
		}
		a.ID, _ = res.LastInsertId()
		for _, mb := range DefaultMailboxes {
			if _, err := tx.ExecContext(ctx, `INSERT INTO mailboxes(account_id, name, role, uid_validity, created_at) VALUES (?,?,?,?,?)`,
				a.ID, mb.Name, mb.Role, now(), now()); err != nil {
				return err
			}
		}
		return nil
	})
}

type AccountPatch struct {
	Name         *string
	PasswordHash *string
	Role         *string
	QuotaBytes   *int64
	Disabled     *bool
	TOTPSecret   *string
	TOTPEnabled  *bool
}

func (s *Store) UpdateAccount(ctx context.Context, id int64, p AccountPatch) error {
	sets := []string{}
	args := []any{}
	add := func(col string, v any) { sets = append(sets, col+" = ?"); args = append(args, v) }
	if p.Name != nil {
		add("name", *p.Name)
	}
	if p.PasswordHash != nil {
		add("password_hash", *p.PasswordHash)
	}
	if p.Role != nil {
		add("role", *p.Role)
	}
	if p.QuotaBytes != nil {
		add("quota_bytes", *p.QuotaBytes)
	}
	if p.Disabled != nil {
		add("disabled", *p.Disabled)
	}
	if p.TOTPSecret != nil {
		add("totp_secret", *p.TOTPSecret)
	}
	if p.TOTPEnabled != nil {
		add("totp_enabled", *p.TOTPEnabled)
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, id)
	res, err := s.DB.ExecContext(ctx, `UPDATE accounts SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteAccount(ctx context.Context, id int64) error {
	var hashes []string
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT blob_hash FROM messages WHERE account_id = ?`, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var h string
		rows.Scan(&h)
		hashes = append(hashes, h)
	}
	rows.Close()
	res, err := s.DB.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	for _, h := range hashes {
		s.releaseBlob(ctx, h)
	}
	return nil
}

// ---- aliases ----

func (s *Store) ListAliases(ctx context.Context) ([]Alias, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, address, targets, created_at FROM aliases ORDER BY address`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Alias{}
	for rows.Next() {
		var a Alias
		var t string
		var created int64
		if err := rows.Scan(&a.ID, &a.Address, &t, &created); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(t), &a.Targets)
		a.CreatedAt = ts(created)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetAlias(ctx context.Context, address string) (*Alias, error) {
	var a Alias
	var t string
	var created int64
	err := s.DB.QueryRowContext(ctx, `SELECT id, address, targets, created_at FROM aliases WHERE address = ?`, Normalize(address)).Scan(&a.ID, &a.Address, &t, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(t), &a.Targets)
	a.CreatedAt = ts(created)
	return &a, nil
}

func normalizeList(in []string) []string {
	out := []string{}
	for _, t := range in {
		if t = Normalize(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func (s *Store) CreateAlias(ctx context.Context, address string, targets []string) (*Alias, error) {
	targets = normalizeList(targets)
	b, _ := json.Marshal(targets)
	res, err := s.DB.ExecContext(ctx, `INSERT INTO aliases(address, targets, created_at) VALUES (?,?,?)`, Normalize(address), string(b), now())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrConflict
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Alias{ID: id, Address: Normalize(address), Targets: targets, CreatedAt: time.Now().UTC()}, nil
}

func (s *Store) UpdateAlias(ctx context.Context, id int64, targets []string) error {
	b, _ := json.Marshal(normalizeList(targets))
	res, err := s.DB.ExecContext(ctx, `UPDATE aliases SET targets = ? WHERE id = ?`, string(b), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteAlias(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM aliases WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ResolveRecipient expands an address into local account e-mails and external
// forward targets. Aliases are expanded recursively (max depth 5), then the
// domain catch-all applies if nothing matched.
func (s *Store) ResolveRecipient(ctx context.Context, addr string) (local []string, external []string, err error) {
	seen := map[string]bool{}
	var walk func(a string, depth int) error
	walk = func(a string, depth int) error {
		a = Normalize(a)
		if seen[a] || depth > 5 {
			return nil
		}
		seen[a] = true
		if acct, err := s.GetAccountByEmail(ctx, a); err == nil {
			if !acct.Disabled {
				local = append(local, acct.Email)
			}
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if al, err := s.GetAlias(ctx, a); err == nil {
			for _, t := range al.Targets {
				if err := walk(t, depth+1); err != nil {
					return err
				}
			}
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		ok, err := s.DomainExists(ctx, DomainOf(a))
		if err != nil {
			return err
		}
		if !ok {
			external = append(external, a)
			return nil
		}
		// Local domain but unknown mailbox: try catch-all once.
		if depth == 0 {
			d, err := s.GetDomain(ctx, DomainOf(a))
			if err == nil && d.CatchAll != nil && *d.CatchAll != "" {
				return walk(*d.CatchAll, depth+1)
			}
		}
		return nil
	}
	err = walk(addr, 0)
	return
}

// SenderAllowed reports whether the account may use addr as envelope/header sender.
func (s *Store) SenderAllowed(ctx context.Context, acct *Account, addr string) bool {
	addr = Normalize(addr)
	if addr == acct.Email {
		return true
	}
	for _, a := range acct.Aliases {
		if a == addr {
			return true
		}
	}
	return false
}

// ---- app passwords ----

func (s *Store) ListAppPasswords(ctx context.Context, accountID int64) ([]AppPassword, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, name, hash, created_at, last_used_at FROM app_passwords WHERE account_id = ? ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AppPassword{}
	for rows.Next() {
		var p AppPassword
		var created int64
		var last sql.NullInt64
		if err := rows.Scan(&p.ID, &p.Name, &p.Hash, &created, &last); err != nil {
			return nil, err
		}
		p.CreatedAt = ts(created)
		p.LastUsedAt = nullTime(last)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) CreateAppPassword(ctx context.Context, accountID int64, name, hash string) (*AppPassword, error) {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO app_passwords(account_id, name, hash, created_at) VALUES (?,?,?,?)`, accountID, name, hash, now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &AppPassword{ID: id, Name: name, CreatedAt: time.Now().UTC()}, nil
}

func (s *Store) TouchAppPassword(ctx context.Context, id int64) {
	_, _ = s.DB.ExecContext(ctx, `UPDATE app_passwords SET last_used_at = ? WHERE id = ?`, now(), id)
}

func (s *Store) DeleteAppPassword(ctx context.Context, accountID, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM app_passwords WHERE id = ? AND account_id = ?`, id, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- sessions ----

func (s *Store) CreateSession(ctx context.Context, tokenHash string, accountID int64, ttl time.Duration) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO sessions(token_hash, account_id, created_at, expires_at) VALUES (?,?,?,?)`,
		tokenHash, accountID, now(), time.Now().Add(ttl).Unix())
	return err
}

func (s *Store) SessionAccount(ctx context.Context, tokenHash string) (int64, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, `SELECT account_id FROM sessions WHERE token_hash = ? AND expires_at > ?`, tokenHash, now()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

func (s *Store) DeleteAccountSessions(ctx context.Context, accountID int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE account_id = ?`, accountID)
	return err
}

func (s *Store) PurgeExpired(ctx context.Context) {
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, now())
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM greylist WHERE last_seen < ?`, time.Now().Add(-40*24*time.Hour).Unix())
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM stats WHERE hour < ?`, time.Now().Add(-90*24*time.Hour).Unix()/3600)
}
