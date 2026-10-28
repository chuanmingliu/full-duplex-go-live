package jinja

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// expr is an expression that can be evaluated in a scope, and can say which
// names it reads.
type expr interface {
	eval(sc *scope) (any, error)
	names(bound map[string]bool, see func(string))
}

// --- tokens ---

type tokKind int

const (
	tEOF tokKind = iota
	tName
	tInt
	tFloat
	tString
	tOp
)

type token struct {
	kind tokKind
	text string
	pos  int
}

func tokenize(src string, base int) ([]token, error) {
	var out []token
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '"' || c == '\'':
			var b strings.Builder
			j := i + 1
			closed := false
			for j < len(src) {
				if src[j] == '\\' && j+1 < len(src) {
					switch src[j+1] {
					case 'n':
						b.WriteByte('\n')
					case 't':
						b.WriteByte('\t')
					case 'r':
						b.WriteByte('\r')
					case '\\', '\'', '"':
						b.WriteByte(src[j+1])
					default:
						b.WriteByte('\\')
						b.WriteByte(src[j+1])
					}
					j += 2
					continue
				}
				if src[j] == c {
					closed = true
					break
				}
				b.WriteByte(src[j])
				j++
			}
			if !closed {
				return nil, errAt(base, "a string opened with %c is never closed in %q", c, src)
			}
			out = append(out, token{kind: tString, text: b.String(), pos: base})
			i = j + 1
		case c >= '0' && c <= '9':
			j := i
			isFloat := false
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '_') {
				j++
			}
			if j+1 < len(src) && src[j] == '.' && src[j+1] >= '0' && src[j+1] <= '9' {
				isFloat = true
				j++
				for j < len(src) && src[j] >= '0' && src[j] <= '9' {
					j++
				}
			}
			if j < len(src) && (src[j] == 'e' || src[j] == 'E') {
				k := j + 1
				if k < len(src) && (src[k] == '+' || src[k] == '-') {
					k++
				}
				if k < len(src) && src[k] >= '0' && src[k] <= '9' {
					isFloat = true
					for k < len(src) && src[k] >= '0' && src[k] <= '9' {
						k++
					}
					j = k
				}
			}
			kind := tInt
			if isFloat {
				kind = tFloat
			}
			out = append(out, token{kind: kind, text: strings.ReplaceAll(src[i:j], "_", ""), pos: base})
			i = j
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= utf8.RuneSelf:
			j := i
			for j < len(src) {
				r, size := utf8.DecodeRuneInString(src[j:])
				if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r >= utf8.RuneSelf) {
					break
				}
				j += size
			}
			out = append(out, token{kind: tName, text: src[i:j], pos: base})
			i = j
		default:
			op := ""
			for _, two := range []string{"==", "!=", "<=", ">=", "//", "**"} {
				if strings.HasPrefix(src[i:], two) {
					op = two
					break
				}
			}
			if op == "" {
				if strings.ContainsRune("+-*/%~<>()[]{}.,:|=", rune(c)) {
					op = string(c)
				} else {
					return nil, errAt(base, "unexpected %q in %q", string(c), src)
				}
			}
			out = append(out, token{kind: tOp, text: op, pos: base})
			i += len(op)
		}
	}
	out = append(out, token{kind: tEOF, pos: base})
	return out, nil
}

// --- parser: Jinja2's own precedence ---

type parser struct {
	toks []token
	i    int
	src  string
	pos  int
}

func parseExpr(src string, pos int) (expr, error) {
	toks, err := tokenize(src, pos)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, src: strings.TrimSpace(src), pos: pos}
	if p.peek().kind == tEOF {
		return nil, errAt(pos, "an empty expression")
	}
	e, err := p.cond()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, errAt(pos, "unexpected %q in %q", t.text, p.src)
	}
	return e, nil
}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) next() token { t := p.toks[p.i]; p.i++; return t }
func (p *parser) isOp(s string) bool {
	t := p.peek()
	return t.kind == tOp && t.text == s
}
func (p *parser) isWord(s string) bool {
	t := p.peek()
	return t.kind == tName && t.text == s
}
func (p *parser) fail(format string, args ...any) error {
	return errAt(p.pos, format+" in %q", append(args, p.src)...)
}
func (p *parser) expectOp(s string) error {
	if !p.isOp(s) {
		return p.fail("expected %q", s)
	}
	p.i++
	return nil
}

