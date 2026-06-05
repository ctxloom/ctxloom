package backends

import "github.com/ctxloom/ctxloom/internal/agent"

// The shared launch substrate — BaseBackend and the capability bases, the
// context file/chunk/rendezvous machinery, and the context-injection hook web —
// lives in internal/agent (the engine-agnostic core). These aliases keep the
// wiring layer (registry, codex/mock) and existing caller references working
// unchanged while the canonical definitions live in the core.
type (
	BaseBackend         = agent.BaseBackend
	BaseContextProvider = agent.BaseContextProvider
	BaseLifecycle       = agent.BaseLifecycle
	BaseMCPManager      = agent.BaseMCPManager
	ContextFileOption   = agent.ContextFileOption
)

var (
	NewBaseBackend         = agent.NewBaseBackend
	NewBaseContextProvider = agent.NewBaseContextProvider
	NewBaseLifecycle       = agent.NewBaseLifecycle
	NewBaseMCPManager      = agent.NewBaseMCPManager
	AssembleContext        = agent.AssembleContext
	GetPromptContent       = agent.GetPromptContent
	LoadPrompts            = agent.LoadPrompts

	WriteContextFile  = agent.WriteContextFile
	ReadContextFile   = agent.ReadContextFile
	WithContextFS     = agent.WithContextFS
	WithContextStderr = agent.WithContextStderr
	ChunkContext      = agent.ChunkContext
	AwaitTurn         = agent.AwaitTurn

	NewContextInjectionHook      = agent.NewContextInjectionHook
	NewContextInjectionChunkHook = agent.NewContextInjectionChunkHook
	NewContextInjectionHooks     = agent.NewContextInjectionHooks
	AppendManagedDynamicHooks    = agent.AppendManagedDynamicHooks
	AssembleManagedHooks         = agent.AssembleManagedHooks
)

const (
	SCMContextDir             = agent.SCMContextDir
	SCMContextSubdir          = agent.SCMContextSubdir
	SCMContextFileEnv         = agent.SCMContextFileEnv
	MaxRecommendedContextSize = agent.MaxRecommendedContextSize
	WarnContextSizeExceeded   = agent.WarnContextSizeExceeded
	WarnContextEffectiveness  = agent.WarnContextEffectiveness
	ContextChunkMaxChars      = agent.ContextChunkMaxChars
	ContextRendezvousTimeout  = agent.ContextRendezvousTimeout
	ContextInjectionTimeout   = agent.ContextInjectionTimeout
)
