package coord

import (
	"encoding/json"
	"sort"
	"time"
)

// Child states on the §6a roster (the wire vocabulary of
// agentbus.RosterEntry.State, preserved).
const (
	StateQueued    = "queued"
	StateExecuting = "executing"
	StateParked    = "parked"
	StateIdle      = "idle"
	StateEnded     = "ended"
)

// RunRecord is the run registry's view of one run attempt.
type RunRecord struct {
	RunID      string
	Harp       string
	Agent      string
	ParentHarp string
	// ParentRunID is the SPAWNING run's run_id — durable delegation lineage,
	// set only when the caller has a run of its own to be a parent of. A
	// plugin-hosted top-level session's own credential carries no run id, so
	// a child it spawns directly leaves this empty; a container top-level
	// session's owned run (StartOwnedRun) DOES carry a run id from the
	// moment it starts, even at depth 0, so a child it spawns has this set —
	// as does any child spawned by an already-delegated child. Mirrors
	// StartRun/RunStarted.parent_run_id on the read-side roster projection
	// (ListRunsResult.RunInfo).
	ParentRunID string
	Runtime     string
	CredHash    string
	Depth       int
	// OneShot mirrors Identity.OneShot — see its doc.
	OneShot bool
	Prompt  string
	State   string
	Ended   bool
	Cause   string
	// Detail is the terminal fact's human-readable detail (runEnded.Detail):
	// for an agent_stop, the stopping session plus the caller's `reason`
	// (`reason` used to be advertised to the model and discarded).
	// Empty when the terminal cause carried no detail.
	Detail           string
	HarnessSessionID string
	// Resumable is the run engine's LIVE resume capability (factRunResumable,
	// from ChatSessionInfo.Resumable) — the one-shot gate's live half. A run
	// tears its engine down at a turn boundary only when this is true AND its
	// plan resolved ResumeModeOneShot AND HarnessSessionID is captured.
	Resumable    bool
	EnqueuedAt   time.Time
	LastActivity time.Time
	// Ladder is the run's resolved escalation ladder (Wave C2), fixed at
	// enqueue.
	Ladder Ladder
	// Permission is the run's resolved permission mode's kind name (Wave
	// F1), fixed at enqueue — mirrors Ladder; see runEnqueued.Permission.
	Permission string
	// MCPServers is the run's resolved MCP server NAMES ONLY (Wave F1),
	// fixed at enqueue — mirrors Ladder; see runEnqueued.MCPServers. Never
	// command/args/env.
	MCPServers []string
}

// runsFold is the RUN REGISTRY fold: every run attempt by run_id, the
// harp→current-run index, and the active credential set (cred hash →
// identity) that backs constant-time verification. It owns the runs journal.
type runsFold struct {
	runs   map[string]*RunRecord
	byHarp map[string]string // harp → latest run_id
	// creds maps hex SHA-256(token) → identity for every ACTIVE credential:
	// live child runs plus registered session owners. Revocation (run end /
	// session cred revoked) removes the entry — persisting only the hash is
	// the whole store.
	creds map[string]Identity
	// project is stamped from session credentials for identity mapping.
	project string
}

func newRunsFold() *runsFold {
	return &runsFold{
		runs:   make(map[string]*RunRecord),
		byHarp: make(map[string]string),
		creds:  make(map[string]Identity),
	}
}

// apply dispatches one fact to its arm. The decode-and-skip per arm is the
// forward-compatibility contract (Fact.decode's doc): an undecodable payload is
// ignored rather than fatal, as is an unknown kind.
func (f *runsFold) apply(fact Fact) {
	switch fact.Kind {
	case factRunEnqueued:
		applyDecoded(fact, f.applyEnqueued)
	case factRunState:
		applyDecoded(fact, f.applyState)
	case factRunEnded:
		applyDecoded(fact, f.applyEnded)
	case factRunHarness:
		applyDecoded(fact, f.applyHarness)
	case factRunResumable:
		applyDecoded(fact, f.applyResumable)
	case factRunReaped:
		applyDecoded(fact, f.applyReaped)
	case factSessionCred:
		applyDecoded(fact, f.applySessionCred)
	case factSessionCredRevoked:
		applyDecoded(fact, f.applySessionCredRevoked)
	}
}