func (p *parser) cond() (expr, error) {
	e, err := p.or()
	if err != nil {
		return nil, err
	}
	for p.isWord("if") {
		p.i++
		test, err := p.or()
		if err != nil {
			return nil, err
		}
		var orelse expr
		if p.isWord("else") {
			p.i++
			if orelse, err = p.cond(); err != nil {
				return nil, err
			}
		}
		e = &condExpr{then: e, test: test, orelse: orelse}
	}
	return e, nil
}

func (p *parser) or() (expr, error) {
	e, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.isWord("or") {
		p.i++
		r, err := p.and()
		if err != nil {
			return nil, err
		}
		e = &boolExpr{op: "or", l: e, r: r}
	}
	return e, nil
}

func (p *parser) and() (expr, error) {
	e, err := p.not()
	if err != nil {
		return nil, err
	}
	for p.isWord("and") {
		p.i++
		r, err := p.not()
		if err != nil {
			return nil, err
		}
		e = &boolExpr{op: "and", l: e, r: r}
	}
	return e, nil
}

func (p *parser) not() (expr, error) {
	if p.isWord("not") {
		p.i++
		e, err := p.not()
		if err != nil {
			return nil, err
		}
		return &notExpr{e: e}, nil
	}
	return p.compare()
}

func (p *parser) compare() (expr, error) {
	e, err := p.math1()
	if err != nil {
		return nil, err
	}
	var ops []string
	var rights []expr
	for {
		op := ""
		switch t := p.peek(); {
		case t.kind == tOp && (t.text == "==" || t.text == "!=" || t.text == "<" || t.text == ">" || t.text == "<=" || t.text == ">="):
			op = t.text
			p.i++
		case t.kind == tName && t.text == "in":
			op = "in"
			p.i++
		case t.kind == tName && t.text == "not" && p.toks[p.i+1].kind == tName && p.toks[p.i+1].text == "in":
			op = "notin"
			p.i += 2
		}
		if op == "" {
			break
		}
		r, err := p.math1()
		if err != nil {
			return nil, err
		}
		ops, rights = append(ops, op), append(rights, r)
	}
	if len(ops) == 0 {
		return e, nil
	}
	return &compareExpr{first: e, ops: ops, rest: rights, pos: p.pos}, nil
}

func (p *parser) math1() (expr, error) {
	e, err := p.concat()
	if err != nil {
		return nil, err
	}
	for p.isOp("+") || p.isOp("-") {
		op := p.next().text
		r, err := p.concat()
		if err != nil {
			return nil, err
		}
		e = &binExpr{op: op, l: e, r: r, pos: p.pos}
	}
	return e, nil
}

func (p *parser) concat() (expr, error) {
	e, err := p.math2()
	if err != nil {
		return nil, err
	}
	for p.isOp("~") {
		p.i++
		r, err := p.math2()
		if err != nil {
			return nil, err
		}
		e = &binExpr{op: "~", l: e, r: r, pos: p.pos}
	}
	return e, nil
}

func (p *parser) math2() (expr, error) {
	e, err := p.pow()
	if err != nil {
		return nil, err
	}
	for p.isOp("*") || p.isOp("/") || p.isOp("//") || p.isOp("%") {
		op := p.next().text
		r, err := p.pow()
		if err != nil {
			return nil, err
		}
		e = &binExpr{op: op, l: e, r: r, pos: p.pos}
	}
	return e, nil
}

func (p *parser) pow() (expr, error) {
	e, err := p.unary(true)
	if err != nil {
		return nil, err
	}
	for p.isOp("**") {
		p.i++
		r, err := p.unary(true)
		if err != nil {
			return nil, err
		}
		e = &binExpr{op: "**", l: e, r: r, pos: p.pos}
	}
	return e, nil
}

func (p *parser) unary(withFilter bool) (expr, error) {
	var e expr
	var err error
	switch {
	case p.isOp("-"):
		p.i++
		if e, err = p.unary(false); err != nil {
			return nil, err
		}
		e = &negExpr{e: e, pos: p.pos}
	case p.isOp("+"):
		p.i++
		if e, err = p.unary(false); err != nil {
			return nil, err
		}
		e = &posExpr{e: e, pos: p.pos}
	default:
		if e, err = p.primary(); err != nil {
			return nil, err
		}
	}
	if e, err = p.postfix(e); err != nil {
		return nil, err
	}
	if withFilter {
		return p.filters(e)
	}
	return e, nil
}

