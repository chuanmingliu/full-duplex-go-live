package jinja

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
)

// The values an expression can have, and how Python — which is what Jinja2
// computes with — treats them: what is true, what equals what, how a number
// is written.

// undefined is a name the template was not given.
type undefined struct{ name string }

func (u undefined) what() string {
	if u.name == "" {
		return "a value"
	}
	return "'" + u.name + "'"
}

// tuple is a list written with round brackets; it differs only in how it is
// printed.
type tuple []any

// dict is a mapping that remembers the order of its keys.
type dict struct {
	keys []string
	m    map[string]any
}

func (d *dict) get(k string) (any, bool) {
	if d.m == nil {
		return nil, false
	}
	v, ok := d.m[k]
	return v, ok
}

func (d *dict) set(k string, v any) {
	if d.m == nil {
		d.m = map[string]any{}
	}
	if _, has := d.m[k]; !has {
		d.keys = append(d.keys, k)
	}
	d.m[k] = v
}

// boundMethod is something.method, waiting to be called.
type boundMethod struct {
	self any
	name string
}

// normalize turns a value given to Render into one of the kinds the
// evaluator knows. A map's keys are put in order, since Go keeps none.
func normalize(v any) any {
	switch v := v.(type) {
	case nil, bool, int64, float64, string, undefined, *dict, tuple:
		return v
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case float32:
		return float64(v)
	case json.Number:
		if n, err := strconv.ParseInt(string(v), 10, 64); err == nil {
			return n
		}
		f, _ := v.Float64()
		return f
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = normalize(item)
		}
		return out
	case []string:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = item
		}
		return out
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		d := &dict{}
		for _, k := range keys {
			d.set(k, normalize(v[k]))
		}
		return d
	case map[string]string:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		d := &dict{}
		for _, k := range keys {
			d.set(k, v[k])
		}
		return d
	}
	return undefined{}
}

func truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case int64:
		return v != 0
	case float64:
		return v != 0
	case string:
		return v != ""
	case []any:
		return len(v) > 0
	case tuple:
		return len(v) > 0
	case *dict:
		return len(v.keys) > 0
	case undefined:
		return false
	}
	return true
}

// str is Python's str(): what {{ value }} writes.
func str(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case undefined:
		return ""
	case *boundMethod:
		return ""
	}
	return repr(v)
}

