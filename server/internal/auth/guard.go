package auth

import (
	"context"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"postwerk/internal/store"
)

// Brute-force protection.
//
// Failed logins are counted per client (IPv4 address or IPv6 /64 network),
// per account and per (client, account) pair. A client is banned once it
// reaches Limits.MaxAttempts failures within Limits.Window, no matter which
// accounts it tried. A successful login only forgives the failures of that
// client for that account, so logging into an own account does not reset the
// counter for attacks against other accounts.
//
// Many failures for one account (from any number of clients) lock the account
// for a short, growing time. The lock does not apply to clients that recently
// logged into that account successfully, so an attacker cannot lock the owner
// out of their usual devices.

type Ban struct {
	IP       string    `json:"ip"`
	Reason   string    `json:"reason"`
	Until    time.Time `json:"until"`
	Failures int       `json:"failures"`
}

type Limits struct {
	MaxAttempts int
	Window      time.Duration
	BanFor      time.Duration
}

// BanStore persists bans so they survive a restart (implemented by *store.Store).
type BanStore interface {
	SaveAuthBan(ctx context.Context, b store.AuthBan) error
	DeleteAuthBan(ctx context.Context, key string) error
	ActiveAuthBans(ctx context.Context) ([]store.AuthBan, error)
}

const (
	// maxTracked bounds every in-memory map of the guard.
	maxTracked = 50000
	// minAccountAttempts is the minimum number of failures for one account
	// (from all clients together) before it is locked.
	minAccountAttempts = 10
	accountLockBase    = time.Minute
	accountLockMax     = 15 * time.Minute
	// lockLevelReset forgets the lock escalation after a quiet hour.
	lockLevelReset = time.Hour
	// knownClientFor is how long a successful login exempts that client
	// from account locks.
	knownClientFor = 30 * 24 * time.Hour
	sweepEvery     = time.Minute
)

type failure struct {
	at    time.Time
	other string // account (in the client map) or client key (in the account map)
}

type accountLock struct {
	until time.Time
	level int
	last  time.Time
}

type Guard struct {
	mu        sync.Mutex
	clients   map[string][]failure
	accounts  map[string][]failure
	bans      map[string]*Ban
	locks     map[string]*accountLock
	known     map[string]time.Time // account + "\x00" + client key -> last success
	limits    func() Limits
	lastSweep time.Time

	OnBan func(ip, reason string)
	// Store persists bans; optional.
	Store BanStore
	// now is replaceable in tests.
	now func() time.Time
}

func NewGuard(limits func() Limits) *Guard {
	return &Guard{
		clients:  map[string][]failure{},
		accounts: map[string][]failure{},
		bans:     map[string]*Ban{},
		locks:    map[string]*accountLock{},
		known:    map[string]time.Time{},
		limits:   limits,
		now:      time.Now,
	}
}

// IPKey returns the counting/ban key of an address: the IPv4 address itself
// or the /64 network of an IPv6 address (one customer usually owns a whole /64).
func IPKey(addr string) string {
	addr = strings.TrimSpace(addr)
	ip := net.ParseIP(addr)
	if ip == nil {
		return strings.ToLower(addr)
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	mask := net.CIDRMask(64, 128)
	return (&net.IPNet{IP: ip.Mask(mask), Mask: mask}).String()
}

func accountKey(account string) string { return store.Normalize(account) }

func (g *Guard) effectiveLimits() Limits {
	l := Limits{MaxAttempts: 5, Window: 10 * time.Minute, BanFor: time.Hour}
	if g.limits != nil {
		got := g.limits()
		if got.MaxAttempts > 0 {
			l.MaxAttempts = got.MaxAttempts
		}
		if got.Window > 0 {
			l.Window = got.Window
		}
		if got.BanFor > 0 {
			l.BanFor = got.BanFor
		}
	}
	return l
}

func accountThreshold(l Limits) int {
	if n := 2 * l.MaxAttempts; n > minAccountAttempts {
		return n
	}
	return minAccountAttempts
}

// Load restores persisted bans (call once at startup).
func (g *Guard) Load(ctx context.Context) error {
	if g.Store == nil {
		return nil
	}
	bans, err := g.Store.ActiveAuthBans(ctx)
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, b := range bans {
		g.bans[b.Key] = &Ban{IP: b.Key, Reason: b.Reason, Until: b.Until.UTC(), Failures: b.Failures}
	}
	return nil
}

// Banned reports whether the client address is currently banned.
func (g *Guard) Banned(ip string) bool {
	key := IPKey(ip)
	g.mu.Lock()
	defer g.mu.Unlock()
	b, ok := g.bans[key]
	if !ok {
		return false
	}
	if g.now().After(b.Until) {
		delete(g.bans, key)
		return false
	}
	return true
}

// AccountLocked reports whether logins for account are temporarily refused
// for this client (the account is under attack and the client is not known
// from a recent successful login).
func (g *Guard) AccountLocked(ip, account string) bool {
	acct := accountKey(account)
	if acct == "" {
		return false
	}
	key := IPKey(ip)
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	l, ok := g.locks[acct]
	if !ok || now.After(l.until) {
		return false
	}
	if t, ok := g.known[acct+"\x00"+key]; ok && now.Sub(t) < knownClientFor {
		return false
	}
	return true
}

// Fail records a failed attempt that is not tied to an account (e.g. a wrong
// setup token).
func (g *Guard) Fail(ip, reason string) { g.FailAccount(ip, "", reason) }

// FailAccount records a failed login of a client for an account.
func (g *Guard) FailAccount(ip, account, reason string) {
	l := g.effectiveLimits()
	key := IPKey(ip)
	acct := accountKey(account)
	now := g.now()
	cutoff := now.Add(-l.Window)

	g.mu.Lock()
	g.sweep(now, cutoff, false)
	var ban *Ban
	list := append(prune(g.clients[key], cutoff), failure{now, acct})
	if len(list) >= l.MaxAttempts {
		ban = &Ban{IP: key, Reason: reason, Until: now.Add(l.BanFor).UTC(), Failures: len(list)}
		g.bans[key] = ban
		delete(g.clients, key)
	} else {
		g.clients[key] = list
	}
	if acct != "" {
		al := append(prune(g.accounts[acct], cutoff), failure{now, key})
		if len(al) >= accountThreshold(l) {
			lk := g.locks[acct]
			if lk == nil {
				lk = &accountLock{}
				g.locks[acct] = lk
			}
			if now.Sub(lk.last) > lockLevelReset {
				lk.level = 0
			}
			d := accountLockBase << lk.level
			if d > accountLockMax || d <= 0 {
				d = accountLockMax
			}
			if lk.level < 8 {
				lk.level++
			}
			lk.until, lk.last = now.Add(d), now
			delete(g.accounts, acct)
		} else {
			g.accounts[acct] = al
		}
	}
	g.bound()
	g.mu.Unlock()

	if ban != nil {
		if g.Store != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = g.Store.SaveAuthBan(ctx, store.AuthBan{Key: ban.IP, Reason: ban.Reason, Until: ban.Until, Failures: ban.Failures})
			cancel()
		}
		if g.OnBan != nil {
			g.OnBan(key, reason)
		}
	}
}

