package sieve

// The parser builds a generic syntax tree following the grammar of
// RFC 5228 section 8.2; compile.go checks the commands and their arguments.

// maxNesting bounds block and test nesting so deeply nested scripts cannot
// exhaust the stack.
const maxNesting = 64

type argKind int

const (
	argTag argKind = iota
	argNumber
	argStrings
)

type arg struct {
	pos  Pos
	kind argKind
	tag  string
	num  int64
	strs []string
	list bool // written as a bracketed string list
}

type testNode struct {
	pos   Pos
	name  string
	args  []arg
	tests []*testNode
}

type cmdNode struct {
	pos      Pos
	name     string
	args     []arg
	tests    []*testNode // the test or test list following the arguments
	block    []*cmdNode
	hasBlock bool
}

type parser struct {
	lx  *lexer
	tok token
}

func parse(src string) ([]*cmdNode, *Error) {
	p := &parser{lx: newLexer(src)}
	if err := p.advance(); err != nil {
		return nil, err
	}
	cmds, err := p.commands(0)
	if err != nil {
		return nil, err
	}
	if p.tok.kind != tEOF {
		if p.tok.kind == tRBrace {
			return nil, errAt(p.tok.pos, "„}“ ohne passendes „{“")
		}
		return nil, errAt(p.tok.pos, "Befehl erwartet, gefunden: %s", p.tok.kind)
	}
	return cmds, nil
}

func (p *parser) advance() *Error {
	t, err := p.lx.next()
	if err != nil {
		return err
	}
	p.tok = t
	return nil
}

func (p *parser) commands(depth int) ([]*cmdNode, *Error) {
	var out []*cmdNode
	for p.tok.kind == tIdent {
		c, err := p.command(depth)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (p *parser) command(depth int) (*cmdNode, *Error) {
	c := &cmdNode{pos: p.tok.pos, name: p.tok.text}
	if err := p.advance(); err != nil {
		return nil, err
	}
	args, tests, err := p.arguments(depth)
	if err != nil {
		return nil, err
	}
	c.args, c.tests = args, tests
	switch p.tok.kind {
	case tSemicolon:
		return c, p.advance()
	case tLBrace:
		if depth >= maxNesting {
			return nil, errAt(p.tok.pos, "Blöcke sind zu tief verschachtelt")
		}
		if err := p.advance(); err != nil {
			return nil, err
		}
		block, err := p.commands(depth + 1)
		if err != nil {
			return nil, err
		}
		if p.tok.kind != tRBrace {
			if p.tok.kind == tEOF {
				return nil, errAt(c.pos, "Block von „%s“ wird nicht mit „}“ geschlossen", c.name)
			}
			return nil, errAt(p.tok.pos, "„}“ oder ein Befehl erwartet, gefunden: %s", p.tok.kind)
		}
		c.block, c.hasBlock = block, true
		return c, p.advance()
	case tEOF:
		return nil, errAt(p.tok.pos, "„;“ fehlt nach „%s“", c.name)
	}
	return nil, errAt(p.tok.pos, "„;“ fehlt nach „%s“ (gefunden: %s)", c.name, p.tok.kind)
}

// arguments reads *argument [test / test-list].
func (p *parser) arguments(depth int) ([]arg, []*testNode, *Error) {
	var args []arg
	for {
		a, ok, err := p.argument()
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			break
		}
		args = append(args, a)
	}
	switch p.tok.kind {
	case tIdent:
		t, err := p.test(depth + 1)
		if err != nil {
			return nil, nil, err
		}
		return args, []*testNode{t}, nil
	case tLParen:
		tests, err := p.testList(depth + 1)
		return args, tests, err
	}
	return args, nil, nil
}

func (p *parser) argument() (arg, bool, *Error) {
	t := p.tok
	switch t.kind {
	case tTag:
		return arg{pos: t.pos, kind: argTag, tag: t.text}, true, p.advance()
	case tNumber:
		return arg{pos: t.pos, kind: argNumber, num: t.num}, true, p.advance()
	case tString:
		return arg{pos: t.pos, kind: argStrings, strs: []string{t.text}}, true, p.advance()
	case tLBracket:
		if err := p.advance(); err != nil {
			return arg{}, false, err
		}
		a := arg{pos: t.pos, kind: argStrings, list: true}
		for {
			if p.tok.kind != tString {
				return arg{}, false, errAt(p.tok.pos, "Zeichenkette in der Liste erwartet, gefunden: %s", p.tok.kind)
			}
			a.strs = append(a.strs, p.tok.text)
			if len(a.strs) > 1024 {
				return arg{}, false, errAt(p.tok.pos, "Liste ist zu lang")
			}
			if err := p.advance(); err != nil {
				return arg{}, false, err
			}
			if p.tok.kind == tRBracket {
				return a, true, p.advance()
			}
			if p.tok.kind != tComma {
				return arg{}, false, errAt(p.tok.pos, "„,“ oder „]“ erwartet, gefunden: %s", p.tok.kind)
			}
			if err := p.advance(); err != nil {
				return arg{}, false, err
			}
		}
	}
	return arg{}, false, nil
}

func (p *parser) test(depth int) (*testNode, *Error) {
	if depth > maxNesting {
		return nil, errAt(p.tok.pos, "Tests sind zu tief verschachtelt")
	}
	if p.tok.kind != tIdent {
		return nil, errAt(p.tok.pos, "Test erwartet, gefunden: %s", p.tok.kind)
	}
	t := &testNode{pos: p.tok.pos, name: p.tok.text}
	if err := p.advance(); err != nil {
		return nil, err
	}
	args, tests, err := p.arguments(depth)
	if err != nil {
		return nil, err
	}
	t.args, t.tests = args, tests
	return t, nil
}

func (p *parser) testList(depth int) ([]*testNode, *Error) {
	if depth > maxNesting {
		return nil, errAt(p.tok.pos, "Tests sind zu tief verschachtelt")
	}
	if err := p.advance(); err != nil { // "("
		return nil, err
	}
	var out []*testNode
	for {
		t, err := p.test(depth)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
		switch p.tok.kind {
		case tComma:
			if err := p.advance(); err != nil {
				return nil, err
			}
		case tRParen:
			return out, p.advance()
		default:
			return nil, errAt(p.tok.pos, "„,“ oder „)“ erwartet, gefunden: %s", p.tok.kind)
		}
	}
}
