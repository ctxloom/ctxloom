package rules

import (
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Validation failures. Each reaches the caller wrapped in a *RuleError naming
// the rule and field; test with errors.Is, never by message text.
var (
	ErrMissingID             = errors.New("missing id")
	ErrDuplicateID           = errors.New("duplicate rule id")
	ErrInvalidAction         = errors.New("invalid action (want allow or deny)")
	ErrInvalidMode           = errors.New("invalid mode (want enable, confirm, or disable)")
	ErrNoConditions          = errors.New("match has no conditions")
	ErrInvalidShell          = errors.New("unknown shell")
	ErrInvalidGlob           = errors.New("invalid glob")
	ErrEmptyPattern          = errors.New("empty pattern")
	ErrInvalidPattern        = errors.New("invalid pattern")
	ErrOptionInCommand       = errors.New("option pattern in match.command; operands only — put options in args_all/args_any/unless")
	ErrArgsOnAllow           = errors.New("args_any/args_all on an allow rule: a position-blind positive predicate widens what the allow clears; allows are purely positional (match.command), narrowed only by unless")
	ErrInvalidAlignment      = errors.New("invalid align (want prefix, exact, or subsequence)")
	ErrAlignmentForAction    = errors.New("align does not suit the action: allow takes prefix or exact, deny takes subsequence")
	ErrAlignmentNeedsCommand = errors.New("align needs match.command")
	ErrDenyUnexplained       = errors.New(`a deny rule needs a message (or a suggest) — without one the agent is told "deny" with no reason and no alternative`)
	ErrConfirmNeedsWindow    = errors.New("mode confirm needs a window; set window_seconds or defaults.repeat_window_seconds")
	ErrDelayNotBelowWindow   = errors.New("delay_seconds must be less than the confirm window")
	ErrRemovedField          = errors.New("removed")
)

// Rule lists, as RuleError.List names them.
const (
	listCommand = "rules"
	listPath    = "path_rules"
)

// RuleError names the rule, and the field within it, a validation failure
// belongs to.
type RuleError struct {
	List   string // "rules" or "path_rules"
	Index  int    // position in List; identifies the rule when RuleID is ""
	RuleID string
	Field  string // e.g. "match.command[2]", "action"; "" for the rule as a whole
	Err    error
}

func (e *RuleError) Error() string {
	loc := fmt.Sprintf("%s[%d]", e.List, e.Index)
	if e.RuleID != "" {
		loc = fmt.Sprintf("rule %q", e.RuleID)
	}
	if e.Field != "" {
		loc += ": " + e.Field
	}
	return loc + ": " + e.Err.Error()
}

func (e *RuleError) Unwrap() error { return e.Err }

// removedMatchFields maps each match key the rule language no longer has to
// the line that replaces it. The load still fails — this is the fix line, not
// a compat path.
var removedMatchFields = map[string]string{
	"unless_arg_contains": "use unless with a regex shape, e.g. unless: ['.+@.+']",
	"min_operands":        "append one '.*' operand pattern to match.command per required operand",
}

// checkRemovedForms walks the raw document for rule shapes the language
// removed and returns a *RuleError naming the replacement. It runs before the
// strict decode, whose generic unknown-field error cannot say what to write
// instead. A document that is not even YAML is left to that decode to report.
func checkRemovedForms(data []byte) error {
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) == 0 {
		return nil
	}
	rules := mappingValue(doc.Content[0], listCommand)
	if rules == nil || rules.Kind != yaml.SequenceNode {
		return nil
	}
	for i, rule := range rules.Content {
		field, fix := removedFormIn(mappingValue(rule, "match"))
		if field == "" {
			continue
		}
		id := ""
		if n := mappingValue(rule, "id"); n != nil {
			id = n.Value
		}
		return &RuleError{List: listCommand, Index: i, RuleID: id, Field: "match." + field,
			Err: fmt.Errorf("%w: %s", ErrRemovedField, fix)}
	}
	return nil
}

// removedFormIn returns the first removed key in one command rule's match
// mapping and the line that replaces it, or "" when there is none.
func removedFormIn(match *yaml.Node) (field, fix string) {
	if match == nil || match.Kind != yaml.MappingNode {
		return "", ""
	}
	for k := 0; k+1 < len(match.Content); k += 2 {
		key := match.Content[k].Value
		if fix, ok := removedMatchFields[key]; ok {
			return key, fix
		}
		switch {
		case key == "path":
			return key, "a file-edit rule is its own kind; move this rule under path_rules"
		case key == "command" && match.Content[k+1].Kind == yaml.ScalarNode:
			return key, "the scalar form is gone; write match.command as a list, e.g. [go, test]"
		}
	}
	return "", ""
}

// mappingValue returns the value node for key in a mapping node, or nil.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for k := 0; k+1 < len(m.Content); k += 2 {
		if m.Content[k].Value == key {
			return m.Content[k+1]
		}
	}
	return nil
}
