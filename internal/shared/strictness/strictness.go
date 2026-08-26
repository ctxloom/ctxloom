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
// escape hatch: it suppresses FATALITY, not RECORDING. Findings are still
// warned and still collected -- so a degraded run can answer "what did you
// skip?", and the retained records can feed metrics -- while the gates
// (formatFindings, FindingsError) render nothing and abort nothing, leaving
// every choke at warn-and-continue. There is deliberately NO config key for
// the mode — a broken config cannot excuse itself (bootstrap circularity).
package strictness

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

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
	// satisfied AS REQUESTED: no reachable runtime, an unrecognized runtime axis
	// value (a typo that would silently land on the host), an external plugin
	// binary that cannot be containerized, the agent image absent/unbuildable, a
	// stale image whose refresh build failed, a configured base image
	// (isolation_base_containerfile) that failed to build, shared-fs probe
	// failed, or no resolvable auth — so the run would otherwise fall back to the
	// UNSANDBOXED host, or run a STALE/substituted image instead of the one
	// requested. Only an explicit request — an agent's `runtime:` trait, the
	// project `runtime:` default, or `--runtime container` — reaches this class;
	// the ambient host default degrades silently and never lands here.
	ClassIsolation Class = "isolation"
	// ClassTask is an EXPLICITLY-requested task-store mutation that could not
	// be applied — today `ctxloom run --seed-task <harp>` against a corrupt,
	// unreadable, or non-matching project task log. Only an explicit request
	// reaches this class, mirroring ClassIsolation: ambient task bookkeeping
	// that nobody asked for stays a plain warning. The point is that a user
	// who named a task must not be told the launch succeeded while the task
	// silently stayed untouched.
	ClassTask Class = "task"
)

// Finding is one collected fatal fault: what broke (Message, already
// formatted), which class it belongs to, and the command or edit that fixes it.
type Finding struct {
	Class   Class
	Message string
	FixIt   string
	// NonDegradable keeps this finding fatal under --degraded as well as in
	// strict mode. It is declared per FINDING, never per class, because the
	// class-wide reading is refuted by the code: an ownership mismatch is
	// ClassIsolation and its sanctioned degraded outcome is a HOST fallback
	// that launches, while a container that died at the daemon is the same
	// class and must not launch at all. The discriminator is the doctrine's
	// own — does LAUNCHING cause the harm? — which is a property of the
	// specific fault, not of its category.
	//
	// The zero value is DEGRADABLE so no existing raise site changes meaning:
	// non-degradability is opt-in, via FailAlways.
	NonDegradable bool
}

var (
	mu       sync.Mutex
	degraded bool
	// findings is the process-wide chronological log — every finding ever
	// recorded, in record order, across every goroutine. It backs ONLY All()
	// and Reset(); a Mark never indexes into it (see window below) — that
	// indirection through ONE shared slice was the concurrency defect.
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
	//
	// generation stays a single global COUNTER (each Checkpoint call bumps it
	// once, under mu, regardless of which goroutine calls it) — but each
	// window (below) captures its OWN generation value at the moment ITS
	// goroutine last called Checkpoint, and record()'s dedup
	// key reads that captured value, never this live variable directly.
	// Two concurrently-opened windows get two different generation numbers
	// exactly as the paragraph above always intended, but that guarantee
	// only holds because each window remembers ITS OWN number — reading this
	// shared, still-mutating counter directly at record() time (the
	// pre-fix shape) let a later Checkpoint on a DIFFERENT goroutine bump
	// this value out from under an earlier window before that window ever
	// recorded anything, so two different windows could end up computing the
	// SAME dedup key from the SAME live value and silently swallow one
	// another's FailOnce.
	generation   int
	onceRecorded = map[string]struct{}{}

	// windowsMu guards windows, the goroutine-id -> *window registry.
	windowsMu sync.Mutex
	windows   = map[int64]*window{}
)

