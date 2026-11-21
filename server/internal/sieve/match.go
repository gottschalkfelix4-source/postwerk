package sieve

import (
	"strconv"
	"strings"
	"time"
)

// asciiLower folds only ASCII letters (i;ascii-casemap, RFC 4790).
func asciiLower(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if b[j] >= 'A' && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

func fold(comparator, s string) string {
	if comparator == "i;ascii-casemap" {
		return asciiLower(s)
	}
	return s
}

// numericValue returns the leading decimal digits of s without leading
// zeros; ok is false when s does not start with a digit (positive infinity
// for i;ascii-numeric).
func numericValue(s string) (string, bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return "", false
	}
	d := strings.TrimLeft(s[:i], "0")
	if d == "" {
		d = "0"
	}
	return d, true
}

// compare orders a and b according to the comparator.
func compare(comparator, a, b string) int {
	if comparator == "i;ascii-numeric" {
		da, oka := numericValue(a)
		db, okb := numericValue(b)
		switch {
		case !oka && !okb:
			return 0
		case !oka:
			return 1
		case !okb:
			return -1
		case len(da) != len(db):
			if len(da) < len(db) {
				return -1
			}
			return 1
		}
		return strings.Compare(da, db)
	}
	return strings.Compare(fold(comparator, a), fold(comparator, b))
}

func relation(op string, c int) bool {
	switch op {
	case "gt":
		return c > 0
	case "ge":
		return c >= 0
	case "lt":
		return c < 0
	case "le":
		return c <= 0
	case "eq":
		return c == 0
	case "ne":
		return c != 0
	}
	return false
}

type globTok struct {
	r    rune
	kind byte // 'l' literal, '*' star, 'e' empty star, '?' any
}

// globMatch matches s against a :matches pattern ("*" any sequence, "?" one
// character, backslash escapes). On success it returns the substrings
// matched by the wildcards (non-greedy, left to right).
func globMatch(pattern, s string, foldCase bool) (bool, []string) {
	var p []globTok
	pr := []rune(pattern)
	for i := 0; i < len(pr); i++ {
		switch pr[i] {
		case '*':
			if len(p) > 0 && p[len(p)-1].kind == '*' {
				// Consecutive stars: the extra ones match the empty string.
				p = append(p, globTok{kind: 'e'})
				continue
			}
			p = append(p, globTok{kind: '*'})
		case '?':
			p = append(p, globTok{kind: '?'})
		case '\\':
			if i+1 < len(pr) {
				i++
			}
			p = append(p, globTok{r: pr[i], kind: 'l'})
		default:
			p = append(p, globTok{r: pr[i], kind: 'l'})
		}
	}
	orig := []rune(s)
	str := orig
	if foldCase {
		str = []rune(asciiLower(s))
		for i := range p {
			if p[i].kind == 'l' && p[i].r >= 'A' && p[i].r <= 'Z' {
				p[i].r += 'a' - 'A'
			}
		}
	}
	P, S := len(p), len(str)
	if (P+1)*(S+1) > 4<<20 {
		return greedyMatch(p, str), nil
	}
	// ok[pi][si]: p[pi:] matches str[si:].
	ok := make([]bool, (P+1)*(S+1))
	at := func(pi, si int) int { return pi*(S+1) + si }
	ok[at(P, S)] = true
	for pi := P - 1; pi >= 0; pi-- {
		for si := S; si >= 0; si-- {
			switch p[pi].kind {
			case '*':
				ok[at(pi, si)] = ok[at(pi+1, si)] || (si < S && ok[at(pi, si+1)])
			case 'e':
				ok[at(pi, si)] = ok[at(pi+1, si)]
			case '?':
				ok[at(pi, si)] = si < S && ok[at(pi+1, si+1)]
			default:
				ok[at(pi, si)] = si < S && str[si] == p[pi].r && ok[at(pi+1, si+1)]
			}
		}
	}
	if !ok[at(0, 0)] {
		return false, nil
	}
	var caps []string
	si := 0
	for pi := 0; pi < P; pi++ {
		switch p[pi].kind {
		case '*':
			k := si
			for !ok[at(pi+1, k)] {
				k++
			}
			caps = append(caps, string(orig[si:k]))
			si = k
		case 'e':
			caps = append(caps, "")
		case '?':
			caps = append(caps, string(orig[si:si+1]))
			si++
		default:
			si++
		}
	}
	return true, caps
}

// greedyMatch is the linear-time wildcard matcher used for very long values
// (no captures).
func greedyMatch(p []globTok, s []rune) bool {
	pi, si, star, mark := 0, 0, -1, 0
	for si < len(s) {
		switch {
		case pi < len(p) && (p[pi].kind == '*' || p[pi].kind == 'e'):
			star, mark = pi, si
			pi++
		case pi < len(p) && (p[pi].kind == '?' || p[pi].r == s[si]):
			pi++
			si++
		case star >= 0:
			pi = star + 1
			mark++
			si = mark
		default:
			return false
		}
	}
	for pi < len(p) && (p[pi].kind == '*' || p[pi].kind == 'e') {
		pi++
	}
	return pi == len(p)
}

// ---- date ----

var dateParts = map[string]bool{
	"year": true, "month": true, "day": true, "date": true, "julian": true, "hour": true, "minute": true,
	"second": true, "time": true, "iso8601": true, "std11": true, "zone": true, "weekday": true,
}

// parseZone parses "+hhmm" / "-hhmm".
func parseZone(z string) (*time.Location, bool) {
	if len(z) != 5 || (z[0] != '+' && z[0] != '-') {
		return nil, false
	}
	h, err1 := strconv.Atoi(z[1:3])
	m, err2 := strconv.Atoi(z[3:5])
	if err1 != nil || err2 != nil || h > 23 || m > 59 {
		return nil, false
	}
	off := h*3600 + m*60
	if z[0] == '-' {
		off = -off
	}
	return time.FixedZone(z, off), true
}

func datePart(t time.Time, part string) string {
	switch part {
	case "year":
		return t.Format("2006")
	case "month":
		return t.Format("01")
	case "day":
		return t.Format("02")
	case "date":
		return t.Format("2006-01-02")
	case "julian":
		// Modified Julian Day.
		mjd := t.Sub(time.Date(1858, 11, 17, 0, 0, 0, 0, t.Location())).Hours() / 24
		return strconv.Itoa(int(mjd))
	case "hour":
		return t.Format("15")
	case "minute":
		return t.Format("04")
	case "second":
		return t.Format("05")
	case "time":
		return t.Format("15:04:05")
	case "iso8601":
		return t.Format("2006-01-02T15:04:05-07:00")
	case "std11":
		return t.Format("Mon, 02 Jan 2006 15:04:05 -0700")
	case "zone":
		return t.Format("-0700")
	case "weekday":
		return strconv.Itoa(int(t.Weekday()))
	}
	return ""
}
