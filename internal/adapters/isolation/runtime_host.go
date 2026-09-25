package isolation

import (
	"context"
)

// Host is the non-container runtime: the runner runs as a bare host
// subprocess (the None and Worktree policies, through RunnerCommand). It
// satisfies Runtime so runtime selection is uniform, but it launches nothing
// itself, so its container-CLI methods are noops.
type Host struct{}

// Name identifies the runtime.
func (Host) Name() string { return "host" }

// Binary is empty — Host execs the runner directly, not via a container CLI.
func (Host) Binary() string { return "" }

// Available is always true — the host can always run a subprocess (None never
// fails; it is the fault-tolerant floor).
func (Host) Available() bool { return true }

// RunArgs is a noop — Host does not launch a container.
func (Host) RunArgs(RunSpec) []string { return nil }

// RemoveArgs is a noop — Host has nothing to tear down.
func (Host) RemoveArgs(string) []string { return nil }

// Expose renders the identity bind mount, same as the OCI runtimes — the host
// path IS the exposed path (no container namespace to remap into).
func (Host) Expose(host, target string, readOnly bool) Mount {
	return Mount{Host: host, Container: target, ReadOnly: readOnly}
}

// ExposeMapped is the identity mount for Host specifically — Host has no
// container namespace to remap into, so this is unconditionally
// Expose(hostPath, hostPath, readOnly) (matching Host.mapper() below, always
// identityMapper).
func (Host) ExposeMapped(hostPath string, readOnly bool) Mount {
	return Mount{Host: hostPath, Container: hostPath, ReadOnly: readOnly}
}

// mapper is always identity — Host launches no container, so there is no
// host↔container path translation to perform.
func (Host) mapper() pathMapper { return identityMapper{} }

// Enumerate is a noop — Host launches no containers, so there is never
// anything to list.
func (Host) Enumerate(context.Context, string) ([]ContainerInfo, error) { return nil, nil }

// reachRoute is empty: a host runner dials the loopback URL as minted.
func (Host) reachRoute(context.Context) (hostRoute, error) { return hostRoute{}, nil }

// The container-CLI grammar is empty: callers gate on Binary() == "" first.
func (Host) inspectRunningArgs(string) []string        { return nil }
func (Host) imageInspectArgs(string, string) []string  { return nil }
func (Host) buildArgs(string, string, string, buildFlags) []string { return nil }
func (Host) daemonNameTemplate() string                { return "" }
func (Host) removeOutcome([]byte, error) removeOutcome { return removeAlreadyGone }
func (Host) passesPUID() bool                          { return false }
func (Host) gatewayInspectArgs() []string              { return nil }