// window is ONE GOROUTINE's privately-owned findings log — the fix for the
// cross-attribution defect: record() appends a finding to the window of the
// SPECIFIC goroutine that recorded it, never to a window some other,
// concurrently-running goroutine happens to have open. A goroutine's window
// lives for that goroutine's whole life unless explicitly released (Close),
// so repeated/sequential Checkpoint calls on one goroutine share ONE
// continuously growing log — Mark indices behave exactly as they did against
// the old process-global slice, just narrowed in scope to one goroutine, so
// nesting/sequential-reread semantics are unchanged for the sequential
// callers that already worked correctly.
type window struct {
	gid      int64
	mu       sync.Mutex
	findings []Finding
	// generation is the global generation value captured by the most recent
	// Checkpoint() call ON THIS GOROUTINE. record's FailOnce
	// dedup key must scope to the RECORDING goroutine's own checkpoint
	// window, not to whatever the shared, monotonically-bumped global
	// generation counter happens to read at the moment record() runs — two
	// windows opened close together (goroutine A checkpoints at generation 1,
	// goroutine B checkpoints at generation 2 while A is still running) used
	// to collide on the SAME live generation value by the time either one
	// actually recorded a finding, silently swallowing one goroutine's
	// FailOnce as "already seen" under the other's window. Zero (the Go zero
	// value) is a legitimate generation for a goroutine that records without
	// ever calling Checkpoint — its dedup then stays scoped to the whole
	// goroutine lifetime, matching pre-fix behavior for a process where no
	// Checkpoint has fired yet.
	generation int
	// open counts the checkpoints currently bracketing work on this
	// goroutine. Nested Checkpoints on one goroutine share ONE window, so the
	// registry entry may only be released when the LAST of them closes:
	// releasing it while an outer Mark is still live detaches that mark — the
	// next record builds a fresh window and the outer Since reads the
	// orphaned old one, missing every finding recorded after the inner Close.
	// A fail-loudly gate going quiet is the one failure this package exists
	// to prevent, and nothing in the API would have detected it.
	open int
}

// currentWindow returns (creating if absent) the calling goroutine's window.
func currentWindow() *window {
	gid := goroutineID()
	windowsMu.Lock()
	w, ok := windows[gid]
	if !ok {
		w = &window{gid: gid}
		windows[gid] = w
	}
	windowsMu.Unlock()
	return w
}

// goroutineID extracts the runtime's own goroutine id from the "goroutine
// 123 [running]:" preamble runtime.Stack prints for the calling goroutine.
// Go hands out ids from a monotonically increasing process-lifetime counter
// and never recycles them (runtime/proc.go's goidgen), so a window keyed by
// this id can never later be handed to a DIFFERENT, unrelated goroutine —
// the only correctness property this package leans on; the id is never used
// for scheduling or anything else runtime.Stack was not designed to expose.
func goroutineID() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	b := bytes.TrimPrefix(buf[:n], []byte("goroutine "))
	if i := bytes.IndexByte(b, ' '); i >= 0 {
		b = b[:i]
	}
	id, _ := strconv.ParseInt(string(b), 10, 64)
	return id
}

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

// Mark is a checkpoint into ONE GOROUTINE's own findings window; Since(mark)
// returns only the findings THAT GOROUTINE recorded after it. Choke owners
// checkpoint at the start of their own startup sequence so an EARLIER,
// COMPLETED invocation in the same process (a previous ACP session, another
// test) never bleeds into their abort decision.
//
// PER-WINDOW OWNERSHIP (the fix for the former cross-attribution defect): a
// finding is attributed to the window open on the SAME GOROUTINE that
// recorded it — never to a window some OTHER, concurrently-running goroutine
// happens to have open. This is what makes Checkpoint/Since safe for
// CONCURRENT callers (the ACP server's session opens, agent_run's per-child
// spawn, the fan-out's per-member isolation gate) with no external
// serialization required. The one invariant this places on callers: the code
// that RECORDS a finding (Fail/FailOnce/Record, however deep the call chain)
// must run on the SAME goroutine that opened the window — do not fan a
// checkpointed bracket of work out onto new goroutines and call
// Fail/FailOnce/Record from them; a finding recorded there attributes to
// whatever window (if any) THAT goroutine has open, never to this one. Every
// current caller already holds this invariant: isolation.Prepare, the config
// loaders, and their call trees never spawn goroutines of their own between a
// Checkpoint and its Since.
type Mark struct {
	w   *window
	idx int
	// release is this checkpoint's one-shot token for its slot in the
	// window's open count. Close is documented as safe to call more than once
	// with the same Mark, so the release must be per-CHECKPOINT rather than
	// per-call: a repeated Close of an inner mark must not consume an outer
	// bracket's slot. Nil on a zero-value Mark, which Close no-ops on.
	release *atomic.Bool
}

