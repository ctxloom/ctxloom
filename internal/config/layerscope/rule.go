package layerscope

import (
	"strings"

	kmaps "github.com/knadh/koanf/maps"
)

// ruleDelim is the character Policy.Check joins/splits flattened path
// segments on internally. It is never "." — a real config key can legitimately
// contain one (an LLM config label like "gpt-4.1"), so joining on it would
// make maps.Flatten collide two DIFFERENT paths onto the same flat string
// (confload's own delim doc, internal/shared/confload/confload.go, is the
// precedent for this exact hazard). This package never round-trips the
// joined string back into segments — maps.Flatten's own keyMap return already
// hands back the real segment slice — so ruleDelim only has to be a byte no
// real key contains, which "\x1f" (ASCII Unit Separator) guarantees.
const ruleDelim = "\x1f"

// wildcard is the one Rule.Path segment that matches any single path segment
// — an agent label, an LLM config label, an MCP server name, ... Exactly one
// path segment, never more: "agents.*" cannot also match "agents.foo.bar" by
// itself (a separate, longer rule is needed for that, and longest-match
// picks it).
const wildcard = "*"

// Rule binds one dotted config path to the Scope its value is a fact about.
// Path segments are joined with ".", and a "*" segment matches exactly one
// path segment at that position — so a wildcard map level (agents.<name>,
// llm.configs.<label>, mcp.servers.<name>) is addressable per LEAF, not just
// as a whole group.
type Rule struct {
	Path  string
	Scope Scope
	// Note is the key-specific reason, appended after Scope.Why() in a
	// Violation's Message.
	Note string
}

// segments splits r.Path on "." into the literal/wildcard segments Lookup
// matches against.
func (r Rule) segments() []string {
	if r.Path == "" {
		return nil
	}
	return strings.Split(r.Path, ".")
}

// Policy is a rule set resolved by LONGEST MATCH: a group rule ("mcp.servers.*",
// ScopeShared — the SET of servers is team policy) is narrowed by a leaf rule
// ("mcp.servers.*.command", ScopeMachine — a binary path on this box) without
// the leaf rule having to restate the group's other fields.
type Policy []Rule

// Lookup returns the most specific Rule whose Path is a prefix of path (a
// literal segment must match exactly; "*" matches any single segment), or
// false if no rule matches at all. "Most specific" means the rule with the
// MOST segments among every matching prefix — a rule shorter than path covers
// path's remainder as a GROUP, unless a longer rule also matches and wins.
func (p Policy) Lookup(path []string) (Rule, bool) {
	best := -1
	var bestRule Rule
	found := false
	for _, rule := range p {
		seg := rule.segments()
		if len(seg) == 0 || len(seg) > len(path) {
			continue
		}
		if !prefixMatches(seg, path) {
			continue
		}
		if len(seg) > best {
			best = len(seg)
			bestRule = rule
			found = true
		}
	}
	return bestRule, found
}

// prefixMatches reports whether pattern (literal segments and "*" wildcards)
// matches the first len(pattern) segments of path.
func prefixMatches(pattern, path []string) bool {
	for i, seg := range pattern {
		if seg == wildcard {
			continue
		}
		if !strings.EqualFold(seg, path[i]) {
			return false
		}
	}
	return true
}

// Violation is one key at a layer that cannot carry it.
type Violation struct {
	Path  []string
	Layer Layer
	Rule  Rule
}

// Check walks one layer's DECODED values and reports every key that layer may
// not set, using koanf/maps.Flatten to obtain each value's dotted path — never
// a bespoke recursive map walker. It never mutates values; the caller (which
// already knows how to remove a key from its own decoded map — see
// internal/config's use of koanf/maps.Delete) drops what this reports.
//
// Flatten treats a slice/array value as a single leaf (it only recurses into
// map[string]any), which is exactly the granularity Rule.Path addresses: a
// whole list (agents.*.escalation, isolation_engines, ...) is one value at one
// path, never inspected element-by-element.
func (p Policy) Check(l Layer, values map[string]any) []Violation {
	if len(values) == 0 {
		return nil
	}
	_, keyMap := kmaps.Flatten(values, nil, ruleDelim)
	var violations []Violation
	for _, path := range keyMap {
		rule, ok := p.Lookup(path)
		if !ok {
			continue
		}
		if rule.Scope.Allows(l) {
			continue
		}
		violations = append(violations, Violation{
			Path:  append([]string{}, path...),
			Layer: l,
			Rule:  rule,
		})
	}
	return violations
}

// Message states the key, the layer it was found at, why that layer cannot
// carry it, and where it belongs instead.
func (v Violation) Message(appPath, homeAppPath string) string {
	var b strings.Builder
	b.WriteString(v.Layer.String())
	b.WriteString(" config carries \"")
	b.WriteString(strings.Join(v.Path, "."))
	b.WriteString("\", which it may not set: ")
	b.WriteString(v.Rule.Scope.Why())
	if v.Rule.Note != "" {
		b.WriteString(" (")
		b.WriteString(v.Rule.Note)
		b.WriteString(")")
	}
	b.WriteString(". Dropped rather than applied — a setting that looks applied and is not is the worse outcome. ")
	b.WriteString(v.FixIt(appPath, homeAppPath))
	return b.String()
}

// FixIt is the edit that clears the violation: remove it from the offending
// layer's file (env/flag have none to remove from — those are the two layers
// this key would need to arrive at a DIFFERENT allowed layer instead), and
// name at least one allowed layer's file to move it to.
func (v Violation) FixIt(appPath, homeAppPath string) string {
	var b strings.Builder
	if file := v.Layer.File(appPath, homeAppPath); file != "" {
		b.WriteString("Remove it from ")
		b.WriteString(file)
		b.WriteString("; ")
	} else {
		b.WriteString("Unset it (")
		b.WriteString(v.Layer.String())
		b.WriteString(" cannot carry it); ")
	}
	if dest := v.firstAllowedFile(appPath, homeAppPath); dest != "" {
		b.WriteString("set it in ")
		b.WriteString(dest)
		b.WriteString(" instead.")
		return b.String()
	}
	b.WriteString("it may only be set via ")
	b.WriteString(v.firstAllowedNonFileLayer())
	b.WriteString(".")
	return b.String()
}

// firstAllowedFile names the first FILE layer (home, then project) v's Scope
// permits, or "" if neither may carry it.
func (v Violation) firstAllowedFile(appPath, homeAppPath string) string {
	for _, l := range []Layer{LayerHome, LayerProject} {
		if v.Rule.Scope.Allows(l) {
			if file := l.File(appPath, homeAppPath); file != "" {
				return file
			}
		}
	}
	return ""
}

// firstAllowedNonFileLayer names the first override layer (env, then flag)
// v's Scope permits, for a Scope with no allowed file layer at all (e.g.
// ScopeInvocation).
func (v Violation) firstAllowedNonFileLayer() string {
	for _, l := range []Layer{LayerEnv, LayerFlag} {
		if v.Rule.Scope.Allows(l) {
			return l.String()
		}
	}
	return "no layer (see Scope.Why)"
}
