// Package launch is the resolved launch's home: it owns the isolation-axis
// vocabulary (value types the cells adapter implements against) and the
// Source every way of asking for a launch is expressed as. It must never
// know cobra, gRPC, docker, or files. Today it holds the vocabulary and the
// Source; the one constructor of the resolved launch arrives with the launch
// unification.
package launch

import (
	"errors"
	"fmt"
	"strings"
)

// The isolation request is two INDEPENDENT enum axes, never bound together:
// WHERE the agent's files live (workspace) and WHERE its engine process runs
// (runtime). Degradation respects the axes: a runtime-axis failure (no
// container runtime, image unbuildable) drops ONLY the runtime dimension —
// the workspace dimension is preserved, never silently added or removed. An
// ownership mismatch is fatal, never a substitution; --degraded falls back to
// the HOST only.

// WorkspaceAxis says where the agent's working directory lives.
type WorkspaceAxis string

const (
	// WorkspaceNone is the shared live project directory (the default; also
	// the meaning of an empty value after defaulting).
	WorkspaceNone WorkspaceAxis = "none"
	// WorkspaceWorktree gives the agent its own git worktree.
	WorkspaceWorktree WorkspaceAxis = "worktree"
)

// RuntimeAxis says where an agent's engine process executes: directly on the
// host, or inside a container under one of two ownership modes.
//
// There is deliberately no "any container" value. Rootless and rootful differ
// in UID mapping, so a workload can genuinely require one, and a config that
// cannot say which one silently gets whichever the host happens to offer —
// see IsContainerRuntimeAxis's doc.
type RuntimeAxis string

const (
	// RuntimeHost runs the engine directly on the host (the default; also
	// the meaning of an empty value after defaulting/parsing).
	RuntimeHost RuntimeAxis = "host"
	// RuntimeRootless runs the engine inside a container on a runtime that
	// maps the container's root to the INVOKING HOST USER.
	RuntimeRootless RuntimeAxis = "container-rootless"
	// RuntimeRootful runs the engine inside a container on a runtime whose
	// container-root is REAL root, with the image entrypoint remapping to the
	// launching uid/gid.
	RuntimeRootful RuntimeAxis = "container-rootful"
)

// Axes is an isolation request: fully defaulted as Launch.Axes, as declared
// (empty where nothing asked) as Launch.Declared. The two axes are declared
// at DIFFERENT levels and meet only here: the runtime axis is an AGENT trait
// (`runtime:` on the binding — a cost/environment call, like engine), while
// the workspace axis is an ORCHESTRATION trait (the invocation decides —
// run/acp `--workspace`, an agent_run spawn's workspace field, the project
// default — because needing a private cwd is a property of how you fan, not
// of who the agent is).
type Axes struct {
	Workspace WorkspaceAxis
	Runtime   RuntimeAxis
}

// WantsWorktree reports the workspace axis asks for a worktree; anything else
// (empty, "none", unknown) is the shared project dir.
func (a Axes) WantsWorktree() bool { return a.Workspace == WorkspaceWorktree }

// WantsContainer reports the runtime axis asks for a container in EITHER
// ownership mode; anything else (empty, "host", unknown) is the host.
func (a Axes) WantsContainer() bool { return IsContainerRuntimeAxis(a.Runtime) }

// Zero reports no isolation on either axis (shared project dir, host).
// Callers use it to skip isolation-only work on the default path.
func (a Axes) Zero() bool { return !a.WantsWorktree() && !a.WantsContainer() }

// IsContainerRuntimeAxis reports whether v is one of the two CONTAINER
// runtime axis values — "is a container boundary requested at all?", a
// DIFFERENT question from "which ownership mode?". Every "did we keep the
// boundary?" check asks this predicate; every SELECTION asks for a specific
// value. Never replace this with an equality test against one const: that
// silently answers "host" for the other ownership mode, which is exactly the
// bug the split exists to prevent.
func IsContainerRuntimeAxis(v RuntimeAxis) bool {
	return v == RuntimeRootless || v == RuntimeRootful
}

// IsContainerRuntime reports whether a runtime-axis STRING asks for a
// container in EITHER ownership mode. It is the string-typed twin of
// IsContainerRuntimeAxis, kept for the boundary callers that still
// legitimately carry a raw string this far (a wire consumer that only has the
// wire spelling, before ParseRuntimeAxis).
func IsContainerRuntime(runtime string) bool {
	return IsContainerRuntimeAxis(RuntimeAxis(runtime))
}

// WorkspaceNames returns the recognized workspace-axis values; RuntimeNames
// the runtime-axis values. Single source for writers (agent set validation,
// CLI completion) and the schema so they never drift from the axes here.
func WorkspaceNames() []string {
	return []string{string(WorkspaceNone), string(WorkspaceWorktree)}
}

// RuntimeNames returns the recognized runtime-axis values, in the order they
// render into user-facing fix-it text and shell completion.
func RuntimeNames() []string {
	return []string{string(RuntimeHost), string(RuntimeRootless), string(RuntimeRootful)}
}

// ParseWorkspaceAxis is the ONE conversion between the workspace-axis string
// vocabulary (config YAML, run/acp --workspace, an agent_run spawn's
// workspace field) and the typed WorkspaceAxis. Every boundary that receives
// a workspace string parses it exactly once, here — never a bare
// WorkspaceAxis(s) conversion, which compiles for any string and hands the
// axis a value nobody admitted.
//
// Empty passes through as "" (the zero value), meaning "this level said
// nothing": each caller's own layering decides what silence resolves to.
// An unrecognized value cannot be treated as silence: `workspace: "wroktree"`
// would not merely fail to isolate a child, it would flip it from its own
// worktree into the PARENT'S LIVE CHECKOUT — strictly further from safety
// than the empty value it resembles. So anything unrecognized is an error
// naming the bad value and the legal ones. This refuses TYPOS: a value the
// user typed that no code path can honor.
func ParseWorkspaceAxis(s string) (WorkspaceAxis, error) {
	switch WorkspaceAxis(s) {
	case "", WorkspaceNone, WorkspaceWorktree:
		return WorkspaceAxis(s), nil
	default:
		return "", fmt.Errorf("unknown workspace axis %q (known: %s)", s, strings.Join(WorkspaceNames(), "|"))
	}
}

