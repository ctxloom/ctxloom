package coord

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// TestCoordinator_ConcurrentTurnsInvariants is the muddy-coil gate
// (one-shot-resume.plan.md Slice 3): a read-only audit found the
// coordinator's mutable state PARTITIONED BY CHILD IDENTITY (maps keyed by
// harp/runID/role/credHash/msgID, decisive steps atomic inside one journal
// Exec or one c.mu window) and no ordering/TOCTOU bug — this test is the
// invariant-asserting proof that claim actually holds under REAL
// concurrency (>= 2 children executing turns at once), not just under
// -race's "every access already locks" guarantee.
//
// Shape: N children spawn via Options.ConcurrencyCap = N (so nothing serializes
// them), all block on a shared barrier mid-turn — genuine overlap, not
// scheduler luck: no child's turn can complete until every sibling's has
// also started — then, still mid-turn (StateExecuting), all N relay an
// approval to the shared parent concurrently, one child additionally floods
// a single request_id across a forced reconnect, every child bridges a
// distinct result to the parent's mailbox, and every child's run is
// terminated by two RACING agent_stop calls (exactly-once terminal). A
// concurrency gauge proves the peak overlap actually happened; a post-run
// reconciliation reopens both journals from disk and replays them through
// FRESH folds to check durable invariants independent of the live
// coordinator's own bookkeeping.
func TestCoordinator_ConcurrentTurnsInvariants(t *testing.T) {
	resetStrictness(t)

	const n = 3
	decisionNames := [n]string{"DECISION_ACCEPT", "DECISION_DECLINE", "DECISION_ACCEPT_FOR_SESSION"}
	decisionEnum := map[string]agentcoordpb.ApprovalDecision_Decision{
		"DECISION_ACCEPT":             agentcoordpb.ApprovalDecision_DECISION_ACCEPT,
		"DECISION_DECLINE":            agentcoordpb.ApprovalDecision_DECISION_DECLINE,
		"DECISION_ACCEPT_FOR_SESSION": agentcoordpb.ApprovalDecision_DECISION_ACCEPT_FOR_SESSION,
	}

	agents := make(map[string]fakeAgent, n)
	ladder := Ladder{{Action: ActionRelayToRole, Role: ParentAddress, Timeout: 10 * time.Second}}
	for i := 0; i < n; i++ {
		agents[fmt.Sprintf("worker-%d", i)] = fakeAgent{
			perm: "plan", runtime: "container", profiles: []string{"p1"},
			viaStartRun: true, ladder: ladder,
		}
	}
	sp := newConcurrencySpawner(agents)

	c, err := New(Options{
		ProjectDir:     t.TempDir(),
		StateDir:       t.TempDir(),
		Spawner:        sp,
		ConcurrencyCap: n, // the seam under test: a resource ceiling >= the child count
	})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	stateDir := c.stateDir

	// The concurrency gauge (children.go's sampleExecGauge/execGaugeHook):
	// the MAX observed count of runs in StateExecuting, across the whole
	// test — proof the test does not pass vacuously (a bug that silently
	// re-serialized everything would cap this at 1).
	var maxExecuting int64
	c.execGaugeHook = func(executing int) {
		for {
			cur := atomic.LoadInt64(&maxExecuting)
			if int64(executing) <= cur || atomic.CompareAndSwapInt64(&maxExecuting, cur, int64(executing)) {
				return
			}
		}
	}

	// Spawn all N children. Enqueue is synchronous (AgentRun returns once the
	// run is journaled); the engine launch is async (c.goTracked).
	harps := make([]string, n)
	runIDs := make([]string, n)
	for i := 0; i < n; i++ {
		out, err := c.AgentRun(context.Background(), ownerIdentity(), fmt.Sprintf("worker-%d", i), fmt.Sprintf("prompt-%d", i), "", "")
		require.NoError(t, err)
		harps[i] = out.Harp
		runIDs[i] = out.RunID
	}

	barrier := newConcurrencyBarrier(n)
	var atBarrier sync.WaitGroup
	atBarrier.Add(n)
	proceed := make(chan struct{})
	var wgChildren sync.WaitGroup
	wgChildren.Add(n)

	// child 0 additionally floods a single request_id across a forced
	// reconnect (reqTrack dedupe, assertion (d)) BEFORE the shared barrier —
	// the barrier + proceed gate below guarantees this fully resolves before
	// any child's ordinary approval relay begins, so the two scenarios never
	// interleave in the parent's mailbox.
	const floodReqID = "flood-req"
	floodItemID := harps[0] + "-flood-item"

	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wgChildren.Done()
			env := sp.waitForHarp(t, harps[i], conformanceWait)
			ch := dialRawRunChannel(t, env[EnvCoordURL], env[EnvCoordCred], env[EnvRunID])
			defer func() { ch.close() }()

			if i == 0 {
				// K=5 concurrent identical-request_id frames on the first
				// channel.
				var wg sync.WaitGroup
				for k := 0; k < 5; k++ {
					wg.Add(1)
					go func() { defer wg.Done(); ch.sendApproval(t, floodReqID, floodItemID) }()
				}
				wg.Wait()

				// The human hasn't answered yet: force a reconnect (kill +
				// redial), then reissue the SAME request_id 5 MORE times
				// concurrently on the fresh channel — exactly the shape
				// approval_reconnect_test.go proves serially, here fired
				// concurrently and doubled across the reconnect boundary.
				ch.close()
				ch = dialRawRunChannel(t, env[EnvCoordURL], env[EnvCoordCred], env[EnvRunID])
				var wg2 sync.WaitGroup
				for k := 0; k < 5; k++ {
					wg2.Add(1)
					go func() { defer wg2.Done(); ch.sendApproval(t, floodReqID, floodItemID) }()
				}
				wg2.Wait()

				got := ch.awaitApprovalDecision(t, floodReqID, conformanceWait)
				assert.Equal(t, agentcoordpb.ApprovalDecision_DECISION_ACCEPT, got.GetDecision(),
					"the flood's single human answer must resolve on the live post-reconnect channel")
			}

			// Genuine overlap: this child's turn cannot proceed past this
			// point until every sibling's has ALSO reached it — the peak in
			// maxExecuting is therefore not incidental scheduling.
			require.NoError(t, barrier.arrive(context.Background()))
			atBarrier.Done()
			<-proceed

			// Still mid-turn (StateExecuting): relay one approval,
			// concurrently with every sibling.
			apprReqID := harps[i] + "-appr"
			ch.sendApproval(t, apprReqID, harps[i]+"-item")
			decision := ch.awaitApprovalDecision(t, apprReqID, conformanceWait)
			assert.Equal(t, decisionEnum[decisionNames[i]], decision.GetDecision(),
				"child %s must receive exactly its OWN parent decision, never a sibling's", harps[i])

			// Bridge a distinct result to the parent, then end the turn.
			sendMessageEvent(t, ch, fmt.Sprintf("m-%d", i), fmt.Sprintf("child %d says hi", i))
			sendCustomEvent(t, ch, CustomTurnIdle)
			require.Eventually(t, func() bool { return c.runState(runIDs[i]) == StateIdle }, conformanceWait, 5*time.Millisecond,
				"child %s must reach StateIdle after its turn boundary", harps[i])

			// Exactly-once terminal: two RACING agent_stop calls.
			var wgStop sync.WaitGroup
			wgStop.Add(2)
			go func() { defer wgStop.Done(); _, _ = c.AgentStop(ownerIdentity(), harps[i]) }()
			go func() { defer wgStop.Done(); _, _ = c.AgentStop(ownerIdentity(), harps[i]) }()
			wgStop.Wait()
		}()
	}

	// The flood-item relay must resolve BEFORE the shared barrier is even
	// approached seriously (child 0 blocks on it before calling
	// barrier.arrive) — answer it now.
	floodMsgs := recvWhere(t, c, func(m Message) bool {
		return m.Kind == "approval_request" && strings.Contains(string(m.Structured), floodItemID)
	}, conformanceWait)
	require.Len(t, floodMsgs, 1, "the flood's 10 concurrent duplicate frames (across a forced reconnect) must relay as EXACTLY ONE mail")
	_, err = c.AgentSend(ownerIdentity(), harps[0], "", "flood reviewed", decisionJSON(t, "DECISION_ACCEPT"), floodMsgs[0].ID)
	require.NoError(t, err)

	// Wait for all N children to have reached the barrier — the instant this
	// returns, all N runs are DEFINITELY live/executing simultaneously: the
	// point to snapshot credential-integrity invariant (c).
	atBarrier.Wait()

	var (
		liveCredHashes = map[string]string{} // credHash -> runID
		credsSnapshot  map[string]Identity
	)
	c.runs.View(func() {
		credsSnapshot = make(map[string]Identity, len(c.runsF.creds))
		for k, v := range c.runsF.creds {
			credsSnapshot[k] = v
		}
		for _, id := range runIDs {
			r := c.runsF.run(id)
			require.NotNil(t, r, "run %s must exist in the fold while barrier-blocked", id)
			require.False(t, r.Ended, "run %s must still be live while barrier-blocked", id)
			if prev, dup := liveCredHashes[r.CredHash]; dup {
				t.Fatalf("credential collision: runs %s and %s share credHash %s", prev, id, r.CredHash)
			}
			liveCredHashes[r.CredHash] = id
		}
	})
	for credHash, runID := range liveCredHashes {
		_, ok := credsSnapshot[credHash]
		assert.True(t, ok, "run %s's credHash must be present in the live creds fold while it is live", runID)
	}
	assert.Equal(t, len(liveCredHashes), len(credsSnapshot),
		"the creds fold must contain EXACTLY the live runs' credentials while barrier-blocked (no session-owner cred registered in this test) — creds fold <=> live runs")

	close(proceed)

	// Drain exactly N approval_request messages (one per child) and answer
	// each with a DISTINCT decision, addressed by the harp the mail actually
	// came FROM — the non-cross-attribution proof (assertion (e)) is in
	// each child goroutine's own assert above; this is the other half.
	apprMsgs := recvNKind(t, c, "approval_request", n, conformanceWait)
	require.Len(t, apprMsgs, n, "exactly one approval_request per child must relay")
	byHarp := map[string]int{}
	for i, h := range harps {
		byHarp[h] = i
	}
	for _, m := range apprMsgs {
		idx, ok := byHarp[m.From]
		require.True(t, ok, "approval_request mail %q from unexpected sender %q", m.ID, m.From)
		_, err := c.AgentSend(ownerIdentity(), m.From, "", "reviewed", decisionJSON(t, decisionNames[idx]), m.ID)
		require.NoError(t, err)
	}

	wgChildren.Wait()

	// Quiescence (assertion (f)): every slot is free and no child holds one
	// — this is exactly the check an R1-style double-acquire regression
	// would fail (slots.free would be short, or rt.slot stuck at
	// slotClaimed/slotHeld).
	c.slots.mu.Lock()
	freeSlots := c.slots.free
	waiters := len(c.slots.waiters)
	c.slots.mu.Unlock()
	assert.Equal(t, n, freeSlots, "slots.free must equal the cap at quiescence")
	assert.Zero(t, waiters, "no waiter should remain parked at quiescence")
	c.mu.Lock()
	for _, h := range harps {
		if rt := c.byHarp[h]; rt != nil {
			assert.Equal(t, slotFree, rt.slot, "child %s must not hold or be claiming a slot once ended", h)
		}
	}
	c.mu.Unlock()

	maxObserved := atomic.LoadInt64(&maxExecuting)
	t.Logf("concurrency gauge: max observed StateExecuting = %d (TurnCap = %d)", maxObserved, n)
	assert.GreaterOrEqual(t, maxObserved, int64(2), "the gauge must observe at least 2 children executing AT ONCE — the test must not pass vacuously")

	c.Close()

	// --- Post-run reconciliation: reopen both journals fresh, replay from
	// disk into BRAND NEW folds, and check durable invariants independent of
	// the live coordinator's own runtime bookkeeping. ---

	runsF2, queueF2, rosterF2, reportsF2 := newRunsFold(), newQueueFold(), newRosterFold(), newReportsFold()
	rfc := &runFactCounts{enqueuedByRunID: map[string]int{}, endedByRunID: map[string]int{}}
	runsStore, err := openStore(filepath.Join(stateDir, "runs.jsonl"), runsF2, queueF2, rosterF2, reportsF2, rfc)
	require.NoError(t, err)
	defer runsStore.Close()

	// (a) run integrity.
	roster := rosterF2.snapshot()
	assert.Len(t, roster, n, "exactly N distinct harps must be on the roster")
	for _, id := range runIDs {
		assert.Equal(t, 1, rfc.enqueuedByRunID[id], "run %s must have exactly one factRunEnqueued", id)
		assert.LessOrEqual(t, rfc.endedByRunID[id], 1, "run %s must have AT MOST one factRunEnded (exactly-once terminal, despite the racing double agent_stop)", id)
		assert.Equal(t, 1, rfc.endedByRunID[id], "run %s must have exactly one factRunEnded (it WAS stopped)", id)
		r := runsF2.run(id)
		require.NotNil(t, r)
		assert.True(t, r.Ended, "run %s must be Ended after reconciliation", id)
		_, revoked := runsF2.creds[r.CredHash]
		assert.False(t, revoked, "an ended run's credential must not remain live (never both live and revoked)", id)
	}
	assert.Empty(t, runsF2.creds, "no run-scoped credential should remain live once every child has been stopped")

	// (b)/(d) mail conservation + reqTrack-dedupe's durable trace.
	mailF2 := newMailFold()
	ma := newMailAudit()
	mailStore, err := openStore(filepath.Join(stateDir, "mailbox.jsonl"), mailF2, ma)
	require.NoError(t, err)
	defer mailStore.Close()

	for role, queued := range ma.queuedByRole {
		consumed := len(ma.consumedIDsByRole[role])
		pending := len(mailF2.pendingFor(role))
		assert.Equal(t, queued, consumed+pending, "role %s: queued must equal consumed + pending", role)
	}
	// Every queued message's recipient (its role bucket) matches the "to"
	// it was actually addressed to — trivially true by construction of
	// queuedByRole (bucketed on p.To), but assert the bucket set is exactly
	// the roles we expect (the parent + nothing stray) to catch a
	// misdelivery class of bug.
	for role := range ma.queuedByRole {
		assert.Equal(t, ownerIdentity().Harp, role, "every message in this scenario is addressed to the parent role")
	}

	floodCount := ma.itemIDCounts[floodItemID]
	assert.Equal(t, 1, floodCount, "the flood's 10 concurrent duplicate request_id frames (across a forced reconnect) must have queued EXACTLY ONE approval_request mail — the reqTrack dedupe's durable trace")

	resultCount := ma.kindCounts["result"]
	assert.Equal(t, n, resultCount, "the parent must hold exactly one bridged result per child turn")

	apprCount := ma.kindCounts["approval_request"]
	assert.Equal(t, n+1, apprCount, "N ordinary approval relays plus the one flood relay")

	exitedCount := ma.kindCounts[KindExited]
	assert.Equal(t, n, exitedCount, "one terminal notice per child")
}

