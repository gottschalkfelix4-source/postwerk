package dnsprov

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Desired describes one record Postwerk wants.
type Desired struct {
	Key      string
	Record   Record // Name may be a FQDN here; it is made zone-relative during planning
	Purpose  string
	Required bool
	// Match selects which existing TXT record this one replaces (by prefix).
	Match string
	// CreateOnly keeps an existing record as is (e.g. a tuned DMARC policy).
	CreateOnly bool
	// Merge combines an existing SPF policy with ours.
	MergeSPF bool
	// Manual forces manual handling with this reason.
	Manual string

	spfExtra string
}

type Change struct {
	Key      string   `json:"key"`
	Type     string   `json:"type"`
	Name     string   `json:"name"`
	Action   string   `json:"action"`
	Current  []string `json:"current"`
	Desired  string   `json:"desired"`
	Purpose  string   `json:"purpose"`
	Required bool     `json:"required"`
	Warning  *string  `json:"warning"`
	Reason   *string  `json:"reason"`

	ops []Op
}

type Plan struct {
	Zone     string   `json:"zone"`
	Provider string   `json:"provider"`
	Changes  []Change `json:"changes"`
}

// Options tune the desired record set.
type Options struct {
	// SPFInclude is added for a smarthost, e.g. "include:_spf.strato.com".
	SPFInclude string
	// DynDNS reports that the host's A record is maintained by DynDNS.
	DynDNS bool
}

// DesiredRecords builds the record set for a mail domain.
func DesiredRecords(domain, host, dkimSelector, dkimValue, ipv4, ipv6 string, opts ...Options) []Desired {
	var opt Options
	if len(opts) > 0 {
		opt = opts[0]
	}
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	hostDot := host + "."
	d := []Desired{}
	if opt.DynDNS {
		d = append(d, Desired{Key: "a", Record: Record{Name: host, Type: "A", Value: "<per DynDNS>"}, Purpose: "Hostname des Mailservers", Required: true,
			Manual: "Der A-Record wird von Postwerk automatisch per DynDNS aktuell gehalten."})
	} else if ipv4 != "" {
		d = append(d, Desired{Key: "a", Record: Record{Name: host, Type: "A", Value: ipv4}, Purpose: "Hostname des Mailservers", Required: true})
	} else {
		d = append(d, Desired{Key: "a", Record: Record{Name: host, Type: "A", Value: "<öffentliche IPv4>"}, Purpose: "Hostname des Mailservers", Required: true,
			Manual: "Öffentliche IPv4 angeben, dann kann der A-Record automatisch gesetzt werden."})
	}
	if ipv6 != "" {
		d = append(d, Desired{Key: "aaaa", Record: Record{Name: host, Type: "AAAA", Value: ipv6}, Purpose: "Hostname des Mailservers (IPv6)"})
	}
	d = append(d,
		Desired{Key: "mx", Record: Record{Name: domain, Type: "MX", Value: hostDot, Priority: 10}, Purpose: "Eingehende Mails an Postwerk leiten", Required: true},
		Desired{Key: "spf", Record: Record{Name: domain, Type: "TXT", Value: MergeSPF("", opt.SPFInclude)}, Purpose: "SPF: erlaubte Absender", Required: true, Match: "v=spf1", MergeSPF: true, spfExtra: opt.SPFInclude},
		Desired{Key: "dkim", Record: Record{Name: dkimSelector + "._domainkey." + domain, Type: "TXT", Value: dkimValue}, Purpose: "DKIM-Schlüssel", Required: true, Match: "v=DKIM1"},
		Desired{Key: "dmarc", Record: Record{Name: "_dmarc." + domain, Type: "TXT", Value: "v=DMARC1; p=quarantine; adkim=r; aspf=r; rua=mailto:postmaster@" + domain}, Purpose: "DMARC-Richtlinie", Required: true, Match: "v=DMARC1", CreateOnly: true},
		Desired{Key: "mta-sts-txt", Record: Record{Name: "_mta-sts." + domain, Type: "TXT", Value: "v=STSv1; id=" + mtaSTSID()}, Purpose: "MTA-STS aktivieren", Match: "v=STSv1", CreateOnly: true},
		Desired{Key: "mta-sts-cname", Record: Record{Name: "mta-sts." + domain, Type: "CNAME", Value: hostDot}, Purpose: "MTA-STS-Policy über HTTPS"},
		Desired{Key: "tlsrpt", Record: Record{Name: "_smtp._tls." + domain, Type: "TXT", Value: "v=TLSRPTv1; rua=mailto:postmaster@" + domain}, Purpose: "TLS-Berichte", Match: "v=TLSRPTv1", CreateOnly: true},
		Desired{Key: "srv-submissions", Record: Record{Name: "_submissions._tcp." + domain, Type: "SRV", Value: "1 465 " + hostDot}, Purpose: "Autokonfiguration: Versand (TLS)"},
		Desired{Key: "srv-submission", Record: Record{Name: "_submission._tcp." + domain, Type: "SRV", Value: "1 587 " + hostDot}, Purpose: "Autokonfiguration: Versand (STARTTLS)"},
		Desired{Key: "srv-pop3s", Record: Record{Name: "_pop3s._tcp." + domain, Type: "SRV", Value: "1 995 " + hostDot}, Purpose: "Autokonfiguration: POP3 (TLS)"},
		Desired{Key: "autoconfig", Record: Record{Name: "autoconfig." + domain, Type: "CNAME", Value: hostDot}, Purpose: "Thunderbird-Autokonfiguration"},
	)
	return d
}

