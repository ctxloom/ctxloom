// Package engine adapts the various LLM hook protocols (Claude Code,
// Antigravity, ...) to a single internal Request/Response shape. The core
// never speaks a specific engine's wire format; an Adapter does the
// translation at the edge.
package engine

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Request is the engine-neutral view of a tool invocation to be checked. It is
// either a command invocation (Command set, from Bash/PowerShell) or a file edit
// (FilePath set, from Edit/Write/…), never both.
type Request struct {
	ToolName string
	Command  string
	// Shell is the dialect to parse with. Empty means the engine did not say;
	// the caller fills in a default.
	Shell ir.Shell
	// FilePath is the target of a file-editing tool (Edit/Write/MultiEdit/…),
	// empty for command invocations. When set, the request is evaluated against
	// path rules instead of command rules.
	FilePath string
	// ToolUngated marks a payload whose tool name the adapter does not
	// recognise, even though the installed hook matched it (that match is why
	// evaluate is running at all — Decode is only ever called with a matched
	// tool name). Extraction is NOT gated on name recognition — Command/FilePath
	// may still be populated when the payload happens to reuse a known field
	// name — but a truly unrecognized payload schema yields neither, and every
	// rule would silently miss. ltk is a cooperative redirect,
	// not a sandbox, but this one case is a deliberate exception to its
	// fail-open posture: cmd/ltk/evaluate.go denies on ToolUngated rather than
	// evaluating a request it cannot fully trust the shape of. See
	// ungatedToolDenyReason there for the full reasoning.
	ToolUngated bool
}

// Response is the engine-neutral decision.
type Response struct {
	Allow   bool
	Reason  string
	Suggest string
	// Confirmable reports whether a denial may be lifted by repeating the exact
	// command within ConfirmWindowSeconds (the "confirm by repeating" override).
	// ConfirmDelaySeconds is a minimum wait before the repeat counts (0 = none).
	// An inviolate rule yields Confirmable=false, so repeating never helps.
	Confirmable          bool
	ConfirmWindowSeconds int
	ConfirmDelaySeconds  int
	// Unanalyzed reports that this decision was reached WITHOUT being able to
	// fully parse the command (top-level, or a nested wrapper body) or a file
	// path's rules — Allow=true here means "on_parse_error let it through",
	// never "the rules were checked and nothing matched". Before this field
	// existed those two outcomes were byte-identical on every surface (the
	// CLI's `check` JSON, the wire Output an Adapter encodes), which is the
	// root cohesion defect: an
	// operator relying on the default fail-open policy had no way to notice
	// their rules were silently not being consulted at all. ParseError
	// carries the human-facing detail when Unanalyzed is true.
	Unanalyzed bool
	ParseError string
}

// unanalyzedNote renders the one-line stderr diagnostic both Adapters emit
// for an Unanalyzed allow — shared so the two engines' wording (and any
// future engine's) stays byte-identical rather than drifting.
func unanalyzedNote(r Response) string {
	detail := r.ParseError
	if detail == "" {
		detail = "reason unavailable"
	}
	return "ltk: could not fully analyze this command (" + detail + "); allowed per defaults.on_parse_error (default: allow)\n"
}

// encodeDecision is the whole Encode contract, shared by every Adapter. Only
// the DENY bytes are engine-specific, so an engine supplies just its own
// encodeDeny and inherits the rest:
//
//   - a clean allow is a zero Output — no bytes, exit 0 — letting the host's
//     own permission flow proceed;
//   - an unanalyzed allow adds the shared stderr note, so "allowed per
//     on_parse_error" is never byte-identical to "checked and nothing matched";
//   - a deny is the engine's decision document on STDOUT at exit 0, never an
//     exit code: both hosts fail OPEN on a non-zero hook, so a denial signalled
//     that way is invisible and the action proceeds anyway.
//
// One home for those rules is the point: an engine cannot implement Encode and
// forget an arm, which is exactly how the unanalyzed diagnostic went missing
// from one adapter before.
func encodeDecision(resp Response, encodeDeny func(string) ([]byte, error)) (Output, error) {
	if resp.Allow {
		if resp.Unanalyzed {
			return Output{Stderr: []byte(unanalyzedNote(resp))}, nil
		}
		return Output{}, nil
	}
	body, err := encodeDeny(resp.Message())
	if err != nil {
		return Output{}, err
	}
	return Output{Stdout: body, ExitCode: 0}, nil
}

