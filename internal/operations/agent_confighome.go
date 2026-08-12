package operations

import (
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/agents"
)

// ResolveConfigHome validates and normalizes a binding's DECLARED
// agents.Agent.ConfigHome into its always-non-empty EFFECTIVE value: the
// declared value when it is one of agents.ConfigHomeNames, else
// agents.ConfigHomeHost — undeclared (empty) and unrecognized both default to
// the real host home, so the controlled in-tree home stays strictly opt-in.
//
// One function, two callers, deliberately — the SAME shape
// ResolveAgentSurfaces already has for the same reason. The agent WRITE path
// (SetAgent) calls it so a typo is refused by the command that set it and
// nothing is persisted; the RESOLVE path (resolveAgentBinding) calls it so a
// hand-edited config.yaml degrades to the safe default (host) with a warning
// rather than blocking the launch — unlike the write path, an unresolvable
// value here is not fatal, because by the time a run reaches this call the
// binding already exists and refusing to launch over it would be a
// regression, not a safety net.
func ResolveConfigHome(declared string) (string, error) {
	switch declared {
	case "":
		return agents.ConfigHomeHost, nil
	case agents.ConfigHomeProject, agents.ConfigHomeHost:
		return declared, nil
	default:
		return agents.ConfigHomeHost, fmt.Errorf("config_home %q: unknown value (known: %s)",
			declared, strings.Join(agents.ConfigHomeNames(), ", "))
	}
}
