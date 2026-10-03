package rules

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// maxPatternLen bounds one pattern's source, and so its compiled size. Rule
// patterns are short argv shapes; anything near this is a mistake.
const maxPatternLen = 1024

// Pattern matches ONE argv element: an RE2 expression implicitly anchored as
// ^(?:src)$ and compiled once, by Parse. The (?:...) group is load-bearing:
// without it `a|b` would anchor as ^a|b$ and match any element ending in b.
//
// A pattern never sees the command line, only one element the shell-aware
// frontend already split and the matcher already classified (program, operand,
// option). That is why a regex here does not reintroduce the sudoers wildcard
// footgun: no pattern can span elements or reclassify a token. RE2 is linear
// in its input, so argv an agent wrote cannot make matching backtrack.
type Pattern struct {
	src string
	re  *regexp.Regexp
}

// UnmarshalYAML accepts a scalar only and records its source. Compilation is
// deferred to Parse's validation, where the owning rule and field are known
// and can be named in the error.
func (p *Pattern) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: a pattern must be a string", node.Line)
	}
	p.src = node.Value
	p.re = nil
	return nil
}

// compile anchors and compiles the source.
func (p *Pattern) compile() error {
	switch {
	case p.src == "":
		return ErrEmptyPattern
	case len(p.src) > maxPatternLen:
		return fmt.Errorf("%w: longer than %d bytes", ErrInvalidPattern, maxPatternLen)
	}
	re, err := regexp.Compile("^(?:" + p.src + ")$")
	if err != nil {
		return fmt.Errorf("%w %q: %w (RE2 has no lookaround or backreferences; express a negation with unless or an allow rule above the deny)", ErrInvalidPattern, p.src, err)
	}
	p.re = re
	return nil
}

// MatchString reports whether s matches the whole pattern. An uncompiled
// Pattern panics: Parse is the only constructor, and a silent false would be a
// deny rule that quietly stopped firing.
func (p Pattern) MatchString(s string) bool {
	if p.re == nil {
		panic(fmt.Sprintf("rules: pattern %q used before Parse compiled it", p.src))
	}
	return p.re.MatchString(s)
}

// String returns the pattern's source as written.
func (p Pattern) String() string { return p.src }

// leadsWithOption reports whether every string the pattern can match begins
// with a literal '-', i.e. the pattern names an option. The check reads RE2's
// literal prefix, so it is best-effort: `(-a|-b)` has none and is not caught.
// A lone "-" is an operand (stdin), not an option.
func (p Pattern) leadsWithOption() bool {
	prefix, complete := p.re.LiteralPrefix()
	if complete && prefix == "-" {
		return false
	}
	return strings.HasPrefix(prefix, "-")
}

// anyMatches reports whether some element of args matches p.
func (p Pattern) anyMatches(args []string) bool {
	for _, a := range args {
		if p.MatchString(a) {
			return true
		}
	}
	return false
}