var mtaSTSID = func() string { return strconv.FormatInt(timeNow().Unix(), 10) }

// display renders a record value for humans.
func display(r Record) string {
	switch r.Type {
	case "MX":
		return strconv.Itoa(r.Priority) + " " + r.Value
	case "SRV":
		return strconv.Itoa(r.Priority) + " " + r.Value
	}
	return r.Value
}

func sameValue(a, b Record) bool {
	if a.Type != b.Type {
		return false
	}
	switch a.Type {
	case "TXT":
		return a.Value == b.Value
	case "MX", "SRV":
		return a.Priority == b.Priority && strings.EqualFold(a.Value, b.Value)
	}
	return strings.EqualFold(strings.TrimSuffix(a.Value, "."), strings.TrimSuffix(b.Value, "."))
}

// MergeSPF keeps foreign mechanisms of an existing SPF record and adds "mx"
// plus optional extra terms (e.g. the smarthost's include).
func MergeSPF(existing string, extra ...string) string {
	all := "-all"
	terms := []string{"mx"}
	seen := map[string]bool{"mx": true, "+mx": true}
	addTerm := func(t string) {
		lt := strings.ToLower(t)
		if t != "" && !seen[lt] {
			seen[lt] = true
			terms = append(terms, t)
		}
	}
	if f := strings.Fields(existing); len(f) > 1 {
		for _, t := range f[1:] {
			lt := strings.ToLower(t)
			switch {
			case strings.HasSuffix(lt, "all") && len(lt) <= 4:
				if lt == "~all" || lt == "?all" {
					all = lt
				}
				continue
			case strings.HasPrefix(lt, "redirect="):
				continue
			}
			addTerm(t)
		}
	}
	for _, e := range extra {
		addTerm(strings.TrimSpace(e))
	}
	return "v=spf1 " + strings.Join(terms, " ") + " " + all
}

