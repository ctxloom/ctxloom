// Package liveness answers ONE question the rest of ctxloom could not answer
// on 2026-07-24, when two delegated agents ran for the better part of an hour
// doing nothing while every signal said they were fine: is this agent making
// PROGRESS, or is it looping?
//
// Every cheap signal the coordinator already had lied. agent_run returned
// valid ids. The roster reported phase "executing". The worktree existed. The
// transcript file existed and was GROWING — with the same composed-context
// block re-appended every ~4-5 seconds, seq pinned at 0, 100% `user` entries,
// zero assistant turns. Nothing in the system could tell "working" from
// "looping".
//
// # The progress definition
//
// This package implements a SHARED definition, supplied to it rather than
// invented by it (a docker-gated progress-asserting acceptance test is built
// against the same words, so the monitor and the test cannot drift into two
// different notions of "alive"). An agent is making PROGRESS when its
// canonical transcript (internal/transcript, one JSON envelope per line at
// paths.HarpCanonicalTranscriptPath) shows all of:
//
//   - seq strictly advances past 0. Seq starts at 0 with no gaps for the
//     lifetime of ONE transcript.Recorder (record.go's Seq doc); the file is
//     opened O_APPEND, so a fresh Recorder — i.e. a RELAUNCH — restarts the
//     numbering. Many records that are ALL seq 0 therefore means every line
//     came from a different Recorder instance: the relaunch/re-delivery loop,
//     verbatim. This is the sharpest single signal in the package.
//   - at least one `entry` record with .entry.type == "assistant".
//   - entry-type VARIETY: more than one distinct .entry.type across records
//     where .kind == "entry". The failing agents were 100% `user`.
//   - no identical content re-appended on a fixed cadence. Records group by
//     (.entry.type, .entry.content); .ts is RECEIPT time (record.go's TS doc),
//     so a re-delivery cadence is directly measurable from it.
//
// Two further clauses are part of the same definition:
//
//   - A MISSING transcript file is a FAILURE SIGNAL, not an error. The
//     recorder opens its file lazily on the first successful Record
//     (recorder.go's NewRecorder doc), so a zero-event chat leaves no file at
//     all — "absent" means "this agent has emitted literally nothing", which
//     is information, not a read failure.
//   - An agent parked awaiting APPROVAL is NOT stalled and must never be
//     reaped. An approval rung (internal/agentcoord/coord/approval.go,
//     ladder.go) can legitimately hold a child for minutes.
//
// # Three states that must never be conflated
//
//	DIED              the process/container is gone with no `complete` for the
//	                  in-flight turn.
//	STALLED           alive, seq not advancing, no recent tool_use, and — where
//	                  observable — no CPU burn.
//	LEGITIMATELY SLOW alive and burning CPU, or parked awaiting approval. A spin
//	                  loop looks different from a hang; a long think is not a stall.
//
// # The design caution this package is built around
//
// The monitor must not itself become a silent no-op. That is the exact
// failure family this whole release cluster is about: every layer reported
// success to the layer above while doing nothing underneath. A monitor that
// always reports healthy is WORSE than no monitor, because it launders the
// failure and hands out false confidence. Hence monitor_test.go's
// three-direction proof — it FIRES on a genuinely stuck agent, does NOT fire
// on a healthy one, and does NOT fire on one parked awaiting approval — and
// hence Report.Reason, which is required to be non-empty for every verdict so
// a caller can always print WHY.
//
// # Reports only
//
// Nothing here cancels, kills, or reaps. See Monitor's doc for the
// justification.
package liveness

import "time"

// State is the monitor's verdict for one agent.
type State string

const (
	// StateUnknown is the zero value: no verdict was reached. It never fires.
	StateUnknown State = "unknown"
	// StateStarting is "too early to judge" — inside the launch grace, with
	// nothing on disk yet. Distinct from StateHealthy on purpose: "we have no
	// evidence" and "we have evidence of life" must not read the same.
	StateStarting State = "starting"
	// StateHealthy is positive evidence of progress.
	StateHealthy State = "healthy"
	// StateSlow is LEGITIMATELY SLOW: alive and burning CPU, or alive with a
	// CPU baseline not yet established. Quiet, but not stuck.
	StateSlow State = "slow"
	// StateAwaitingApproval is parked on an approval rung. It outranks every
	// stall rule below it — a child waiting on a human must NEVER be reaped.
	StateAwaitingApproval State = "awaiting_approval"
	// StateStalled is the incident state: alive, producing no progress.
	StateStalled State = "stalled"
	// StateDied is gone with an unfinished turn.
	StateDied State = "died"
)

