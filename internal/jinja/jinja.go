// Package jinja renders the part of Jinja2 that prompt templates are written
// in, and renders it as Jinja2 does: the same whitespace control, the same
// truth, the same text for a number or a None.
//
// The prompts this service is given were written for a Python renderer —
// {% if x is defined and x is not none and x != "" %}, {% set years =
// (months | int // 12) %}, {{ when.split(' ')[0] if ' ' in when else when }}
// — and a template that renders one character differently here is a different
// prompt. So what is supported is supported exactly, checked against Jinja2
// itself (testdata/cases.json is its output), and what is not supported is an
// error naming the construct, never a guess.
//
// Supported: {{ expressions }}, {% if / elif / else / endif %}, {% set name =
// expression %}, {% for x in list %} with loop.index and its relations,
// {% raw %}, {# comments #}, and the - of whitespace control. In expressions:
// literals, names, attributes and subscripts and slices, calls of the string
// and dict methods listed in methods.go, filters and tests listed there,
// arithmetic, comparison, in, and / or / not, ~, and a if c else b.
// Not supported: macros, includes, inheritance, block sets, filter blocks,
// autoescape.
package jinja

import (
	"fmt"
	"strings"
)

// Template is a template parsed and ready to render.
type Template struct {
	nodes []node
	src   string
}

// Error is a template that could not be parsed or rendered, and where.
type Error struct {
	Pos int // character offset in the template
	Msg string
}

func (e *Error) Error() string { return fmt.Sprintf("template, character %d: %s", e.Pos, e.Msg) }

func errAt(pos int, format string, args ...any) *Error {
	return &Error{Pos: pos, Msg: fmt.Sprintf(format, args...)}
}

// Parse reads a template.
func Parse(src string) (*Template, error) {
	// Jinja2 drops one newline from the very end of a template.
	body := src
	if strings.HasSuffix(body, "\r\n") {
		body = body[:len(body)-2]
	} else if strings.HasSuffix(body, "\n") {
		body = body[:len(body)-1]
	}
	chunks, err := split(body)
	if err != nil {
		return nil, err
	}
	p := &blockParser{chunks: chunks}
	nodes, end, err := p.until()
	if err != nil {
		return nil, err
	}
	if end != nil {
		return nil, errAt(end.pos, "{%% %s %%} with nothing open to close", end.word)
	}
	return &Template{nodes: nodes, src: src}, nil
}

// Render fills the template in. Values are JSON-like: nil, bool, int, int64,
// float64, json.Number, string, []any, map[string]any. A name the template
// uses and vars does not have is undefined, as in Jinja2: empty where it is
// printed, false where it is tested, an error where something is asked of it.
func (t *Template) Render(vars map[string]any) (string, error) {
	sc := &scope{vars: map[string]any{}}
	for k, v := range vars {
		sc.vars[k] = normalize(v)
	}
	var b strings.Builder
	if err := renderNodes(t.nodes, sc, &b); err != nil {
		return "", err
	}
	return b.String(), nil
}

// Names lists the variables the template reads that it does not itself set,
// in the order it first reads them: what a caller is expected to supply.
func (t *Template) Names() []string {
	seen := map[string]bool{}
	var out []string
	walkNames(t.nodes, map[string]bool{}, func(name string) {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	})
	return out
}

// FirstVariable is the character offset of the first thing in the template
// that can differ between renders: everything before it is the same text for
// every caller, which is what a model's prompt cache can keep.
func (t *Template) FirstVariable() int {
	chunks, err := split(t.src)
	if err != nil {
		return 0
	}
	for _, c := range chunks {
		if c.kind != chunkText && c.kind != chunkComment {
			return len([]rune(t.src[:c.pos]))
		}
	}
	return len([]rune(t.src))
}

// --- splitting a template into text and tags ---

type chunkKind int

const (
	chunkText chunkKind = iota
	chunkExpr
	chunkStmt
	chunkComment
)

type chunk struct {
	kind chunkKind
	text string // text, or what is between the delimiters
	pos  int    // byte offset of the chunk in the template
}