func (p *parser) primary() (expr, error) {
	t := p.next()
	switch t.kind {
	case tName:
		switch t.text {
		case "true", "True":
			return &litExpr{v: true}, nil
		case "false", "False":
			return &litExpr{v: false}, nil
		case "none", "None":
			return &litExpr{v: nil}, nil
		case "and", "or", "not", "if", "else", "in", "is":
			return nil, p.fail("unexpected %q", t.text)
		}
		return &nameExpr{name: t.text}, nil
	case tString:
		s := t.text
		for p.peek().kind == tString { // adjacent strings join
			s += p.next().text
		}
		return &litExpr{v: s}, nil
	case tInt:
		n, err := strconv.ParseInt(t.text, 10, 64)
		if err != nil {
			return nil, p.fail("%q is too large a number", t.text)
		}
		return &litExpr{v: n}, nil
	case tFloat:
		f, err := strconv.ParseFloat(t.text, 64)
		if err != nil {
			return nil, p.fail("%q is not a number", t.text)
		}
		return &litExpr{v: f}, nil
	case tOp:
		switch t.text {
		case "(":
			if p.isOp(")") {
				p.i++
				return &listExpr{tuple: true}, nil
			}
			e, err := p.cond()
			if err != nil {
				return nil, err
			}
			if p.isOp(",") { // a tuple: treated as a list
				items := []expr{e}
				for p.isOp(",") {
					p.i++
					if p.isOp(")") {
						break
					}
					item, err := p.cond()
					if err != nil {
						return nil, err
					}
					items = append(items, item)
				}
				if err := p.expectOp(")"); err != nil {
					return nil, err
				}
				return &listExpr{items: items, tuple: true}, nil
			}
			if err := p.expectOp(")"); err != nil {
				return nil, err
			}
			return e, nil
		case "[":
			var items []expr
			for !p.isOp("]") {
				item, err := p.cond()
				if err != nil {
					return nil, err
				}
				items = append(items, item)
				if !p.isOp(",") {
					break
				}
				p.i++
			}
			if err := p.expectOp("]"); err != nil {
				return nil, err
			}
			return &listExpr{items: items}, nil
		case "{":
			d := &dictExpr{}
			for !p.isOp("}") {
				k, err := p.cond()
				if err != nil {
					return nil, err
				}
				if err := p.expectOp(":"); err != nil {
					return nil, err
				}
				v, err := p.cond()
				if err != nil {
					return nil, err
				}
				d.keys, d.values = append(d.keys, k), append(d.values, v)
				if !p.isOp(",") {
					break
				}
				p.i++
			}
			if err := p.expectOp("}"); err != nil {
				return nil, err
			}
			return d, nil
		}
	}
	if t.kind == tEOF {
		return nil, p.fail("the expression ends too soon")
	}
	return nil, p.fail("unexpected %q", t.text)
}

func (p *parser) postfix(e expr) (expr, error) {
	for {
		switch {
		case p.isOp("."):
			p.i++
			t := p.next()
			if t.kind != tName && t.kind != tInt {
				return nil, p.fail("expected a name after \".\"")
			}
			e = &attrExpr{e: e, name: t.text, pos: p.pos}
		case p.isOp("["):
			p.i++
			var parts [3]expr
			isSlice := false
			n := 0
			for {
				if !p.isOp(":") && !p.isOp("]") {
					part, err := p.cond()
					if err != nil {
						return nil, err
					}
					parts[n] = part
				}
				if p.isOp(":") && n < 2 {
					p.i++
					isSlice = true
					n++
					continue
				}
				break
			}
			if err := p.expectOp("]"); err != nil {
				return nil, err
			}
			if isSlice {
				e = &sliceExpr{e: e, lo: parts[0], hi: parts[1], step: parts[2], pos: p.pos}
			} else {
				if parts[0] == nil {
					return nil, p.fail("an empty subscript")
				}
				e = &indexExpr{e: e, index: parts[0], pos: p.pos}
			}
		case p.isOp("("):
			args, kwargs, err := p.args()
			if err != nil {
				return nil, err
			}
			e = &callExpr{fn: e, args: args, kwargs: kwargs, pos: p.pos}
		default:
			return e, nil
		}
	}
}

