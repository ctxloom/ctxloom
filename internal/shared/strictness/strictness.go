// Package strictness owns ctxloom's fail-loudly mode. Startup faults are
// STRICT by default: each instrumented choke calls Fail/FailOnce/Record with a
// failure class and a fix-it command; the warning line still streams to stderr
// exactly as before (so no diagnostic is ever lost, whichever command path
// fired it), and in strict mode a fatal Finding is additionally collected. The
// startup choke owners (`ctxloom run`, `ctxloom mcp`, `ctxloom acp`) then check
// the collected findings once — all of them, never first-error — and abort
// pre-launch with a distinct exit code listing every finding and its fix.
//
// Degraded mode (`--degraded` flag / CTXLOOM_DEGRADED=1 env, flag wins) is the
// escape hatch: recording is disabled, so every choke degrades to today's
// warn-and-continue behavior verbatim. There is deliberately NO config key for
// the mode — a broken config cannot excuse itself (bootstrap circularity).
package strictness

import (
	"fmt"
	"sync"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// prog stamps the warning lines; this package is ctxloom-internal, so the
// binary name is fixed (clidiag stays parameterized for the companion binaries).
const prog = "ctxloom"

// Class buckets a fatal finding by the per-class strictness table, so the
// abort listing reads as a diagnosis ("[config] ...", "[sync] ...") rather
// than an undifferentiated wall of text.
type Class string

const (
	// ClassConfig is a present-but-broken config file (unreadable / parse /
	// schema-invalid). An absent config is fine and never a finding.
	ClassConfig Class = "config"
	// ClassMigration is a lossy in-memory schema migration (a setting the
	// upgrade pipeline had to drop).
	ClassMigration Class = "migration"
	// ClassSync is a lockfile-pinned item that is neither in the local cache
	// nor fetchable. A refresh failure with a complete cache stays a plain
	// warning in both modes and never reaches this class.
	ClassSync Class = "sync"
	// ClassRef is an unresolvable configured reference: a default profile, a
	// profile parent, or a profile-pushed fragment that fails to load.
	ClassRef Class = "ref"
	// ClassApply is a hook/MCP/settings apply failure or a context
	// regeneration failure (partial apply is no longer success in strict).
	ClassApply Class = "apply"
	// ClassBundle is a load/parse failure of a lockfile-active or local
	// bundle. Builtin (in-binary) bundle failures stay warnings.
	ClassBundle Class = "bundle"
	// ClassTrust is a corrupt/unreadable trust store (the deny-all posture).
	ClassTrust Class = "trust-store"
	// ClassIsolation is an EXPLICITLY-requested container runtime that cannot be
	// satisfied (no reachable runtime, agent image absent/unbuildable, shared-fs
	// probe failed, no resolvable auth) so the run would otherwise fall back to
	// the UNSANDBOXED host. Only an explicit request — an agent's `runtime:`
	// trait, the project `runtime:` default, or `--runtime container` — reaches
	// this class; the ambient host default degrades silently and never lands
	// here.
	ClassIsolation Class = "isolation"
)

// Finding is one collected fatal fault: what broke (Message, already
// formatted), which class it belongs to, and the command or edit that fixes it.
type Finding struct {
	Class   Class
	Message string
	FixIt   string
}

var (
	mu       sync.Mutex
	degraded bool
	findings []Finding
	// generation counts Checkpoint calls. onceRecorded keys FailOnce
	// recordings by generation+class+message, so the RECORDING dedup is
	// scoped to one checkpoint window: a long-lived server (`ctxloom acp`)
	// that refuses a session over a FailOnce finding must see the SAME
	// finding again when the unfixed session is retried under a new
	// Checkpoint — a process-wide dedup would swallow the re-fire and the
	// retry would open silently on broken context. The PRINT dedup
	// (clidiag.WarnOnce) deliberately stays process-wide; worst case of the
	// window scoping is a duplicate line inside one findings listing.
	generation   int
	onceRecorded = map[string]struct{}{}
)

// SetDegraded switches the process into (or out of) degraded mode. Called once
// at startup from the CTXLOOM_DEGRADED env read and the --degraded flag (flag
// wins when both are set).
func SetDegraded(v bool) {
	mu.Lock()
	defer mu.Unlock()
	degraded = v
}

// Degraded reports whether the process runs in degraded (warn-and-continue)
// mode.
func Degraded() bool {
	mu.Lock()
	defer mu.Unlock()
	return degraded
}

// Mark is a checkpoint into the findings list; Since(mark) returns only the
// findings recorded after it. Choke owners checkpoint at the start of their
// own startup sequence so an EARLIER, COMPLETED invocation in the same
// process (a previous ACP session, another test) never bleeds into their
// abort decision.
//
// LIMITATION: windows are only isolated when they run SEQUENTIALLY. A Mark is
// an index into the one process-global findings list, so windows that overlap
// in time interleave: a finding recorded by a concurrent goroutine lands
// inside every open window and is attributed to all of them. Callers that
// checkpoint on concurrent goroutines (the ACP server's session opens, the
// fan-out's per-member isolation gate) must serialize their checkpoint→gate
// sections externally; per-window finding ownership inside this package is
// the eventual fix.
type Mark int

// Checkpoint returns a Mark for the current findings position and opens a new
// FailOnce recording-dedup window (see generation above).
func Checkpoint() Mark {
	mu.Lock()
	defer mu.Unlock()
	generation++
	return Mark(len(findings))
}

// Since returns a copy of the findings recorded after mark, in record order.
func Since(mark Mark) []Finding {
	mu.Lock()
	defer mu.Unlock()
	if int(mark) >= len(findings) {
		return nil
	}
	out := make([]Finding, len(findings)-int(mark))
	copy(out, findings[mark:])
	return out
}

// All returns a copy of every finding recorded so far.
func All() []Finding {
	return Since(0)
}

// Reset clears the collected findings, the FailOnce dedup set, and the
// checkpoint generation (test seam; the mode is left untouched — use
// SetDegraded).
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	findings = nil
	generation = 0
	onceRecorded = map[string]struct{}{}
}

