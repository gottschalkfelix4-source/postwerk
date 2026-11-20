// Package spam scores inbound messages using authentication results, DNS
// blocklists, header heuristics and a Bayes classifier.
package spam

import (
	"context"
	"fmt"
	"math"
	"net"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"postwerk/internal/mailauth"
	"postwerk/internal/message"
	"postwerk/internal/store"
)

type Verdict struct {
	Score  float64
	Rules  []string
	BayesP float64 // -1 if not available
	DNSBL  []string
	Tokens []string
	IsSpam bool
	Reject bool
}

type Filter struct {
	Store    *store.Store
	Settings *store.SettingsCache
	mu       sync.Mutex // serializes training
}

type Input struct {
	IP     net.IP
	Helo   string
	Auth   mailauth.Result
	Parsed *message.Parsed
	Raw    []byte
}

func (f *Filter) Score(ctx context.Context, in Input) Verdict {
	cfg := f.Settings.Get().Spam
	v := Verdict{BayesP: -1}
	add := func(score float64, rule string) {
		v.Score += score
		v.Rules = append(v.Rules, fmt.Sprintf("%s (%+.1f)", rule, score))
	}

	switch in.Auth.SPF {
	case "fail":
		add(3.5, "SPF_FAIL")
	case "softfail":
		add(1.5, "SPF_SOFTFAIL")
	case "pass":
		add(-0.5, "SPF_PASS")
	}
	switch in.Auth.DKIM {
	case "fail":
		add(2, "DKIM_FAIL")
	case "pass":
		add(-1, "DKIM_PASS")
	}
	switch in.Auth.DMARC {
	case "fail":
		switch in.Auth.DMARCPolicy {
		case "reject":
			add(8, "DMARC_REJECT")
		case "quarantine":
			add(5, "DMARC_QUARANTINE")
		default:
			add(2, "DMARC_FAIL")
		}
	case "pass":
		add(-1, "DMARC_PASS")
	}

	if cfg.DNSBL.Enabled && in.IP != nil && !isPrivate(in.IP) {
		v.DNSBL = checkDNSBL(ctx, in.IP, cfg.DNSBL.Servers)
		for _, l := range v.DNSBL {
			add(4, "DNSBL "+l)
		}
	}

	p := in.Parsed
	if p != nil {
		if p.MessageID == "" {
			add(1.5, "MISSING_MID")
		}
		if p.Date.IsZero() {
			add(1, "MISSING_DATE")
		}
		if p.Subject == "" {
			add(1, "EMPTY_SUBJECT")
		}
		if isShouting(p.Subject) {
			add(1.5, "SUBJECT_ALL_CAPS")
		}
		if p.Text == "" && p.HTML == "" && len(p.Attachments) == 0 {
			add(1.5, "EMPTY_BODY")
		}
		if p.HTML != "" && len(strings.TrimSpace(message.HTMLToText(p.HTML))) < 20 && strings.Count(strings.ToLower(p.HTML), "<img") > 0 {
			add(2, "HTML_IMAGE_ONLY")
		}
		if len(p.ReplyTo) > 0 && p.From.Email != "" && store.DomainOf(p.ReplyTo[0].Email) != store.DomainOf(p.From.Email) {
			add(0.8, "REPLYTO_DIFFERENT_DOMAIN")
		}
		if displayNameSpoof(p.From) {
			add(3, "FROM_NAME_SPOOF")
		}
		for _, a := range p.Attachments {
			if dangerousExt(a.Filename) {
				add(5, "DANGEROUS_ATTACHMENT "+a.Filename)
			}
		}
		lower := strings.ToLower(p.Subject + " " + p.Text)
		for _, kw := range phishWords {
			if strings.Contains(lower, kw) {
				add(0.7, "PHISH_PHRASE \""+kw+"\"")
			}
		}
		if n := len(reURL.FindAllString(p.Text, -1)); n > 25 {
			add(1, "MANY_URLS")
		}
		v.Tokens = Tokenize(p.Subject + " " + p.Text)
	}

	if cfg.Bayes.Enabled && len(v.Tokens) > 0 {
		if prob, ok := f.bayes(ctx, v.Tokens, cfg.Bayes.MinTokens); ok {
			v.BayesP = prob
			switch {
			case prob > 0.99:
				add(5, fmt.Sprintf("BAYES_99 (%.2f)", prob))
			case prob > 0.9:
				add(3, fmt.Sprintf("BAYES_90 (%.2f)", prob))
			case prob > 0.7:
				add(1.5, fmt.Sprintf("BAYES_70 (%.2f)", prob))
			case prob < 0.01:
				add(-3, fmt.Sprintf("BAYES_00 (%.2f)", prob))
			case prob < 0.1:
				add(-1.5, fmt.Sprintf("BAYES_10 (%.2f)", prob))
			}
		}
	}

	v.Score = math.Round(v.Score*10) / 10
	v.IsSpam = v.Score >= cfg.JunkThreshold
	v.Reject = cfg.RejectThreshold > 0 && v.Score >= cfg.RejectThreshold
	return v
}

