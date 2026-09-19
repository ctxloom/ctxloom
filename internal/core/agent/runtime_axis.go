package agent

import "github.com/ctxloom/ctxloom/internal/core/launch"

// RuntimeAxis is launch.RuntimeAxis under this package's established name:
// where an agent's engine process executes. The vocabulary is declared once,
// in core/launch; the alias and the re-exports below carry this package's
// names forward for its callers, so nothing here can drift from it.
type RuntimeAxis = launch.RuntimeAxis

const (
	RuntimeHost              = launch.RuntimeHost
	RuntimeContainerRootless = launch.RuntimeRootless
	RuntimeContainerRootful  = launch.RuntimeRootful
)

// IsContainerRuntimeAxis is launch.IsContainerRuntimeAxis.
func IsContainerRuntimeAxis(v RuntimeAxis) bool { return launch.IsContainerRuntimeAxis(v) }

// IsContainerRuntime is launch.IsContainerRuntime.
func IsContainerRuntime(runtime string) bool { return launch.IsContainerRuntime(runtime) }

// RuntimeNames is launch.RuntimeNames.
func RuntimeNames() []string { return launch.RuntimeNames() }

// ParseRuntimeAxis is launch.ParseRuntimeAxis.
func ParseRuntimeAxis(s string) (RuntimeAxis, error) { return launch.ParseRuntimeAxis(s) }
