// Package engine is the engine port. An engine KIND is a value: a struct the
// engine package defines, embedding Base (the engine root: the declarative
// Definition plus the views over it and the common decisioning) and carrying
// the engine-specific logic as methods. It is built ONCE at the composition
// root (engines.Build returns the Registry) and is immutable; it is
// instantiated MANY times, once per session, through Instance(Session). Core
// code holds Engine values and asks them; it never names an engine and no
// capability flag exists anywhere in core.
//
// DRY by construction: the engine declares ONE typed approach per surface
// kind; every view of them (Surfaces, Carries, Static) and the common
// decisioning (Delegate: preface items to the dynamic approach when one is
// provided, everything else to the static approach types) live on Base,
// written once here. Capabilities that may be absent are SLICES —
// Instance.Drivers(), Transcripts() — and empty means none; the caller that
// requires one refuses loudly at its point of use with ErrUnsupported.
// Capabilities that are single-valued are a real implementation or
// ErrUnsupported — Container(), Instance.Resume(key). Home() is a null
// object: the zero HomeSpec relocates nothing and seeds nothing.
// Requiredness of a surface (a nil Context on an engine that must carry a
// system prompt) is refused at Instance(), loudly.
//
// Import discipline (the acyclicity property): engine imports present,
// sessions and wire — all leaves — and NOTHING that imports engine. The
// engine-facing projection of a launch (Session) and of a package (Items)
// are declared HERE, so launch, delivery and composite import engine and
// engine imports none of them.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/ctxloom/ctxloom/internal/core/present"
)

// Name is the registry key and the ONLY spelling of an engine.
type Name string

// String renders the key.
func (n Name) String() string { return string(n) }

// Mode is how a run is driven: Interactive = a pty; Structured = the engine's
// native structured protocol. The values are the wire's (llm.proto's
// ExecutionMode: INTERACTIVE = 0, ONESHOT = 1) and the zero value is a real
// mode, because every caller that never set one has always meant Interactive.
type Mode int

const (
	Interactive Mode = iota
	Structured
)

// String renders the mode; an unknown value is visibly bad, never silently
// one of the two.
func (m Mode) String() string {
	switch m {
	case Interactive:
		return "interactive"
	case Structured:
		return "structured"
	default:
		return "mode(" + strconv.Itoa(int(m)) + ")"
	}
}

// ErrUnsupported is the loud refusal returned when the operation being
// performed depends on a capability this engine lacks: by the engine's own
// method (Instance on a definition missing a required surface, Resume) or by
// the caller that required a slice to be non-empty (a Structured turn with
// no driver).
type ErrUnsupported struct {
	Engine     Name
	Capability string
}

func (e ErrUnsupported) Error() string {
	return fmt.Sprintf("engine %q does not support %s", e.Engine, e.Capability)
}