// MakePlan compares the zone with the desired records.
func MakePlan(ctx context.Context, p Provider, info Info, domain string, desired []Desired) (*Plan, error) {
	zone, err := p.Zone(ctx, domain)
	if err != nil {
		return nil, err
	}
	existing, err := p.List(ctx, zone)
	if err != nil {
		return nil, err
	}
	supported := map[string]bool{}
	for _, t := range info.Supports {
		supported[t] = true
	}
	plan := &Plan{Zone: zone, Provider: info.ID, Changes: []Change{}}
	for _, d := range desired {
		want := d.Record
		inZone := strings.EqualFold(strings.TrimSuffix(want.Name, "."), zone) || strings.HasSuffix(strings.ToLower(strings.TrimSuffix(want.Name, ".")), "."+zone)
		fq := strings.ToLower(strings.TrimSuffix(want.Name, ".")) + "."
		want.Name = relName(want.Name, zone)
		c := Change{Key: d.Key, Type: want.Type, Name: fq, Current: []string{}, Desired: display(want), Purpose: d.Purpose, Required: d.Required}

		var matching, others []Record
		for _, r := range existing {
			if r.Name != want.Name {
				continue
			}
			switch {
			case r.Type == want.Type && (d.Match == "" || strings.HasPrefix(strings.ToLower(r.Value), strings.ToLower(d.Match))):
				matching = append(matching, r)
			case want.Type == "CNAME" || r.Type == "CNAME":
				others = append(others, r)
			}
		}
		for _, r := range matching {
			c.Current = append(c.Current, display(r))
		}

		manual := func(reason string) {
			c.Action = "manual"
			c.Reason = &reason
			plan.Changes = append(plan.Changes, c)
		}
		switch {
		case d.Manual != "":
			manual(d.Manual)
			continue
		case !inZone:
			manual(fmt.Sprintf("%s liegt nicht in der Zone %s und muss beim zuständigen Anbieter gesetzt werden.", undot(fq), zone))
			continue
		case !supported[want.Type]:
			manual(fmt.Sprintf("%s unterstützt %s-Einträge nicht automatisch – bitte im Kundenmenü von Hand setzen.", info.Name, want.Type))
			continue
		case len(others) > 0:
			manual(fmt.Sprintf("Unter %s gibt es bereits andere Einträge (%s), die mit einem CNAME kollidieren.", undot(fq), others[0].Type))
			continue
		}

		if d.MergeSPF && len(matching) > 0 {
			want.Value = MergeSPF(matching[0].Value, d.spfExtra)
			c.Desired = want.Value
		}

		switch {
		case len(matching) == 1 && sameValue(matching[0], want):
			c.Action = "unchanged"
		case len(matching) > 0 && d.CreateOnly:
			c.Action = "unchanged"
			w := "Vorhandener Eintrag bleibt erhalten."
			c.Warning = &w
		case len(matching) == 0:
			c.Action = "create"
			want.TTL = 3600
			c.ops = []Op{{Kind: OpCreate, New: want}}
		default:
			c.Action = "update"
			want.TTL = 3600
			c.ops = []Op{{Kind: OpUpdate, Old: matching[0], New: want}}
			for _, extra := range matching[1:] {
				c.ops = append(c.ops, Op{Kind: OpDelete, Old: extra})
			}
			switch want.Type {
			case "MX":
				w := "Bestehende MX-Einträge werden ersetzt – eingehende Mails gehen danach an Postwerk."
				c.Warning = &w
			case "A", "AAAA":
				w := "Der Hostname zeigt danach auf die neue IP-Adresse."
				c.Warning = &w
			case "TXT":
				if d.MergeSPF {
					w := "Die bestehende SPF-Regel wird um „mx“ ergänzt; vorhandene Freigaben bleiben erhalten."
					c.Warning = &w
				}
			}
		}
		plan.Changes = append(plan.Changes, c)
	}
	return plan, nil
}

type Failure struct {
	Key   string `json:"key"`
	Error string `json:"error"`
}

type ApplyResult struct {
	Applied []string  `json:"applied"`
	Failed  []Failure `json:"failed"`
	Manual  []string  `json:"manual"`
}

// ApplyPlan executes the selected changes of a plan.
func ApplyPlan(ctx context.Context, p Provider, plan *Plan, keys []string) *ApplyResult {
	sel := map[string]bool{}
	for _, k := range keys {
		sel[k] = true
	}
	res := &ApplyResult{Applied: []string{}, Failed: []Failure{}, Manual: []string{}}
	var ops []Op
	var owner []int
	for i, c := range plan.Changes {
		if c.Action == "manual" {
			res.Manual = append(res.Manual, c.Key)
			continue
		}
		if !sel[c.Key] || len(c.ops) == 0 {
			continue
		}
		for _, op := range c.ops {
			ops = append(ops, op)
			owner = append(owner, i)
		}
	}
	if len(ops) == 0 {
		return res
	}
	errs := p.Apply(ctx, plan.Zone, ops)
	failed := map[int]string{}
	for i, err := range errs {
		if err != nil {
			if _, ok := failed[owner[i]]; !ok {
				failed[owner[i]] = err.Error()
			}
		}
	}
	done := map[int]bool{}
	for _, i := range owner {
		if done[i] {
			continue
		}
		done[i] = true
		if msg, ok := failed[i]; ok {
			res.Failed = append(res.Failed, Failure{Key: plan.Changes[i].Key, Error: msg})
		} else {
			res.Applied = append(res.Applied, plan.Changes[i].Key)
		}
	}
	return res
}