// args parses a parenthesised argument list; the opening bracket is next.
func (p *parser) args() ([]expr, map[string]expr, error) {
	p.i++ // (
	var args []expr
	kwargs := map[string]expr{}
	for !p.isOp(")") {
		if t := p.peek(); t.kind == tName && p.toks[p.i+1].kind == tOp && p.toks[p.i+1].text == "=" {
			p.i += 2
			v, err := p.cond()
			if err != nil {
				return nil, nil, err
			}
			kwargs[t.text] = v
		} else {
			v, err := p.cond()
			if err != nil {
				return nil, nil, err
			}
			args = append(args, v)
		}
		if !p.isOp(",") {
			break
		}
		p.i++
	}
	if err := p.expectOp(")"); err != nil {
		return nil, nil, err
	}
	return args, kwargs, nil
}

func (p *parser) filters(e expr) (expr, error) {
	for {
		switch {
		case p.isOp("|"):
			p.i++
			t := p.next()
			if t.kind != tName {
				return nil, p.fail("expected a filter's name after \"|\"")
			}
			f := &filterExpr{e: e, name: t.text, pos: p.pos}
			if _, known := filters[t.text]; !known {
				return nil, p.fail("the filter %q is not supported", t.text)
			}
			if p.isOp("(") {
				var err error
				if f.args, f.kwargs, err = p.args(); err != nil {
					return nil, err
				}
			}
			e = f
		case p.isWord("is"):
			p.i++
			negate := false
			if p.isWord("not") {
				negate = true
				p.i++
			}
			t := p.next()
			if t.kind != tName {
				return nil, p.fail("expected a test's name after \"is\"")
			}
			name := t.text
			if _, known := tests[name]; !known {
				return nil, p.fail("the test %q is not supported", name)
			}
			te := &testExpr{e: e, name: name, negate: negate, pos: p.pos}
			if p.isOp("(") {
				var err error
				if te.args, _, err = p.args(); err != nil {
					return nil, err
				}
			} else if t := p.peek(); (t.kind == tInt || t.kind == tFloat || t.kind == tString ||
				t.kind == tName && !isKeyword(t.text)) && testTakesArg[name] {
				arg, err := p.unary(false)
				if err != nil {
					return nil, err
				}
				te.args = []expr{arg}
			}
			e = te
		case p.isOp("("):
			args, kwargs, err := p.args()
			if err != nil {
				return nil, err
			}
			e = &callExpr{fn: e, args: args, kwargs: kwargs, pos: p.pos}
		default:
			return e, nil
		}
	}
}

func isKeyword(s string) bool {
	switch s {
	case "and", "or", "not", "if", "else", "in", "is":
		return true
	}
	return false
}

// --- nodes ---

type litExpr struct{ v any }

func (e *litExpr) eval(*scope) (any, error)            { return e.v, nil }
func (e *litExpr) names(map[string]bool, func(string)) {}

type nameExpr struct{ name string }

func (e *nameExpr) eval(sc *scope) (any, error) {
	if v, ok := sc.get(e.name); ok {
		return v, nil
	}
	return undefined{name: e.name}, nil
}
func (e *nameExpr) names(bound map[string]bool, see func(string)) {
	if !bound[e.name] {
		see(e.name)
	}
}

type listExpr struct {
	items []expr
	tuple bool
}

