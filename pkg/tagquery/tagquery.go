// Package tagquery implements a postfix (Reverse-Polish-Notation) boolean
// tag-query language: a slash-separated path of tags and `and`/`or`/`not`
// operators, evaluated onto a stack to build an AST.
//
// It is a Go port of the tagfs postfix query evaluator
// (github.com/benjaminabbitt/tagfs), ported by way of its zero-dependency
// TypeScript reference port. The package has no external dependencies and
// no coupling to any filesystem, storage, or item-collection type: an Expr
// is evaluated per item via a caller-supplied "has this tag" predicate, so
// any consumer (file trees, task stores, etc.) can reuse it unchanged.
//
// Grammar: split a query path on `/`, discard empty components, and
// evaluate the remaining tokens as postfix (RPN):
//
//   - a token that is not "and", "or", or "not" (case-insensitively) is a
//     tag: push Term(tag).
//   - "and" pops two operands and pushes And(left, right); needs >= 2
//     operands on the stack.
//   - "or" pops two operands and pushes Or(left, right); needs >= 2
//     operands on the stack. Can be disabled via the DisableOr option.
//   - "not" pops one operand and pushes Not(operand); needs >= 1 operand on
//     the stack.
//   - if more than one expression remains on the stack once all tokens are
//     consumed, they are folded left-associatively into an And chain
//     (implicit AND): "/foo/bar" behaves like "/foo/bar/and".
//   - an empty query (e.g. "", "/", "//") parses to Universal, which
//     matches everything.
//
// Operators are matched case-insensitively ("AND", "And", "and" are all the
// `and` operator); tags are matched case-sensitively.
//
// Malformed input (an operator with too few operands, or "or" used while
// disabled) is a parse error — Parse never silently degrades to an
// always-false or always-true expression.
package tagquery

import (
	"errors"
	"fmt"
	"strings"
)

// Kind identifies the shape of an Expr node.
type Kind int

const (
	// KindTerm is a leaf node matching a single tag.
	KindTerm Kind = iota
	// KindAnd is a conjunction of two expressions.
	KindAnd
	// KindOr is a disjunction of two expressions.
	KindOr
	// KindNot is the negation of an expression.
	KindNot
	// KindEmpty is the expression that matches nothing.
	KindEmpty
	// KindUniversal is the expression that matches everything.
	KindUniversal
)

// String returns the Kind's name, e.g. "Term" or "And".
func (k Kind) String() string {
	switch k {
	case KindTerm:
		return "Term"
	case KindAnd:
		return "And"
	case KindOr:
		return "Or"
	case KindNot:
		return "Not"
	case KindEmpty:
		return "Empty"
	case KindUniversal:
		return "Universal"
	default:
		return "Unknown"
	}
}

// Expr is a node in a tag-query AST: a closed sum type over Term, And, Or,
// Not, Empty, and Universal, discriminated by Kind. Build nodes with the
// Term, And, Or, Not, Empty, and Universal constructor functions rather
// than composing Expr literals directly; the zero value of Expr is a valid
// Term with an empty tag, which is rarely what's wanted.
type Expr struct {
	Kind Kind

	// Tag holds the matched tag when Kind == KindTerm.
	Tag string

	// Left and Right hold the operands when Kind == KindAnd or KindOr.
	Left, Right *Expr

	// Operand holds the negated expression when Kind == KindNot.
	Operand *Expr
}

// Term returns a leaf expression matching a single tag.
func Term(tag string) Expr {
	return Expr{Kind: KindTerm, Tag: tag}
}

// And returns the conjunction of two expressions.
func And(left, right Expr) Expr {
	return Expr{Kind: KindAnd, Left: &left, Right: &right}
}

// Or returns the disjunction of two expressions.
func Or(left, right Expr) Expr {
	return Expr{Kind: KindOr, Left: &left, Right: &right}
}

// Not returns the negation of an expression.
func Not(operand Expr) Expr {
	return Expr{Kind: KindNot, Operand: &operand}
}

// Empty returns the expression that matches nothing.
func Empty() Expr {
	return Expr{Kind: KindEmpty}
}

// Universal returns the expression that matches everything. Parse returns
// Universal for an empty query.
func Universal() Expr {
	return Expr{Kind: KindUniversal}
}