// Checkpoint returns a Mark for the current findings position on the CALLING
// GOROUTINE and opens a new FailOnce recording-dedup window (see generation's
// doc). Pair a checkpoint opened on a goroutine that will not outlive its own
// bracket (a per-request handler, a fan-out member) with a deferred
// Close(mark), so a long-lived process does not accumulate one registry entry
// per goroutine forever; a checkpoint on a goroutine that exits with the
// process (a one-shot CLI command) may skip it — the entry dies with the
// process either way.
func Checkpoint() Mark {
	mu.Lock()
	generation++
	gen := generation
	mu.Unlock()

	w := currentWindow()
	w.mu.Lock()
	defer w.mu.Unlock()
	// Stamp THIS goroutine's window with the generation value this
	// very Checkpoint call captured, so record()'s dedup key (below) reads
	// this goroutine's own last-checkpointed generation instead of the live,
	// shared global counter — which a concurrently-checkpointing goroutine
	// could have bumped again before this one's window ever records
	// anything.
	w.generation = gen
	w.open++
	return Mark{w: w, idx: len(w.findings), release: &atomic.Bool{}}
}

// Since returns a copy of the findings this mark's goroutine recorded after
// it, in record order. Safe to call more than once against the same mark
// (some callers re-check the same window at two gates). A zero-value Mark
// (the Go zero value, e.g. a literal 0 carried over from callers written
// against the old int-typed Mark) means "from the very start" — resolved
// against the CALLING goroutine's own window, the same meaning index 0 had
// against the old process-global slice for a caller that never checkpointed.
func Since(mark Mark) []Finding {
	w := mark.w
	if w == nil {
		w = currentWindow()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if mark.idx >= len(w.findings) {
		return nil
	}
	out := make([]Finding, len(w.findings)-mark.idx)
	copy(out, w.findings[mark.idx:])
	return out
}

// All returns a copy of every finding recorded so far, PROCESS-WIDE across
// every goroutine — unlike Since, which is scoped to one goroutine's window.
// Test seam: nothing in production reads it (Since/FindingsError, scoped to
// the calling goroutine's own window, are what every real gate uses) — it
// exists for cross-goroutine test assertions that Since structurally cannot
// provide. Grows without bound for the life of the process;
// do not call it from anything long-lived.
func All() []Finding {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Finding, len(findings))
	copy(out, findings)
	return out
}

// Close releases mark's slot in its goroutine's window, and releases the
// goroutine-keyed registry entry itself once the LAST bracketing checkpoint
// has closed — never while an outer Mark on the same goroutine is still live
// (see window.open). Safe to call multiple times, or with a zero Mark; a
// no-op either way. See Checkpoint's doc for when a caller should bother —
// long-lived processes that open many short-lived concurrent windows (one per
// request goroutine) should call it so the registry does not grow one entry
// per goroutine forever; a window on a goroutine that terminates with the
// process (a one-shot CLI command) needs no explicit release.
func Close(mark Mark) {
	if mark.w == nil || mark.release == nil || mark.release.Swap(true) {
		return
	}
	w := mark.w
	w.mu.Lock()
	w.open--
	last := w.open <= 0
	w.mu.Unlock()
	if !last {
		return
	}
	windowsMu.Lock()
	if windows[w.gid] == w {
		delete(windows, w.gid)
	}
	windowsMu.Unlock()
}