// Success records a successful login. It only forgives the failures of this
// client for this account; failures for other accounts keep counting.
func (g *Guard) Success(ip, account string) {
	key := IPKey(ip)
	acct := accountKey(account)
	if acct == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if list, ok := g.clients[key]; ok {
		list = without(list, acct)
		if len(list) == 0 {
			delete(g.clients, key)
		} else {
			g.clients[key] = list
		}
	}
	if list, ok := g.accounts[acct]; ok {
		list = without(list, key)
		if len(list) == 0 {
			delete(g.accounts, acct)
		} else {
			g.accounts[acct] = list
		}
	}
	g.known[acct+"\x00"+key] = g.now()
	g.bound()
}

func (g *Guard) List() []Ban {
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	out := []Ban{}
	for key, b := range g.bans {
		if now.After(b.Until) {
			delete(g.bans, key)
			continue
		}
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Until.After(out[j].Until) })
	return out
}

// Unban lifts a ban. ip may be an address or a key as returned by List.
func (g *Guard) Unban(ip string) {
	keys := []string{strings.TrimSpace(ip), IPKey(ip)}
	if _, n, err := net.ParseCIDR(strings.TrimSpace(ip)); err == nil {
		keys = append(keys, n.String())
		if ones, bits := n.Mask.Size(); bits == 128 && ones >= 64 {
			keys = append(keys, IPKey(n.IP.String()))
		}
	}
	g.mu.Lock()
	for _, k := range keys {
		delete(g.bans, k)
		delete(g.clients, k)
	}
	g.mu.Unlock()
	if g.Store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, k := range keys {
			_ = g.Store.DeleteAuthBan(ctx, k)
		}
	}
}

// sweep drops expired state at most once per sweepEvery (always if force).
// Callers hold g.mu.
func (g *Guard) sweep(now, cutoff time.Time, force bool) {
	if !force && now.Sub(g.lastSweep) < sweepEvery {
		return
	}
	g.lastSweep = now
	for _, m := range []map[string][]failure{g.clients, g.accounts} {
		for k, list := range m {
			if list = prune(list, cutoff); len(list) == 0 {
				delete(m, k)
			} else {
				m[k] = list
			}
		}
	}
	for k, b := range g.bans {
		if now.After(b.Until) {
			delete(g.bans, k)
		}
	}
	for k, l := range g.locks {
		if now.After(l.until) && now.Sub(l.last) > lockLevelReset {
			delete(g.locks, k)
		}
	}
	for k, t := range g.known {
		if now.Sub(t) > knownClientFor {
			delete(g.known, k)
		}
	}
}

// bound keeps every map below maxTracked entries. Callers hold g.mu.
func (g *Guard) bound() {
	if len(g.clients) <= maxTracked && len(g.accounts) <= maxTracked && len(g.bans) <= maxTracked &&
		len(g.locks) <= maxTracked && len(g.known) <= maxTracked {
		return
	}
	now := g.now()
	// The limits callback only reads settings and never takes the guard lock.
	g.sweep(now, now.Add(-g.effectiveLimits().Window), true)
	trimFailures(g.clients)
	trimFailures(g.accounts)
	for k := range g.bans {
		if len(g.bans) <= maxTracked {
			break
		}
		delete(g.bans, k)
	}
	for k := range g.locks {
		if len(g.locks) <= maxTracked {
			break
		}
		delete(g.locks, k)
	}
	for k := range g.known {
		if len(g.known) <= maxTracked {
			break
		}
		delete(g.known, k)
	}
}

func trimFailures(m map[string][]failure) {
	for k := range m { // map iteration order is random: evicts arbitrary entries
		if len(m) <= maxTracked {
			return
		}
		delete(m, k)
	}
}

func prune(list []failure, cutoff time.Time) []failure {
	out := list[:0]
	for _, f := range list {
		if f.at.After(cutoff) {
			out = append(out, f)
		}
	}
	return out
}

func without(list []failure, other string) []failure {
	out := list[:0]
	for _, f := range list {
		if f.other != other {
			out = append(out, f)
		}
	}
	return out
}