// repr is Python's repr(): how a value is written inside a list, and how an
// error names it.
func repr(v any) string {
	switch v := v.(type) {
	case nil:
		return "None"
	case bool:
		if v {
			return "True"
		}
		return "False"
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return pyFloat(v)
	case string:
		quote := "'"
		if strings.Contains(v, "'") && !strings.Contains(v, `"`) {
			quote = `"`
		}
		r := strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\r", `\r`, "\t", `\t`, quote, `\`+quote)
		return quote + r.Replace(v) + quote
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			parts[i] = repr(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case tuple:
		parts := make([]string, len(v))
		for i, item := range v {
			parts[i] = repr(item)
		}
		if len(parts) == 1 {
			return "(" + parts[0] + ",)"
		}
		return "(" + strings.Join(parts, ", ") + ")"
	case *dict:
		parts := make([]string, len(v.keys))
		for i, k := range v.keys {
			parts[i] = repr(k) + ": " + repr(v.m[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case undefined:
		return "Undefined"
	}
	return "?"
}

// pyFloat writes a float as Python does: the shortest digits that read back
// the same, always with a point or an exponent, the exponent from 1e16 up and
// below 1e-4.
func pyFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64)
	exp, _ := strconv.Atoi(e[strings.IndexByte(e, 'e')+1:])
	if exp < -4 || exp >= 16 {
		return e
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// number reads v as a number: bools count, as they do in Python.
func number(v any) (f float64, i int64, isInt, ok bool) {
	switch v := v.(type) {
	case int64:
		return float64(v), v, true, true
	case bool:
		if v {
			return 1, 1, true, true
		}
		return 0, 0, true, true
	case float64:
		return v, 0, false, true
	}
	return 0, 0, false, false
}

func asInt(v any) (int64, bool) {
	_, i, isInt, ok := number(v)
	return i, ok && isInt
}

func equal(l, r any) bool {
	if lf, li, lInt, ok := number(l); ok {
		rf, ri, rInt, ok := number(r)
		if !ok {
			return false
		}
		if lInt && rInt {
			return li == ri
		}
		return lf == rf
	}
	switch l := l.(type) {
	case nil:
		return r == nil
	case string:
		rs, ok := r.(string)
		return ok && l == rs
	case undefined:
		_, ok := r.(undefined)
		return ok
	case []any:
		rl, ok := r.([]any)
		return ok && equalSeq(l, rl)
	case tuple:
		rl, ok := r.(tuple)
		return ok && equalSeq(l, rl)
	case *dict:
		rd, ok := r.(*dict)
		if !ok || len(l.keys) != len(rd.keys) {
			return false
		}
		for _, k := range l.keys {
			rv, has := rd.get(k)
			if !has || !equal(l.m[k], rv) {
				return false
			}
		}
		return true
	}
	return false
}

func equalSeq(l, r []any) bool {
	if len(l) != len(r) {
		return false
	}
	for i := range l {
		if !equal(l[i], r[i]) {
			return false
		}
	}
	return true
}

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "None"
	case bool:
		return "a bool"
	case int64:
		return "an int"
	case float64:
		return "a float"
	case string:
		return "a string"
	case []any:
		return "a list"
	case tuple:
		return "a tuple"
	case *dict:
		return "a dict"
	case undefined:
		return "undefined"
	}
	return "that"
}

// order compares two values that have an order: -1, 0 or 1.
func order(l, r any, pos int) (int, error) {
	if lf, li, lInt, ok := number(l); ok {
		if rf, ri, rInt, ok := number(r); ok {
			if lInt && rInt {
				switch {
				case li < ri:
					return -1, nil
				case li > ri:
					return 1, nil
				}
				return 0, nil
			}
			switch {
			case lf < rf:
				return -1, nil
			case lf > rf:
				return 1, nil
			}
			return 0, nil
		}
	}
	if ls, ok := l.(string); ok {
		if rs, ok := r.(string); ok {
			return strings.Compare(ls, rs), nil
		}
	}
	seq := func(v any) ([]any, bool) {
		switch v := v.(type) {
		case []any:
			return v, true
		case tuple:
			return v, true
		}
		return nil, false
	}
	if ll, ok := seq(l); ok {
		if rl, ok := seq(r); ok {
			for i := 0; i < len(ll) && i < len(rl); i++ {
				if equal(ll[i], rl[i]) {
					continue
				}
				return order(ll[i], rl[i], pos)
			}
			switch {
			case len(ll) < len(rl):
				return -1, nil
			case len(ll) > len(rl):
				return 1, nil
			}
			return 0, nil
		}
	}
	return 0, errAt(pos, "%s and %s cannot be put in order", typeName(l), typeName(r))
}

func compare(op string, l, r any, pos int) (bool, error) {
	switch op {
	case "==":
		return equal(l, r), nil
	case "!=":
		return !equal(l, r), nil
	case "in", "notin":
		in, err := contains(r, l, pos)
		return in == (op == "in"), err
	}
	c, err := order(l, r, pos)
	if err != nil {
		return false, err
	}
	switch op {
	case "<":
		return c < 0, nil
	case ">":
		return c > 0, nil
	case "<=":
		return c <= 0, nil
	}
	return c >= 0, nil
}

func contains(container, item any, pos int) (bool, error) {
	switch c := container.(type) {
	case string:
		s, ok := item.(string)
		if !ok {
			return false, errAt(pos, "only a string can be looked for in a string, not %s", typeName(item))
		}
		return strings.Contains(c, s), nil
	case []any:
		for _, x := range c {
			if equal(x, item) {
				return true, nil
			}
		}
		return false, nil
	case tuple:
		for _, x := range c {
			if equal(x, item) {
				return true, nil
			}
		}
		return false, nil
	case *dict:
		s, ok := item.(string)
		if !ok {
			return false, nil
		}
		_, has := c.get(s)
		return has, nil
	case undefined:
		return false, nil
	}
	return false, errAt(pos, "nothing can be looked for in %s", typeName(container))
}

func arith(op string, l, r any, pos int) (any, error) {
	if op == "~" {
		return str(l) + str(r), nil
	}
	for _, v := range []any{l, r} {
		if u, ok := v.(undefined); ok {
			return nil, errAt(pos, "%s is undefined and cannot be used in arithmetic", u.what())
		}
	}
	lf, li, lInt, lNum := number(l)
	rf, ri, rInt, rNum := number(r)
	if lNum && rNum {
		bothInt := lInt && rInt
		switch op {
		case "+":
			if bothInt {
				return li + ri, nil
			}
			return lf + rf, nil
		case "-":
			if bothInt {
				return li - ri, nil
			}
			return lf - rf, nil
		case "*":
			if bothInt {
				return li * ri, nil
			}
			return lf * rf, nil
		case "/":
			if rf == 0 {
				return nil, errAt(pos, "division by zero")
			}
			return lf / rf, nil
		case "//":
			if rf == 0 {
				return nil, errAt(pos, "division by zero")
			}
			if bothInt {
				q := li / ri
				if (li%ri != 0) && ((li < 0) != (ri < 0)) {
					q--
				}
				return q, nil
			}
			return math.Floor(lf / rf), nil
		case "%":
			if rf == 0 {
				return nil, errAt(pos, "division by zero")
			}
			if bothInt {
				m := li % ri
				if m != 0 && ((m < 0) != (ri < 0)) {
					m += ri
				}
				return m, nil
			}
			m := math.Mod(lf, rf)
			if m != 0 {
				if (m < 0) != (rf < 0) {
					m += rf
				}
			} else {
				m = math.Copysign(0, rf) // a zero takes the divisor's sign
			}
			return m, nil
		case "**":
			p := math.Pow(lf, rf)
			if bothInt && ri >= 0 && math.Abs(p) < 1<<62 {
				return int64(p), nil
			}
			return p, nil
		}
	}
	switch op {
	case "+":
		switch l := l.(type) {
		case string:
			if rs, ok := r.(string); ok {
				return l + rs, nil
			}
		case []any:
			if rl, ok := r.([]any); ok {
				return append(append([]any{}, l...), rl...), nil
			}
		case tuple:
			if rl, ok := r.(tuple); ok {
				return append(append(tuple{}, l...), rl...), nil
			}
		}
	case "*":
		if s, ok := l.(string); ok && rNum && rInt {
			return strings.Repeat(s, int(max(ri, 0))), nil
		}
		if s, ok := r.(string); ok && lNum && lInt {
			return strings.Repeat(s, int(max(li, 0))), nil
		}
	}
	return nil, errAt(pos, "%q cannot be applied to %s and %s", op, typeName(l), typeName(r))
}

func subscript(v, index any, pos int) (any, error) {
	at := func(n int) (int, bool) {
		i, ok := asInt(index)
		if !ok {
			return 0, false
		}
		if i < 0 {
			i += int64(n)
		}
		return int(i), i >= 0 && i < int64(n)
	}
	switch v := v.(type) {
	case undefined:
		return nil, errAt(pos, "%s is undefined and has no parts", v.what())
	case string:
		runes := []rune(v)
		if i, ok := at(len(runes)); ok {
			return string(runes[i]), nil
		}
	case []any:
		if i, ok := at(len(v)); ok {
			return v[i], nil
		}
	case tuple:
		if i, ok := at(len(v)); ok {
			return v[i], nil
		}
	case *dict:
		if k, ok := index.(string); ok {
			if item, has := v.get(k); has {
				return item, nil
			}
		}
	}
	return undefined{}, nil
}

// slice is value[lo:hi:step] as Python cuts it.
func slice(v any, lo, hi, step *int64, pos int) (any, error) {
	var n int
	var runes []rune
	var items []any
	isTuple := false
	switch v := v.(type) {
	case string:
		runes = []rune(v)
		n = len(runes)
	case []any:
		items, n = v, len(v)
	case tuple:
		items, n, isTuple = v, len(v), true
	case undefined:
		return nil, errAt(pos, "%s is undefined and cannot be cut", v.what())
	default:
		return nil, errAt(pos, "%s cannot be cut with [:]", typeName(v))
	}
	st := int64(1)
	if step != nil {
		st = *step
	}
	if st == 0 {
		return nil, errAt(pos, "a slice's step cannot be zero")
	}
	clamp := func(p *int64, def, low, high int64) int64 {
		if p == nil {
			return def
		}
		x := *p
		if x < 0 {
			x += int64(n)
		}
		return min(max(x, low), high)
	}
	var idx []int
	if st > 0 {
		a, b := clamp(lo, 0, 0, int64(n)), clamp(hi, int64(n), 0, int64(n))
		for i := a; i < b; i += st {
			idx = append(idx, int(i))
		}
	} else {
		a, b := clamp(lo, int64(n)-1, -1, int64(n)-1), clamp(hi, -1, -1, int64(n)-1)
		for i := a; i > b; i += st {
			idx = append(idx, int(i))
		}
	}
	if runes != nil || n == 0 && items == nil {
		out := make([]rune, len(idx))
		for k, i := range idx {
			out[k] = runes[i]
		}
		return string(out), nil
	}
	out := make([]any, len(idx))
	for k, i := range idx {
		out[k] = items[i]
	}
	if isTuple {
		return tuple(out), nil
	}
	return out, nil
}

func iterate(v any, pos int) ([]any, error) {
	switch v := v.(type) {
	case []any:
		return v, nil
	case tuple:
		return v, nil
	case string:
		runes := []rune(v)
		out := make([]any, len(runes))
		for i, r := range runes {
			out[i] = string(r)
		}
		return out, nil
	case *dict:
		out := make([]any, len(v.keys))
		for i, k := range v.keys {
			out[i] = k
		}
		return out, nil
	case undefined:
		return nil, nil
	}
	return nil, errAt(pos, "%s cannot be looped over", typeName(v))
}