// applyDecoded decodes fact's payload into a fresh P and hands it, with the
// fact's own time, to arm — skipping the arm entirely when the payload does not
// decode. One definition of that guard instead of one per arm.
func applyDecoded[P any](fact Fact, arm func(p P, at time.Time)) {
	var p P
	if fact.decode(&p) != nil {
		return
	}
	arm(p, fact.At)
}

func (f *runsFold) applyEnqueued(p runEnqueued, at time.Time) {
	f.runs[p.RunID] = &RunRecord{
		RunID:        p.RunID,
		Harp:         p.Harp,
		Agent:        p.Agent,
		ParentHarp:   p.ParentHarp,
		ParentRunID:  p.ParentRunID,
		Runtime:      p.Runtime,
		CredHash:     p.CredHash,
		Depth:        p.Depth,
		OneShot:      p.OneShot,
		Prompt:       p.Prompt,
		State:        StateQueued,
		EnqueuedAt:   at,
		LastActivity: at,
		Ladder:       ladderFromFact(p.Ladder),
		Permission:   p.Permission,
		MCPServers:   p.MCPServers,
	}
	f.byHarp[p.Harp] = p.RunID
	if p.CredHash != "" {
		f.creds[p.CredHash] = Identity{Harp: p.Harp, RunID: p.RunID, Depth: p.Depth, OneShot: p.OneShot, Project: f.project}
	}
}

func (f *runsFold) applyState(p runState, at time.Time) {
	if r := f.runs[p.RunID]; r != nil && !r.Ended {
		r.State = p.State
		r.LastActivity = at
	}
}

func (f *runsFold) applyEnded(p runEnded, at time.Time) {
	r := f.runs[p.RunID]
	if r == nil || r.Ended {
		return // exactly one terminal per run_id, whatever raced to cause it
	}
	r.Ended = true
	r.State = StateEnded
	r.Cause = p.Cause
	r.Detail = p.Detail
	r.LastActivity = at
	// Revocation is part of the terminal fact: the credential dies with the
	// run.
	delete(f.creds, r.CredHash)
}

func (f *runsFold) applyHarness(p runHarness, _ time.Time) {
	if r := f.runs[p.RunID]; r != nil {
		r.HarnessSessionID = p.HarnessSessionID
	}
}

func (f *runsFold) applyResumable(p runResumable, _ time.Time) {
	if r := f.runs[p.RunID]; r != nil {
		r.Resumable = p.Resumable
	}
}

// applyReaped drops the evicted run records (one-shot retention). byHarp is
// never touched — the reap set excludes every harp's current run, so the
// harp→current-run index (and the resume key it points at) survives.
func (f *runsFold) applyReaped(p runReaped, _ time.Time) {
	for _, id := range p.RunIDs {
		delete(f.runs, id)
	}
}

func (f *runsFold) applySessionCred(p sessionCred, _ time.Time) {
	if p.Project != "" {
		f.project = p.Project
	}
	// The session-owner credential is never a resolved `driving: oneshot`
	// agent — it is the human's own top-level session — so OneShot stays
	// the zero value (false), explicit here for parity with Depth: 0.
	f.creds[p.CredHash] = Identity{Harp: p.Harp, Depth: 0, OneShot: false, Project: p.Project}
}

func (f *runsFold) applySessionCredRevoked(p sessionCred, _ time.Time) {
	delete(f.creds, p.CredHash)
}

// run returns the record for run_id (nil when unknown).
func (f *runsFold) run(runID string) *RunRecord { return f.runs[runID] }

// currentRun returns the harp's latest run attempt (nil when the harp was
// never spawned here).
func (f *runsFold) currentRun(harp string) *RunRecord {
	id, ok := f.byHarp[harp]
	if !ok {
		return nil
	}
	return f.runs[id]
}

// identityFor resolves a credential hash to its identity.
func (f *runsFold) identityFor(credHash string) (Identity, bool) {
	id, ok := f.creds[credHash]
	return id, ok
}

