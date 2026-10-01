package engine

import (
	"strconv"
	"strings"
)

// Sandbox is the engine-neutral bound on what the engine's own commands may
// touch; each engine maps it to its mechanism (PermissionModel.Sandboxes
// lists the ones it can enforce).
type Sandbox int

const (
	// SandboxUnspecified is the zero value: nobody declared one. The
	// engine's default replaces it at resolve; it is never resolved.
	SandboxUnspecified Sandbox = iota
	// SandboxReadOnly lets commands read, never write.
	SandboxReadOnly
	// SandboxWorkspaceWrite lets commands write the working tree only.
	SandboxWorkspaceWrite
	// SandboxFull bounds nothing: whatever contains the process is the
	// boundary.
	SandboxFull
)

// String renders the config spelling; an unspecified or out-of-range value
// is visibly not one.
func (s Sandbox) String() string {
	switch s {
	case SandboxReadOnly:
		return "read-only"
	case SandboxWorkspaceWrite:
		return "workspace-write"
	case SandboxFull:
		return "full"
	case SandboxUnspecified:
		return "sandbox(unspecified)"
	default:
		return "sandbox(" + strconv.Itoa(int(s)) + ")"
	}
}

// ParseSandbox maps a config spelling to a Sandbox, ignoring case and
// surrounding space.
func ParseSandbox(s string) (Sandbox, bool) {
	for _, v := range []Sandbox{SandboxReadOnly, SandboxWorkspaceWrite, SandboxFull} {
		if strings.EqualFold(strings.TrimSpace(s), v.String()) {
			return v, true
		}
	}
	return SandboxUnspecified, false
}

// SandboxNames lists the accepted spellings.
func SandboxNames() []string {
	return []string{SandboxReadOnly.String(), SandboxWorkspaceWrite.String(), SandboxFull.String()}
}

// Posture is an engine's resolved permission posture as core carries it:
// the engine's name and its own document. Core never reads the document;
// the engine's PermissionModel wrote it and the engine reads it back.
type Posture struct {
	Engine   Name
	Document map[string]any
}

// Clone copies the posture so a copy's edits never reach the original.
func (p Posture) Clone() Posture {
	p.Document = cloneDoc(p.Document)
	return p
}

// cloneDoc deep-copies a JSON-shaped document.
func cloneDoc(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneDoc(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = cloneValue(e)
		}
		return out
	case []string:
		return append([]string(nil), t...)
	default:
		return v
	}
}

// PostureRequest is what an engine resolves its posture from.
type PostureRequest struct {
	// Declared are the engine's own documents from each rung that declared
	// one, nearest first (an agent binding's block for this engine, then
	// the llm label's keys). Each key is taken from the first that has it.
	Declared []Declaration
	// Mode is a per-launch override of the posture's mode (the
	// --permissions flag), in the engine's own vocabulary; "" is none.
	Mode string
	// Degraded asks for the engine's floor, announced through Warn,
	// instead of a refusal of a declaration that cannot be honoured.
	Degraded bool
	Warn     func(format string, args ...any)
}

// Declaration is one rung's document and how to name the rung in a refusal.
type Declaration struct {
	Document map[string]any
	From     string
}

// PermissionModel is an engine's permission vocabulary: its own keys and
// postures, and how it validates, resolves and reports them. Core holds the
// resolved document without reading it.
type PermissionModel interface {
	// Keys are the keys the engine's document accepts (a label's own keys,
	// a binding's block for this engine).
	Keys() []string
	// Postures are the engine's mode vocabulary.
	Postures() []string
	// Validate refuses a document the engine cannot honour, naming the
	// offending key or value; it runs where the document is written and
	// where it is resolved.
	Validate(doc map[string]any) error
	// Resolve settles the engine's document from what was declared,
	// defaults included; under Degraded an unhonourable declaration drops
	// to Floor, announced.
	Resolve(req PostureRequest) (map[string]any, error)
	// Floor is the most restrictive posture the engine can name: what a
	// degraded launch falls back to.
	Floor() map[string]any
	// Decode reads a resolved document back (off the wire, the journal)
	// and names its posture.
	Decode(doc map[string]any) (string, error)
	// Transitions are the postures an approval may move a session at doc
	// to (a plan's continuation, a mode change riding an allow), the one
	// doc declares a plan continues at first: a presenter's default.
	Transitions(doc map[string]any) []string
	// Sandboxes are the sandbox values the engine can enforce on runtime
	// (launch.RuntimeAxis spelling); DefaultSandbox is the one it takes
	// when none is declared.
	Sandboxes(runtime string) []Sandbox
	DefaultSandbox() Sandbox
	// Reviewer reports whether the engine can serve approver: reviewer.
	Reviewer() bool
}