func split(src string) ([]chunk, error) {
	var out []chunk
	stripNext := false
	i := 0
	text := func(s string, pos int, stripRight bool) {
		if stripNext {
			s = strings.TrimLeft(s, " \t\r\n")
		}
		if stripRight {
			s = strings.TrimRight(s, " \t\r\n")
		}
		if s != "" {
			out = append(out, chunk{kind: chunkText, text: s, pos: pos})
		}
	}
	for i < len(src) {
		open, found := nextOpen(src, i)
		if !found {
			break
		}
		var kind chunkKind
		var closer string
		switch src[open+1] {
		case '{':
			kind, closer = chunkExpr, "}}"
		case '%':
			kind, closer = chunkStmt, "%}"
		default:
			kind, closer = chunkComment, "#}"
		}
		inner := open + 2
		stripLeft := false
		if inner < len(src) && src[inner] == '-' {
			stripLeft = true
			inner++
		} else if inner < len(src) && src[inner] == '+' && kind != chunkExpr {
			inner++
		}
		end := closeOf(src, inner, closer, kind)
		if end < 0 {
			return nil, errAt(open, "%s opened here is never closed", src[open:open+2])
		}
		text(src[i:open], i, stripLeft)
		body := src[inner:end]
		stripNext = false
		if strings.HasSuffix(body, "-") {
			body = body[:len(body)-1]
			stripNext = true
		} else if strings.HasSuffix(body, "+") && kind != chunkExpr {
			body = body[:len(body)-1]
		}
		out = append(out, chunk{kind: kind, text: body, pos: open})
		i = end + 2
		// {% raw %} takes everything to {% endraw %} as text.
		if kind == chunkStmt && strings.TrimSpace(body) == "raw" {
			out = out[:len(out)-1]
			rawEnd, after, stripRaw, ok := endRaw(src, i)
			if !ok {
				return nil, errAt(open, "{%% raw %%} opened here is never closed")
			}
			raw := src[i:rawEnd]
			if stripNext {
				raw = strings.TrimLeft(raw, " \t\r\n")
			}
			if stripRaw.left {
				raw = strings.TrimRight(raw, " \t\r\n")
			}
			if raw != "" {
				out = append(out, chunk{kind: chunkText, text: raw, pos: i})
			}
			stripNext = stripRaw.right
			i = after
		}
	}
	text(src[i:], i, false)
	return out, nil
}

// nextOpen finds the next {{, {% or {# at or after from.
func nextOpen(src string, from int) (int, bool) {
	for k := from; k+1 < len(src); k++ {
		if src[k] == '{' && (src[k+1] == '{' || src[k+1] == '%' || src[k+1] == '#') {
			return k, true
		}
	}
	return 0, false
}

