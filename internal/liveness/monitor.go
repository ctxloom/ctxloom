package liveness

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Thresholds are every number the verdict ladder depends on. They live in one
// exported struct, with the reasoning for each default written down next to
// it, because an unsigned magic number is how a monitor becomes untrustworthy:
// the first time someone disagrees with a verdict they must be able to see
// what was assumed and change it, not guess.
type Thresholds struct {
	// StartGrace is how long after StartedAt the monitor refuses to reach any
	// ABSENCE-based verdict (no transcript, no assistant entry, no variety).
	//
	// Derived, not chosen: it matches the coordinator's own dial-home budget
	// for one launch attempt (coord.defaultRunnerAwaitTimeout, widened to 5m
	// on 2026-07-24 precisely because a slow-but-successful container start
	// under host contention was being declared a failure). Firing a stall
	// verdict before the LAUNCH path itself would give up is a guaranteed
	// false positive, so this can never be shorter than that budget.
	//
	// Note what it does NOT gate: the two POSITIVE-evidence rules (a
	// re-delivery cadence, and seq pinned at 0 across many records) fire
	// immediately regardless of age, because they are observations of a loop
	// happening rather than inferences from silence.
	StartGrace time.Duration

	// QuietGrace is how long every activity clock (transcript receipt times,
	// worktree mtime, the coordinator's own LastActivity) may stand still
	// before silence becomes suspicious.
	//
	// The floor is set by the longest legitimately SILENT single operation an
	// agent performs. A tool_use entry is recorded when the call STARTS, so a
	// ten-minute test run shows one record and then nothing until it
	// finishes; this project's own acceptance suite is minutes long. 10
	// minutes clears that with margin. It is deliberately not tighter: an
	// agent writing files is rescued by the worktree clock anyway, so the
	// cost of a generous value is only latency on a real stall, while the
	// cost of a tight one is crying wolf over every long build.
	QuietGrace time.Duration

	// RedeliveryMinRepeats is how many identical (entry type, content)
	// records constitute a loop. Two can happen honestly — a retry, a user
	// pasting the same thing twice. Three identical bodies on a metronome do
	// not happen by accident; at the incident's ~4-5s cadence this is reached
	// in about 15 seconds, which is why the loop is caught in seconds rather
	// than in the hour it actually took.
	RedeliveryMinRepeats int

	// RedeliveryJitterRatio is how far consecutive gaps may stray from their
	// median and still count as a fixed CADENCE, as a fraction of that
	// median. 0.25 accepts a machine loop whose period wobbles with load
	// (the incident's own "every ~4-5 seconds" is ±11% around 4.5s) while
	// rejecting anything paced by a model's variable thinking time.
	RedeliveryJitterRatio float64
}

// DefaultThresholds returns the tuned defaults. See each field for why.
func DefaultThresholds() Thresholds {
	return Thresholds{
		StartGrace:            5 * time.Minute,
		QuietGrace:            10 * time.Minute,
		RedeliveryMinRepeats:  3,
		RedeliveryJitterRatio: 0.25,
	}
}

// normalize fills unset (zero) fields from the defaults so a caller can
// override one threshold without restating the rest — and so a zero-value
// Thresholds can never silently degrade the ladder into firing on everything.
func (t Thresholds) normalize() Thresholds {
	d := DefaultThresholds()
	if t.StartGrace <= 0 {
		t.StartGrace = d.StartGrace
	}
	if t.QuietGrace <= 0 {
		t.QuietGrace = d.QuietGrace
	}
	if t.RedeliveryMinRepeats <= 0 {
		t.RedeliveryMinRepeats = d.RedeliveryMinRepeats
	}
	if t.RedeliveryJitterRatio <= 0 {
		t.RedeliveryJitterRatio = d.RedeliveryJitterRatio
	}
	return t
}

// Options configures a Monitor. Every seam is injectable so the ladder can be
// tested against constructed evidence without a filesystem, a clock, or a
// process — which is what makes the three-direction proof in monitor_test.go
// deterministic rather than a timing race.
type Options struct {
	// Now overrides the clock (nil = time.Now).
	Now func() time.Time
	// Thresholds overrides the tuning; zero fields fall back to defaults.
	Thresholds Thresholds
	// Probes are the runtime-polymorphic process evidence sources. Nil means
	// no process evidence at all — the transcript rules still apply, but no
	// DIED verdict is ever reached.
	Probes []Probe
	// ReadTranscript overrides the transcript reader (nil = ReadTranscript).
	ReadTranscript func(path string) (TranscriptStat, error)
	// NewestMTime overrides the filesystem evidence (nil = NewestMTime).
	NewestMTime func(root string) (time.Time, bool, error)
}