// Matches evaluates the expression against a single item, represented only
// by a "does this item have this tag" predicate. It has no notion of a
// wider universe of items: Term consults has, And/Or/Not combine their
// operands with &&/||/!, Universal always returns true, and Empty always
// returns false.
func (e Expr) Matches(has func(tag string) bool) bool {
	switch e.Kind {
	case KindTerm:
		return has(e.Tag)
	case KindAnd:
		return e.Left.Matches(has) && e.Right.Matches(has)
	case KindOr:
		return e.Left.Matches(has) || e.Right.Matches(has)
	case KindNot:
		return !e.Operand.Matches(has)
	case KindUniversal:
		return true
	case KindEmpty:
		return false
	default:
		return false
	}
}

// ExtractTags returns the set of every tag named by a Term node anywhere in
// the expression.
func (e Expr) ExtractTags() map[string]struct{} {
	out := make(map[string]struct{})
	e.extractTagsInto(out)
	return out
}

func (e Expr) extractTagsInto(out map[string]struct{}) {
	switch e.Kind {
	case KindTerm:
		out[e.Tag] = struct{}{}
	case KindAnd, KindOr:
		e.Left.extractTagsInto(out)
		e.Right.extractTagsInto(out)
	case KindNot:
		e.Operand.extractTagsInto(out)
	}
}

// String renders the expression back into postfix path form, e.g.
// And(Term("foo"), Term("bar")) renders as "foo/bar/and". Parsing the
// result of String reproduces an equivalent expression for any tree Parse
// can itself produce (Empty is not reachable from Parse and has no
// distinct rendering from Universal, both of which render as "").
func (e Expr) String() string {
	switch e.Kind {
	case KindTerm:
		return e.Tag
	case KindAnd:
		return e.Left.String() + "/" + e.Right.String() + "/and"
	case KindOr:
		return e.Left.String() + "/" + e.Right.String() + "/or"
	case KindNot:
		return e.Operand.String() + "/not"
	case KindUniversal, KindEmpty:
		return ""
	default:
		return ""
	}
}

// ParseError reports a malformed postfix tag query: an operator that was
// evaluated with too few operands on the stack.
type ParseError struct {
	// Op is the lowercased operator that underflowed ("and", "or", "not").
	Op string
	// Need is the number of operands the operator requires.
	Need int
	// Got is the number of operands actually on the stack.
	Got int
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("tagquery: operator %q needs %d operand(s), got %d", e.Op, e.Need, e.Got)
}

// ErrOrDisabled is returned by Parse when the query uses the `or` operator
// while it has been disabled via the DisableOr option.
var ErrOrDisabled = errors.New("tagquery: `or` operator is disabled")

// Option configures Parse.
type Option func(*parseConfig)

type parseConfig struct {
	orEnabled bool
}

// DisableOr turns off the `or` operator: Parse returns ErrOrDisabled if the
// query contains it. `or` is enabled by default.
func DisableOr() Option {
	return func(c *parseConfig) { c.orEnabled = false }
}

// Parse parses a slash-separated postfix tag-query path into an Expr. See
// the package doc comment for the full grammar. Malformed input (operator
// arity underflow, or a disabled operator) is reported as an error rather
// than degrading to a silently-permissive or silently-empty expression.
func Parse(path string, opts ...Option) (Expr, error) {
	cfg := parseConfig{orEnabled: true}
	for _, opt := range opts {
		opt(&cfg)
	}

	var stack []Expr
	for _, seg := range strings.Split(path, "/") {
		if seg == "" {
			continue
		}

		switch strings.ToLower(seg) {
		case "and":
			if len(stack) < 2 {
				return Expr{}, &ParseError{Op: "and", Need: 2, Got: len(stack)}
			}
			left, right := stack[len(stack)-2], stack[len(stack)-1]
			stack = append(stack[:len(stack)-2], And(left, right))
		case "or":
			if !cfg.orEnabled {
				return Expr{}, ErrOrDisabled
			}
			if len(stack) < 2 {
				return Expr{}, &ParseError{Op: "or", Need: 2, Got: len(stack)}
			}
			left, right := stack[len(stack)-2], stack[len(stack)-1]
			stack = append(stack[:len(stack)-2], Or(left, right))
		case "not":
			if len(stack) < 1 {
				return Expr{}, &ParseError{Op: "not", Need: 1, Got: len(stack)}
			}
			operand := stack[len(stack)-1]
			stack = append(stack[:len(stack)-1], Not(operand))
		default:
			stack = append(stack, Term(seg))
		}
	}

	if len(stack) == 0 {
		return Universal(), nil
	}

	result := stack[0]
	for _, next := range stack[1:] {
		result = And(result, next)
	}
	return result, nil
}
