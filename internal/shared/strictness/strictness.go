// Package strictness owns ctxloom's fail-loudly mode. Startup faults are
// STRICT by default: each instrumented choke calls Fail/FailOnce/Record with a
// report.Kind and a remedy; the warning line still streams to stderr
// exactly as before (so no diagnostic is ever lost, whichever command path
// fired it), and in strict mode a fatal Finding is additionally collected. The
// startup choke owners (`ctxloom run`, `ctxloom mcp`) then check
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
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// prog stamps the warning lines Fail and its siblings print: they are the
// ctxloom binary's own legacy channel. A family binary that renders findings
// under its own name does so through Mode.Sink, never through these.
const prog = "ctxloom"

// ExitCodeFatalFindings is the process exit status of a strict-mode startup
// abort — the run refused to launch over the findings collected here. It lives
// in this package because this package owns the fatal-vs-warn decision; the CLI
// binds its exit-code table to it, and a READER of that status binds to it too.
//
// The reader is what makes it exported rather than a CLI-local literal: when
// ctxloom runs as a plugin INSIDE a container, its refusal reaches the host only
// as a process status, and the host must be able to tell "the inner ctxloom
// refused over config" from a genuine transport fault. See
// isolation.containerRunner.Diagnose, which reads it to say so.
const ExitCodeFatalFindings = 3