// ErrUnknownRuntimeAxis is ParseRuntimeAxis's refusal of a spelling the
// runtime axis does not have.
var ErrUnknownRuntimeAxis = errors.New("unknown runtime axis")

// ParseRuntimeAxis is the ONE conversion between the runtime-axis string
// vocabulary (config YAML, CLI flags, wire fields, Gherkin cells) and the
// typed RuntimeAxis. Every boundary that receives a runtime string parses it
// exactly once, here — never a local switch, never a string compare, never a
// default arm — and past that parse only the typed value travels.
//
// Empty passes through as "" (the zero value) rather than the literal
// RuntimeHost: IsContainerRuntimeAxis treats "" and RuntimeHost identically
// (neither is a container), so nothing that asks the predicate can tell them
// apart, and a caller that serializes the resolved axis keeps its existing
// blank-means-unset shape. Any OTHER unrecognized spelling — a typo, a
// retired value like bare "container" — is refused with an error naming the
// bad value and the legal ones. This function never warns and never
// degrades: the runtime axis is a security boundary and gets no caller-side
// softening. The refusal wraps ErrUnknownRuntimeAxis.
func ParseRuntimeAxis(s string) (RuntimeAxis, error) {
	switch RuntimeAxis(s) {
	case "", RuntimeHost, RuntimeRootless, RuntimeRootful:
		return RuntimeAxis(s), nil
	default:
		return "", fmt.Errorf("%w %q (known: %s)", ErrUnknownRuntimeAxis, s, strings.Join(RuntimeNames(), "|"))
	}
}

// DirtyTreeHandler is what a worktree spawn does when the parent tree is
// dirty; coord and launch share the value. It is a DEFINED TYPE, and every
// boundary that receives one of these spellings converts through
// ParseDirtyTreeHandler exactly once: the vocabulary reaches ctxloom from a
// channel typed by a MODEL (agent_run's free-form input), and the fallback
// member WRITES TO THE USER'S REPOSITORY — an unrecognized spelling that
// resolved to the default would auto-commit the parent's working tree on the
// strength of a typo. Unset and unparseable are different inputs — unset
// takes the caller's default, unparseable stops.
type DirtyTreeHandler string

const (
	DirtyTreeHandlerCommit DirtyTreeHandler = "commit"
	DirtyTreeHandlerCopy   DirtyTreeHandler = "copy"
	DirtyTreeHandlerStale  DirtyTreeHandler = "stale"
	DirtyTreeHandlerFail   DirtyTreeHandler = "fail"
)

// DirtyTreeHandlerNames returns the recognized handler values, in the order
// they render into user-facing fix-it text and the wire schemas.
func DirtyTreeHandlerNames() []string {
	return []string{
		string(DirtyTreeHandlerCommit),
		string(DirtyTreeHandlerCopy),
		string(DirtyTreeHandlerStale),
		string(DirtyTreeHandlerFail),
	}
}

// ParseDirtyTreeHandler is the ONE conversion between the dirty-tree-handler
// string vocabulary and the typed DirtyTreeHandler. Empty passes through as
// "" (the zero value), meaning "this level said nothing"; any other
// unrecognized spelling is an ERROR naming the bad value and the legal ones.
// It never warns and never degrades: the default member commits the user's
// working tree, so a spelling nobody recognizes must stop the spawn rather
// than reach it.
func ParseDirtyTreeHandler(s string) (DirtyTreeHandler, error) {
	switch DirtyTreeHandler(s) {
	case "", DirtyTreeHandlerCommit, DirtyTreeHandlerCopy, DirtyTreeHandlerStale, DirtyTreeHandlerFail:
		return DirtyTreeHandler(s), nil
	default:
		return "", fmt.Errorf("unknown dirty_tree_handler %q (known: %s)", s, strings.Join(DirtyTreeHandlerNames(), "|"))
	}
}

// ImageConfig carries the user's image configuration for containerized
// isolation: Image is the optional prebuilt agent-image override, run AS-IS
// and never built; BaseContainerfile is the optional user base Containerfile
// an on-the-fly local build layers the engine's agent stage onto instead of
// an auto-detected devcontainer / the embedded default base. AppRoot +
// NoDevcontainerBase + DevcontainerService drive the auto-detected project
// devcontainer base. Zero value = the engine's defaults (devcontainer
// auto-detect ON).
type ImageConfig struct {
	Image             string
	BaseContainerfile string
	// AppRoot is the project root devcontainer auto-detection resolves
	// .devcontainer/devcontainer.json (or .devcontainer.json) against; ""
	// disables auto-detection (same effect as NoDevcontainerBase).
	AppRoot string
	// NoDevcontainerBase opts out of devcontainer auto-detection.
	NoDevcontainerBase bool
	// DevcontainerService names the docker-compose service to use as the base
	// when the detected devcontainer.json declares dockerComposeFile.
	DevcontainerService string
	// Engines names WHICH per-engine images to build; empty = just this
	// invocation's engine. It is NOT a composition set — an agent image
	// carries exactly ONE engine.
	Engines []string
}
