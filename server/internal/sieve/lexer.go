// Package sieve implements the Sieve mail filtering language (RFC 5228)
// with the extensions fileinto, reject/ereject (RFC 5429), envelope,
// imap4flags (RFC 5232), copy (RFC 3894), body (RFC 5173), variables
// (RFC 5229), vacation (RFC 5230), relational (RFC 5231), regex, subaddress
// (RFC 5233), date (RFC 5260) and mailbox (RFC 5490).
//
// Compile parses and validates a script; Execute runs it against a message
// and returns the resulting actions. Execution never touches storage: the
// caller applies the actions.
package sieve

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxScriptSize is the largest accepted script in bytes.
const MaxScriptSize = 64 << 10

// Error is a compile or runtime error with an optional source position.
// Messages are German because they are shown in the web interface.
type Error struct {
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Msg    string `json:"message"`
}

func (e *Error) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("Zeile %d, Spalte %d: %s", e.Line, e.Column, e.Msg)
	}
	return e.Msg
}

// Pos is a position in the script source (1-based).
type Pos struct{ Line, Col int }

func errAt(p Pos, format string, args ...any) *Error {
	return &Error{Line: p.Line, Column: p.Col, Msg: fmt.Sprintf(format, args...)}
}

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tTag
	tNumber
	tString
	tLBracket
	tRBracket
	tLParen
	tRParen
	tLBrace
	tRBrace
	tComma
	tSemicolon
)

func (k tokKind) String() string {
	switch k {
	case tEOF:
		return "Ende des Skripts"
	case tIdent:
		return "Bezeichner"
	case tTag:
		return "Parameter"
	case tNumber:
		return "Zahl"
	case tString:
		return "Zeichenkette"
	case tLBracket:
		return "„[“"
	case tRBracket:
		return "„]“"
	case tLParen:
		return "„(“"
	case tRParen:
		return "„)“"
	case tLBrace:
		return "„{“"
	case tRBrace:
		return "„}“"
	case tComma:
		return "„,“"
	case tSemicolon:
		return "„;“"
	}
	return "?"
}

type token struct {
	kind tokKind
	text string // identifier/tag name (lowercase) or string value
	num  int64
	pos  Pos
}

var punctuation = map[byte]tokKind{'[': tLBracket, ']': tRBracket, '(': tLParen, ')': tRParen, '{': tLBrace, '}': tRBrace, ',': tComma, ';': tSemicolon}

type lexer struct {
	src  string
	i    int
	line int
	col  int
}

func newLexer(src string) *lexer { return &lexer{src: src, line: 1, col: 1} }

func (l *lexer) pos() Pos { return Pos{l.line, l.col} }

func (l *lexer) peekByte(off int) byte {
	if l.i+off < len(l.src) {
		return l.src[l.i+off]
	}
	return 0
}