// activeRunsForCred lists non-ended runs owned by the credential hash — the
// runner-loss synthesis set.
func (f *runsFold) activeRunsForCred(credHash string) []string {
	var out []string
	for id, r := range f.runs {
		if !r.Ended && r.CredHash == credHash {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// queueFold is the SPAWN QUEUE fold: run_ids waiting to start, in enqueue
// order, plus the executing count the D4 cap checks. Restart-safe by
// construction — it is a pure fold over the same runs journal (the
// stranded-queue bug class dies structurally).
type queueFold struct {
	order     []string        // queued run_ids, enqueue order
	inQueue   map[string]bool // membership for O(1) removal marking
	executing int
	state     map[string]string // run_id → last state (to keep executing exact)
}

func newQueueFold() *queueFold {
	return &queueFold{inQueue: make(map[string]bool), state: make(map[string]string)}
}

func (f *queueFold) apply(fact Fact) {
	switch fact.Kind {
	case factRunEnqueued:
		var p runEnqueued
		if fact.decode(&p) != nil {
			return
		}
		f.order = append(f.order, p.RunID)
		f.inQueue[p.RunID] = true
		f.state[p.RunID] = StateQueued
	case factRunState:
		var p runState
		if fact.decode(&p) != nil {
			return
		}
		f.transition(p.RunID, p.State)
	case factRunEnded:
		var p runEnded
		if fact.decode(&p) != nil {
			return
		}
		f.transition(p.RunID, StateEnded)
	case factRunReaped:
		var p runReaped
		if fact.decode(&p) != nil {
			return
		}
		// Retention: forget the reaped runs' last-state (the reap set is all
		// ended runs, so none remain queued/executing — executing is
		// unaffected). Keeps queueFold.state from growing one entry per
		// one-shot turn forever.
		for _, id := range p.RunIDs {
			delete(f.state, id)
			delete(f.inQueue, id)
		}
		f.compact()
	}
}

func (f *queueFold) transition(runID, to string) {
	from, known := f.state[runID]
	if !known || from == StateEnded {
		return
	}
	f.state[runID] = to
	if from == StateExecuting && to != StateExecuting {
		f.executing--
	}
	if from != StateExecuting && to == StateExecuting {
		f.executing++
	}
	if from == StateQueued && to != StateQueued {
		delete(f.inQueue, runID)
		f.compact()
	}
	if from != StateQueued && to == StateQueued {
		f.order = append(f.order, runID)
		f.inQueue[runID] = true
	}
}

// compact drops dequeued heads so order stays small.
func (f *queueFold) compact() {
	i := 0
	for _, id := range f.order {
		if f.inQueue[id] {
			f.order[i] = id
			i++
		}
	}
	f.order = f.order[:i]
}

// queued returns the waiting run_ids in enqueue order (a copy).
//
// test-only: no production caller — it is the observation seam
// for the fold-determinism replay-equivalence acceptance test
// (replay_test.go), which duplicating the order/inQueue filter inline would
// not improve. Keep.
func (f *queueFold) queued() []string {
	out := make([]string, 0, len(f.order))
	for _, id := range f.order {
		if f.inQueue[id] {
			out = append(out, id)
		}
	}
	return out
}

// RosterEntry is one session in the coordinator's roster — a delegation
// child, with its lineage and coordinator-visible state. It is the single
// state behind BOTH transports (the MCP surface and the surviving bus roster
// verb).
type RosterEntry struct {
	Harp             string `json:"harp"`
	Agent            string `json:"agent,omitempty"`
	State            string `json:"state"`
	Parent           string `json:"parent,omitempty"`
	LastActivityUnix int64  `json:"last_activity_unix,omitempty"`
}

// rosterFold is the ROSTER fold: per-harp coordinator-visible state (the
// harp's LATEST run attempt wins). It owns the runs journal.
type rosterFold struct {
	entries map[string]*RosterEntry
	current map[string]string // harp → current run_id
	byRun   map[string]string // run_id → harp
}

func newRosterFold() *rosterFold {
	return &rosterFold{
		entries: make(map[string]*RosterEntry),
		current: make(map[string]string),
		byRun:   make(map[string]string),
	}
}

func (f *rosterFold) apply(fact Fact) {
	switch fact.Kind {
	case factRunEnqueued:
		var p runEnqueued
		if fact.decode(&p) != nil {
			return
		}
		f.entries[p.Harp] = &RosterEntry{
			Harp:             p.Harp,
			Agent:            p.Agent,
			State:            StateQueued,
			Parent:           p.ParentHarp,
			LastActivityUnix: fact.At.Unix(),
		}
		f.current[p.Harp] = p.RunID
		f.byRun[p.RunID] = p.Harp
	case factRunState:
		var p runState
		if fact.decode(&p) != nil {
			return
		}
		f.touch(p.RunID, p.State, fact.At)
	case factRunEnded:
		var p runEnded
		if fact.decode(&p) != nil {
			return
		}
		f.touch(p.RunID, StateEnded, fact.At)
	case factRunReaped:
		var p runReaped
		if fact.decode(&p) != nil {
			return
		}
		// Retention: forget the reaped runs' run_id→harp back-index. entries
		// and current are HARP-keyed (one per harp) and untouched — the
		// roster still shows every harp's latest state; only the per-run
		// index (which grows one entry per one-shot turn) is pruned. Never
		// prunes a current run's index (the reap set excludes it), so touch()
		// for the live run still resolves.
		for _, id := range p.RunIDs {
			if h, ok := f.byRun[id]; ok && f.current[h] != id {
				delete(f.byRun, id)
			}
		}
	}
}

func (f *rosterFold) touch(runID, state string, at time.Time) {
	harp, ok := f.byRun[runID]
	if !ok || f.current[harp] != runID {
		return // a superseded attempt no longer drives the roster
	}
	if e := f.entries[harp]; e != nil {
		e.State = state
		e.LastActivityUnix = at.Unix()
	}
}

// snapshot lists the roster sorted by harp (stable output), as copies.
func (f *rosterFold) snapshot() []RosterEntry {
	out := make([]RosterEntry, 0, len(f.entries))
	for _, e := range f.entries {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Harp < out[j].Harp })
	return out
}

// Message is one mailbox message as delivered to agent_recv.
type Message struct {
	ID   string `json:"id,omitempty"`
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind,omitempty"`
	Body string `json:"body"`
	// Structured is an optional structured companion (arbitrary JSON object,
	// e.g. the escalation ladder's relayed ApprovalRequest proto projection
	// — Wave C2). Kept as raw JSON here so the mailbox fold stays proto-free;
	// the gRPC/MCP edges convert to/from structpb.Struct.
	Structured json.RawMessage `json:"structured,omitempty"`
	// InReplyTo correlates this message to an earlier one's ID (e.g. an
	// approval_request's relay and the parent's ApprovalDecision reply).
	InReplyTo string `json:"in_reply_to,omitempty"`
}

// mailFold is the MAILBOX fold: durable role-addressed queues plus the
// consume cursor (the consumed message_id set). At-least-once: a message
// leaves the pending queue only via a factMailConsumed; delivery itself is
// runtime state, so an unacked delivery is re-delivered after a restart.
// Queued facts are deduped on message_id.
type mailFold struct {
	pending  map[string][]Message // role → pending, arrival order
	seen     map[string]bool      // message_id → queued (dedupe)
	consumed map[string]bool      // message_id → consumed (cursor)
}

func newMailFold() *mailFold {
	return &mailFold{
		pending:  make(map[string][]Message),
		seen:     make(map[string]bool),
		consumed: make(map[string]bool),
	}
}

func (f *mailFold) apply(fact Fact) {
	switch fact.Kind {
	case factMailQueued:
		var p mailQueued
		if fact.decode(&p) != nil {
			return
		}
		if f.seen[p.MessageID] {
			return // dedupe on message_id
		}
		f.seen[p.MessageID] = true
		f.pending[p.To] = append(f.pending[p.To], Message{
			ID: p.MessageID, From: p.From, To: p.To, Kind: p.Kind, Body: p.Body,
			Structured: p.Structured, InReplyTo: p.InReplyTo,
		})
	case factMailConsumed:
		var p mailConsumed
		if fact.decode(&p) != nil {
			return
		}
		drop := make(map[string]bool, len(p.MessageIDs))
		for _, id := range p.MessageIDs {
			if !f.consumed[id] {
				f.consumed[id] = true
				drop[id] = true
			}
		}
		q := f.pending[p.Role]
		i := 0
		for _, m := range q {
			if !drop[m.ID] {
				q[i] = m
				i++
			}
		}
		if i == 0 {
			delete(f.pending, p.Role)
		} else {
			f.pending[p.Role] = q[:i]
		}
	}
}

// pendingFor returns the role's pending messages (copies, arrival order).
func (f *mailFold) pendingFor(role string) []Message {
	q := f.pending[role]
	out := make([]Message, len(q))
	copy(out, q)
	return out
}
