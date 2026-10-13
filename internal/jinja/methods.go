package jinja

import (
	"math"
	"strconv"
	"strings"
	"unicode"
)

// The filters, tests and methods a template may use. Each does what Jinja2's
// (or Python's) of the same name does for the kinds of value this package
// has; one that is not here is an error when the template is parsed.

type filterFunc func(v any, args []any, kwargs map[string]any, pos int) (any, error)
type testFunc func(v any, args []any, pos int) (bool, error)

var filters map[string]filterFunc
var tests map[string]testFunc

// testTakesArg lists the tests that may be followed by a bare argument, as in
// "n is divisibleby 3".
var testTakesArg = map[string]bool{"divisibleby": true, "eq": true, "equalto": true, "ne": true,
	"lt": true, "gt": true, "le": true, "ge": true, "in": true, "sameas": true}

var dictMethods = map[string]bool{"get": true, "keys": true, "values": true, "items": true}

var strMethods = map[string]bool{"split": true, "strip": true, "lstrip": true, "rstrip": true,
	"startswith": true, "endswith": true, "replace": true, "upper": true, "lower": true, "find": true,
	"join": true, "count": true, "isdigit": true, "zfill": true, "capitalize": true}

// arg picks an argument given by position or by name.
func arg(args []any, kwargs map[string]any, i int, name string, def any) any {
	if i < len(args) {
		return args[i]
	}
	if v, ok := kwargs[name]; ok {
		return v
	}
	return def
}

func toInt(v any, def any) any {
	switch v := v.(type) {
	case int64:
		return v
	case bool:
		if v {
			return int64(1)
		}
		return int64(0)
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return def
		}
		return int64(v)
	case string:
		s := asciiDigits(strings.TrimSpace(v))
		if n, err := strconv.ParseInt(strings.ReplaceAll(s, "_", ""), 10, 64); err == nil && !strings.HasPrefix(s, "_") && !strings.HasSuffix(s, "_") {
			return n
		}
		if f, ok := pyParseFloat(s); ok && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return int64(f)
		}
	}
	return def
}

// asciiDigits rewrites any script's decimal digits as 0 to 9, which is what
// Python's int() and float() read them as: "１２" is twelve.
func asciiDigits(s string) string {
	if strings.IndexFunc(s, func(r rune) bool { return r > unicode.MaxASCII && unicode.IsDigit(r) }) < 0 {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r <= unicode.MaxASCII || !unicode.IsDigit(r) {
			return r
		}
		// A script's digits are ten in a row: count back to its zero.
		for k := rune(0); k < 10; k++ {
			if !unicode.IsDigit(r - k - 1) {
				return '0' + k
			}
		}
		return r
	}, s)
}