// closeOf finds where a tag ends, not counting a closer inside a string.
func closeOf(src string, from int, closer string, kind chunkKind) int {
	if kind == chunkComment {
		if k := strings.Index(src[from:], closer); k >= 0 {
			return from + k
		}
		return -1
	}
	quote := byte(0)
	for k := from; k < len(src); k++ {
		c := src[k]
		if quote != 0 {
			if c == '\\' {
				k++
			} else if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if strings.HasPrefix(src[k:], closer) {
			return k
		}
	}
	return -1
}

type rawStrip struct{ left, right bool }

func endRaw(src string, from int) (rawEnd, after int, strip rawStrip, ok bool) {
	for k := from; k < len(src); {
		open, found := nextOpen(src, k)
		if !found {
			return 0, 0, strip, false
		}
		if src[open+1] != '%' {
			k = open + 2
			continue
		}
		end := strings.Index(src[open:], "%}")
		if end < 0 {
			return 0, 0, strip, false
		}
		body := src[open+2 : open+end]
		left := strings.HasPrefix(body, "-")
		right := strings.HasSuffix(body, "-")
		if strings.TrimSpace(strings.Trim(body, "-+")) == "endraw" {
			return open, open + end + 2, rawStrip{left: left, right: right}, true
		}
		k = open + end + 2
	}
	return 0, 0, strip, false
}

// --- statements ---

type node interface{}

type textNode struct{ text string }
type printNode struct {
	e   expr
	pos int
}
type ifNode struct {
	conds  []expr
	bodies [][]node
	orelse []node
	pos    int
}
type setNode struct {
	name string
	e    expr
	pos  int
}
type forNode struct {
	names  []string
	iter   expr
	body   []node
	orelse []node
	pos    int
}

type blockEnd struct {
	word string
	rest string
	pos  int
}

type blockParser struct {
	chunks []chunk
	i      int
}

// until parses nodes up to a tag that closes or continues a block — elif,
// else, endif, endfor — and returns it, or nil at the end of the template.
func (p *blockParser) until() ([]node, *blockEnd, error) {
	var out []node
	for p.i < len(p.chunks) {
		c := p.chunks[p.i]
		p.i++
		switch c.kind {
		case chunkText:
			out = append(out, textNode{text: c.text})
		case chunkComment:
		case chunkExpr:
			e, err := parseExpr(c.text, c.pos)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, printNode{e: e, pos: c.pos})
		case chunkStmt:
			word, rest := firstWord(c.text)
			switch word {
			case "elif", "else", "endif", "endfor":
				return out, &blockEnd{word: word, rest: rest, pos: c.pos}, nil
			case "if":
				n, err := p.parseIf(rest, c.pos)
				if err != nil {
					return nil, nil, err
				}
				out = append(out, n)
			case "set":
				name, value, ok := strings.Cut(rest, "=")
				name = strings.TrimSpace(name)
				if !ok || !isName(name) {
					return nil, nil, errAt(c.pos, "{%% set %%} must be {%% set name = expression %%}; block sets and several names at once are not supported")
				}
				// "=" may be the first of "==" only inside the expression.
				e, err := parseExpr(value, c.pos)
				if err != nil {
					return nil, nil, err
				}
				out = append(out, setNode{name: name, e: e, pos: c.pos})
			case "for":
				n, err := p.parseFor(rest, c.pos)
				if err != nil {
					return nil, nil, err
				}
				out = append(out, n)
			case "":
				return nil, nil, errAt(c.pos, "an empty {%% %%}")
			default:
				return nil, nil, errAt(c.pos, "{%% %s %%} is not supported", word)
			}
		}
	}
	return out, nil, nil
}

func (p *blockParser) parseIf(cond string, pos int) (node, error) {
	n := ifNode{pos: pos}
	for {
		e, err := parseExpr(cond, pos)
		if err != nil {
			return nil, err
		}
		body, end, err := p.until()
		if err != nil {
			return nil, err
		}
		n.conds = append(n.conds, e)
		n.bodies = append(n.bodies, body)
		if end == nil {
			return nil, errAt(pos, "{%% if %%} opened here is never closed")
		}
		switch end.word {
		case "elif":
			cond, pos = end.rest, end.pos
			continue
		case "else":
			body, end2, err := p.until()
			if err != nil {
				return nil, err
			}
			if end2 == nil || end2.word != "endif" {
				return nil, errAt(end.pos, "{%% else %%} here is not followed by {%% endif %%}")
			}
			n.orelse = body
			return n, nil
		case "endif":
			return n, nil
		default:
			return nil, errAt(end.pos, "{%% %s %%} where {%% endif %%} was expected", end.word)
		}
	}
}

func (p *blockParser) parseFor(rest string, pos int) (node, error) {
	targets, iter, ok := cutWord(rest, "in")
	if !ok {
		return nil, errAt(pos, "{%% for %%} must be {%% for name in expression %%}")
	}
	n := forNode{pos: pos}
	for _, name := range strings.Split(targets, ",") {
		name = strings.TrimSpace(name)
		if !isName(name) {
			return nil, errAt(pos, "{%% for %%}: %q is not a name", name)
		}
		n.names = append(n.names, name)
	}
	if strings.Contains(iter, " if ") || strings.Contains(iter, " recursive") {
		return nil, errAt(pos, "{%% for ... if %%} and recursive loops are not supported")
	}
	e, err := parseExpr(iter, pos)
	if err != nil {
		return nil, err
	}
	n.iter = e
	body, end, err := p.until()
	if err != nil {
		return nil, err
	}
	n.body = body
	if end != nil && end.word == "else" {
		n.orelse, end, err = p.until()
		if err != nil {
			return nil, err
		}
	}
	if end == nil || end.word != "endfor" {
		return nil, errAt(pos, "{%% for %%} opened here is never closed")
	}
	return n, nil
}