// AutoLearn trains clear-cut cases.
func (f *Filter) AutoLearn(ctx context.Context, v Verdict) {
	cfg := f.Settings.Get().Spam
	if !cfg.Bayes.Enabled || !cfg.Bayes.AutoLearn || len(v.Tokens) == 0 {
		return
	}
	if v.Score >= cfg.RejectThreshold-1 && v.Score >= 10 {
		f.Train(ctx, v.Tokens, true, 1)
	} else if v.Score <= -1.5 {
		f.Train(ctx, v.Tokens, false, 1)
	}
}

func (f *Filter) Train(ctx context.Context, tokens []string, isSpam bool, delta int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = f.Store.BayesTrain(ctx, uniq(tokens), isSpam, delta)
}

// Relearn moves a message from one class to the other.
func (f *Filter) Relearn(ctx context.Context, tokens []string, toSpam bool) {
	f.Train(ctx, tokens, !toSpam, -1)
	f.Train(ctx, tokens, toSpam, 1)
}

func (f *Filter) bayes(ctx context.Context, tokens []string, minMsgs int) (float64, bool) {
	nSpam, nHam := f.Store.BayesCounts(ctx)
	if nSpam+nHam < int64(minMsgs) || nSpam < 10 || nHam < 10 {
		return 0, false
	}
	counts, err := f.Store.BayesTokens(ctx, uniq(tokens))
	if err != nil {
		return 0, false
	}
	// Robinson's method with Fisher's inverse chi-square combining.
	type tp struct{ p, dev float64 }
	var probs []tp
	const s, x = 1.0, 0.5
	for _, c := range counts {
		sp, hm := float64(c[0]), float64(c[1])
		if sp+hm < 2 {
			continue
		}
		ps := sp / float64(nSpam)
		ph := hm / float64(nHam)
		if ps+ph == 0 {
			continue
		}
		p := ps / (ps + ph)
		n := sp + hm
		f := (s*x + n*p) / (s + n)
		probs = append(probs, tp{f, math.Abs(f - 0.5)})
	}
	if len(probs) < 5 {
		return 0, false
	}
	sort.Slice(probs, func(i, j int) bool { return probs[i].dev > probs[j].dev })
	if len(probs) > 150 {
		probs = probs[:150]
	}
	var lnS, lnH float64
	for _, t := range probs {
		lnS += math.Log(1 - t.p)
		lnH += math.Log(t.p)
	}
	n := float64(len(probs))
	S := 1 - chi2Q(-2*lnS, 2*n)
	H := 1 - chi2Q(-2*lnH, 2*n)
	// S is the spamminess and H the hamminess indicator (SpamBayes).
	return (1 + S - H) / 2, true
}

func chi2Q(x2, v float64) float64 {
	m := x2 / 2
	sum := math.Exp(-m)
	term := sum
	for i := 1; i < int(v/2); i++ {
		term *= m / float64(i)
		sum += term
	}
	return math.Min(sum, 1)
}

