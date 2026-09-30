package agents

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Permissions is a `permissions:` block as written: the launch-time
// permission posture and the rules a binding (or an llm label, or the
// project) declares. Every value is the raw spelling — a hand-edited
// config.yaml can hold anything — and the launch resolver
// (launch.resolvePolicy) is where each is parsed and refused. An empty
// field declares nothing, and the next rung answers for it.
type Permissions struct {
	// Mode is the starting posture (engine.PermissionModeNames).
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`
	// AfterPlan makes a plan posture plan-first: the posture an approved
	// plan continues at (default | acceptEdits).
	AfterPlan string `yaml:"after_plan,omitempty" json:"after_plan,omitempty"`
	// Allow, Deny and Ask are engine-native permission rules.
	Allow []string `yaml:"allow,omitempty" json:"allow,omitempty"`
	Deny  []string `yaml:"deny,omitempty" json:"deny,omitempty"`
	Ask   []string `yaml:"ask,omitempty" json:"ask,omitempty"`
	// Approver is who answers what the rules leave open
	// (engine.ApproverNames).
	Approver string `yaml:"approver,omitempty" json:"approver,omitempty"`
	// ApprovalTimeout is how long a request waits for the approver, as a
	// duration ("20m").
	ApprovalTimeout string `yaml:"approval_timeout,omitempty" json:"approval_timeout,omitempty"`
}

// PermissionsKeys are the keys a permissions block accepts, in the order
// the refusal names them.
var PermissionsKeys = []string{"mode", "after_plan", "allow", "deny", "ask", "approver", "approval_timeout"}

// IsZero reports a block that declares nothing; yaml omits it.
func (p Permissions) IsZero() bool {
	return p.Mode == "" && p.AfterPlan == "" && p.Approver == "" && p.ApprovalTimeout == "" &&
		len(p.Allow)+len(p.Deny)+len(p.Ask) == 0
}

// UnmarshalYAML decodes a block STRICTLY: it is a privilege grant, and the
// config's lenient decode would drop a misspelled key in silence — a `dney:`
// that vanished leaves the denial undeclared, reported as success. A
// scalar is refused with the spelling that replaces it.
func (p *Permissions) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: permissions is a mapping, not %q — write `permissions: {mode: %s}` (keys: %s)",
			node.Line, node.Value, node.Value, strings.Join(PermissionsKeys, ", "))
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if key := node.Content[i].Value; !slices.Contains(PermissionsKeys, key) {
			return fmt.Errorf("line %d: permissions has no key %q (known: %s)", node.Content[i].Line, key, strings.Join(PermissionsKeys, ", "))
		}
	}
	type plain Permissions // the same fields, without this method
	return node.Decode((*plain)(p))
}

// Clone copies the block, rules included, so a copy's edits never reach
// the original.
func (p Permissions) Clone() Permissions {
	p.Allow, p.Deny, p.Ask = slices.Clone(p.Allow), slices.Clone(p.Deny), slices.Clone(p.Ask)
	return p
}

// String renders the block on one line for a listing: the mode as written,
// then each other declared field as key=value; "" for an empty block.
func (p Permissions) String() string {
	var parts []string
	if p.Mode != "" {
		parts = append(parts, p.Mode)
	}
	add := func(key, v string) {
		if v != "" {
			parts = append(parts, key+"="+v)
		}
	}
	list := func(rules []string) string {
		if len(rules) == 0 {
			return ""
		}
		return "[" + strings.Join(rules, ", ") + "]"
	}
	add("after_plan", p.AfterPlan)
	add("allow", list(p.Allow))
	add("deny", list(p.Deny))
	add("ask", list(p.Ask))
	add("approver", p.Approver)
	add("approval_timeout", p.ApprovalTimeout)
	return strings.Join(parts, ", ")
}
