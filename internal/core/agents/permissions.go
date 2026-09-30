package agents

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// NeutralPermissions are the permission fields every level declares the
// same way, whatever the engine: who answers what the rules leave open,
// how long they wait, and the sandbox bounding the engine's own commands.
// Values are the raw spellings; the launch resolver parses and refuses
// them. As a document of its own it is the PROJECT's block, which knows no
// engine: an engine's mode or rules there are refused, naming where they go.
type NeutralPermissions struct {
	Approver        string `yaml:"approver,omitempty" json:"approver,omitempty"`
	ApprovalTimeout string `yaml:"approval_timeout,omitempty" json:"approval_timeout,omitempty"`
	Sandbox         string `yaml:"sandbox,omitempty" json:"sandbox,omitempty"`
	Network         *bool  `yaml:"network,omitempty" json:"network,omitempty"`
}

// NeutralKeys are the neutral fields' keys.
var NeutralKeys = []string{"approver", "approval_timeout", "sandbox", "network"}

// IsZero reports that nothing neutral is declared.
func (n NeutralPermissions) IsZero() bool {
	return n.Approver == "" && n.ApprovalTimeout == "" && n.Sandbox == "" && n.Network == nil
}

// UnmarshalYAML decodes the project's block strictly: an engine's key is
// refused naming where it is declared instead.
func (n *NeutralPermissions) UnmarshalYAML(node *yaml.Node) error {
	if err := mappingNode(node, "permissions: {sandbox: workspace-write}"); err != nil {
		return err
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if key := node.Content[i].Value; !slices.Contains(NeutralKeys, key) {
			return fmt.Errorf("line %d: the project's permissions take only %s; %q is an engine's key — declare it at agents.<name>.permissions.<engine>.%s or llm.configs.<label>.permissions.%s",
				node.Content[i].Line, strings.Join(NeutralKeys, ", "), key, key, key)
		}
	}
	return n.decode(node)
}

// decode reads the neutral keys of a mapping, ignoring the rest.
func (n *NeutralPermissions) decode(node *yaml.Node) error {
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, val := node.Content[i].Value, node.Content[i+1]
		var err error
		switch key {
		case "approver":
			err = val.Decode(&n.Approver)
		case "approval_timeout":
			err = val.Decode(&n.ApprovalTimeout)
		case "sandbox":
			err = val.Decode(&n.Sandbox)
		case "network":
			var b bool
			if err = val.Decode(&b); err == nil {
				n.Network = &b
			}
		}
		if err != nil {
			return fmt.Errorf("line %d: permissions %s: %w", val.Line, key, err)
		}
	}
	return nil
}

// fields renders the declared neutral fields, in key order.
func (n NeutralPermissions) fields() map[string]any {
	out := map[string]any{}
	for k, v := range map[string]string{"approver": n.Approver, "approval_timeout": n.ApprovalTimeout, "sandbox": n.Sandbox} {
		if v != "" {
			out[k] = v
		}
	}
	if n.Network != nil {
		out["network"] = *n.Network
	}
	return out
}

// mappingNode refuses a node that is not a mapping, naming the spelling
// that replaces a scalar.
func mappingNode(node *yaml.Node, want string) error {
	if node.Kind == yaml.MappingNode {
		return nil
	}
	return fmt.Errorf("line %d: permissions is a mapping, not %q — write `%s`", node.Line, node.Value, strings.ReplaceAll(want, "<value>", node.Value))
}

// Permissions is an agent binding's block: the neutral fields, and one
// block per engine keyed by the engine's name. A binding's engine is known
// only when it resolves (run --llm, the profiles' llm), and one binding may
// carry blocks for several engines; each engine validates its own block.
type Permissions struct {
	NeutralPermissions
	Engines map[string]map[string]any
}

// IsZero reports a block that declares nothing; yaml omits it.
func (p Permissions) IsZero() bool { return p.NeutralPermissions.IsZero() && len(p.Engines) == 0 }

// UnmarshalYAML decodes the neutral keys, and every other key as an
// engine's block — which must be a mapping; a mode or a rule written
// beside the neutral keys is refused, naming the engine block it belongs
// in.
func (p *Permissions) UnmarshalYAML(node *yaml.Node) error {
	if err := mappingNode(node, "permissions: {<engine>: {mode: <value>}}"); err != nil {
		return err
	}
	*p = Permissions{}
	if err := p.NeutralPermissions.decode(node); err != nil {
		return err
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, val := node.Content[i].Value, node.Content[i+1]
		if slices.Contains(NeutralKeys, key) {
			continue
		}
		if val.Kind != yaml.MappingNode {
			return fmt.Errorf("line %d: %q is not a neutral key (%s): an engine's keys go in its own block — permissions: {<engine>: {%s: ...}}", node.Content[i].Line, key, strings.Join(NeutralKeys, ", "), key)
		}
		var block map[string]any
		if err := val.Decode(&block); err != nil {
			return fmt.Errorf("line %d: permissions.%s: %w", val.Line, key, err)
		}
		if p.Engines == nil {
			p.Engines = map[string]map[string]any{}
		}
		p.Engines[key] = block
	}
	return nil
}