var reURL = regexp.MustCompile(`https?://[^\s"'<>]+`)
var reWord = regexp.MustCompile(`[\p{L}\p{N}$€!][\p{L}\p{N}'$€!.-]{1,30}`)

func Tokenize(s string) []string {
	s = strings.ToLower(s)
	var out []string
	for _, u := range reURL.FindAllString(s, -1) {
		host := u
		if i := strings.Index(host, "://"); i >= 0 {
			host = host[i+3:]
		}
		if i := strings.IndexAny(host, "/?#"); i >= 0 {
			host = host[:i]
		}
		out = append(out, "url:"+host)
	}
	for _, w := range reWord.FindAllString(reURL.ReplaceAllString(s, " "), -1) {
		w = strings.Trim(w, ".-'")
		if len(w) < 3 || len(w) > 30 {
			continue
		}
		out = append(out, w)
		if len(out) > 3000 {
			break
		}
	}
	return out
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, t := range in {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func isShouting(s string) bool {
	letters, upper := 0, 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
			if unicode.IsUpper(r) {
				upper++
			}
		}
	}
	return letters >= 12 && float64(upper)/float64(letters) > 0.8
}

var brands = []string{"paypal", "amazon", "sparkasse", "volksbank", "dhl", "apple", "microsoft", "netflix", "ing-diba", "commerzbank", "deutsche bank", "telekom", "vodafone", "postbank", "n26"}

func displayNameSpoof(from store.Address) bool {
	name := strings.ToLower(from.Name)
	dom := store.DomainOf(from.Email)
	if strings.Contains(name, "@") && !strings.Contains(name, from.Email) {
		return true
	}
	for _, b := range brands {
		if strings.Contains(name, b) && !strings.Contains(dom, strings.ReplaceAll(b, " ", "")) {
			return true
		}
	}
	return false
}

var dangerous = []string{".exe", ".scr", ".bat", ".cmd", ".com", ".pif", ".vbs", ".js", ".jse", ".wsf", ".hta", ".msi", ".jar", ".ps1", ".lnk", ".iso", ".img"}

func dangerousExt(name string) bool {
	n := strings.ToLower(name)
	for _, e := range dangerous {
		if strings.HasSuffix(n, e) {
			return true
		}
	}
	return false
}

var phishWords = []string{"konto gesperrt", "verify your account", "bestätigen sie ihre", "ihr konto wurde", "dringend handeln",
	"password expires", "passwort läuft ab", "unusual sign-in", "ungewöhnliche aktivität", "gewinnspiel", "you have won", "bitcoin", "lottery"}

func isPrivate(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

func checkDNSBL(ctx context.Context, ip net.IP, servers []string) []string {
	v4 := ip.To4()
	if v4 == nil {
		return nil // IPv6 DNSBL lookups are not supported yet
	}
	rev := fmt.Sprintf("%d.%d.%d.%d", v4[3], v4[2], v4[1], v4[0])
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	var hits []string
	for _, s := range servers {
		s := strings.TrimSpace(s)
		if s == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			addrs, err := net.DefaultResolver.LookupHost(ctx, rev+"."+s)
			if err != nil || len(addrs) == 0 {
				return
			}
			// 127.255.255.x answers from Spamhaus mean "query refused", not listed.
			if strings.HasPrefix(addrs[0], "127.255.255.") {
				return
			}
			if strings.HasPrefix(addrs[0], "127.") {
				mu.Lock()
				hits = append(hits, s)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return hits
}

// GreylistKey builds the triplet key using the /24 network of the client.
func GreylistKey(ip net.IP, from, rcpt string) string {
	n := ip.String()
	if v4 := ip.To4(); v4 != nil {
		n = fmt.Sprintf("%d.%d.%d", v4[0], v4[1], v4[2])
	}
	return n + "|" + strings.ToLower(from) + "|" + strings.ToLower(rcpt)
}