// concurrencyBarrier lets N parties rendezvous exactly once, all resuming
// together — the mechanism that forces GENUINE overlap in the invariant
// test above: no participant's simulated turn can proceed past arrive()
// until every other participant has also called it, so a peak of N
// simultaneously-executing children is not incidental scheduling luck, it
// is REQUIRED for the test to proceed at all.
type concurrencyBarrier struct {
	mu      sync.Mutex
	n       int
	arrived int
	release chan struct{}
}

func newConcurrencyBarrier(n int) *concurrencyBarrier {
	return &concurrencyBarrier{n: n, release: make(chan struct{})}
}

func (b *concurrencyBarrier) arrive(ctx context.Context) error {
	b.mu.Lock()
	b.arrived++
	last := b.arrived >= b.n
	ch := b.release
	b.mu.Unlock()
	if last {
		close(ch)
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// concurrencySpawner is a fakeSpawner whose StartEngine attaches ONLY a
// lifecycle RunnerLink (answering StartRun OK), leaving each run's
// RunChannel for the test to drive raw — mirrors
// approval_reconnect_test.go's approvalReconnectSpawner, generalized to N
// concurrent children keyed by harp (env["CTXLOOM_SESSION_HARP"]) rather
// than a single envCh.
type concurrencySpawner struct {
	*fakeSpawner
	mu    sync.Mutex
	links []*RunnerLink
	envs  map[string]map[string]string // harp -> runnerEnv trio
	ready map[string]chan struct{}     // harp -> closed once envs[harp] is set
}

func newConcurrencySpawner(agentsMap map[string]fakeAgent) *concurrencySpawner {
	return &concurrencySpawner{
		fakeSpawner: newFakeSpawner(agentsMap, nil),
		envs:        make(map[string]map[string]string),
		ready:       make(map[string]chan struct{}),
	}
}

func (s *concurrencySpawner) waitForHarp(t *testing.T, harp string, wait time.Duration) map[string]string {
	t.Helper()
	s.mu.Lock()
	if env, ok := s.envs[harp]; ok {
		s.mu.Unlock()
		return env
	}
	ch, ok := s.ready[harp]
	if !ok {
		ch = make(chan struct{})
		s.ready[harp] = ch
	}
	s.mu.Unlock()
	select {
	case <-ch:
	case <-time.After(wait):
		t.Fatalf("child runner for harp %s never started", harp)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.envs[harp]
}

func (s *concurrencySpawner) StartEngine(ctx context.Context, plan *SpawnPlan, env, runnerEnv map[string]string) (*EngineSpawn, error) {
	harp := env["CTXLOOM_SESSION_HARP"]
	handler := func(req *agentcoordpb.RunnerRequest) *agentcoordpb.RunnerResponse {
		if req.GetStartRun() != nil {
			return &agentcoordpb.RunnerResponse{
				Status: okStatus(""),
				Kind:   &agentcoordpb.RunnerResponse_StartRun{StartRun: &agentcoordpb.StartRunResult{}},
			}
		}
		return &agentcoordpb.RunnerResponse{Status: okStatus("")}
	}
	link, err := DialRunner(ctx, runnerEnv[EnvCoordURL], runnerEnv[EnvCoordCred], runnerEnv[EnvRunID], plan.Backend, "test", handler)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.links = append(s.links, link)
	s.envs[harp] = runnerEnv
	if ch, ok := s.ready[harp]; ok {
		close(ch)
	} else {
		ch := make(chan struct{})
		close(ch)
		s.ready[harp] = ch
	}
	s.mu.Unlock()
	var killOnce sync.Once
	kill := func() { killOnce.Do(func() { link.Shutdown(0, "") }) }
	return &EngineSpawn{WorkDir: "/work", Env: env, Model: "test-model", Kill: kill}, nil
}

// sendCustomEvent sends one ctxloom/* custom event frame raw over ch — the
// turn-state transitions (CustomTurnStarted/CustomTurnIdle) the engine host
// normally emits (runchannel.go's handleCustomEvent).
func sendCustomEvent(t *testing.T, ch *rawRunChannel, name string) {
	t.Helper()
	require.NoError(t, ch.stream.Send(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Event{Event: &agentcoordpb.AgentEvent{
		Payload: &agentcoordpb.AgentEvent_Custom{Custom: &agentcoordpb.CustomEvent{Name: name}},
	}}}))
}

// sendMessageEvent emits one complete plane-1 FINAL message (MessageStarted +
// MessageDelta) raw over ch — accumulateFinalText's turn-output accumulator
// (children.go), so the eventual CustomTurnIdle's bridgeTurnResult has
// something non-empty to deliver to the parent.
func sendMessageEvent(t *testing.T, ch *rawRunChannel, msgID, text string) {
	t.Helper()
	require.NoError(t, ch.stream.Send(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Event{Event: &agentcoordpb.AgentEvent{
		Payload: &agentcoordpb.AgentEvent_MessageStarted{MessageStarted: &agentcoordpb.MessageStarted{
			MessageId: msgID,
			Channel:   agentcoordpb.MessageChannel_MESSAGE_CHANNEL_FINAL,
		}},
	}}}))
	require.NoError(t, ch.stream.Send(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Event{Event: &agentcoordpb.AgentEvent{
		Payload: &agentcoordpb.AgentEvent_MessageDelta{MessageDelta: &agentcoordpb.MessageDelta{
			MessageId: msgID,
			Text:      text,
		}},
	}}}))
}

