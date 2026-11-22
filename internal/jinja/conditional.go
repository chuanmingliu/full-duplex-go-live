package jinja

import "fmt"

// ParseConditional accepts bounded, non-iterating Jinja templates for
// rendering on the conversational path.
func ParseConditional(src string) (*Template, error) {
	t, err := Parse(src)
	if err != nil {
		return nil, err
	}
	if !conditionalNodes(t.nodes) {
		return nil, fmt.Errorf("use Jinja text, interpolation, if/elif/else, or set with simple expressions; loops, calls, filters and arithmetic are unavailable")
	}
	return t, nil
}

func conditionalNodes(nodes []node) bool {
	for _, n := range nodes {
		switch n := n.(type) {
		case textNode:
		case printNode:
			if !conditionalExpr(n.e) {
				return false
			}
		case setNode:
			if !conditionalExpr(n.e) {
				return false
			}
		case ifNode:
			for i, cond := range n.conds {
				if !conditionalExpr(cond) || !conditionalNodes(n.bodies[i]) {
					return false
				}
			}
			if !conditionalNodes(n.orelse) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func conditionalExpr(e expr) bool {
	switch x := e.(type) {
	case nil, *litExpr, *nameExpr:
		return true
	case *attrExpr:
		return conditionalExpr(x.e)
	case *indexExpr:
		return conditionalExpr(x.e) && conditionalExpr(x.index)
	case *condExpr:
		return conditionalExpr(x.test) && conditionalExpr(x.then) && conditionalExpr(x.orelse)
	case *boolExpr:
		return conditionalExpr(x.l) && conditionalExpr(x.r)
	case *notExpr:
		return conditionalExpr(x.e)
	case *testExpr:
		switch x.name {
		case "defined", "undefined", "none", "boolean", "true", "false":
			return len(x.args) == 0 && conditionalExpr(x.e)
		}
		return false
	case *listExpr:
		for _, arg := range x.items {
			if !conditionalExpr(arg) {
				return false
			}
		}
		return true
	case *compareExpr:
		for _, arg := range x.rest {
			if !conditionalExpr(arg) {
				return false
			}
		}
		return conditionalExpr(x.first)
	default:
		return false
	}
}