// Monitor decides whether agents are making progress. It REPORTS ONLY.
//
// Nothing here cancels, stops, kills, or reaps, and that is a decision rather
// than an omission. A stall verdict is an inference from absence, and every
// absence-based rule in the ladder has a false-positive mode (a slow container
// start, a long silent tool call, a runtime no probe can see). The
// blast radius of acting on a wrong answer is a working agent destroyed
// mid-task with its worktree possibly unmerged; the blast radius of a wrong
// REPORT is a human looking at a healthy agent. Those are not comparable, and
// automation is not worth the difference. If an acting path is ever added it
// must be off by default and justified separately — the reap decision belongs
// to whoever owns agent_stop semantics, not here.
//
// A Monitor is safe for concurrent use.
type Monitor struct {
	now      func() time.Time
	thr      Thresholds
	probes   []Probe
	readTx   func(string) (TranscriptStat, error)
	newestFS func(string) (time.Time, bool, error)
}

// New builds a Monitor.
func New(opts Options) *Monitor {
	m := &Monitor{
		now:      opts.Now,
		thr:      opts.Thresholds.normalize(),
		probes:   opts.Probes,
		readTx:   opts.ReadTranscript,
		newestFS: opts.NewestMTime,
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.readTx == nil {
		thr := m.thr
		m.readTx = func(p string) (TranscriptStat, error) { return ReadTranscriptWith(p, thr) }
	}
	if m.newestFS == nil {
		m.newestFS = NewestMTime
	}
	return m
}

// Assess reaches one verdict for t. It never returns an error: an observation
// that failed is reported as evidence that is ABSENT (Evidence's *Observed
// bits), never as a silently healthy agent.
//
// The ladder below is ordered first-match-wins, and the order is the design:
//
//  0. Approval park — outranks everything. A child on an approval rung must
//     never be called stalled, from either the coordinator's word or the
//     transcript's own trailing permission record.
//  1. Death — the process is observably gone. Split by whether the in-flight
//     turn ever closed, so a clean end is not reported as a death.
//     2/3. POSITIVE evidence of a loop — a re-delivery cadence, or seq pinned at
//     0 across many records. These are observations of a loop HAPPENING, not
//     inferences from silence, so they carry no grace period and fire in
//     seconds rather than in the hour the incident actually took.
//  4. Launch grace — below it, absence proves nothing.
//     5..8. ABSENCE-based rules, every one of them age- and quiet-gated.
//
// Everything that can rescue a target to a non-firing verdict is checked
// before everything that can condemn it, at each rung.
func (m *Monitor) Assess(ctx context.Context, t Target) Report {
	now := m.now()
	ev := m.gather(ctx, t, now)
	state, reason := m.verdict(t, ev, now)
	return Report{
		Harp: t.Harp, Agent: t.Agent, Runtime: t.Runtime,
		State: state, Reason: reason, Evidence: ev, At: now,
	}
}

// rung is one step of the ladder. It returns a state and its reason, or an
// empty state to defer to the next rung. Each rung is its own named function
// so the ORDER is visible in one place (verdict, below) and no single rung can
// grow into an unreviewable pile of conditions.
type rung func(t Target, ev Evidence, now time.Time) (State, string)

func (m *Monitor) verdict(t Target, ev Evidence, now time.Time) (State, string) {
	for _, r := range []rung{
		m.approvalRung,
		m.deathRung,
		m.endedRung,
		m.loopRung,
		m.graceRung,
		m.quietRung,
	} {
		if s, reason := r(t, ev, now); s != "" {
			return s, reason
		}
	}
	tx := ev.Transcript
	if !ev.QuietObserved {
		return StateUnknown, "no activity clock available (no record timestamps, no worktree mtime, no coordinator activity)"
	}
	return StateHealthy, fmt.Sprintf("seq %d, %d assistant turn(s), entry types: %s", tx.MaxSeq, tx.AssistantEntries, joinOrNone(tx.EntryTypes))
}

// approvalRung: AWAITING APPROVAL outranks everything. Two independent
// sources, either sufficient — getting this wrong turns a working system into
// one that kills its own children, so whichever half is wired up wrongly must
// not be able to cause that on its own.
func (m *Monitor) approvalRung(t Target, ev Evidence, _ time.Time) (State, string) {
	if t.AwaitingApproval {
		return StateAwaitingApproval, fmt.Sprintf("parked on an approval rung (roster state %q) — an approval can legitimately hold a child for minutes", orUnknown(t.RosterState))
	}
	if ev.Transcript.PendingPermission {
		return StateAwaitingApproval, "the transcript's last record is a permission request with nothing after it — waiting on a decision"
	}
	return "", ""
}

// deathRung: observably gone. Requires POSITIVE observation of absence — a
// probe that could not see the target says nothing, and "nothing" is never
// promoted to "dead". Split by whether the in-flight turn closed, so a clean
// end is never reported as a death.
func (m *Monitor) deathRung(_ Target, ev Evidence, _ time.Time) (State, string) {
	if !ev.Proc.Observed || ev.Proc.Alive {
		return "", ""
	}
	tx := ev.Transcript
	if tx.TurnClosed {
		return StateHealthy, fmt.Sprintf("the run ended after a completed turn (%d complete record(s)) — a clean end, not a death", tx.Completes)
	}
	if !tx.Observed {
		// The runtime being gone is a POSITIVE observation and still fires;
		// the turn boundary is not, so the reason must not claim one.
		return StateDied, fmt.Sprintf("the runtime is gone and the transcript could not be read, so a clean end cannot be ruled out [%s]",
			orUnknown(ev.Proc.Detail))
	}
	return StateDied, fmt.Sprintf("the runtime is gone with no `complete` for the in-flight turn (%d record(s), %d complete) [%s]",
		tx.Records, tx.Completes, orUnknown(ev.Proc.Detail))
}

// endedRung: a run the coordinator has ALREADY terminated cannot be stalled.
//
// Target.Ended used to be passed in and read by nothing (U056-F03), while the
// coordinator's roster fold is never pruned and reapEndedRuns never reaps a
// harp's current run — so every cleanly-finished child kept being enumerated,
// went quiet (because it was done), lost its runner (so no probe could see
// it), and was condemned ten minutes later. The watchdog cried wolf on every
// success, which is precisely how a monitor gets ignored.
//
// It sits BELOW deathRung on purpose: a run that ended with its runtime
// observably gone and its turn still open is a death, and saying so is the
// whole point of the rung above.
func (m *Monitor) endedRung(t Target, ev Evidence, _ time.Time) (State, string) {
	if !t.Ended {
		return "", ""
	}
	tx := ev.Transcript
	if tx.Observed && tx.TurnClosed {
		return StateHealthy, fmt.Sprintf("the coordinator has ended this run and its last turn closed (%d complete record(s)) — a clean end", tx.Completes)
	}
	return StateUnknown, "the coordinator has ended this run; a terminated run makes no further progress, so there is nothing left to judge"
}

// loopRung: POSITIVE evidence of a loop. Both rules here are observations of a
// loop HAPPENING rather than inferences from silence, so neither carries a
// grace period — which is what turns "an hour of nothing" into "seconds".
func (m *Monitor) loopRung(_ Target, ev Evidence, _ time.Time) (State, string) {
	tx := ev.Transcript
	if r := tx.Redelivery; r != nil && r.Repeats >= m.thr.RedeliveryMinRepeats {
		return StateStalled, fmt.Sprintf("the same %s content was re-delivered %d times on a %s cadence (max deviation %s) — a re-delivery loop, not work: %q",
			r.EntryType, r.Repeats, r.Cadence.Round(time.Millisecond), r.MaxDeviation.Round(time.Millisecond), r.Sample)
	}
	// Seq restarts per Recorder against an O_APPEND file, so many records that
	// never advanced past 0 means many recorders — many relaunches — not one
	// conversation.
	if tx.SeqPinned {
		return StateStalled, fmt.Sprintf("seq is pinned at 0 across %d records — every line came from a fresh recorder, i.e. a relaunch loop (entry types: %s, assistant turns: %d)",
			tx.Records, joinOrNone(tx.EntryTypes), tx.AssistantEntries)
	}
	return "", ""
}

// graceRung: below the launch grace, absence proves nothing — the coordinator
// itself waits StartGrace for a runner to dial home, so firing before the
// launch path would give up is a guaranteed false positive. Past it, no
// transcript at all means the engine emitted zero events (the recorder creates
// the file on its first record), which is a stall.
func (m *Monitor) graceRung(t Target, ev Evidence, _ time.Time) (State, string) {
	// A transcript that could not be OBSERVED is a broken instrument, not an
	// engine that emitted zero events, and every rule below this line reasons
	// from absence. Answering "unknown" with the failure attached is the only
	// honest verdict available (U056-F01/F05).
	if !ev.Transcript.Observed {
		if ev.TranscriptError != "" {
			return StateUnknown, "the transcript could not be read, so there is no evidence to judge progress by: " + ev.TranscriptError
		}
		return StateUnknown, "no transcript was looked at for this run (no path resolved), so there is no evidence to judge progress by"
	}
	if ev.Transcript.Exists {
		return "", ""
	}
	if t.StartedAt.IsZero() {
		return StateUnknown, "no transcript and no start time — nothing to measure progress against"
	}
	if ev.Age < m.thr.StartGrace {
		return StateStarting, fmt.Sprintf("launched %s ago, no transcript yet — still inside the %s launch grace", ev.Age.Round(time.Second), m.thr.StartGrace)
	}
	return StateStalled, fmt.Sprintf("no canonical transcript exists %s after launch — the engine has emitted zero events (the recorder creates the file on its first record)", ev.Age.Round(time.Second))
}

// quietRung: everything gated on the target being silent on EVERY clock past
// the quiet grace. Recent activity anywhere rescues first, so a working agent
// can never be condemned by a definition clause it merely has not satisfied
// yet.
func (m *Monitor) quietRung(t Target, ev Evidence, now time.Time) (State, string) {
	if !ev.QuietObserved {
		return "", ""
	}
	quiet := ev.Quiet
	if quiet < m.thr.QuietGrace {
		tx := ev.Transcript
		return StateHealthy, fmt.Sprintf("active %s ago (seq %d, %d assistant turn(s), entry types: %s)",
			quiet.Round(time.Second), tx.MaxSeq, tx.AssistantEntries, joinOrNone(tx.EntryTypes))
	}
	// Every rule below rests on the target being silent EVERYWHERE, and the
	// worktree is one of the everywheres. A walk that was denied could be
	// hiding a stream of writes, so "quiet on every clock" is not a sentence
	// the monitor is entitled to say — exactly as with an unreadable
	// transcript in graceRung (U056-F08).
	if ev.FSError != "" {
		return StateUnknown, fmt.Sprintf("quiet for %s on the clocks that could be read, but the worktree could not be walked, so filesystem activity cannot be ruled out: %s",
			quiet.Round(time.Second), ev.FSError)
	}
	// Everything from here down is an ABSENCE-based verdict, and StartGrace is
	// defined as how long the monitor refuses to reach one. That gate needs an
	// age, and Target.StartedAt's contract is that a zero value withholds it:
	// the monitor does not invent an age it does not know.
	if t.StartedAt.IsZero() {
		return StateUnknown, fmt.Sprintf("quiet for %s, but this run has no start time, so the launch grace cannot be applied and silence cannot yet be read as a stall",
			quiet.Round(time.Second))
	}
	if ev.Age < m.thr.StartGrace {
		return StateStarting, fmt.Sprintf("quiet for %s but launched only %s ago — still inside the %s launch grace, where absence proves nothing",
			quiet.Round(time.Second), ev.Age.Round(time.Second), m.thr.StartGrace)
	}
	if s, reason := m.contentRung(ev, quiet); s != "" {
		return s, reason
	}
	return StateStalled, fmt.Sprintf("quiet for %s on every clock (last tool_use %s) — no transcript record, %s, no coordinator activity",
		quiet.Round(time.Second), agoOrNever(ev.Transcript.LastToolUse, now), fsClause(t, ev))
}

// fsClause says what the monitor actually saw of the worktree, which is three
// states and not two: nobody gave it a path, it walked and found nothing
// countable, or it walked and everything it found was old. Only the last is an
// absence of writes; reporting the other two as one was how a reason claimed
// an observation that had never been made.
func fsClause(t Target, ev Evidence) string {
	switch {
	case t.WorkDir == "":
		return "no worktree was looked at"
	case !ev.FSObserved:
		return "no countable file in the worktree"
	default:
		return "no worktree write"
	}
}

// contentRung applies the shared definition's two CONTENT clauses — at least
// one assistant entry, and entry-type variety — but only to a target that is
// ALSO silent. An agent visibly doing things is not stalled just because it
// has not spoken yet.
func (m *Monitor) contentRung(ev Evidence, quiet time.Duration) (State, string) {
	tx := ev.Transcript
	if tx.AssistantEntries == 0 {
		return StateStalled, fmt.Sprintf("quiet for %s with zero assistant turns in %d records (entry types: %s) — the engine has never produced a turn",
			quiet.Round(time.Second), tx.Records, joinOrNone(tx.EntryTypes))
	}
	if tx.EntryRecords >= 3 && len(tx.EntryTypes) <= 1 {
		return StateStalled, fmt.Sprintf("quiet for %s with %d entries of a single type (%s) — no entry-type variety",
			quiet.Round(time.Second), tx.EntryRecords, joinOrNone(tx.EntryTypes))
	}
	return "", ""
}

// gather collects every piece of evidence, in cheapest-first order. A failed
// observation lands as an absent one (with the failure in the Detail/Reason
// text), never as a silently favourable one.
func (m *Monitor) gather(ctx context.Context, t Target, now time.Time) Evidence {
	ev := Evidence{}
	if t.TranscriptPath != "" {
		st, err := m.readTx(t.TranscriptPath)
		ev.Transcript = st
		if err != nil {
			// A broken READ is not an observation of stillness. Observed is
			// forced false here as well as in the reader, because readTx is
			// an injectable seam and a seam that reported an error while
			// leaving Observed set would put the ladder right back to
			// condemning on a failure (U056-F01).
			ev.Transcript.Observed = false
			ev.TranscriptError = err.Error()
			clidiag.Warn("ctxloom", "liveness: %s: read transcript: %v", t.Harp, err)
		}
	}
	if t.WorkDir != "" {
		mt, ok, err := m.newestFS(t.WorkDir)
		if err != nil {
			// Same rule as the transcript above: a walk that FAILED is not a
			// worktree that was still. The failure is carried on the evidence
			// so the ladder can decline to judge instead of counting a
			// missing clock as a silent one (U056-F08).
			ev.FSError = err.Error()
			clidiag.Warn("ctxloom", "liveness: %s: walk worktree: %v", t.Harp, err)
		}
		ev.FSMTime, ev.FSObserved = mt, ok
	}
	ev.Proc = mergeProbes(ctx, m.probes, t)
	if !t.StartedAt.IsZero() {
		ev.Age = now.Sub(t.StartedAt)
	}
	// Quiet is measured against the NEWEST of every clock available: an agent
	// is only silent if it is silent everywhere.
	var newest time.Time
	for _, c := range []time.Time{ev.Transcript.LastTS, ev.FSMTime, t.LastActivity} {
		if c.After(newest) {
			newest = c
		}
	}
	if !newest.IsZero() {
		ev.QuietObserved = true
		if q := now.Sub(newest); q > 0 {
			ev.Quiet = q
		}
	}
	return ev
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func joinOrNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ",")
}

func agoOrNever(at time.Time, now time.Time) string {
	if at.IsZero() {
		return "never"
	}
	return now.Sub(at).Round(time.Second).String() + " ago"
}

// AssessAll assesses every target, preserving order.
func (m *Monitor) AssessAll(ctx context.Context, targets []Target) []Report {
	out := make([]Report, 0, len(targets))
	for _, t := range targets {
		out = append(out, m.Assess(ctx, t))
	}
	return out
}

// Watch was a self-contained poll loop; deleted (U056-F10) as test-only and
// duplicated by the real production driver, coord.livenessWatchdog
// (internal/agentcoord/coord/liveness.go), which has its own ticker plus the
// transition-suppression and Forget-reaping this one lacked.