// recvNKind polls the OWNER's mailbox until at least n distinct (by id)
// messages of kind have been observed, or wait elapses — recvKind/recvWhere
// (fake_test.go) return as soon as ONE match exists, which is not enough
// when a test needs to collect one mail PER CHILD before answering any of
// them.
func recvNKind(t *testing.T, c *Coordinator, kind string, n int, wait time.Duration) []Message {
	t.Helper()
	deadline := time.Now().Add(wait)
	seen := map[string]bool{}
	var out []Message
	for {
		msgs, err := c.AgentRecv(context.Background(), ownerIdentity(), 10*time.Millisecond)
		if err == nil {
			for _, m := range msgs {
				if m.Kind == kind && !seen[m.ID] {
					seen[m.ID] = true
					out = append(out, m)
				}
			}
		}
		if len(out) >= n || time.Now().After(deadline) {
			return out
		}
	}
}

// decisionJSON builds an agent_send structured payload carrying one
// ApprovalDecision (decisionFromStructured's expected shape).
func decisionJSON(t *testing.T, decision string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{"decision": decision, "note": "test"})
	require.NoError(t, err)
	return b
}

// runFactCounts is a test-only fold layered on the SAME runs.jsonl replay
// (openStore accepts multiple folds over one journal) counting
// factRunEnqueued/factRunEnded occurrences PER run_id — the raw material for
// "each run exactly one enqueued, at most one ended" (assertion (a))
// independent of runsFold's own (already-reconciling) state.
type runFactCounts struct {
	enqueuedByRunID map[string]int
	endedByRunID    map[string]int
}