// MarshalYAML writes the neutral keys and the engine blocks side by side.
func (p Permissions) MarshalYAML() (any, error) {
	out := p.NeutralPermissions.fields()
	for name, block := range p.Engines {
		out[name] = block
	}
	return out, nil
}

// MarshalJSON writes the config's own shape (MarshalYAML's).
func (p Permissions) MarshalJSON() ([]byte, error) {
	doc, _ := p.MarshalYAML()
	return json.Marshal(doc)
}

// Clone copies the block, engine blocks included.
func (p Permissions) Clone() Permissions {
	p.NeutralPermissions = p.NeutralPermissions.clone()
	if p.Engines != nil {
		engines := make(map[string]map[string]any, len(p.Engines))
		for k, v := range p.Engines {
			engines[k] = cloneDoc(v)
		}
		p.Engines = engines
	}
	return p
}

// Clone copies the block so a copy's edits never reach the original.
func (n NeutralPermissions) Clone() NeutralPermissions { return n.clone() }

func (n NeutralPermissions) clone() NeutralPermissions {
	if n.Network != nil {
		b := *n.Network
		n.Network = &b
	}
	return n
}

// String renders the block on one line for a listing: the neutral fields,
// then each engine's block, sorted.
func (p Permissions) String() string {
	parts := neutralParts(p.NeutralPermissions)
	for _, name := range slices.Sorted(maps.Keys(p.Engines)) {
		parts = append(parts, name+"={"+docString(p.Engines[name])+"}")
	}
	return strings.Join(parts, ", ")
}

// LabelPermissions is an llm label's block: the label's type is its engine,
// so the engine's own keys sit flat beside the neutral fields. The label's
// engine validates Engine.
type LabelPermissions struct {
	NeutralPermissions
	Engine map[string]any
}

// IsZero reports a block that declares nothing.
func (l LabelPermissions) IsZero() bool { return l.NeutralPermissions.IsZero() && len(l.Engine) == 0 }

// UnmarshalYAML splits the neutral keys from the engine's.
func (l *LabelPermissions) UnmarshalYAML(node *yaml.Node) error {
	if err := mappingNode(node, "permissions: {mode: <value>}"); err != nil {
		return err
	}
	*l = LabelPermissions{}
	if err := l.NeutralPermissions.decode(node); err != nil {
		return err
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, val := node.Content[i].Value, node.Content[i+1]
		if slices.Contains(NeutralKeys, key) {
			continue
		}
		var v any
		if err := val.Decode(&v); err != nil {
			return fmt.Errorf("line %d: permissions.%s: %w", val.Line, key, err)
		}
		if l.Engine == nil {
			l.Engine = map[string]any{}
		}
		l.Engine[key] = v
	}
	return nil
}

// MarshalYAML writes the neutral and engine keys flat.
func (l LabelPermissions) MarshalYAML() (any, error) {
	out := l.NeutralPermissions.fields()
	maps.Copy(out, l.Engine)
	return out, nil
}

// MarshalJSON writes the config's own shape (MarshalYAML's).
func (l LabelPermissions) MarshalJSON() ([]byte, error) {
	doc, _ := l.MarshalYAML()
	return json.Marshal(doc)
}

// Clone copies the block.
func (l LabelPermissions) Clone() LabelPermissions {
	l.NeutralPermissions = l.NeutralPermissions.clone()
	l.Engine = cloneDoc(l.Engine)
	return l
}

// String renders the block on one line.
func (l LabelPermissions) String() string {
	parts := neutralParts(l.NeutralPermissions)
	if len(l.Engine) > 0 {
		parts = append(parts, docString(l.Engine))
	}
	return strings.Join(parts, ", ")
}

func neutralParts(n NeutralPermissions) []string {
	var parts []string
	for _, kv := range [][2]string{{"approver", n.Approver}, {"approval_timeout", n.ApprovalTimeout}, {"sandbox", n.Sandbox}} {
		if kv[1] != "" {
			parts = append(parts, kv[0]+"="+kv[1])
		}
	}
	if n.Network != nil {
		parts = append(parts, "network="+strconv.FormatBool(*n.Network))
	}
	return parts
}

// docString renders an engine document key=value, keys sorted.
func docString(doc map[string]any) string {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(doc)) {
		parts = append(parts, k+"="+valueString(doc[k]))
	}
	return strings.Join(parts, ", ")
}

func valueString(v any) string {
	switch t := v.(type) {
	case []any:
		items := make([]string, len(t))
		for i, e := range t {
			items[i] = valueString(e)
		}
		return "[" + strings.Join(items, ", ") + "]"
	case []string:
		return "[" + strings.Join(t, ", ") + "]"
	default:
		return fmt.Sprint(t)
	}
}

// cloneDoc deep-copies a YAML/JSON-shaped document.
func cloneDoc(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneDoc(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = cloneValue(e)
		}
		return out
	case []string:
		return slices.Clone(t)
	default:
		return v
	}
}
