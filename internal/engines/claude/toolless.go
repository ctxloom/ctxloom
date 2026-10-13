package claude

import (
	"slices"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// ToolLess binds a session to claude's tool-less launch (engine.ToolLess;
// marauding-magnolia, decision #5): no built-in tool (--tools "") and no MCP
// server (--strict-mcp-config, with every presented --mcp-config dropped,
// so neither ctxloom's file, the repository's .mcp.json nor a user-level
// server loads). With nothing left to ask about, nobody is asked: the
// posture is claude's default under approver none (--permission-prompts
// none headless; dontAsk on every turn's settings), whatever the session's
// own posture document says. Instance's requiredness holds here too.
func (c Claude) ToolLess(s engine.Session) (engine.Instance, error) {
	if c.Context == nil {
		return nil, engine.ErrUnsupported{Engine: c.Name, Capability: "context"}
	}
	pos := posture{mode: modeDefault, approver: engine.ApproverNone, sandbox: engine.SandboxFull}
	return &instance{c: c, s: s, pos: pos, toolLess: true}, nil
}

var _ engine.ToolLess = Claude{}

// toolLessSources are a tool-less launch's MCP sources: --strict-mcp-config
// on args (once: an untrusted repository's launch already carries it), and
// the presented argv with every --mcp-config pair dropped, so the strict
// flag leaves no MCP server at all.
func toolLessSources(args, presented []string) (withStrict, surfaces []string) {
	if !slices.Contains(args, flagStrictMCPConfig) {
		args = append(args, flagStrictMCPConfig)
	}
	for i := 0; i < len(presented); i++ {
		if presented[i] == flagMCPConfig {
			i++
			continue
		}
		surfaces = append(surfaces, presented[i])
	}
	return args, surfaces
}