// advance consumes n bytes, keeping line and column up to date.
func (l *lexer) advance(n int) {
	for ; n > 0 && l.i < len(l.src); n-- {
		c := l.src[l.i]
		l.i++
		switch {
		case c == '\n':
			l.line++
			l.col = 1
		case c < 0x80 || c >= 0xC0:
			// Count runes, not continuation bytes.
			l.col++
		}
	}
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentChar(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

// skipSpace skips white space and comments.
func (l *lexer) skipSpace() *Error {
	for l.i < len(l.src) {
		c := l.src[l.i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			l.advance(1)
		case c == '#':
			for l.i < len(l.src) && l.src[l.i] != '\n' {
				l.advance(1)
			}
		case c == '/' && l.peekByte(1) == '*':
			start := l.pos()
			l.advance(2)
			for {
				if l.i >= len(l.src) {
					return errAt(start, "Kommentar wird nicht mit „*/“ abgeschlossen")
				}
				if l.src[l.i] == '*' && l.peekByte(1) == '/' {
					l.advance(2)
					break
				}
				l.advance(1)
			}
		default:
			return nil
		}
	}
	return nil
}

func (l *lexer) next() (token, *Error) {
	if err := l.skipSpace(); err != nil {
		return token{}, err
	}
	p := l.pos()
	if l.i >= len(l.src) {
		return token{kind: tEOF, pos: p}, nil
	}
	c := l.src[l.i]
	if k, ok := punctuation[c]; ok {
		l.advance(1)
		return token{kind: k, pos: p}, nil
	}
	switch {
	case c == '"':
		s, err := l.quoted()
		return token{kind: tString, text: s, pos: p}, err
	case c == ':':
		l.advance(1)
		if !isIdentStart(l.peekByte(0)) {
			return token{}, errAt(p, "Nach „:“ wird der Name eines Parameters erwartet")
		}
		start := l.i
		for l.i < len(l.src) && isIdentChar(l.src[l.i]) {
			l.advance(1)
		}
		return token{kind: tTag, text: strings.ToLower(l.src[start:l.i]), pos: p}, nil
	case c >= '0' && c <= '9':
		return l.number(p)
	case isIdentStart(c):
		start := l.i
		for l.i < len(l.src) && isIdentChar(l.src[l.i]) {
			l.advance(1)
		}
		word := l.src[start:l.i]
		if strings.EqualFold(word, "text") && l.peekByte(0) == ':' {
			l.advance(1)
			s, err := l.multiline(p)
			return token{kind: tString, text: s, pos: p}, err
		}
		return token{kind: tIdent, text: strings.ToLower(word), pos: p}, nil
	}
	r, _ := utf8.DecodeRuneInString(l.src[l.i:])
	return token{}, errAt(p, "Unerwartetes Zeichen „%c“", r)
}

func (l *lexer) number(p Pos) (token, *Error) {
	var n int64
	for l.i < len(l.src) && l.src[l.i] >= '0' && l.src[l.i] <= '9' {
		n = n*10 + int64(l.src[l.i]-'0')
		if n > 1<<40 {
			return token{}, errAt(p, "Zahl ist zu groß")
		}
		l.advance(1)
	}
	switch l.peekByte(0) {
	case 'k', 'K':
		n <<= 10
		l.advance(1)
	case 'm', 'M':
		n <<= 20
		l.advance(1)
	case 'g', 'G':
		n <<= 30
		l.advance(1)
	}
	if isIdentChar(l.peekByte(0)) {
		return token{}, errAt(l.pos(), "Ungültige Zahl")
	}
	return token{kind: tNumber, num: n, pos: p}, nil
}

// quoted reads a quoted string. A backslash escapes the following
// character; undefined escapes simply yield the character (RFC 5228 2.4.2).
func (l *lexer) quoted() (string, *Error) {
	start := l.pos()
	l.advance(1)
	var b strings.Builder
	for {
		if l.i >= len(l.src) {
			return "", errAt(start, "Zeichenkette wird nicht mit „\"“ abgeschlossen")
		}
		c := l.src[l.i]
		switch c {
		case '"':
			l.advance(1)
			return b.String(), nil
		case '\\':
			if l.i+1 >= len(l.src) {
				return "", errAt(start, "Zeichenkette wird nicht mit „\"“ abgeschlossen")
			}
			b.WriteByte(l.src[l.i+1])
			l.advance(2)
		default:
			b.WriteByte(c)
			l.advance(1)
		}
	}
}

// multiline reads a "text:" string up to a line consisting of a single dot.
func (l *lexer) multiline(p Pos) (string, *Error) {
	// Rest of the "text:" line: white space and an optional hash comment.
	for l.i < len(l.src) && (l.src[l.i] == ' ' || l.src[l.i] == '\t') {
		l.advance(1)
	}
	if l.peekByte(0) == '#' {
		for l.i < len(l.src) && l.src[l.i] != '\n' {
			l.advance(1)
		}
	}
	if l.peekByte(0) == '\r' {
		l.advance(1)
	}
	if l.peekByte(0) != '\n' {
		return "", errAt(l.pos(), "Nach „text:“ muss eine neue Zeile beginnen")
	}
	l.advance(1)
	var b strings.Builder
	for {
		if l.i >= len(l.src) {
			return "", errAt(p, "Mehrzeiliger Text wird nicht mit einer Zeile „.“ abgeschlossen")
		}
		end := strings.IndexByte(l.src[l.i:], '\n')
		var line string
		if end < 0 {
			line = l.src[l.i:]
			end = len(line)
		} else {
			line = l.src[l.i : l.i+end]
			end++
		}
		trimmed := strings.TrimSuffix(line, "\r")
		l.advance(end)
		if trimmed == "." {
			return b.String(), nil
		}
		if strings.HasPrefix(trimmed, "..") {
			trimmed = trimmed[1:]
		}
		b.WriteString(trimmed)
		b.WriteString("\r\n")
	}
}
