package backends

import "github.com/ctxloom/shared/agent"

// The shared launch substrate lives in internal/agent (the engine-agnostic
// core). These aliases cover only what the wiring layer (codex/mock, the
// command-file dispatcher) and external callers (cmd, operations) still
// reference by the backends path — the per-agent capability bases moved out
// with the claude/gemini packages and are no longer aliased here.
type (
	BaseBackend       = agent.BaseBackend
	ContextFileOption = agent.ContextFileOption
	CommandFileOption = agent.CommandFileOption
)

var (
	NewBaseBackend   = agent.NewBaseBackend
	AssembleContext  = agent.AssembleContext
	GetPromptContent = agent.GetPromptContent

	WriteContextFile  = agent.WriteContextFile
	ReadContextFile   = agent.ReadContextFile
	WithContextFS     = agent.WithContextFS
	WithContextStderr = agent.WithContextStderr
	ChunkContext      = agent.ChunkContext
	AwaitTurn         = agent.AwaitTurn

	NewContextInjectionHook  = agent.NewContextInjectionHook
	NewContextInjectionHooks = agent.NewContextInjectionHooks
	// AssembleManagedHooks is now a real backends-package function (managed.go),
	// not an agent alias — the config-coupled hook assembly lives host-side.

	WithCommandFS               = agent.WithCommandFS
	SetExecutablePathForTesting = agent.SetExecutablePathForTesting
	WarnOnCtxloomPathSkew       = agent.WarnOnCtxloomPathSkew
)

const (
	SCMContextSubdir          = agent.SCMContextSubdir
	MaxRecommendedContextSize = agent.MaxRecommendedContextSize
	WarnContextEffectiveness  = agent.WarnContextEffectiveness
	ContextInjectionTimeout   = agent.ContextInjectionTimeout
)