// pyParseFloat reads what Python's float() reads.
func pyParseFloat(s string) (float64, bool) {
	s = asciiDigits(strings.TrimSpace(s))
	if s == "" {
		return 0, false
	}
	lower := strings.ToLower(s)
	switch strings.TrimLeft(lower, "+-") {
	case "inf", "infinity":
		if strings.HasPrefix(lower, "-") {
			return math.Inf(-1), true
		}
		return math.Inf(1), true
	case "nan":
		return math.NaN(), true
	}
	// Go's parser also takes hex floats and "0x" forms, which Python's does not.
	if strings.ContainsAny(lower, "xp") {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(s, "_", ""), 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func length(v any, pos int) (int64, error) {
	switch v := v.(type) {
	case string:
		return int64(len([]rune(v))), nil
	case []any:
		return int64(len(v)), nil
	case tuple:
		return int64(len(v)), nil
	case *dict:
		return int64(len(v.keys)), nil
	case undefined:
		return 0, nil
	}
	return 0, errAt(pos, "%s has no length", typeName(v))
}

func init() {
	filters = map[string]filterFunc{
		// int and float fall back to their default for a value that is not a
		// number, but not for one that is not there: Jinja2 fails on that, so
		// a template has to guard it with "is defined", and so it must here.
		"int": func(v any, args []any, kwargs map[string]any, pos int) (any, error) {
			if u, isU := v.(undefined); isU {
				return nil, errAt(pos, "%s is undefined and cannot be made a number", u.what())
			}
			return toInt(v, arg(args, kwargs, 0, "default", int64(0))), nil
		},
		"float": func(v any, args []any, kwargs map[string]any, pos int) (any, error) {
			if u, isU := v.(undefined); isU {
				return nil, errAt(pos, "%s is undefined and cannot be made a number", u.what())
			}
			def := arg(args, kwargs, 0, "default", float64(0))
			switch v := v.(type) {
			case string:
				if f, ok := pyParseFloat(v); ok {
					return f, nil
				}
				return def, nil
			}
			if f, _, _, ok := number(v); ok {
				return f, nil
			}
			return def, nil
		},
		"string": func(v any, _ []any, _ map[string]any, _ int) (any, error) { return str(v), nil },
		"default": func(v any, args []any, kwargs map[string]any, _ int) (any, error) {
			def := arg(args, kwargs, 0, "default_value", "")
			boolean := truthy(arg(args, kwargs, 1, "boolean", false))
			if _, isU := v.(undefined); isU || boolean && !truthy(v) {
				return def, nil
			}
			return v, nil
		},
		"length": func(v any, _ []any, _ map[string]any, pos int) (any, error) { return length(v, pos) },
		"trim": func(v any, args []any, kwargs map[string]any, _ int) (any, error) {
			if chars, ok := arg(args, kwargs, 0, "chars", nil).(string); ok {
				return strings.Trim(str(v), chars), nil
			}
			return pyStrip(str(v), true, true), nil
		},
		"upper": func(v any, _ []any, _ map[string]any, _ int) (any, error) { return strings.ToUpper(str(v)), nil },
		"lower": func(v any, _ []any, _ map[string]any, _ int) (any, error) { return strings.ToLower(str(v)), nil },
		"capitalize": func(v any, _ []any, _ map[string]any, _ int) (any, error) {
			return capitalize(str(v)), nil
		},
		"replace": func(v any, args []any, kwargs map[string]any, pos int) (any, error) {
			if len(args) < 2 {
				return nil, errAt(pos, "replace needs what to replace and what with")
			}
			n := -1
			if c, ok := asInt(arg(args, kwargs, 2, "count", nil)); ok {
				n = int(c)
			}
			return strings.Replace(str(v), str(args[0]), str(args[1]), n), nil
		},
		"join": func(v any, args []any, kwargs map[string]any, pos int) (any, error) {
			items, err := iterate(v, pos)
			if err != nil {
				return nil, err
			}
			parts := make([]string, len(items))
			for i, item := range items {
				parts[i] = str(item)
			}
			return strings.Join(parts, str(arg(args, kwargs, 0, "d", ""))), nil
		},
		"round": func(v any, args []any, kwargs map[string]any, pos int) (any, error) {
			f, whole, isInt, ok := number(v)
			if !ok {
				return nil, errAt(pos, "round needs a number, not %s", typeName(v))
			}
			precision, _ := asInt(arg(args, kwargs, 0, "precision", int64(0)))
			method := str(arg(args, kwargs, 1, "method", "common"))
			scale := math.Pow(10, float64(precision))
			switch method {
			case "common":
				if isInt { // a whole number stays one
					if precision >= 0 {
						return whole, nil
					}
					return int64(math.RoundToEven(f*scale) / scale), nil
				}
				// Python rounds the decimal the float really is, to even on a
				// tie; formatting to the precision does the same.
				r, _ := strconv.ParseFloat(strconv.FormatFloat(f, 'f', int(max(precision, 0)), 64), 64)
				if precision < 0 {
					r = math.RoundToEven(f*scale) / scale
				}
				return r, nil
			case "ceil":
				return math.Ceil(f*scale) / scale, nil
			case "floor":
				return math.Floor(f*scale) / scale, nil
			}
			return nil, errAt(pos, "round's method is common, ceil or floor")
		},
		"abs": func(v any, _ []any, _ map[string]any, pos int) (any, error) {
			f, i, isInt, ok := number(v)
			if !ok {
				return nil, errAt(pos, "abs needs a number, not %s", typeName(v))
			}
			if isInt {
				if i < 0 {
					return -i, nil
				}
				return i, nil
			}
			return math.Abs(f), nil
		},
		"first": func(v any, _ []any, _ map[string]any, pos int) (any, error) {
			items, err := iterate(v, pos)
			if err != nil || len(items) == 0 {
				return undefined{}, err
			}
			return items[0], nil
		},
		"last": func(v any, _ []any, _ map[string]any, pos int) (any, error) {
			items, err := iterate(v, pos)
			if err != nil || len(items) == 0 {
				return undefined{}, err
			}
			return items[len(items)-1], nil
		},
		"list": func(v any, _ []any, _ map[string]any, pos int) (any, error) {
			items, err := iterate(v, pos)
			if err != nil {
				return nil, err
			}
			return append([]any{}, items...), nil
		},
		"safe": func(v any, _ []any, _ map[string]any, _ int) (any, error) { return v, nil },
	}
	filters["d"] = filters["default"]
	filters["count"] = filters["length"]

	compareTest := func(op string) testFunc {
		return func(v any, args []any, pos int) (bool, error) {
			if len(args) != 1 {
				return false, errAt(pos, "this test needs one value to compare with")
			}
			return compare(op, v, args[0], pos)
		}
	}
	tests = map[string]testFunc{
		"defined":   func(v any, _ []any, _ int) (bool, error) { _, u := v.(undefined); return !u, nil },
		"undefined": func(v any, _ []any, _ int) (bool, error) { _, u := v.(undefined); return u, nil },
		"none":      func(v any, _ []any, _ int) (bool, error) { return v == nil, nil },
		"string":    func(v any, _ []any, _ int) (bool, error) { _, ok := v.(string); return ok, nil },
		"number":    func(v any, _ []any, _ int) (bool, error) { _, _, _, ok := number(v); return ok, nil },
		"integer":   func(v any, _ []any, _ int) (bool, error) { _, ok := v.(int64); return ok, nil },
		"float":     func(v any, _ []any, _ int) (bool, error) { _, ok := v.(float64); return ok, nil },
		"boolean":   func(v any, _ []any, _ int) (bool, error) { _, ok := v.(bool); return ok, nil },
		"true":      func(v any, _ []any, _ int) (bool, error) { b, ok := v.(bool); return ok && b, nil },
		"false":     func(v any, _ []any, _ int) (bool, error) { b, ok := v.(bool); return ok && !b, nil },
		"mapping":   func(v any, _ []any, _ int) (bool, error) { _, ok := v.(*dict); return ok, nil },
		"sequence": func(v any, _ []any, _ int) (bool, error) {
			switch v.(type) {
			case string, []any, tuple, *dict:
				return true, nil
			}
			return false, nil
		},
		"iterable": func(v any, _ []any, _ int) (bool, error) {
			switch v.(type) {
			case string, []any, tuple, *dict, undefined:
				return true, nil
			}
			return false, nil
		},
		"odd": func(v any, _ []any, pos int) (bool, error) {
			i, ok := asInt(v)
			if !ok {
				return false, errAt(pos, "odd needs a whole number, not %s", typeName(v))
			}
			return i%2 != 0, nil
		},
		"even": func(v any, _ []any, pos int) (bool, error) {
			i, ok := asInt(v)
			if !ok {
				return false, errAt(pos, "even needs a whole number, not %s", typeName(v))
			}
			return i%2 == 0, nil
		},
		"divisibleby": func(v any, args []any, pos int) (bool, error) {
			if len(args) != 1 {
				return false, errAt(pos, "divisibleby needs a number to divide by")
			}
			m, err := arith("%", v, args[0], pos)
			if err != nil {
				return false, err
			}
			return !truthy(m), nil
		},
		"eq": compareTest("=="), "equalto": compareTest("=="), "ne": compareTest("!="),
		"lt": compareTest("<"), "gt": compareTest(">"), "le": compareTest("<="), "ge": compareTest(">="),
		"in": compareTest("in"),
	}
}

func capitalize(s string) string {
	runes := []rune(s)
	for i, r := range runes {
		if i == 0 {
			runes[i] = unicode.ToUpper(r)
		} else {
			runes[i] = unicode.ToLower(r)
		}
	}
	return string(runes)
}

// pyStrip is str.strip() with no argument: Python's idea of whitespace.
func pyStrip(s string, left, right bool) string {
	if left {
		s = strings.TrimLeftFunc(s, unicode.IsSpace)
	}
	if right {
		s = strings.TrimRightFunc(s, unicode.IsSpace)
	}
	return s
}

// pySplit is str.split(sep, maxsplit).
func pySplit(s string, sep any, maxsplit int64, pos int) (any, error) {
	var parts []string
	if sep == nil {
		// Runs of whitespace separate, and none is kept at either end.
		rest := strings.TrimLeftFunc(s, unicode.IsSpace)
		for rest != "" {
			if maxsplit >= 0 && int64(len(parts)) == maxsplit {
				parts = append(parts, rest) // what is left is kept as it is
				break
			}
			i := strings.IndexFunc(rest, unicode.IsSpace)
			if i < 0 {
				parts = append(parts, rest)
				break
			}
			parts = append(parts, rest[:i])
			rest = strings.TrimLeftFunc(rest[i:], unicode.IsSpace)
		}
	} else {
		sp, ok := sep.(string)
		if !ok {
			return nil, errAt(pos, "split's separator must be a string, not %s", typeName(sep))
		}
		if sp == "" {
			return nil, errAt(pos, "split's separator cannot be empty")
		}
		if maxsplit < 0 {
			parts = strings.Split(s, sp)
		} else {
			parts = strings.SplitN(s, sp, int(maxsplit)+1)
		}
	}
	out := make([]any, len(parts))
	for i, p := range parts {
		out[i] = p
	}
	return out, nil
}

func callMethod(self any, name string, args []any, kwargs map[string]any, pos int) (any, error) {
	switch self := self.(type) {
	case string:
		text := func(i int, what string) (string, error) {
			if i >= len(args) {
				return "", errAt(pos, "%s needs %s", name, what)
			}
			s, ok := args[i].(string)
			if !ok {
				return "", errAt(pos, "%s needs %s as a string, not %s", name, what, typeName(args[i]))
			}
			return s, nil
		}
		switch name {
		case "split":
			maxsplit := int64(-1)
			if n, ok := asInt(arg(args, kwargs, 1, "maxsplit", nil)); ok {
				maxsplit = n
			}
			return pySplit(self, arg(args, kwargs, 0, "sep", nil), maxsplit, pos)
		case "strip", "lstrip", "rstrip":
			left, right := name != "rstrip", name != "lstrip"
			if len(args) > 0 && args[0] != nil {
				chars, err := text(0, "the characters to strip")
				if err != nil {
					return nil, err
				}
				out := self
				if left {
					out = strings.TrimLeft(out, chars)
				}
				if right {
					out = strings.TrimRight(out, chars)
				}
				return out, nil
			}
			return pyStrip(self, left, right), nil
		case "startswith", "endswith":
			s, err := text(0, "what to look for")
			if err != nil {
				return nil, err
			}
			if name == "startswith" {
				return strings.HasPrefix(self, s), nil
			}
			return strings.HasSuffix(self, s), nil
		case "replace":
			old, err := text(0, "what to replace")
			if err != nil {
				return nil, err
			}
			with, err := text(1, "what to replace it with")
			if err != nil {
				return nil, err
			}
			n := -1
			if c, ok := asInt(arg(args, kwargs, 2, "count", nil)); ok {
				n = int(c)
			}
			return strings.Replace(self, old, with, n), nil
		case "upper":
			return strings.ToUpper(self), nil
		case "lower":
			return strings.ToLower(self), nil
		case "capitalize":
			return capitalize(self), nil
		case "find":
			s, err := text(0, "what to look for")
			if err != nil {
				return nil, err
			}
			i := strings.Index(self, s)
			if i < 0 {
				return int64(-1), nil
			}
			return int64(len([]rune(self[:i]))), nil
		case "count":
			s, err := text(0, "what to count")
			if err != nil {
				return nil, err
			}
			if s == "" {
				return int64(len([]rune(self)) + 1), nil
			}
			return int64(strings.Count(self, s)), nil
		case "isdigit":
			if self == "" {
				return false, nil
			}
			for _, r := range self {
				if !unicode.IsDigit(r) {
					return false, nil
				}
			}
			return true, nil
		case "zfill":
			n, ok := asInt(arg(args, kwargs, 0, "width", nil))
			if !ok {
				return nil, errAt(pos, "zfill needs a width")
			}
			sign := ""
			body := self
			if strings.HasPrefix(body, "-") || strings.HasPrefix(body, "+") {
				sign, body = body[:1], body[1:]
			}
			for int64(len([]rune(sign+body))) < n {
				body = "0" + body
			}
			return sign + body, nil
		case "join":
			if len(args) != 1 {
				return nil, errAt(pos, "join needs a list")
			}
			items, err := iterate(args[0], pos)
			if err != nil {
				return nil, err
			}
			parts := make([]string, len(items))
			for i, item := range items {
				s, ok := item.(string)
				if !ok {
					return nil, errAt(pos, "join needs strings, and found %s", typeName(item))
				}
				parts[i] = s
			}
			return strings.Join(parts, self), nil
		}
	case *dict:
		switch name {
		case "get":
			if len(args) == 0 {
				return nil, errAt(pos, "get needs a key")
			}
			if k, ok := args[0].(string); ok {
				if v, has := self.get(k); has {
					return v, nil
				}
			}
			return arg(args, kwargs, 1, "default", nil), nil
		case "keys":
			out := make([]any, len(self.keys))
			for i, k := range self.keys {
				out[i] = k
			}
			return out, nil
		case "values":
			out := make([]any, len(self.keys))
			for i, k := range self.keys {
				out[i] = self.m[k]
			}
			return out, nil
		case "items":
			out := make([]any, len(self.keys))
			for i, k := range self.keys {
				out[i] = tuple{k, self.m[k]}
			}
			return out, nil
		}
	}
	return nil, errAt(pos, "%s has no method %q that is supported here", typeName(self), name)
}