// Firing reports whether s is a state an operator must be told about
// unprompted. Only the two genuine failures fire; "slow" and
// "awaiting_approval" deliberately do not, because crying wolf over a long
// think is how a monitor gets ignored.
func (s State) Firing() bool { return s == StateStalled || s == StateDied }

// Target is one agent the monitor is asked about. The caller (the coordinator
// adapter, or a test) fills in what IT knows; every field is optional and the
// monitor degrades to the evidence it actually has rather than assuming.
type Target struct {
	// Harp is the ctxloom session id — the agent's address and this report's
	// key. Required.
	Harp string
	// Agent is the configured agent name, for the human-readable reason only.
	Agent string
	// Runtime is the agent's runtime axis ("host", "container", ""). It
	// selects which Probe(s) apply — evidence of life differs between a host
	// process and a container, which is why the process layer is an interface
	// and not a function.
	Runtime string
	// StartedAt is when this run was enqueued. The launch grace is measured
	// from here; zero disables every age-gated rule (the monitor will not
	// invent an age it does not know).
	StartedAt time.Time
	// LastActivity is the coordinator's own fold timestamp for the run — a
	// third activity clock alongside the transcript's and the filesystem's.
	LastActivity time.Time
	// RosterState is the coordinator's §6a state string (queued/executing/
	// parked/idle/ended), carried for the reason text only. The monitor never
	// branches on it — the roster reporting "executing" while an agent looped
	// is precisely the lie this package exists to catch.
	RosterState string
	// AwaitingApproval marks a child parked on an approval rung (or, more
	// conservatively, parked at all). It short-circuits to
	// StateAwaitingApproval before any stall rule runs.
	AwaitingApproval bool
	// Ended marks a run the coordinator has already terminated. The monitor
	// still assesses it, to distinguish a clean end from a death mid-turn.
	Ended bool
	// TranscriptPath is paths.HarpCanonicalTranscriptPath(Harp). Empty skips
	// transcript evidence entirely (and with it every sharp rule).
	TranscriptPath string
	// WorkDir is the agent's worktree, for filesystem-mtime evidence. Empty
	// skips it.
	WorkDir string
	// PID is the host-side process, when the caller knows one (0 = unknown).
	PID int
	// ContainerID is the container, when the caller knows one.
	ContainerID string
}

// Report is one verdict. Reason is ALWAYS non-empty — a verdict a human
// cannot audit is the same false confidence this package exists to remove.
type Report struct {
	Harp     string   `json:"harp"`
	Agent    string   `json:"agent,omitempty"`
	Runtime  string   `json:"runtime,omitempty"`
	State    State    `json:"state"`
	Reason   string   `json:"reason"`
	Evidence Evidence `json:"evidence"`
	At       time.Time
}

// Firing is Report.State.Firing.
func (r Report) Firing() bool { return r.State.Firing() }

// Evidence is everything the monitor looked at to reach a verdict — attached
// to the Report so a disagreement is arguable from the record rather than
// from a re-run.
type Evidence struct {
	Transcript TranscriptStat `json:"transcript"`
	// TranscriptError is the reader's failure text when the transcript could
	// not be observed (TranscriptStat.Observed false because the read broke,
	// rather than because nobody asked). Carried into the reason so an
	// operator sees WHY the monitor has nothing to say, instead of a
	// confident verdict manufactured from the failure.
	TranscriptError string    `json:"transcript_error,omitempty"`
	Proc            ProcState `json:"proc"`
	// FSMTime is the newest mtime under Target.WorkDir; FSObserved says
	// whether the walk succeeded at all.
	FSMTime    time.Time `json:"fs_mtime,omitempty"`
	FSObserved bool      `json:"fs_observed,omitempty"`
	// Age is now-StartedAt (zero when StartedAt is unknown).
	Age time.Duration `json:"age,omitempty"`
	// Quiet is now minus the NEWEST of every activity clock the monitor has
	// (transcript ts, filesystem mtime, coordinator LastActivity). Zero when
	// no clock is known at all.
	Quiet time.Duration `json:"quiet,omitempty"`
	// QuietObserved distinguishes "quiet for 0s" from "no activity clock at
	// all" — the same absent-vs-zero distinction the rest of this package is
	// pedantic about.
	QuietObserved bool `json:"quiet_observed,omitempty"`
}