// Message renders the reason and suggestion into a single human-facing
// string. Never empty: both Adapters' Encode call this only when rendering a
// DENY, and a deny that renders to zero bytes tells the agent
// nothing — no reason, no way to comply. Ordinary rule authoring already
// cannot produce this in practice (validateRule requires a message or
// suggest on every enabled deny rule, rules.go), but that is a config-time
// guard, not a type-level one; this is the belt to its suspenders for any
// other path that constructs a bare Response{Allow: false}.
func (r Response) Message() string {
	switch {
	case r.Reason == "" && r.Suggest == "":
		return "denied by ltk (no reason or suggested alternative was configured for this rule)"
	case r.Suggest == "":
		return r.Reason
	case r.Reason == "":
		return "Use instead: " + r.Suggest
	default:
		return r.Reason + "\n\nUse instead: " + r.Suggest
	}
}

// Output is the full result an adapter produces for one decision: what to write
// to each stream and the process exit code. This covers every engine's protocol
// without changing the interface:
//
//   - Claude Code / Antigravity / Codex: deny → JSON on Stdout, ExitCode 0;
//     allow → empty.
//
// A zero Output (no bytes, exit 0) means "write nothing" — pass-through.
type Output struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Adapter translates one hook engine's stdin/stdout protocol. It is what the
// `evaluate` (runtime) path needs.
type Adapter interface {
	// Name is the engine identifier (e.g. "claude-code").
	Name() string
	// Decode parses the engine's hook payload from stdin bytes.
	Decode(input []byte) (Request, error)
	// Encode renders the decision into the engine's wire output (streams + exit
	// code).
	Encode(resp Response) (Output, error)
}

// Engine is everything specific to one hook host: the runtime Adapter plus the
// management surface (where its hook config lives and how to add/remove the
// hook). Engine-specific details — settings paths, the on-disk hook format —
// live entirely in the engine's own module, so adding an engine never touches
// the `manage` command.
type Engine interface {
	Adapter
	// Detect scores how relevant this engine is to the project rooted at dir
	// (e.g. presence of .claude/). 0 means "not present here". The manage
	// command installs into the highest-scoring engine.
	Detect(dir string) int
	// SettingsPath returns the engine's hook-config file for the given scope.
	SettingsPath(dir string, global bool) (string, error)
	// HookCommand builds the command line the hook should run.
	HookCommand(bin, configPath string) string
	// Install merges a hook running command into the engine's settings bytes
	// (empty in → fresh config). Idempotent; returns the new settings bytes and
	// a human-facing note when the merge repaired an existing install (e.g.
	// upgraded a stale matcher), empty otherwise.
	Install(settings []byte, command string) (data []byte, note string, err error)
	// Uninstall removes any hook running command from the settings bytes. removed
	// reports whether a matching hook was actually found and dropped, so callers
	// can skip a no-op rewrite and avoid claiming a removal that did not happen.
	Uninstall(settings []byte, command string) (data []byte, removed bool, err error)
}

// engines is the registry of known engines. Order matters for Detect ties:
// earlier wins, so Claude Code outranks Antigravity when both score equally.
func engines() []Engine {
	return []Engine{ClaudeCode{}, Antigravity{}}
}

// All returns every registered engine, in the same order engines() and Detect
// use. Exported so a caller that must pick among engines by some means other
// than name or directory detection — e.g. cmd/ltk/evaluate.go trying each
// adapter's Decode against a payload it received under an unrecognized
// --engine, rather than guessing claude-code — can enumerate them
// without reaching into this package's internals.
func All() []Engine {
	return engines()
}

// Get returns the engine for a name: the canonical name or a declared alias,
// case-insensitively. There is no prefix matching — a typo must error rather
// than silently pick an engine.
func Get(name string) (Engine, error) {
	want := agent.CanonicalEngineName(name)
	for _, e := range engines() {
		if e.Name() == want {
			return e, nil
		}
	}
	return nil, fmt.Errorf("unknown engine %q", name)
}

// Detect returns the most relevant engine for the project at dir, or ok=false
// if none is detected there.
func Detect(dir string) (Engine, bool) {
	var best Engine
	bestScore := 0
	for _, e := range engines() {
		if s := e.Detect(dir); s > bestScore {
			best, bestScore = e, s
		}
	}
	return best, best != nil
}
