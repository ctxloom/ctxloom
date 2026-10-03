package agent

import (
	"errors"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// Hosted is the instance half's REMAINING contract over an engine value:
// what agent.Backend still needs that the port (engine.Engine) does not
// carry. An engine.Engine the composition root ships also implements
// Hosted; the adapters that drive a Backend assert it on the registry's
// value (HostedIn). Everything declarative is the port's; this
// interface leaves with Backend.
type Hosted interface {
	// Backend constructs a fresh backend over the injected launcher — the
	// substrate that execs processes lives with the runner, not the engine.
	Backend(Launcher) Backend
	// NewConfig returns the engine's zero typed config; a labeled LLM
	// entry's body is decoded into it (DecodeConfig).
	NewConfig() BackendConfig
	// Declaration is the engine's named-form table: per surface kind, the
	// approach names a binding may select (`agent edit --surface`). The
	// names are validated config; nothing constructs a form from it.
	Declaration() Declaration
	// SettingsReader constructs the engine's settings reader, which reports
	// ctxloom's wiring in the engine's own settings files.
	SettingsReader(SettingsOptions) SettingsReader
	// HookGlobalScope is the project/global settings-path collision guard
	// `manage hooks install` applies; false for an engine whose global path
	// never collapses onto the project path.
	HookGlobalScope() (HookGlobalScope, bool)
}

// HostedIn resolves name in reg to its kind's instance-half contract by
// EXACT match on the registered name. No alias, case or prefix resolution:
// an engine has one spelling, and any other reaches the caller unresolved so
// it is refused rather than rounded to a real engine.
func HostedIn(reg engine.Registry, name string) (Hosted, bool) {
	e, ok := reg.Lookup(engine.Name(name))
	if !ok {
		return nil, false
	}
	h, ok := e.(Hosted)
	return h, ok
}

// HookGlobalScope resolves an engine's project-scoped config path (under a
// workDir) and its user-global path, for an engine whose two can collide.
type HookGlobalScope struct {
	Paths func(workDir string) (projectPath, globalPath string, err error)
	// Label is the human-facing name of the global scope, read into the
	// refusal/warning message.
	Label string
}

// Configurable is implemented by a Backend that accepts its own typed
// config: the argument is the engine's concrete BackendConfig (decoded into
// what Hosted.NewConfig returned), so no shared code ever type-switches on
// engine specifics. A config that is not the engine's own type is refused
// with ErrBackendConfigType rather than ignored.
type Configurable interface {
	Configure(cfg BackendConfig) error
}

// ErrBackendConfigType is a Configurable refusing a config that is not its
// engine's own typed config.
var ErrBackendConfigType = errors.New("backend config is not this engine's type")