func (r *runFactCounts) apply(fact Fact) {
	switch fact.Kind {
	case factRunEnqueued:
		var p runEnqueued
		if fact.decode(&p) == nil {
			r.enqueuedByRunID[p.RunID]++
		}
	case factRunEnded:
		var p runEnded
		if fact.decode(&p) == nil {
			r.endedByRunID[p.RunID]++
		}
	}
}

// mailAudit is a test-only fold layered on the SAME mailbox.jsonl replay
// (alongside a fresh mailFold) reconstructing PER-ROLE queued/consumed
// counts — mailFold itself tracks a global seen/consumed dedupe set, not
// per-role queued totals, so this is the reconciliation invariant's own
// independent accounting. itemIDCounts/kindCounts key off the message
// body/structured payload — the durable trace assertion (d) (reqTrack
// dedupe) and (b) (one result per turn) read from disk, not from live
// runtime bookkeeping.
type mailAudit struct {
	queuedByRole      map[string]int
	queuedIDsByRole   map[string]map[string]bool
	consumedIDsByRole map[string]map[string]bool
	itemIDCounts      map[string]int // structured payload's item_id -> queued count
	kindCounts        map[string]int // Message.Kind -> queued count
}

func newMailAudit() *mailAudit {
	return &mailAudit{
		queuedByRole:      map[string]int{},
		queuedIDsByRole:   map[string]map[string]bool{},
		consumedIDsByRole: map[string]map[string]bool{},
		itemIDCounts:      map[string]int{},
		kindCounts:        map[string]int{},
	}
}