// Fail reports a fatal-class fault at a choke. The warning line streams to
// stderr in BOTH modes (identical to the clidiag call it replaces, so command
// paths that never check findings keep today's diagnostics); in strict mode
// the finding is additionally recorded for the startup choke owner to abort
// on. fixit names the command or edit that resolves the fault ("" when the
// message already says).
func Fail(class Class, fixit, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	clidiag.Warn(prog, "%s", msg)
	record(class, fixit, msg, false)
}

// FailOnce is Fail with per-process dedup on the formatted message: the
// warning prints at most once (clidiag.WarnOnce) and the finding records at
// most once. For chokes that re-fire per subsystem (e.g. an unresolvable
// profile parent hit by every loader build).
func FailOnce(class Class, fixit, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	clidiag.WarnOnce(prog, "%s", msg)
	record(class, fixit, msg, true)
}

// Record collects a finding WITHOUT printing anything — for chokes that
// already own their (richer) stderr reporting, e.g. the sync summary's
// per-item failure breakdown. No-op in degraded mode.
func Record(class Class, fixit, format string, args ...any) {
	record(class, fixit, fmt.Sprintf(format, args...), false)
}

// record appends a finding in strict mode, honoring the FailOnce dedup set
// when once is set. The dedup key includes the current checkpoint generation,
// so the dedup collapses repeats WITHIN one window but never swallows a
// re-fire in a later window (see generation's doc).
func record(class Class, fixit, msg string, once bool) {
	mu.Lock()
	defer mu.Unlock()
	if degraded {
		return
	}
	if once {
		key := fmt.Sprintf("%d\x00%s\x00%s", generation, class, msg)
		if _, seen := onceRecorded[key]; seen {
			return
		}
		onceRecorded[key] = struct{}{}
	}
	findings = append(findings, Finding{Class: class, Message: msg, FixIt: fixit})
}