var (
	mu sync.Mutex
	// findings is the process-wide chronological log — every finding ever
	// recorded, in record order, across every goroutine. It backs ONLY All()
	// and Reset(); a Mark never indexes into it (see window below) — that
	// indirection through ONE shared slice was the concurrency defect.
	findings report.Findings
	// generation counts Checkpoint calls. onceRecorded keys FailOnce
	// recordings by generation+class+message, so the RECORDING dedup is
	// scoped to one checkpoint window: a long-lived server that opens many
	// sessions in one process and refuses one over a FailOnce finding must
	// see the SAME
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
	findings report.Findings
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
func Since(mark Mark) report.Findings {
	w := mark.w
	if w == nil {
		w = currentWindow()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if mark.idx >= len(w.findings) {
		return nil
	}
	out := make(report.Findings, len(w.findings)-mark.idx)
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
func All() report.Findings {
	mu.Lock()
	defer mu.Unlock()
	out := make(report.Findings, len(findings))
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

// Reset clears the collected findings — the process-wide log AND every
// goroutine's window (so no outstanding Mark from before the reset can still
// read stale data) — the FailOnce dedup set, and the checkpoint generation
// (test seam; the mode is a value the caller holds, not state here).
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

// Ledger records a fail-loudly report.Finding without rendering it. It is
// the one entry a rendering sink uses after it has written the text itself:
// the Finding's Once and NonDegradable carry the record's dedup and
// --degraded semantics. An advisory finding (empty Kind) is not a fault and
// is not recorded.
func Ledger(f report.Finding) {
	if !f.Fatal() {
		return
	}
	f.Text = detailOr(f.Kind, f.Text)
	record(f)
}

// Sink is the one place a core report.Finding becomes stderr text. The
// core returns or reports findings and never touches the process's
// diagnostic channel; the composition root builds this sink once, with the
// binary's own name, and hands it down. An advisory renders as the family's
// "<prog>: warning:" line (or the structured envelope when --format asked
// for one); a fail-loudly finding renders the same way, with its remedy as
// the fix line (or the envelope's remedy), and is additionally ledgered for
// the startup gate; a Quiet one is ledgered only.
//
// The streamed fix is shown even though an abort listing may repeat it:
// many Fail paths never reach a listing, and for those the stream is the
// only place the remedy is ever seen.
func Sink(prog string) report.Sink {
	return report.SinkFunc(func(f report.Finding) {
		if f.Fatal() {
			f.Text = detailOr(f.Kind, f.Text)
		}
		if !f.Quiet {
			if f.Once {
				clidiag.WarnRemedyOnce(prog, f.Remedy, "%s", f.Text)
			} else {
				clidiag.WarnRemedy(prog, f.Remedy, "%s", f.Text)
			}
		}
		Ledger(f)
	})
}

// Mode is the strictness posture ONE composition runs under: the program
// that renders its findings and whether --degraded waives the ordinary ones.
// It is a value the composition root builds from its flags and environment
// and hands down; two compositions in one process may differ, and neither
// can change the other's.
type Mode struct {
	Prog     string
	Degraded bool
}

// Actionable filters found to what this mode's gate must act on: everything
// in strict mode; under Degraded, only the NonDegradable findings.
func (m Mode) Actionable(found report.Findings) report.Findings {
	if !m.Degraded {
		return found
	}
	var out report.Findings
	for _, f := range found {
		if f.NonDegradable {
			out = append(out, f)
		}
	}
	return out
}

// Listing is the ONE rendering of a findings block: header, then one
// "[kind] text" bullet per finding, each followed by its clifmt.FixLine.
// It renders exactly the findings it is given — a gate filters with
// Actionable (or its own class filter) first — and "" when there are none.
// Every finding's remedy is printed: a listing that shows only some of them
// leaves the user to guess the rest.
func (m Mode) Listing(header string, found report.Findings) string {
	if len(found) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(header)
	for _, f := range found {
		b.WriteString("\n  - [" + string(f.Kind) + "] " + f.Text)
		b.WriteString(clifmt.FixLine("    ", f.Remedy))
	}
	return b.String()
}

// listingError is Listing as an error for a gate that returns rather than
// exits, or nil when found is empty. The listing already carries every
// remedy in its text; the error additionally names the fix structurally
// (report.Error.Fix, which clifmt.RenderError puts in the envelope) only
// when there is exactly ONE finding, because a single remedy field cannot
// honestly stand for several.
func (m Mode) listingError(header string, found report.Findings) error {
	if len(found) == 0 {
		return nil
	}
	e := report.Error{Msg: m.Listing(header, found)}
	if len(found) == 1 {
		e.Fix = found[0].Remedy
	}
	return e
}

// Sink is the rendering sink for this mode's program (see Sink).
func (m Mode) Sink() report.Sink { return Sink(m.Prog) }

// FindingsError renders the findings recorded since mark that this mode
// must act on as one error — "fatal startup findings:" followed by the
// Listing — or nil when nothing actionable was collected. It is the one
// owner of the per-call, keeps-running error-render variant (as opposed to
// a process-exit abort, which prints a phase-naming header and belongs to
// its own callers), so the adapters that need it share one rendering
// without importing one another.
func (m Mode) FindingsError(mark Mark) error {
	return m.listingError("fatal startup findings:", m.Actionable(Since(mark)))
}

// Fail reports a fatal-class fault at a choke. The warning line streams to
// stderr in BOTH modes (identical to the clidiag call it replaces, so command
// paths that never check findings keep today's diagnostics); in strict mode
// the finding is additionally recorded for the startup choke owner to abort
// on. remedy names the command or edit that resolves the fault ("" when the
// message already says); it streams as the warning's fix line.
func Fail(kind report.Kind, remedy, format string, args ...any) {
	Sink(prog).Report(report.Failf(kind, remedy, format, args...))
}

// FailOnce is Fail with dedup on BOTH halves — but the two dedups have
// different scopes and are not interchangeable. The warning LINE is deduped
// process-wide and permanently (clidiag.WarnOnce), keyed on the rendered line.
// The FINDING is deduped only within the recording goroutine's current
// checkpoint window, and on kind as well as text (see generation's doc):
// a fault re-fired in a LATER window must record again, or a session refused
// over it and retried unfixed opens silently on broken context. For chokes
// that re-fire per subsystem (e.g. an unresolvable profile parent hit by
// every loader build).
func FailOnce(kind report.Kind, remedy, format string, args ...any) {
	Sink(prog).Report(report.FailOncef(kind, remedy, format, args...))
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
// Be sure the remedy is honest before using this. A remedy that offers
// --degraded as its way out cannot belong to a NonDegradable finding: the
// escape hatch it names would not work, and a remedy the caller cannot follow
// is proof the check is firing outside its own design premise.
func FailAlways(kind report.Kind, remedy, format string, args ...any) {
	Sink(prog).Report(report.FailAlwaysf(kind, remedy, format, args...))
}

// Record collects a finding WITHOUT printing anything — for chokes that
// already own their (richer) stderr reporting, e.g. the sync summary's
// per-item failure breakdown. Still collects in degraded mode: degraded
// suppresses fatality, not recording.
func Record(kind report.Kind, remedy, format string, args ...any) {
	Sink(prog).Report(report.Recordf(kind, remedy, format, args...))
}

// RecordOnce is Record with FailOnce's recording dedup: for a choke that owns
// its own reporting AND can be re-entered within one window, where the extra
// findings are copies of one problem rather than news. A config load is the
// case that motivated it — memoized, re-consulted from ~80 call sites, and
// re-reporting the same broken file each time, which turns one bad key into an
// abort block that lists it N times with N identical remedies.
//
// The dedup is scoped exactly as FailOnce's is — to the recording goroutine's
// current checkpoint window, keyed on kind AND text — so a fault that is
// still unfixed re-fires in the NEXT window. A process-wide dedup here would
// let a long-lived server refuse one session over a broken config and then open
// the next one silently on the same config.
func RecordOnce(kind report.Kind, remedy, format string, args ...any) {
	Sink(prog).Report(report.RecordOncef(kind, remedy, format, args...))
}

// detailOr substitutes a statement of what is known for a message that
// formatted to nothing. Every renderer of a Finding writes the text into a
// bullet unconditionally, so a blank text produces a bullet carrying a fix
// and no statement of what broke: a loud failure with an empty payload,
// which is exactly the shape this package exists to prevent. The kind is
// the only thing still known at that point, so it is what the substitute
// reports. Applied in Sink before rendering as well as in Ledger, so the
// streamed warning line and the collected finding always agree.
func detailOr(kind report.Kind, msg string) string {
	if strings.TrimSpace(msg) == "" {
		return fmt.Sprintf("unspecified %s failure: the choke reported no detail", kind)
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
func record(f report.Finding) {
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
	if f.Once {
		key := fmt.Sprintf("%d\x00%s\x00%s", gen, f.Kind, f.Text)
		if _, seen := onceRecorded[key]; seen {
			mu.Unlock()
			return
		}
		onceRecorded[key] = struct{}{}
	}
	findings = append(findings, f)
	mu.Unlock()

	w.mu.Lock()
	w.findings = append(w.findings, f)
	w.mu.Unlock()
}
