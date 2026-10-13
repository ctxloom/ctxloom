package launch

import (
	"errors"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// A tool-less launch (Source.ToolLess) grants the model no tool and attaches
// no MCP server. It is NOT plan: it names no posture, so the plan/approver
// gates (resolveApprover, plansFirstNeedsHuman, requireApprovalRoute) are
// never asked about it — its policy is toolLessPolicy, not resolvePolicy's,
// and that policy plans nothing. What makes the run tool-less is the
// engine's own launch (engine.ToolLess), and the package's MCP servers are
// dropped here so no engine is handed one to attach.

var (
	// ErrToolLessUnsupported refuses a tool-less launch on an engine that
	// does not implement engine.ToolLess: nothing would make its run
	// tool-less.
	ErrToolLessUnsupported = errors.New("launch: the engine cannot run tool-less")
	// ErrToolLessPosture refuses a tool-less source that also names a
	// permission posture: a tool-less run has no posture to name.
	ErrToolLessPosture = errors.New("launch: a tool-less launch takes no permission posture")
)

// ToolLessLabel is the display name of a tool-less launch's posture, where a
// posture's label is shown (a session record, the run's start echo).
const ToolLessLabel = "tool-less"

// policyFor is the launch's permission policy: a tool-less launch's own
// (toolLessPolicy), else THE permission resolution (resolvePolicy).
func policyFor(rep report.Reporter, src Source, d permissionDecls, eng engine.Engine, runtime RuntimeAxis) (engine.PermissionPolicy, error) {
	if src.ToolLess {
		return toolLessPolicy(src, eng)
	}
	return resolvePolicy(rep, src, d, eng, runtime)
}

// toolLessPolicy is a tool-less launch's policy: no posture document (the
// engine's own tool-less launch replaces it), nobody to approve anything —
// there is no tool to ask about — and the engine's default sandbox, which
// bounds commands a tool-less run never issues. It refuses an engine that
// does not implement the marker, and a source that also names a posture.
func toolLessPolicy(src Source, eng engine.Engine) (engine.PermissionPolicy, error) {
	name := eng.Root().Name
	if _, ok := eng.(engine.ToolLess); !ok {
		return engine.PermissionPolicy{}, toolLessUnsupported(name)
	}
	if src.Permission != "" {
		return engine.PermissionPolicy{}, fmt.Errorf("%w (it asked for %q)", ErrToolLessPosture, src.Permission)
	}
	sandbox := engine.SandboxFull
	if model, ok := eng.Permissions().Get(); ok {
		sandbox = model.DefaultSandbox()
	}
	return engine.PermissionPolicy{
		Posture:         engine.Posture{Engine: name, Label: ToolLessLabel},
		Approver:        engine.ApproverNone,
		ApprovalTimeout: engine.DefaultApprovalTimeout,
		Sandbox:         sandbox,
	}, nil
}

// toolLessPackage is pkg as a launch carries it: a tool-less launch's with
// no MCP server, so no engine is handed one to attach.
func toolLessPackage(pkg composite.Package, src Source) composite.Package {
	if src.ToolLess {
		pkg.MCP = nil
	}
	return pkg
}

func toolLessUnsupported(name engine.Name) error {
	return fmt.Errorf("%w: engine %s does not implement engine.ToolLess, so nothing would keep its run tool-less", ErrToolLessUnsupported, name)
}

// Instance binds the launch's session to eng: a tool-less launch through the
// engine's own tool-less launch (engine.ToolLess), refused on an engine that
// does not implement it; any other through Instance.
func (l Launch) Instance(eng engine.Engine) (engine.Instance, error) {
	if !l.ToolLess {
		return eng.Instance(l.Session())
	}
	tl, ok := eng.(engine.ToolLess)
	if !ok {
		return nil, toolLessUnsupported(eng.Root().Name)
	}
	return tl.ToolLess(l.Session())
}
