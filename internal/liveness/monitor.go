package liveness

import (
	"context"
	"sync"
	"time"
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
	// minutes clears that with margin. It is deliberately not tighter: where
	// CPU is observable, a burning process is rescued to StateSlow anyway, so
	// the cost of a generous value is only latency on a real stall, while the
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

	// CPUBurnFloor is how much CPU time must accumulate BETWEEN two samples
	// for the process to count as burning, expressed as a fraction of the
	// wall time between them. 0.02 (2% of one core) is above the noise of a
	// process merely being scheduled to service a timer or a heartbeat, and
	// far below anything a genuinely working engine consumes.
	CPUBurnFloor float64
}

// DefaultThresholds returns the tuned defaults. See each field for why.
func DefaultThresholds() Thresholds {
	return Thresholds{
		StartGrace:            5 * time.Minute,
		QuietGrace:            10 * time.Minute,
		RedeliveryMinRepeats:  3,
		RedeliveryJitterRatio: 0.25,
		CPUBurnFloor:          0.02,
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
	if t.CPUBurnFloor <= 0 {
		t.CPUBurnFloor = d.CPUBurnFloor
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
// start, a long silent tool call, a runtime whose CPU we cannot read). The
// blast radius of acting on a wrong answer is a working agent destroyed
// mid-task with its worktree possibly unmerged; the blast radius of a wrong
// REPORT is a human looking at a healthy agent. Those are not comparable, and
// automation is not worth the difference. If an acting path is ever added it
// must be off by default and justified separately — the reap decision belongs
// to whoever owns agent_stop semantics, not here.
//
// A Monitor is safe for concurrent use.
type Monitor struct {
	now       func() time.Time
	thr       Thresholds
	probes    []Probe
	readTx    func(string) (TranscriptStat, error)
	newestFS  func(string) (time.Time, bool, error)

	mu   sync.Mutex
	last map[string]cpuSample
}

// cpuSample is one prior CPU reading, kept so the NEXT assessment can
// difference it. A single absolute cumulative CPU number says nothing about
// whether anything is happening right now, which is why a first assessment
// never reaches a CPU-based stall verdict.
type cpuSample struct {
	at  time.Time
	cpu time.Duration
}

// New builds a Monitor.
func New(opts Options) *Monitor {
	m := &Monitor{
		now:      opts.Now,
		thr:      opts.Thresholds.normalize(),
		probes:   opts.Probes,
		readTx:   opts.ReadTranscript,
		newestFS: opts.NewestMTime,
		last:     make(map[string]cpuSample),
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.readTx == nil {
		m.readTx = ReadTranscript
	}
	if m.newestFS == nil {
		m.newestFS = NewestMTime
	}
	return m
}

// Thresholds returns the monitor's effective tuning (post-normalization), so
// a caller printing a verdict can also print what it was measured against.
func (m *Monitor) Thresholds() Thresholds { return m.thr }

// Assess reaches one verdict for t. It never returns an error: an observation
// that failed is reported as evidence that is absent, never as a silently
// healthy agent.
func (m *Monitor) Assess(ctx context.Context, t Target) Report {
	// STUB (red): the deliberately-wrong monitor — the one that always says
	// everything is fine. Replaced by the real ladder once the three-direction
	// test proves it fires.
	return Report{Harp: t.Harp, Agent: t.Agent, Runtime: t.Runtime, State: StateHealthy, Reason: "not implemented", At: m.now()}
}

// AssessAll assesses every target, preserving order.
func (m *Monitor) AssessAll(ctx context.Context, targets []Target) []Report {
	out := make([]Report, 0, len(targets))
	for _, t := range targets {
		out = append(out, m.Assess(ctx, t))
	}
	return out
}

// Watch polls enumerate every interval and hands each report to emit. It is
// the "who drives this" answer for a long-lived host: a poll, not an event
// stream, because the signals that matter are ABSENCES and nothing emits an
// event when nothing happens.
//
// Returns when ctx ends. emit is called synchronously on the polling
// goroutine, so a slow emit slows the poll rather than growing a queue.
func (m *Monitor) Watch(ctx context.Context, interval time.Duration, enumerate func() []Target, emit func(Report)) {
	if interval <= 0 || enumerate == nil || emit == nil {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, rep := range m.AssessAll(ctx, enumerate()) {
				emit(rep)
			}
		}
	}
}
