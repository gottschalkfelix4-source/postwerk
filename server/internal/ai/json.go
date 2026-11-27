package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

// Small local models often produce JSON that is almost, but not quite, valid:
// Python literals, comments, trailing commas, quoted numbers, or a single
// object where a list was requested. The helpers below extract the first JSON
// value and repair these mistakes before giving up.
var (
	reQuotedNumber = regexp.MustCompile(`("spam_probability"\s*:\s*)"(-?[0-9]+(?:[.,][0-9]+)?)"`)
	reQuotedBool   = regexp.MustCompile(`(?i)("(?:phishing|suspicious)"\s*:\s*)"(true|false|1|0|yes|no|ja|nein)"`)
)

// decodeLooseJSON extracts the first balanced JSON object or array from a model
// answer, repairs common formatting mistakes and decodes it into out.
func decodeLooseJSON(text string, out any) error {
	var lastErr error
	for _, candidate := range jsonCandidates(text) {
		for _, version := range []string{candidate, sanitizeJSON(candidate)} {
			resetJSONTarget(out)
			if err := json.Unmarshal([]byte(version), out); err != nil {
				lastErr = err
				continue
			}
			return nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no JSON object or array found")
	}
	return fmt.Errorf("ai returned invalid JSON: %w", lastErr)
}

// jsonCandidates returns the first balanced object and array. Prose and code
// fences around the answer are ignored. If the answer starts with an array, it
// is tried first so a struct target does not silently accept only the first of
// several objects.
func jsonCandidates(text string) []string {
	t := strings.TrimSpace(text)
	obj, objOK := firstBalanced(t, '{')
	arr, arrOK := firstBalanced(t, '[')
	objStart, arrStart := strings.IndexByte(t, '{'), strings.IndexByte(t, '[')
	switch {
	case objOK && arrOK && arrStart < objStart:
		out := arrayCandidates(arr)
		// A bracketed aside before the answer ("siehe [1]") must not hide
		// a following object, but an object inside the array is one of its
		// elements and must not be picked alone.
		if end, ok := balancedEnd(t, arrStart, '[', ']'); ok && objStart >= end {
			out = append(out, obj)
		}
		return out
	case objOK && arrOK:
		return []string{obj, arr}
	case objOK:
		return []string{obj}
	case arrOK:
		return arrayCandidates(arr)
	default:
		return []string{t}
	}
}

// arrayCandidates unwraps a single-element array so a model that wraps its
// object in [...] still works. Multi-element arrays stay as they are: picking
// one element would silently discard the others.
func arrayCandidates(arr string) []string {
	out := []string{arr}
	var items []json.RawMessage
	if json.Unmarshal([]byte(arr), &items) == nil && len(items) == 1 {
		out = append(out, string(items[0]))
	}
	return out
}

// firstBalanced returns the first complete {...} or [...] value. If the value
// is truncated, the rest of the text is returned so the decoder can report the
// real error.
func firstBalanced(s string, open byte) (string, bool) {
	start := strings.IndexByte(s, open)
	if start < 0 {
		return "", false
	}
	closeCh := byte('}')
	if open == '[' {
		closeCh = ']'
	}
	depth := 0
	inString, escaped := false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case open:
			depth++
		case closeCh:
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return s[start:], true
}

// sanitizeJSON removes comments and trailing commas, replaces Python literals
// outside strings and normalizes quoted numbers/booleans.
func sanitizeJSON(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inString, escaped := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			b.WriteByte(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			b.WriteByte(c)
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			i++ // skip the closing '/'
		case c == ',':
			j := i + 1
			for j < len(s) && isJSONSpace(s[j]) {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue
			}
			b.WriteByte(c)
		case c == 'N' && strings.HasPrefix(s[i:], "None") && tokenAt(s, i, 4):
			b.WriteString("null")
			i += 3
		case c == 'T' && strings.HasPrefix(s[i:], "True") && tokenAt(s, i, 4):
			b.WriteString("true")
			i += 3
		case c == 'F' && strings.HasPrefix(s[i:], "False") && tokenAt(s, i, 5):
			b.WriteString("false")
			i += 4
		default:
			b.WriteByte(c)
		}
	}
	return wrapObjectLists(normalizeQuotedScalars(b.String()))
}

func tokenAt(s string, i, n int) bool {
	if i > 0 && isJSONNameChar(s[i-1]) {
		return false
	}
	if i+n < len(s) && isJSONNameChar(s[i+n]) {
		return false
	}
	return true
}

func isJSONNameChar(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func normalizeQuotedScalars(s string) string {
	s = reQuotedNumber.ReplaceAllStringFunc(s, func(m string) string {
		parts := reQuotedNumber.FindStringSubmatch(m)
		return parts[1] + strings.ReplaceAll(parts[2], ",", ".")
	})
	s = reQuotedBool.ReplaceAllStringFunc(s, func(m string) string {
		parts := reQuotedBool.FindStringSubmatch(m)
		v := strings.ToLower(parts[2])
		switch v {
		case "true", "1", "yes", "ja":
			v = "true"
		default:
			v = "false"
		}
		return parts[1] + v
	})
	return s
}

// wrapObjectLists turns {"events": {...}} into {"events": [{...}]}. Small
// models sometimes return one object where a list was requested.
func wrapObjectLists(s string) string {
	for _, key := range []string{"events", "tasks"} {
		s = wrapObjectList(s, key)
	}
	return s
}

func wrapObjectList(s, key string) string {
	needle := `"` + key + `"`
	from := 0
	for {
		i := strings.Index(s[from:], needle)
		if i < 0 {
			return s
		}
		i += from
		j := i + len(needle)
		for j < len(s) && isJSONSpace(s[j]) {
			j++
		}
		if j >= len(s) || s[j] != ':' {
			from = i + len(needle)
			continue
		}
		j++
		for j < len(s) && isJSONSpace(s[j]) {
			j++
		}
		if j >= len(s) || s[j] != '{' {
			from = i + len(needle)
			continue
		}
		end, ok := balancedEnd(s, j, '{', '}')
		if !ok {
			return s
		}
		s = s[:j] + "[" + s[j:end] + "]" + s[end:]
		from = end + 2
	}
}

func balancedEnd(s string, start int, open, closeCh byte) (int, bool) {
	depth := 0
	inString, escaped := false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case open:
			depth++
		case closeCh:
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

// resetJSONTarget clears out before another decode attempt so a failed first
// answer cannot leave partial fields behind.
func resetJSONTarget(out any) {
	rv := reflect.ValueOf(out)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return
	}
	rv.Elem().SetZero()
}