func (a *mailAudit) apply(fact Fact) {
	switch fact.Kind {
	case factMailQueued:
		var p mailQueued
		if fact.decode(&p) != nil {
			return
		}
		if a.queuedIDsByRole[p.To] == nil {
			a.queuedIDsByRole[p.To] = map[string]bool{}
		}
		if a.queuedIDsByRole[p.To][p.MessageID] {
			return // dedupe on message_id, mirrors mailFold.seen
		}
		a.queuedIDsByRole[p.To][p.MessageID] = true
		a.queuedByRole[p.To]++
		a.kindCounts[p.Kind]++
		if id := extractItemID(p.Structured); id != "" {
			a.itemIDCounts[id]++
		}
	case factMailConsumed:
		var p mailConsumed
		if fact.decode(&p) != nil {
			return
		}
		if a.consumedIDsByRole[p.Role] == nil {
			a.consumedIDsByRole[p.Role] = map[string]bool{}
		}
		for _, id := range p.MessageIDs {
			a.consumedIDsByRole[p.Role][id] = true
		}
	}
}

// extractItemID pulls "item_id" out of a queued mail's structured payload
// (the approval_request proto projection, approval.go's
// approvalRequestStructured) without a full protojson decode — the raw JSON
// substring is enough to identify which relay a queued mail belongs to for
// this test's dedupe count.
func extractItemID(structured json.RawMessage) string {
	if len(structured) == 0 {
		return ""
	}
	var wrapper struct {
		ApprovalRequest struct {
			ItemID string `json:"item_id"`
		} `json:"approval_request"`
	}
	if json.Unmarshal(structured, &wrapper) != nil {
		return ""
	}
	return wrapper.ApprovalRequest.ItemID
}