// FindingsError renders the findings recorded since mark as a single error —
// "fatal startup findings:" followed by one "  - message (fix: ...)" line
// per finding — or nil when nothing was collected or the process is
// degraded. This is the one shared owner for the per-call, keeps-running
// error-render variant (as opposed to a process-exit abort, which prints a
// richer class-tagged listing and belongs to its own callers): internal/cli,
// internal/agentcoord/coord, and internal/operations each used to carry a
// byte-identical copy of this rendering because none of those three may
// import one another — but all three already import this leaf package, so
// hoisting the render here removes the duplication without an import cycle.
func FindingsError(mark Mark) error {
	found := Actionable(Since(mark))
	if len(found) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("fatal startup findings:")
	for _, f := range found {
		b.WriteString("\n  - " + f.Message)
		if f.FixIt != "" {
			b.WriteString(" (fix: " + f.FixIt + ")")
		}
	}
	return errors.New(b.String())
}

// Reset clears the collected findings — the process-wide log AND every
// goroutine's window (so no outstanding Mark from before the reset can still
// read stale data) — the FailOnce dedup set, and the checkpoint generation
// (test seam; the mode is left untouched — use SetDegraded).
func Reset() {
	mu.Lock()
	findings = nil
	generation = 0
	onceRecorded = map[string]struct{}{}
	mu.Unlock()

	windowsMu.Lock()
	for _, w := range windows {
		w.mu.Lock()
		w.findings = nil
		w.mu.Unlock()
	}
	windows = map[int64]*window{}
	windowsMu.Unlock()
}

// Fail reports a fatal-class fault at a choke. The warning line streams to
// stderr in BOTH modes (identical to the clidiag call it replaces, so command
// paths that never check findings keep today's diagnostics); in strict mode
// the finding is additionally recorded for the startup choke owner to abort
// on. fixit names the command or edit that resolves the fault ("" when the
// message already says).
func Fail(class Class, fixit, format string, args ...any) {
	msg := detailOr(class, fmt.Sprintf(format, args...))
	clidiag.Warn(prog, "%s", msg)
	record(class, fixit, msg, false, false)
}

// FailOnce is Fail with dedup on BOTH halves — but the two dedups have
// different scopes and are not interchangeable. The warning LINE is deduped
// process-wide and permanently (clidiag.WarnOnce), keyed on the rendered line.
// The FINDING is deduped only within the recording goroutine's current
// checkpoint window, and on class as well as message (see generation's doc):
// a fault re-fired in a LATER window must record again, or a session refused
// over it and retried unfixed opens silently on broken context. For chokes
// that re-fire per subsystem (e.g. an unresolvable profile parent hit by
// every loader build).
func FailOnce(class Class, fixit, format string, args ...any) {
	msg := detailOr(class, fmt.Sprintf(format, args...))
	clidiag.WarnOnce(prog, "%s", msg)
	record(class, fixit, msg, true, false)
}

// FailAlways is Fail for a fault whose HARM IS THE LAUNCH ITSELF: it records a
// NonDegradable finding, so the gates act on it under --degraded exactly as
// they do in strict mode.
//
// Reach for it only when proceeding causes the damage, never merely because a
// fault is serious. The test is the doctrine's: a profile that fails to parse
// is serious and still degradable -- the user gets a working LLM with less
// context. A container runtime that cannot provide the isolation it claimed is
// not, because the launch IS the exposure.
//
// Be sure the remedy is honest before using this. A fix-it that offers
// --degraded as its way out cannot belong to a NonDegradable finding: the
// escape hatch it names would not work, and a remedy the caller cannot follow
// is proof the check is firing outside its own design premise. Every existing
// ClassIsolation raise site fails that test today, which is why none of them
// uses this.
func FailAlways(class Class, fixit, format string, args ...any) {
	msg := detailOr(class, fmt.Sprintf(format, args...))
	clidiag.Warn(prog, "%s", msg)
	record(class, fixit, msg, false, true)
}