func (e *listExpr) eval(sc *scope) (any, error) {
	out := make([]any, 0, len(e.items))
	for _, item := range e.items {
		v, err := item.eval(sc)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if e.tuple {
		return tuple(out), nil
	}
	return out, nil
}
func (e *listExpr) names(bound map[string]bool, see func(string)) {
	for _, item := range e.items {
		item.names(bound, see)
	}
}

type dictExpr struct{ keys, values []expr }

func (e *dictExpr) eval(sc *scope) (any, error) {
	out := &dict{}
	for i := range e.keys {
		k, err := e.keys[i].eval(sc)
		if err != nil {
			return nil, err
		}
		v, err := e.values[i].eval(sc)
		if err != nil {
			return nil, err
		}
		out.set(str(k), v)
	}
	return out, nil
}
func (e *dictExpr) names(bound map[string]bool, see func(string)) {
	for i := range e.keys {
		e.keys[i].names(bound, see)
		e.values[i].names(bound, see)
	}
}

type condExpr struct{ then, test, orelse expr }

func (e *condExpr) eval(sc *scope) (any, error) {
	t, err := e.test.eval(sc)
	if err != nil {
		return nil, err
	}
	if truthy(t) {
		return e.then.eval(sc)
	}
	if e.orelse == nil {
		return undefined{}, nil
	}
	return e.orelse.eval(sc)
}
func (e *condExpr) names(bound map[string]bool, see func(string)) {
	e.test.names(bound, see)
	e.then.names(bound, see)
	if e.orelse != nil {
		e.orelse.names(bound, see)
	}
}

type boolExpr struct {
	op   string
	l, r expr
}

func (e *boolExpr) eval(sc *scope) (any, error) {
	l, err := e.l.eval(sc)
	if err != nil {
		return nil, err
	}
	if (e.op == "and") != truthy(l) {
		return l, nil
	}
	return e.r.eval(sc)
}
func (e *boolExpr) names(bound map[string]bool, see func(string)) {
	e.l.names(bound, see)
	e.r.names(bound, see)
}

type notExpr struct{ e expr }

func (e *notExpr) eval(sc *scope) (any, error) {
	v, err := e.e.eval(sc)
	if err != nil {
		return nil, err
	}
	return !truthy(v), nil
}
func (e *notExpr) names(bound map[string]bool, see func(string)) { e.e.names(bound, see) }

type compareExpr struct {
	first expr
	ops   []string
	rest  []expr
	pos   int
}

func (e *compareExpr) eval(sc *scope) (any, error) {
	l, err := e.first.eval(sc)
	if err != nil {
		return nil, err
	}
	for i, op := range e.ops {
		r, err := e.rest[i].eval(sc)
		if err != nil {
			return nil, err
		}
		ok, err := compare(op, l, r, e.pos)
		if err != nil {
			return nil, err
		}
		if !ok {
			return false, nil
		}
		l = r
	}
	return true, nil
}
func (e *compareExpr) names(bound map[string]bool, see func(string)) {
	e.first.names(bound, see)
	for _, r := range e.rest {
		r.names(bound, see)
	}
}

type binExpr struct {
	op   string
	l, r expr
	pos  int
}

func (e *binExpr) eval(sc *scope) (any, error) {
	l, err := e.l.eval(sc)
	if err != nil {
		return nil, err
	}
	r, err := e.r.eval(sc)
	if err != nil {
		return nil, err
	}
	return arith(e.op, l, r, e.pos)
}
func (e *binExpr) names(bound map[string]bool, see func(string)) {
	e.l.names(bound, see)
	e.r.names(bound, see)
}

type negExpr struct {
	e   expr
	pos int
}

func (e *negExpr) eval(sc *scope) (any, error) {
	v, err := e.e.eval(sc)
	if err != nil {
		return nil, err
	}
	return arith("-", int64(0), v, e.pos)
}
func (e *negExpr) names(bound map[string]bool, see func(string)) { e.e.names(bound, see) }

type posExpr struct {
	e   expr
	pos int
}

func (e *posExpr) eval(sc *scope) (any, error) {
	v, err := e.e.eval(sc)
	if err != nil {
		return nil, err
	}
	return arith("+", int64(0), v, e.pos)
}
func (e *posExpr) names(bound map[string]bool, see func(string)) { e.e.names(bound, see) }

type attrExpr struct {
	e    expr
	name string
	pos  int
}

func (e *attrExpr) eval(sc *scope) (any, error) {
	v, err := e.e.eval(sc)
	if err != nil {
		return nil, err
	}
	if u, ok := v.(undefined); ok {
		return nil, errAt(e.pos, "%s is undefined, so it has no %q", u.what(), e.name)
	}
	switch v := v.(type) {
	case *dict:
		if dictMethods[e.name] {
			return &boundMethod{self: v, name: e.name}, nil
		}
		if item, ok := v.get(e.name); ok {
			return item, nil
		}
	case string:
		if strMethods[e.name] {
			return &boundMethod{self: v, name: e.name}, nil
		}
	}
	// Anything else has no such part, which is undefined rather than an
	// error until something is asked of it.
	return undefined{name: e.name}, nil
}
func (e *attrExpr) names(bound map[string]bool, see func(string)) { e.e.names(bound, see) }

type indexExpr struct {
	e, index expr
	pos      int
}

func (e *indexExpr) eval(sc *scope) (any, error) {
	v, err := e.e.eval(sc)
	if err != nil {
		return nil, err
	}
	i, err := e.index.eval(sc)
	if err != nil {
		return nil, err
	}
	return subscript(v, i, e.pos)
}
func (e *indexExpr) names(bound map[string]bool, see func(string)) {
	e.e.names(bound, see)
	e.index.names(bound, see)
}

type sliceExpr struct {
	e, lo, hi, step expr
	pos             int
}

func (e *sliceExpr) eval(sc *scope) (any, error) {
	v, err := e.e.eval(sc)
	if err != nil {
		return nil, err
	}
	bound := func(x expr) (*int64, error) {
		if x == nil {
			return nil, nil
		}
		b, err := x.eval(sc)
		if err != nil {
			return nil, err
		}
		if b == nil {
			return nil, nil
		}
		n, ok := asInt(b)
		if !ok {
			return nil, errAt(e.pos, "a slice's bounds must be whole numbers, not %s", repr(b))
		}
		return &n, nil
	}
	lo, err := bound(e.lo)
	if err != nil {
		return nil, err
	}
	hi, err := bound(e.hi)
	if err != nil {
		return nil, err
	}
	step, err := bound(e.step)
	if err != nil {
		return nil, err
	}
	return slice(v, lo, hi, step, e.pos)
}
func (e *sliceExpr) names(bound map[string]bool, see func(string)) {
	e.e.names(bound, see)
	for _, x := range []expr{e.lo, e.hi, e.step} {
		if x != nil {
			x.names(bound, see)
		}
	}
}

type callExpr struct {
	fn     expr
	args   []expr
	kwargs map[string]expr
	pos    int
}

func (e *callExpr) eval(sc *scope) (any, error) {
	fn, err := e.fn.eval(sc)
	if err != nil {
		return nil, err
	}
	m, ok := fn.(*boundMethod)
	if !ok {
		if u, isU := fn.(undefined); isU {
			return nil, errAt(e.pos, "%s is undefined and cannot be called", u.what())
		}
		return nil, errAt(e.pos, "%s cannot be called", repr(fn))
	}
	args, kwargs, err := evalArgs(sc, e.args, e.kwargs)
	if err != nil {
		return nil, err
	}
	return callMethod(m.self, m.name, args, kwargs, e.pos)
}
func (e *callExpr) names(bound map[string]bool, see func(string)) {
	e.fn.names(bound, see)
	for _, a := range e.args {
		a.names(bound, see)
	}
	for _, a := range e.kwargs {
		a.names(bound, see)
	}
}

type filterExpr struct {
	e      expr
	name   string
	args   []expr
	kwargs map[string]expr
	pos    int
}

func (e *filterExpr) eval(sc *scope) (any, error) {
	v, err := e.e.eval(sc)
	if err != nil {
		return nil, err
	}
	args, kwargs, err := evalArgs(sc, e.args, e.kwargs)
	if err != nil {
		return nil, err
	}
	return filters[e.name](v, args, kwargs, e.pos)
}
func (e *filterExpr) names(bound map[string]bool, see func(string)) {
	e.e.names(bound, see)
	for _, a := range e.args {
		a.names(bound, see)
	}
	for _, a := range e.kwargs {
		a.names(bound, see)
	}
}

type testExpr struct {
	e      expr
	name   string
	negate bool
	args   []expr
	pos    int
}

func (e *testExpr) eval(sc *scope) (any, error) {
	v, err := e.e.eval(sc)
	if err != nil {
		return nil, err
	}
	args, _, err := evalArgs(sc, e.args, nil)
	if err != nil {
		return nil, err
	}
	ok, err := tests[e.name](v, args, e.pos)
	if err != nil {
		return nil, err
	}
	return ok != e.negate, nil
}
func (e *testExpr) names(bound map[string]bool, see func(string)) {
	e.e.names(bound, see)
	for _, a := range e.args {
		a.names(bound, see)
	}
}

func evalArgs(sc *scope, args []expr, kwargs map[string]expr) ([]any, map[string]any, error) {
	out := make([]any, 0, len(args))
	for _, a := range args {
		v, err := a.eval(sc)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, v)
	}
	kw := map[string]any{}
	for k, a := range kwargs {
		v, err := a.eval(sc)
		if err != nil {
			return nil, nil, err
		}
		kw[k] = v
	}
	return out, kw, nil
}