// Engine is the port. The engine package's own struct type satisfies it by
// embedding Base — the engine root: the Definition plus the views and the
// common decisioning written once in core — and adding the methods that
// carry engine-specific logic.
//
// THE CONSTRUCTOR. Each engine package has one: the place its Definition
// and its typed approaches are assembled into a Base and, through Instance,
// session specifics are fed in. It is the ONE place an incoherent
// declaration is refused — a mode with no argv grammar, a nameless or
// rootless approach, a dynamic approach with no MCP approach — by
// Base.Validate; kinds need no check because the typed fields make a
// missing or duplicate kind a compile error, and requiredness is
// Instance()'s. NewRegistry does not re-validate; conformance asserts
// Validate holds. The SHAPE is the plain constructor: options applied,
// Validate once. A typestate builder is REJECTED as non-obvious machinery;
// it is named only as the fallback if requiredness must ever become
// compile-time.
type Engine interface {
	// Root is the engine root (Base): the declarative Definition and, on it,
	// the derived views and the common delivery decisioning (Delegate). A
	// value, equal on every call; the embedded Base supplies it.
	Root() Base
	// Instance binds one session. Instance lifetime = session lifetime: a
	// one-shot session's turns are frames driven on the SAME Instance; the
	// engine PROCESS is per turn and need not survive between turns. It
	// refuses LOUDLY (ErrUnsupported naming the kind) when a surface this
	// engine requires to run the session is nil on its Definition.
	Instance(s Session) (Instance, error)
	// Exports maps a package's items to this engine's native export shapes,
	// decoding each item's per-engine block against Definition.ExportSchema.
	// A block the schema refuses is an error naming the engine and the
	// item: the engine exports nothing on a guess.
	Exports(items Items) (Exports, error)
	// Home says how the engine's config/credential home relocates into the
	// session home. The zero HomeSpec is the null object: nothing to relocate
	// and nothing to seed.
	Home() HomeSpec
	// Container says how a containerized run is built and authenticated.
	// Refuses with ErrUnsupported when the engine has no image: a container
	// binding then fails at Resolve, never later.
	Container() (ContainerSpec, error)
	// Transcripts are the version-scoped readers of the engine's own store.
	// Empty means none, and every consumer of transcripts keeps operating.
	Transcripts() []TranscriptReader
	// Hooks decodes the engine's native hook payloads. An engine that fires
	// no hooks returns a codec whose Decode refuses with ErrUnsupported —
	// unreachable, since no payload arrives.
	Hooks() HookCodec
	// Wake is how an idle session of this engine is made to start a turn
	// (see WakeSpec). An engine with an Interactive mode must decide it.
	Wake() Declared[WakeSpec]
	// Approvals is the engine's approval codec. Undeclared, a session whose
	// approver is the human cannot be launched on this engine: there is
	// no way to put its requests to one.
	Approvals() Declared[ApprovalCodec]
	// Permissions is the engine's permission model. Undeclared, no posture
	// can be resolved for it and a launch on it is refused.
	Permissions() Declared[PermissionModel]
}

// Instance is one engine kind bound to one session.
type Instance interface {
	// Exec composes the process the runner execs from the presentations
	// delivery produced. It is the only place engine-specific argv is
	// composed; the result parses against Definition.CLI (the anti-drift
	// test). The Env it returns holds ONLY engine-native variables; the runner
	// stamps identity env on top.
	Exec(presented []present.Presentation) (Exec, error)
	// Drivers are the engine's native structured drivers. Empty means the
	// engine is driven only through a pty; the runner then refuses a
	// Structured turn with ErrUnsupported{Capability: "drive"}.
	Drivers() []StructuredDriver
	// Resume re-attaches this instance to the native session key, so the
	// next Exec or Turn continues that session. A real implementation or
	// ErrUnsupported.
	Resume(key string) error
}

// Exec is the exec projection.
type Exec struct {
	Binary      string
	Args        []string
	Env         map[string]string
	WorkDir     string
	Interactive bool
	StdinPrompt []byte
}

// StructuredDriver runs the engine's native structured protocol for one
// turn: the runner calls Turn once per turn on the SAME Instance, passing
// the native key it learned; the engine process is a discrete per-turn
// process inside a runner that stays. Every native event is relayed on
// out as it arrives (a nil out relays nothing — the caller wants the result
// alone); the driver never closes out.
type StructuredDriver interface {
	Turn(ctx context.Context, ex Exec, in Turn, out chan<- Event) (TurnResult, error)
}

// Turn is one structured turn's input.
type Turn struct {
	Prompt string
	Resume string // native key; "" on the first turn
	// Posture is the permission posture this turn's process runs at.
	Posture TurnPosture
}

// Event is one structured-protocol event the driver relays.
type Event struct {
	Kind    string
	Payload json.RawMessage
}

// TurnResult is what a structured turn yields.
type TurnResult struct {
	NativeKey string // the key the NEXT turn resumes by
	Answer    string
	// ExitCode is the status the turn's engine process exited with ON ITS
	// OWN — information, never the turn's verdict: a turn that answered and
	// then exited non-zero still answered. nil when there is no such status:
	// the turn ended the process itself (an interrupt, a teardown), so the
	// status would be ctxloom's doing, not the engine's.
	ExitCode *int
}