// Actionable returns the findings a gate must ACT on in the current mode: every
// finding in strict mode, and only the NonDegradable ones under --degraded.
//
// It is the single place the mode is consulted when deciding fatality, so the
// renderers and the class-filtered gates cannot drift into disagreeing about
// what --degraded means. A gate that filters by class should filter its own
// class and then pass the result through here, rather than testing Degraded()
// itself.
func Actionable(found []Finding) []Finding {
	if !Degraded() {
		return found
	}
	var out []Finding
	for _, f := range found {
		if f.NonDegradable {
			out = append(out, f)
		}
	}
	return out
}

// Record collects a finding WITHOUT printing anything — for chokes that
// already own their (richer) stderr reporting, e.g. the sync summary's
// per-item failure breakdown. Still collects in degraded mode: degraded
// suppresses fatality, not recording.
func Record(class Class, fixit, format string, args ...any) {
	record(class, fixit, detailOr(class, fmt.Sprintf(format, args...)), false, false)
}

// RecordOnce is Record with FailOnce's recording dedup: for a choke that owns
// its own reporting AND can be re-entered within one window, where the extra
// findings are copies of one problem rather than news. A config load is the
// case that motivated it — memoized, re-consulted from ~80 call sites, and
// re-reporting the same broken file each time, which turns one bad key into an
// abort block that lists it N times with N identical fix-its.
//
// The dedup is scoped exactly as FailOnce's is — to the recording goroutine's
// current checkpoint window, keyed on class AND message — so a fault that is
// still unfixed re-fires in the NEXT window. A process-wide dedup here would
// let a long-lived server refuse one session over a broken config and then open
// the next one silently on the same config.
func RecordOnce(class Class, fixit, format string, args ...any) {
	record(class, fixit, detailOr(class, fmt.Sprintf(format, args...)), true, false)
}

// detailOr substitutes a statement of what is known for a message that
// formatted to nothing. Every renderer of a Finding — FindingsError here,
// cli.formatFindings, operations.isolationGateErr — writes the message into a
// bullet unconditionally, so a blank message produces a bullet carrying a
// fix-it and no statement of what broke: a loud failure with an empty payload,
// which is exactly the shape this package exists to prevent. The class is the
// only thing still known at that point, so it is what the substitute reports.
// Applied at all three entry points rather than in record alone, so the
// streamed warning line and the collected finding always agree.
func detailOr(class Class, msg string) string {
	if strings.TrimSpace(msg) == "" {
		return fmt.Sprintf("unspecified %s failure: the choke reported no detail", class)
	}
	return msg
}

// record appends a finding in strict mode, honoring the FailOnce dedup set
// when once is set. The dedup key includes the current checkpoint generation,
// so the dedup collapses repeats WITHIN one window but never swallows a
// re-fire in a later window (see generation's doc). The finding lands in
// BOTH the process-wide log (All()/Reset()'s view) and the CALLING
// GOROUTINE's own window (Since()'s view) — never any other goroutine's
// window, which is the per-window ownership fix.
func record(class Class, fixit, msg string, once, nonDegradable bool) {
	// Fetch the recording goroutine's OWN window generation before
	// taking mu, so the FailOnce dedup key below is scoped to this
	// goroutine's last-checkpointed generation — never the live global
	// counter, which a different, concurrently-checkpointing goroutine may
	// have advanced past this one's value by the time this call runs.
	w := currentWindow()
	w.mu.Lock()
	gen := w.generation
	w.mu.Unlock()

	mu.Lock()
	if once {
		key := fmt.Sprintf("%d\x00%s\x00%s", gen, class, msg)
		if _, seen := onceRecorded[key]; seen {
			mu.Unlock()
			return
		}
		onceRecorded[key] = struct{}{}
	}
	f := Finding{Class: class, Message: msg, FixIt: fixit, NonDegradable: nonDegradable}
	findings = append(findings, f)
	mu.Unlock()

	w.mu.Lock()
	w.findings = append(w.findings, f)
	w.mu.Unlock()
}