func firstWord(s string) (word, rest string) {
	s = strings.TrimSpace(s)
	for i, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return s[:i], strings.TrimSpace(s[i:])
		}
	}
	return s, ""
}

// cutWord cuts s at the first standalone word w that is outside any string or
// bracket.
func cutWord(s, w string) (before, after string, ok bool) {
	depth, quote := 0, byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		}
		if depth == 0 && strings.HasPrefix(s[i:], w) && (i == 0 || isSpace(s[i-1])) && (i+len(w) == len(s) || isSpace(s[i+len(w)])) {
			return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+len(w):]), true
		}
	}
	return "", "", false
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func isName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// --- rendering ---

type scope struct {
	vars   map[string]any
	parent *scope
}

func (s *scope) get(name string) (any, bool) {
	for sc := s; sc != nil; sc = sc.parent {
		if v, ok := sc.vars[name]; ok {
			return v, true
		}
	}
	return nil, false
}

func renderNodes(nodes []node, sc *scope, b *strings.Builder) error {
	for _, n := range nodes {
		switch n := n.(type) {
		case textNode:
			b.WriteString(n.text)
		case printNode:
			v, err := n.e.eval(sc)
			if err != nil {
				return err
			}
			b.WriteString(str(v))
		case setNode:
			v, err := n.e.eval(sc)
			if err != nil {
				return err
			}
			sc.vars[n.name] = v
		case ifNode:
			done := false
			for i, cond := range n.conds {
				v, err := cond.eval(sc)
				if err != nil {
					return err
				}
				if truthy(v) {
					if err := renderNodes(n.bodies[i], sc, b); err != nil {
						return err
					}
					done = true
					break
				}
			}
			if !done {
				if err := renderNodes(n.orelse, sc, b); err != nil {
					return err
				}
			}
		case forNode:
			v, err := n.iter.eval(sc)
			if err != nil {
				return err
			}
			items, err := iterate(v, n.pos)
			if err != nil {
				return err
			}
			if len(items) == 0 {
				if err := renderNodes(n.orelse, sc, b); err != nil {
					return err
				}
				continue
			}
			for i, item := range items {
				inner := &scope{vars: map[string]any{}, parent: sc}
				if len(n.names) == 1 {
					inner.vars[n.names[0]] = item
				} else {
					var parts []any
					switch item := item.(type) {
					case []any:
						parts = item
					case tuple:
						parts = item
					}
					if len(parts) != len(n.names) {
						return errAt(n.pos, "{%% for %%}: cannot unpack %s into %d names", repr(item), len(n.names))
					}
					for k, name := range n.names {
						inner.vars[name] = parts[k]
					}
				}
				inner.vars["loop"] = normalize(map[string]any{
					"index": int64(i + 1), "index0": int64(i), "first": i == 0, "last": i == len(items)-1,
					"length": int64(len(items)), "revindex": int64(len(items) - i), "revindex0": int64(len(items) - i - 1),
				})
				if err := renderNodes(n.body, inner, b); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func walkNames(nodes []node, bound map[string]bool, see func(string)) {
	for _, n := range nodes {
		switch n := n.(type) {
		case printNode:
			n.e.names(bound, see)
		case setNode:
			n.e.names(bound, see)
			bound[n.name] = true
		case ifNode:
			for i, c := range n.conds {
				c.names(bound, see)
				walkNames(n.bodies[i], bound, see)
			}
			walkNames(n.orelse, bound, see)
		case forNode:
			n.iter.names(bound, see)
			inner := map[string]bool{"loop": true}
			for k := range bound {
				inner[k] = true
			}
			for _, name := range n.names {
				inner[name] = true
			}
			walkNames(n.body, inner, see)
			walkNames(n.orelse, bound, see)
		}
	}
}
