package opencode

import (
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// denyAll is OPENCODE_PERMISSION denying every tool: one rule, the
// wildcard permission at the wildcard pattern. opencode (1.18.35) offers the
// model no tool whose last matching rule is a `*`-pattern deny
// (Permission.disabled), MCP and plugin tools included, and `run` adds only
// deny rules of its own after it — so the request carries no tool at all.
const denyAll = `{"*":"deny"}`

// ToolLess binds a session to opencode's tool-less launch (engine.ToolLess;
// marauding-magnolia, decision #5): OPENCODE_PERMISSION denying every tool
// on the Exec and on every turn, never --auto, and the project's own config
// off even on a trusted repository, whatever the session's posture says.
// The launch's package carries no MCP server (launch.Resolve drops them),
// and the generated config (configContent) names none, so none is
// delivered. Instance's refusals hold here too.
func (o Opencode) ToolLess(s engine.Session) (engine.Instance, error) {
	if o.Context == nil {
		return nil, engine.ErrUnsupported{Engine: o.Name, Capability: "context"}
	}
	cfg, err := decodeConfig(s.Label.Body)
	if err != nil {
		return nil, err
	}
	return &instance{o: o, s: s, cfg: cfg, pos: posture{mode: modeBuild}, toolLess: true}, nil
}

var _ engine.ToolLess = Opencode{}

// permission is the OPENCODE_PERMISSION a process at mode ("" = the
// session's) runs with: the deny-all for a tool-less launch, whatever the
// mode; else the posture's document.
func (i *instance) permission(mode string) (string, error) {
	if i.toolLess {
		return denyAll, nil
	}
	return i.pos.document(mode)
}
