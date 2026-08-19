package agent

import (
	"fmt"
	"strings"
)

// RuntimeAxis says where an agent's engine process executes: directly on the
// host, or inside a container under one of two ownership modes. This is the
// SINGLE declaration of the runtime-axis vocabulary — defined here, in the
// lowest package every consumer can already import, so nothing above it
// (internal/lm/isolation included) declares a second, independently-spelled
// copy. isolation.RuntimeAxis is a type ALIAS of this type, not a new one:
// the two names are the same type, immune to drift by construction, so
// nothing pins their agreement with a test — there is nothing left to drift.
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
	// RuntimeContainerRootless runs the engine inside a container on a
	// runtime that maps the container's root to the INVOKING HOST USER.
	RuntimeContainerRootless RuntimeAxis = "container-rootless"
	// RuntimeContainerRootful runs the engine inside a container on a
	// runtime whose container-root is REAL root, with the image entrypoint
	// remapping to the launching uid/gid.
	RuntimeContainerRootful RuntimeAxis = "container-rootful"
)

// IsContainerRuntimeAxis reports whether v is one of the two CONTAINER
// runtime axis values — "is a container boundary requested at all?", a
// DIFFERENT question from "which ownership mode?". Every "did we keep the
// boundary?" check asks this predicate; every SELECTION asks for a specific
// value. Never replace this with an equality test against one const: that
// silently answers "host" for the other ownership mode, which is exactly the
// bug the split exists to prevent (see IsContainerRuntime's doc for the
// string-typed twin of this same rule).
func IsContainerRuntimeAxis(v RuntimeAxis) bool {
	return v == RuntimeContainerRootless || v == RuntimeContainerRootful
}

// IsContainerRuntime reports whether a runtime-axis STRING asks for a
// container in EITHER ownership mode. It is the string-typed twin of
// IsContainerRuntimeAxis, kept for the boundary callers that still legitimately
// carry a raw string this far (a ChatRequest.Runtime consumer that only has
// the wire spelling, before ParseRuntimeAxis). Every "is the engine
// containerized?" gate asks one of these two, never an equality test against
// one const.
func IsContainerRuntime(runtime string) bool {
	return IsContainerRuntimeAxis(RuntimeAxis(runtime))
}

// RuntimeNames returns the recognized runtime-axis values, in the order they
// render into user-facing fix-it text and shell completion. Single source for
// writers (agent set validation, CLI completion, ParseRuntimeAxis's own error
// text) and the schema, so they never drift from the axis declared above.
func RuntimeNames() []string {
	return []string{string(RuntimeHost), string(RuntimeContainerRootless), string(RuntimeContainerRootful)}
}

// ParseRuntimeAxis is the ONE conversion between the runtime-axis string
// vocabulary (config YAML, CLI flags, gRPC/ChatRequest wire fields, Gherkin
// Examples cells and step parameters) and the typed RuntimeAxis enum. Every
// boundary that receives a runtime string parses it exactly once, here —
// never a local switch, never a string compare, never a default arm — and
// past that parse only the typed value travels; nothing downstream
// re-interprets a string.
//
// Empty parses as RuntimeHost: that is the documented default for an unset
// value (an agent with no runtime: declared, a project with no runtime:
// default). Any other unrecognized spelling — a typo, a retired value like
// bare "container" — is refused with an error naming the bad value and the
// legal ones. This function never warns and never degrades: a caller that
// wants the old "warn and fall back to host" behavior for an ADVISORY axis
// must decide that at ITS OWN call site (see isolation.warnUnknownAxes for
// the workspace axis, which stays a warn); the runtime axis is a security
// boundary and gets no such caller-side softening.
func ParseRuntimeAxis(s string) (RuntimeAxis, error) {
	switch RuntimeAxis(s) {
	case "", RuntimeHost:
		return RuntimeHost, nil
	case RuntimeContainerRootless:
		return RuntimeContainerRootless, nil
	case RuntimeContainerRootful:
		return RuntimeContainerRootful, nil
	default:
		return "", fmt.Errorf("unknown runtime axis %q (known: %s)", s, strings.Join(RuntimeNames(), "|"))
	}
}
