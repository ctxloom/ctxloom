package operations

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/git"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	tasksops "github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/triggers"
)

// triageTestConfig is a minimal config with a fast-role label, mirroring
// oneshotTestConfig's shape but scoped to what trigger evaluation reads
// (cfg.FastLabel / cfg.ResolveLLM).
func triageTestConfig() *config.Config {
	return config.NewFixture(config.Fixture{
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"claude-fast": {Type: "claude-code", Body: map[string]any{"model": "haiku"}},
			},
			Defaults: config.RoleDefaults{Fast: "claude-fast"},
		},
	})
}

// fullFakeClient is a minimal pb.Client double for testing EvaluateTriggers
// without a real backend. Run replies with `out` (or, when `outs` is set, one
// scripted string per call — the last entry repeats for any call beyond the
// script; or, when `respond` is set, whatever it returns for that request's
// prompt — see respond's doc comment for why chunking needs this third
// mode), and records every request it received for prompt inspection.
//
// Chunking makes multiple chunks call Run CONCURRENTLY (bounded by
// triageConcurrency), so both call-counting and gotReqs need a lock — with a
// single serial call per test (the pre-chunking norm) the race never showed,
// but concurrent chunk calls hit it every time under -race.
type fullFakeClient struct {
	out  string
	outs []string
	// respond, when set, picks this call's response from the REQUEST content
	// rather than call order — call order across concurrent chunks is not
	// deterministic, so a test that needs "chunk containing task X returns
	// garbage, chunk containing task Y succeeds" must dispatch on the
	// prompt, not on which goroutine happened to call Run first.
	respond func(req *pb.RunStart) string

	mu      sync.Mutex
	calls   atomic.Int32
	gotReqs []*pb.RunStart
}

var _ pb.Client = (*fullFakeClient)(nil)

func (c *fullFakeClient) Run(_ context.Context, req *pb.RunStart, _ io.Reader, stdout, _ io.Writer, _ <-chan *pb.WindowSize) (int32, error) {
	i := int(c.calls.Add(1)) - 1
	c.mu.Lock()
	c.gotReqs = append(c.gotReqs, req)
	c.mu.Unlock()

	var out string
	switch {
	case c.respond != nil:
		out = c.respond(req)
	case len(c.outs) > 0:
		if i < len(c.outs) {
			out = c.outs[i]
		} else {
			out = c.outs[len(c.outs)-1]
		}
	default:
		out = c.out
	}
	_, _ = io.WriteString(stdout, out)
	return 0, nil
}

// requestCount is a race-safe read of how many Run calls this client has
// received — gotReqs itself is written under mu, so len(c.gotReqs) needs the
// same lock rather than a bare read.
func (c *fullFakeClient) requestCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.gotReqs)
}

// requestAt is a race-safe read of one previously-recorded request, for
// tests that inspect prompt content by index.
func (c *fullFakeClient) requestAt(i int) *pb.RunStart {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gotReqs[i]
}
func (c *fullFakeClient) Info(context.Context) (*pb.LLMInfo, error) { return &pb.LLMInfo{}, nil }
func (c *fullFakeClient) RunWithModelInfo(context.Context, *pb.RunStart, io.Reader, io.Writer, io.Writer, <-chan *pb.WindowSize) (*pb.RunResult, error) {
	return &pb.RunResult{}, nil
}
func (c *fullFakeClient) GetSession(context.Context, string) (*agent.Session, error) { return nil, nil }
func (c *fullFakeClient) WatchSession(context.Context, string) (<-chan *pb.WatchEvent, <-chan error, error) {
	return nil, nil, nil
}
func (c *fullFakeClient) Chat(context.Context, agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error) {
	return nil, nil, nil, nil
}
func (c *fullFakeClient) ListSessions(context.Context) ([]agent.SessionMeta, error)  { return nil, nil }
func (c *fullFakeClient) GetPlans(context.Context, string) ([]agent.PlanFile, error) { return nil, nil }
func (c *fullFakeClient) Kill()                                                      {}

// countingClientFactory wraps client in a pb.ClientFactory that counts how
// many times it was invoked, so a test can pin retry behavior (exactly one
// call vs. exactly two) without depending on internal call ordering.
func countingClientFactory(client pb.Client) (pb.ClientFactory, *atomic.Int32) {
	var n atomic.Int32
	return func(string, string, int) (pb.Client, error) {
		n.Add(1)
		return client, nil
	}, &n
}

func newTaskContext(t *testing.T, projectID string) tasksops.TaskContext {
	t.Helper()
	dir := taskstest.ProjectDir(t)
	return tasksops.TaskContext{WorkDir: dir, ProjectID: projectID, SessionHarp: "test-session"}
}

func TestEvaluateTriggers_NoDeferredTasksSkipsTheLLMCall(t *testing.T) {
	tc := newTaskContext(t, "proj-1")
	_, err := tasksops.AddTask(tc, "just working", "", "")
	require.NoError(t, err)

	client := &fullFakeClient{out: `[]`}
	factory, calls := countingClientFactory(client)

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{
		TaskContext: tc,
		Factory:     factory,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, res.Evaluated)
	assert.Empty(t, res.Verdicts)
	assert.Equal(t, int32(0), calls.Load(), "no deferred tasks means no LLM call at all")
}

func TestEvaluateTriggers_HappyPath(t *testing.T) {
	tc := newTaskContext(t, "proj-2")
	deferred, err := tasksops.AddTask(tc, "wire the signing CLI", "Deferred", "when the signing CLI ships")
	require.NoError(t, err)
	_, err = tasksops.AddTask(tc, "unrelated active task", "", "")
	require.NoError(t, err)

	verdictJSON := `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"fired","evidence":["commit abc"],"reasoning":"it shipped"}]`
	client := &fullFakeClient{out: verdictJSON}
	factory, calls := countingClientFactory(client)

	gitFake := &git.Fake{LogEntries: map[string][]git.LogEntry{
		"/repo": {{SHA: "abc1234567890", Date: time.Now(), Subject: "feat: ship the CLI", Files: []string{"cli.go"}}},
	}}

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{
		TaskContext: tc,
		RepoDir:     "/repo",
		Factory:     factory,
		Git:         gitFake,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load(), "exactly one LLM call for the whole batch")
	assert.Equal(t, 1, res.Evaluated)
	assert.False(t, res.Degraded)
	require.Len(t, res.Verdicts, 1)
	assert.Equal(t, deferred.Task.HarpID, res.Verdicts[0].HarpID)
	assert.Equal(t, triggers.Fired, res.Verdicts[0].Outcome)

	// Evidence made it into the prompt: the task, its trigger, the active
	// task's status, and the git evidence.
	require.NotEmpty(t, client.gotReqs)
	prompt := client.gotReqs[0].Prompt.Content
	assert.Contains(t, prompt, deferred.Task.HarpID)
	assert.Contains(t, prompt, "when the signing CLI ships")
	assert.Contains(t, prompt, "feat: ship the CLI")
	assert.Contains(t, prompt, "unrelated active task")

	// The fast label's model rode through.
	assert.Equal(t, "haiku", client.gotReqs[0].Options.Model)
	assert.Equal(t, pb.ExecutionMode_ONESHOT, client.gotReqs[0].Options.Mode)
	assert.True(t, client.gotReqs[0].Options.SkipSetup)
}

// TestEvaluateTriggers_RepoStateReachesThePrompt is the regression for the
// live used-lurk failure: an existence-style trigger ("package X exists") was
// judged not-fired because the package was UNCOMMITTED and the evidence pack
// only carried commit history. The repo's current state — including untracked
// work — must reach the model.
func TestEvaluateTriggers_RepoStateReachesThePrompt(t *testing.T) {
	tc := newTaskContext(t, "proj-repo-state")
	_, err := tasksops.AddTask(tc, "park me", "Deferred", "the internal/shared/tasks/triggers package exists")
	require.NoError(t, err)

	client := &fullFakeClient{out: `[]`}
	factory, _ := countingClientFactory(client)

	gitFake := &git.Fake{
		Dirs:    []string{"internal/shared/tasks/triggers"},
		Changes: []string{"?? internal/shared/tasks/triggers/parse.go"},
	}

	_, err = EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{
		TaskContext: tc,
		RepoDir:     "/repo",
		Factory:     factory,
		Git:         gitFake,
	})
	require.NoError(t, err)

	require.NotEmpty(t, client.gotReqs)
	prompt := client.gotReqs[0].Prompt.Content
	assert.Contains(t, prompt, "internal/shared/tasks/triggers", "the directory inventory must reach the model")
	assert.Contains(t, prompt, "?? internal/shared/tasks/triggers/parse.go", "uncommitted work must reach the model")
}

// The round-2 counterpart of TestEvaluateTriggers_RepoStateReachesThePrompt.
// Round 2 is the FINAL look at an escalated trigger — it may not settle one on
// LESS evidence than the tentative round that escalated it, and the
// existence-style trigger that motivated repo state in the first place is
// exactly the kind that escalates (U128-F11).
func TestEvaluateTriggers_RepoStateReachesTheEscalationPrompt(t *testing.T) {
	tc := newTaskContext(t, "proj-repo-state-round2")
	deferred, err := tasksops.AddTask(tc, "park me", "Deferred", "the internal/shared/tasks/triggers package exists")
	require.NoError(t, err)
	_, err = tasksops.AddTask(tc, "ship the triggers package", "Done", "")
	require.NoError(t, err)

	round1 := `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"needs-investigation","evidence":[],"reasoning":"unsure",` +
		`"queries":[{"type":"path_exists","path":"internal/shared/tasks/triggers"}]}]`
	round2 := `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"fired","evidence":["it exists"],"reasoning":"present now"}]`
	client := &fullFakeClient{outs: []string{round1, round2}}
	factory, _ := countingClientFactory(client)

	gitFake := &git.Fake{
		Dirs:    []string{"internal/shared/tasks/triggers"},
		Changes: []string{"?? internal/shared/tasks/triggers/parse.go"},
	}

	_, err = EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{
		TaskContext: tc,
		RepoDir:     "/repo",
		Factory:     factory,
		Git:         gitFake,
	})
	require.NoError(t, err)

	require.Len(t, client.gotReqs, 2)
	escalation := client.gotReqs[1].Prompt.Content
	assert.Contains(t, escalation, "=== Repository state right now ===", "round 2 must see what exists NOW")
	assert.Contains(t, escalation, "?? internal/shared/tasks/triggers/parse.go", "uncommitted work must reach the escalation round too")
	assert.Contains(t, escalation, "ship the triggers package", "the other-tasks cross-reference must survive into round 2")
}

func TestEvaluateTriggers_NeverMutatesTaskStatus(t *testing.T) {
	tc := newTaskContext(t, "proj-3")
	deferred, err := tasksops.AddTask(tc, "park me", "Deferred", "when x happens")
	require.NoError(t, err)

	client := &fullFakeClient{out: `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"fired","evidence":["commit abc"],"reasoning":"clearly fired"}]`}
	factory, _ := countingClientFactory(client)

	_, err = EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{TaskContext: tc, Factory: factory})
	require.NoError(t, err)

	list, err := tasksops.ListTasks(tc, []string{tasks.StatusDeferred}, "", true, false, 0)
	require.NoError(t, err)
	require.Len(t, list.Tasks, 1, "a fired verdict must not move the task off Deferred")
	assert.Equal(t, tasks.StatusDeferred, list.Tasks[0].Status)
}

func TestEvaluateTriggers_RetriesOnceThenSucceeds(t *testing.T) {
	tc := newTaskContext(t, "proj-4")
	deferred, err := tasksops.AddTask(tc, "park me", "Deferred", "when x happens")
	require.NoError(t, err)

	client := &fullFakeClient{outs: []string{
		"not json at all",
		`[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"not-fired","evidence":[],"reasoning":"nothing yet"}]`,
	}}
	factory, calls := countingClientFactory(client)

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{TaskContext: tc, Factory: factory})
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load(), "one retry after the first parse failure")
	assert.False(t, res.Degraded)
	require.Len(t, res.Verdicts, 1)
	assert.Equal(t, triggers.NotFired, res.Verdicts[0].Outcome)
}

func TestEvaluateTriggers_DegradesAfterExhaustingRetries(t *testing.T) {
	tc := newTaskContext(t, "proj-5")
	deferred, err := tasksops.AddTask(tc, "park me", "Deferred", "when x happens")
	require.NoError(t, err)

	client := &fullFakeClient{out: "garbage, always garbage"}
	factory, calls := countingClientFactory(client)

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{TaskContext: tc, Factory: factory})
	require.NoError(t, err, "a degraded evaluation is reported, not returned as a hard error")
	assert.Equal(t, int32(2), calls.Load(), "exactly one retry, never more")
	assert.True(t, res.Degraded)
	assert.NotEmpty(t, res.Warning)
	require.Len(t, res.Verdicts, 1)
	assert.Equal(t, deferred.Task.HarpID, res.Verdicts[0].HarpID)
	assert.Equal(t, triggers.CannotDetermine, res.Verdicts[0].Outcome, "a failed evaluation must never claim fired")
}

func TestEvaluateTriggers_MissingVerdictBecomesCannotDetermine(t *testing.T) {
	tc := newTaskContext(t, "proj-6")
	d1, err := tasksops.AddTask(tc, "task one", "Deferred", "condition one")
	require.NoError(t, err)
	d2, err := tasksops.AddTask(tc, "task two", "Deferred", "condition two")
	require.NoError(t, err)

	// The model only answers for d1.
	client := &fullFakeClient{out: `[{"harp_id":"` + d1.Task.HarpID + `","outcome":"fired","evidence":["commit abc"],"reasoning":"x"}]`}
	factory, _ := countingClientFactory(client)

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{TaskContext: tc, Factory: factory})
	require.NoError(t, err)
	assert.Equal(t, 2, res.Evaluated)
	require.Len(t, res.Verdicts, 2)

	byHarp := map[string]triggers.Verdict{}
	for _, v := range res.Verdicts {
		byHarp[v.HarpID] = v
	}
	assert.Equal(t, triggers.Fired, byHarp[d1.Task.HarpID].Outcome)
	assert.Equal(t, triggers.CannotDetermine, byHarp[d2.Task.HarpID].Outcome, "a task the model omitted must not silently vanish")
	assert.Equal(t, 1, res.Omitted, "the omission must be COUNTED, not just silently backstopped to cannot-determine")
}

// ---------------------------------------------------------------------------
// Chunking (the fix for the live silent-omission defect: a batch too large
// for one call makes the model drop tasks from its response)
// ---------------------------------------------------------------------------

// A miss set larger than ChunkSize must ride multiple model calls, one per
// chunk, rather than one call carrying everything — this is the mechanism
// the whole feature depends on.
func TestEvaluateTriggers_SplitsLargeMissSetIntoMultipleChunkCalls(t *testing.T) {
	tc := newTaskContext(t, "proj-chunk-1")
	var harps []string
	for i := 0; i < 5; i++ {
		task, err := tasksops.AddTask(tc, fmt.Sprintf("task %d", i), "Deferred", fmt.Sprintf("condition %d", i))
		require.NoError(t, err)
		harps = append(harps, task.Task.HarpID)
	}

	// Every chunk gets a genuine, fired verdict for whichever harp ids its
	// prompt mentions — dispatch on request content since chunk calls run
	// concurrently and completion order is not deterministic.
	client := &fullFakeClient{respond: func(req *pb.RunStart) string {
		var objs []string
		for _, h := range harps {
			if strings.Contains(req.Prompt.Content, h) {
				objs = append(objs, fmt.Sprintf(`{"harp_id":%q,"outcome":"fired","evidence":["commit abc"],"reasoning":"x"}`, h))
			}
		}
		return "[" + strings.Join(objs, ",") + "]"
	}}
	factory, calls := countingClientFactory(client)

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{
		TaskContext: tc,
		Factory:     factory,
		ChunkSize:   2,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), calls.Load(), "5 tasks at chunk size 2 must ride ceil(5/2)=3 calls")
	assert.Equal(t, 5, res.Evaluated)
	require.Len(t, res.Verdicts, 5)
	assert.False(t, res.Degraded)
	assert.Equal(t, 0, res.Omitted)
	for _, v := range res.Verdicts {
		assert.Equal(t, triggers.Fired, v.Outcome)
	}

	// No single chunk call's prompt carried every task's evidence.
	got := client.requestCount()
	require.Equal(t, 3, got)
	for i := 0; i < got; i++ {
		prompt := client.requestAt(i).Prompt.Content
		mentioned := 0
		for _, h := range harps {
			if strings.Contains(prompt, h) {
				mentioned++
			}
		}
		assert.LessOrEqual(t, mentioned, 2, "one chunk call must never carry more than ChunkSize tasks' evidence")
	}
}

// One chunk's call degrading (both attempts fail) must NOT poison the other
// chunks: their tasks still get real verdicts, and only the bad chunk's
// tasks fall back to cannot-determine.
func TestEvaluateTriggers_OneBadChunkDoesNotPoisonTheOthers(t *testing.T) {
	tc := newTaskContext(t, "proj-chunk-2")
	bad1, err := tasksops.AddTask(tc, "bad task one", "Deferred", "condition bad one")
	require.NoError(t, err)
	bad2, err := tasksops.AddTask(tc, "bad task two", "Deferred", "condition bad two")
	require.NoError(t, err)
	good1, err := tasksops.AddTask(tc, "good task one", "Deferred", "condition good one")
	require.NoError(t, err)
	good2, err := tasksops.AddTask(tc, "good task two", "Deferred", "condition good two")
	require.NoError(t, err)

	// bad1/bad2 land in one chunk (always garbage, so it degrades after its
	// retry); good1/good2 land in the other (a real, well-formed response).
	// Dispatch on request content — chunk order/interleaving is not
	// deterministic under concurrency.
	client := &fullFakeClient{respond: func(req *pb.RunStart) string {
		if strings.Contains(req.Prompt.Content, bad1.Task.HarpID) {
			return "garbage, not json, always garbage"
		}
		return `[{"harp_id":"` + good1.Task.HarpID + `","outcome":"fired","evidence":["commit abc"],"reasoning":"x"},` +
			`{"harp_id":"` + good2.Task.HarpID + `","outcome":"not-fired","evidence":[],"reasoning":"y"}]`
	}}
	factory, calls := countingClientFactory(client)

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{
		TaskContext: tc,
		Factory:     factory,
		ChunkSize:   2,
	})
	require.NoError(t, err)
	// The bad chunk retries once (2 calls), the good chunk succeeds first try
	// (1 call) — 3 total, run with bounded concurrency so order is not fixed.
	assert.Equal(t, int32(3), calls.Load())
	assert.True(t, res.Degraded, "the batch as a whole reports degraded because at least one chunk failed")
	assert.NotEmpty(t, res.Warning)

	byHarp := map[string]triggers.Verdict{}
	for _, v := range res.Verdicts {
		byHarp[v.HarpID] = v
	}
	require.Len(t, res.Verdicts, 4)
	assert.Equal(t, triggers.CannotDetermine, byHarp[bad1.Task.HarpID].Outcome, "the degraded chunk's tasks fall back")
	assert.Equal(t, triggers.CannotDetermine, byHarp[bad2.Task.HarpID].Outcome, "the degraded chunk's tasks fall back")
	assert.Equal(t, triggers.Fired, byHarp[good1.Task.HarpID].Outcome, "the OTHER chunk's real verdict must still land")
	assert.Equal(t, triggers.NotFired, byHarp[good2.Task.HarpID].Outcome, "the OTHER chunk's real verdict must still land")

	// A degraded chunk's fallback is a call/parse failure, not the model
	// silently dropping a task from an answer it produced — Omitted must
	// stay 0 here; Degraded/Warning already say what went wrong.
	assert.Equal(t, 0, res.Omitted)
}

// A chunk whose response is well-formed JSON but simply doesn't mention one
// of its assigned tasks is a genuine OMISSION (the live-observed defect),
// distinct from a degraded chunk — it must be counted in Omitted even though
// the chunk containing it did not degrade.
func TestEvaluateTriggers_ChunkOmissionIsCountedSeparatelyFromDegrade(t *testing.T) {
	tc := newTaskContext(t, "proj-chunk-3")
	kept, err := tasksops.AddTask(tc, "kept task", "Deferred", "condition kept")
	require.NoError(t, err)
	dropped, err := tasksops.AddTask(tc, "dropped task", "Deferred", "condition dropped")
	require.NoError(t, err)

	// Both tasks land in the same (only) chunk; the response only mentions
	// one of them.
	client := &fullFakeClient{out: `[{"harp_id":"` + kept.Task.HarpID + `","outcome":"fired","evidence":["commit abc"],"reasoning":"x"}]`}
	factory, calls := countingClientFactory(client)

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{
		TaskContext: tc,
		Factory:     factory,
		ChunkSize:   10,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load(), "one well-formed call, no retry — this is an omission, not a call failure")
	assert.False(t, res.Degraded, "an omission from an otherwise-valid response is not a degraded chunk")

	byHarp := map[string]triggers.Verdict{}
	for _, v := range res.Verdicts {
		byHarp[v.HarpID] = v
	}
	assert.Equal(t, triggers.Fired, byHarp[kept.Task.HarpID].Outcome)
	assert.Equal(t, triggers.CannotDetermine, byHarp[dropped.Task.HarpID].Outcome)
	assert.Equal(t, 1, res.Omitted, "the caller must be able to see that exactly one task was silently dropped")
}

// The round-2 escalation call is ALSO chunked and can degrade independently
// of round 1: if the escalation chunk containing a needs-investigation task
// fails, that task must land on cannot-determine (never fired/not-fired),
// without disturbing round 1's own Degraded/Warning bookkeeping.
// TestEvaluateTriggers_DegradedEscalationChunkSetsTopLevelDegraded fixes
// U088-F08: EvaluateTriggersResult.Degraded is documented as "true when at
// least one CHUNK's LLM call/parse failed even after its one retry"
// (task_triggers.go:106-116), and a round-2 (escalation) chunk's failure is
// not less real than a round-1 one — escalateNeedsInvestigation now folds
// its own degradation into result.Degraded/Warning instead of discarding it.
func TestEvaluateTriggers_DegradedEscalationChunkSetsTopLevelDegraded(t *testing.T) {
	tc := newTaskContext(t, "proj-chunk-4")
	deferred, err := tasksops.AddTask(tc, "park me", "Deferred", "when internal/foo exists")
	require.NoError(t, err)

	repo := t.TempDir()
	round1 := `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"needs-investigation","evidence":[],"reasoning":"unsure",` +
		`"queries":[{"type":"path_exists","path":"internal/foo"}]}]`
	client := &fullFakeClient{outs: []string{round1, "garbage", "still garbage"}}
	factory, calls := countingClientFactory(client)

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{
		TaskContext: tc,
		RepoDir:     repo,
		Factory:     factory,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), calls.Load(), "round 1 once, plus the (single-chunk) escalation round's own one retry")
	assert.True(t, res.Degraded, "a round-2 chunk's LLM/parse failure must reach Degraded, per its own doc — an escalation-round failure is not less real than a round-1 one")
	require.Len(t, res.Verdicts, 1)
	assert.Equal(t, triggers.CannotDetermine, res.Verdicts[0].Outcome)
	assert.Equal(t, 0, res.Omitted, "a degraded escalation chunk is a call failure, not an omission")
}

// ---------------------------------------------------------------------------
// Escalation (round 2 / evidence queries)
// ---------------------------------------------------------------------------

// TestEvaluateTriggers_EscalatesAndSettlesInRoundTwo proves the whole two-
// round protocol end to end: round 1 asks for a path_exists query, ctxloom
// executes it against a real repo dir, and round 2 (a second scripted model
// response) settles the verdict using the query result.
func TestEvaluateTriggers_EscalatesAndSettlesInRoundTwo(t *testing.T) {
	tc := newTaskContext(t, "proj-esc-1")
	deferred, err := tasksops.AddTask(tc, "wire the signing CLI", "Deferred", "when internal/signing/cli.go exists")
	require.NoError(t, err)

	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "internal", "signing"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "internal", "signing", "cli.go"), []byte("package signing\n"), 0o644))

	round1 := `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"needs-investigation","evidence":[],"reasoning":"unsure whether the file exists",` +
		`"queries":[{"type":"path_exists","path":"internal/signing/cli.go"}]}]`
	round2 := `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"fired","evidence":["path_exists confirmed"],"reasoning":"the file exists now"}]`
	client := &fullFakeClient{outs: []string{round1, round2}}
	factory, calls := countingClientFactory(client)

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{
		TaskContext: tc,
		RepoDir:     repo,
		Factory:     factory,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load(), "round 1 plus exactly one escalation round")
	require.Len(t, res.Verdicts, 1)
	assert.Equal(t, triggers.Fired, res.Verdicts[0].Outcome)
	assert.Contains(t, res.Verdicts[0].Reasoning, "exists now")
	assert.Empty(t, res.Verdicts[0].Queries, "the final verdict never carries the internal query request")

	// The round-2 prompt must have carried the query result forward.
	require.Len(t, client.gotReqs, 2)
	assert.Contains(t, client.gotReqs[1].Prompt.Content, "exists")
}

// BuildPrompt on an empty Batch is a zero-payload success: a complete prompt
// whose "=== Deferred tasks ===" header has nothing beneath it, asking a model
// to judge nothing and paying for the call. Nothing in production can build
// one, and THESE are the guards that make it so — an empty task set yields no
// chunk, so no prompt is ever constructed for it. They are load-bearing:
// remove either "return nil" and an empty-batch model call becomes reachable
// (U128-F07).
func TestChunking_AnEmptySetYieldsNoChunkSoNoPromptIsEverBuilt(t *testing.T) {
	assert.Empty(t, chunkMissTasks(nil, nil, defaultTriageChunkSize), "no cache misses means no round-1 call")
	assert.Empty(t, chunkMissTasks([]tasks.Task{}, []triggers.TaskInput{}, 0), "…including at the unbounded chunk size")
	assert.Empty(t, chunkFollowups(nil, defaultTriageChunkSize), "no escalations means no round-2 call")
	assert.Empty(t, chunkFollowups([]triggers.FollowupTask{}, 0))

	// And no chunk that IS produced is empty, so every prompt built from one
	// names at least one task.
	missTasks := []tasks.Task{{HarpID: "a"}, {HarpID: "b"}, {HarpID: "c"}}
	missInputs := []triggers.TaskInput{{HarpID: "a"}, {HarpID: "b"}, {HarpID: "c"}}
	for size := 1; size <= 4; size++ {
		for _, c := range chunkMissTasks(missTasks, missInputs, size) {
			assert.NotEmpty(t, c.inputs, "chunk size %d produced a chunk with no evidence in it", size)
			assert.NotEmpty(t, c.tasks, "chunk size %d produced a chunk with no tasks in it", size)
		}
		for _, c := range chunkFollowups([]triggers.FollowupTask{{TaskInput: triggers.TaskInput{HarpID: "a"}}, {TaskInput: triggers.TaskInput{HarpID: "b"}}}, size) {
			assert.NotEmpty(t, c, "chunk size %d produced an empty followup chunk", size)
		}
	}
}

// A needs-investigation verdict whose EVERY query was refused looks, from the
// outside, exactly like one that asked for nothing: no escalation call, the
// verdict left as-is. They are not the same event — the second is a model
// asking for shapes outside the whitelist, or paths that escape the repo — and
// the counts are the only thing that tells them apart (U128-F12).
func TestEvaluateTriggers_RefusedQueriesAreCountedNotSwallowed(t *testing.T) {
	tc := newTaskContext(t, "proj-refused-queries")
	deferred, err := tasksops.AddTask(tc, "park me", "Deferred", "when x happens")
	require.NoError(t, err)

	round1 := `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"needs-investigation","evidence":[],"reasoning":"unsure",` +
		`"queries":[{"type":"shell_exec","path":"internal/foo"},{"type":"path_exists","path":"/etc/passwd"},{"type":"grep"}]}]`
	client := &fullFakeClient{out: round1}
	factory, calls := countingClientFactory(client)

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{
		TaskContext: tc,
		Factory:     factory,
	})
	require.NoError(t, err)

	assert.Equal(t, int32(1), calls.Load(), "nothing survived sanitizing, so there is no round 2")
	assert.Equal(t, 3, res.QueriesRejected, "every refused query is counted")
	assert.Equal(t, 1, res.TasksRefusedEveryQuery, "the task had all of its queries refused")
	assert.False(t, res.Degraded, "a refused query is not a call or parse failure")
	assert.Equal(t, 0, res.Omitted, "a refused query is not a dropped task")
	require.Len(t, res.Verdicts, 1)
	assert.Equal(t, triggers.NeedsInvestigation, res.Verdicts[0].Outcome, "the verdict is still left for a human")
}

// The other side of the same gate: a model that asked for nothing has nothing
// refused, so the counts stay at zero and cannot be read as a fault.
func TestEvaluateTriggers_NoQueriesAskedCountsNoRefusals(t *testing.T) {
	tc := newTaskContext(t, "proj-no-queries-asked")
	deferred, err := tasksops.AddTask(tc, "park me", "Deferred", "when x happens")
	require.NoError(t, err)

	client := &fullFakeClient{out: `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"needs-investigation","evidence":[],"reasoning":"unsure"}]`}
	factory, _ := countingClientFactory(client)

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{
		TaskContext: tc,
		Factory:     factory,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, res.QueriesRejected)
	assert.Equal(t, 0, res.TasksRefusedEveryQuery)
}

// A round-1 needs-investigation with NO queries has nothing for round 2 to
// execute — it must NOT trigger an escalation call, and stays
// needs-investigation for a human to look at directly.
func TestEvaluateTriggers_NeedsInvestigationWithNoQueriesStaysUnescalated(t *testing.T) {
	tc := newTaskContext(t, "proj-esc-2")
	deferred, err := tasksops.AddTask(tc, "park me", "Deferred", "when something vague happens")
	require.NoError(t, err)

	client := &fullFakeClient{out: `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"needs-investigation","evidence":[],"reasoning":"unclear"}]`}
	factory, calls := countingClientFactory(client)

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{TaskContext: tc, Factory: factory})
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load(), "no queries means no escalation call")
	require.Len(t, res.Verdicts, 1)
	assert.Equal(t, triggers.NeedsInvestigation, res.Verdicts[0].Outcome)
}

// If round 2 still comes back needs-investigation (a model ignoring the
// round-2 instruction), that must be forced to cannot-determine — the hard
// cap that keeps escalation to exactly one round, never a loop.
func TestEvaluateTriggers_RoundTwoNeedsInvestigationIsForcedToCannotDetermine(t *testing.T) {
	tc := newTaskContext(t, "proj-esc-3")
	deferred, err := tasksops.AddTask(tc, "park me", "Deferred", "when internal/foo exists")
	require.NoError(t, err)

	repo := t.TempDir()
	round1 := `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"needs-investigation","evidence":[],"reasoning":"unsure",` +
		`"queries":[{"type":"path_exists","path":"internal/foo"}]}]`
	round2 := `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"needs-investigation","evidence":[],"reasoning":"still unsure"}]`
	client := &fullFakeClient{outs: []string{round1, round2}}
	factory, calls := countingClientFactory(client)

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{
		TaskContext: tc,
		RepoDir:     repo,
		Factory:     factory,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load())
	require.Len(t, res.Verdicts, 1)
	assert.Equal(t, triggers.CannotDetermine, res.Verdicts[0].Outcome)
}

// Malicious/adversarial queries in the round-1 response (path traversal, an
// unwhitelisted type) must be dropped rather than executed — but a task that
// mixes one bad query with one good one still gets to escalate on the good
// one.
func TestEvaluateTriggers_DropsUnsafeQueriesButKeepsSafeOnes(t *testing.T) {
	tc := newTaskContext(t, "proj-esc-4")
	deferred, err := tasksops.AddTask(tc, "park me", "Deferred", "when internal/foo exists")
	require.NoError(t, err)

	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "internal", "foo"), 0o755))

	round1 := `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"needs-investigation","evidence":[],"reasoning":"unsure",` +
		`"queries":[{"type":"path_exists","path":"../../etc/passwd"},{"type":"shell_exec","path":"x"},{"type":"path_exists","path":"internal/foo"}]}]`
	round2 := `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"fired","evidence":["commit abc"],"reasoning":"confirmed"}]`
	client := &fullFakeClient{outs: []string{round1, round2}}
	factory, calls := countingClientFactory(client)

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{
		TaskContext: tc,
		RepoDir:     repo,
		Factory:     factory,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load(), "the surviving safe query still triggers round 2")
	require.Len(t, client.gotReqs, 2)
	assert.NotContains(t, client.gotReqs[1].Prompt.Content, "etc/passwd", "the rejected query must never reach a follow-up prompt as if it ran")
	require.Len(t, res.Verdicts, 1)
	assert.Equal(t, triggers.Fired, res.Verdicts[0].Outcome)
}

// A degraded escalation round (both round-2 attempts fail) must resolve to
// cannot-determine per task, never silently drop the task or claim fired.
func TestEvaluateTriggers_DegradedEscalationRoundBecomesCannotDetermine(t *testing.T) {
	tc := newTaskContext(t, "proj-esc-5")
	deferred, err := tasksops.AddTask(tc, "park me", "Deferred", "when internal/foo exists")
	require.NoError(t, err)

	repo := t.TempDir()
	round1 := `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"needs-investigation","evidence":[],"reasoning":"unsure",` +
		`"queries":[{"type":"path_exists","path":"internal/foo"}]}]`
	client := &fullFakeClient{outs: []string{round1, "garbage", "still garbage"}}
	factory, calls := countingClientFactory(client)

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{
		TaskContext: tc,
		RepoDir:     repo,
		Factory:     factory,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), calls.Load(), "round 1 once, plus the escalation round's own one retry")
	require.Len(t, res.Verdicts, 1)
	assert.Equal(t, triggers.CannotDetermine, res.Verdicts[0].Outcome)
}

// U088-F03: a round-2 (escalation) chunk failure marks that task
// cannot-determine, but round 1 had already marked its harp id cacheable (a
// needs-investigation with valid queries IS a genuine round-1 answer) — and
// nothing cleared that mark on the round-2 failure path, so the transient
// fallback got persisted to the verdict cache, contradicting
// EvaluateTriggersResult.Degraded's own doc ("Degraded/cannot-determine
// fallback verdicts are never written to the cache"). A fresh client/factory
// for the second call proves whether the second EvaluateTriggers call
// actually re-ran both rounds (uncached) or served a poisoned cache entry.
func TestEvaluateTriggers_DegradedEscalationRoundIsNeverCached(t *testing.T) {
	tc := newTaskContext(t, "proj-esc-6")
	deferred, err := tasksops.AddTask(tc, "park me", "Deferred", "when internal/foo exists")
	require.NoError(t, err)

	repo := t.TempDir()
	round1 := `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"needs-investigation","evidence":[],"reasoning":"unsure",` +
		`"queries":[{"type":"path_exists","path":"internal/foo"}]}]`
	client1 := &fullFakeClient{outs: []string{round1, "garbage", "still garbage"}}
	factory1, calls1 := countingClientFactory(client1)

	res1, err := EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{
		TaskContext: tc,
		RepoDir:     repo,
		Factory:     factory1,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), calls1.Load())
	require.Len(t, res1.Verdicts, 1)
	assert.Equal(t, triggers.CannotDetermine, res1.Verdicts[0].Outcome)

	// A DIFFERENT client/factory for the second call: if the first call's
	// degraded-escalation fallback was (incorrectly) cached, this one is
	// never even consulted and the second result would still read
	// cannot-determine with zero new calls.
	round2 := `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"fired","evidence":["path_exists confirmed"],"reasoning":"now it resolves"}]`
	client2 := &fullFakeClient{outs: []string{round1, round2}}
	factory2, calls2 := countingClientFactory(client2)

	res2, err := EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{
		TaskContext: tc,
		RepoDir:     repo,
		Factory:     factory2,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls2.Load(), "the degraded-escalation verdict was never cached, so both rounds ran again")
	require.Len(t, res2.Verdicts, 1)
	assert.Equal(t, triggers.Fired, res2.Verdicts[0].Outcome)
	assert.False(t, res2.Verdicts[0].Cached)
}

// U088-F09: triggers.ParseVerdicts only checks that harp_id is non-empty,
// never that it belongs to the chunk that was actually asked about — a
// hallucinated (or cross-chunk) harp id must be dropped before it can ever
// reach the verdict cache, where it would sit forever (nothing prunes
// entries for harp ids outside the current Deferred set).
func TestEvaluateTriggers_HallucinatedHarpIDIsNotCached(t *testing.T) {
	tc := newTaskContext(t, "proj-hallucinate-1")
	deferred, err := tasksops.AddTask(tc, "wire the signing CLI", "Deferred", "when the signing CLI ships")
	require.NoError(t, err)

	client := &fullFakeClient{out: `[` +
		`{"harp_id":"` + deferred.Task.HarpID + `","outcome":"fired","evidence":["commit abc"],"reasoning":"it shipped"},` +
		`{"harp_id":"ghost-not-in-request","outcome":"not-fired","evidence":[],"reasoning":"hallucinated"}]`}
	factory, calls := countingClientFactory(client)

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), EvaluateTriggersRequest{TaskContext: tc, Factory: factory})
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load())
	require.Len(t, res.Verdicts, 1, "only the actually-requested task's verdict must be surfaced")
	assert.Equal(t, deferred.Task.HarpID, res.Verdicts[0].HarpID)

	cache := loadTriggerCache(tc.ProjectID)
	_, hallucinated := cache.Tasks["ghost-not-in-request"]
	assert.False(t, hallucinated, "a verdict for a harp id never in the request must not be persisted to the cache")
}

// ---------------------------------------------------------------------------
// Verdict caching
// ---------------------------------------------------------------------------

func TestEvaluateTriggers_SecondCallReusesCacheAndMakesZeroModelCalls(t *testing.T) {
	tc := newTaskContext(t, "proj-cache-1")
	deferred, err := tasksops.AddTask(tc, "wire the signing CLI", "Deferred", "when the signing CLI ships")
	require.NoError(t, err)

	client := &fullFakeClient{out: `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"fired","evidence":["commit abc"],"reasoning":"it shipped"}]`}
	factory, calls := countingClientFactory(client)

	req := EvaluateTriggersRequest{TaskContext: tc, Factory: factory}

	res1, err := EvaluateTriggers(context.Background(), triageTestConfig(), req)
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load())
	require.Len(t, res1.Verdicts, 1)
	assert.False(t, res1.Verdicts[0].Cached)
	assert.Equal(t, 0, res1.CacheHits)
	assert.Equal(t, 1, res1.CacheMisses)

	res2, err := EvaluateTriggers(context.Background(), triageTestConfig(), req)
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load(), "second call must make ZERO additional model calls")
	require.Len(t, res2.Verdicts, 1)
	assert.True(t, res2.Verdicts[0].Cached)
	assert.Equal(t, triggers.Fired, res2.Verdicts[0].Outcome)
	assert.Equal(t, 1, res2.CacheHits)
	assert.Equal(t, 0, res2.CacheMisses)
}

// Refresh must bypass the cache even when nothing about the evidence changed.
func TestEvaluateTriggers_RefreshBypassesCache(t *testing.T) {
	tc := newTaskContext(t, "proj-cache-2")
	deferred, err := tasksops.AddTask(tc, "park me", "Deferred", "when x happens")
	require.NoError(t, err)

	client := &fullFakeClient{out: `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"not-fired","evidence":[],"reasoning":"nothing yet"}]`}
	factory, calls := countingClientFactory(client)

	req := EvaluateTriggersRequest{TaskContext: tc, Factory: factory}
	_, err = EvaluateTriggers(context.Background(), triageTestConfig(), req)
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load())

	req.Refresh = true
	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), req)
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load(), "refresh must force a fresh model call")
	assert.False(t, res.Verdicts[0].Cached)
	assert.Equal(t, 1, res.CacheMisses)
}

// A change to the task's evidence (a new commit landing since it was
// deferred) must invalidate the cache — the fingerprint changes, so the
// stale verdict is not reused.
func TestEvaluateTriggers_EvidenceChangeInvalidatesCache(t *testing.T) {
	tc := newTaskContext(t, "proj-cache-3")
	deferred, err := tasksops.AddTask(tc, "wire the signing CLI", "Deferred", "when the signing CLI ships")
	require.NoError(t, err)

	client := &fullFakeClient{out: `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"not-fired","evidence":[],"reasoning":"nothing yet"}]`}
	factory, calls := countingClientFactory(client)

	gitFake := &git.Fake{LogEntries: map[string][]git.LogEntry{}}
	req := EvaluateTriggersRequest{TaskContext: tc, RepoDir: "/repo", Factory: factory, Git: gitFake}

	_, err = EvaluateTriggers(context.Background(), triageTestConfig(), req)
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load())

	// New evidence appears: a commit lands after the first evaluation.
	gitFake.LogEntries["/repo"] = []git.LogEntry{{SHA: "abc1234567890", Date: time.Now(), Subject: "feat: ship the CLI", Files: []string{"cli.go"}}}
	client.out = `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"fired","evidence":["commit abc"],"reasoning":"it shipped"}]`

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), req)
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load(), "new evidence must invalidate the cache and trigger a fresh call")
	assert.Equal(t, triggers.Fired, res.Verdicts[0].Outcome)
	assert.False(t, res.Verdicts[0].Cached)
}

// A degraded (fallback) verdict must never be cached — a transient model
// failure shouldn't freeze the task at cannot-determine until its evidence
// happens to change.
func TestEvaluateTriggers_DegradedVerdictsAreNeverCached(t *testing.T) {
	tc := newTaskContext(t, "proj-cache-4")
	deferred, err := tasksops.AddTask(tc, "park me", "Deferred", "when x happens")
	require.NoError(t, err)

	client := &fullFakeClient{out: "garbage, always garbage"}
	factory, calls := countingClientFactory(client)
	req := EvaluateTriggersRequest{TaskContext: tc, Factory: factory}

	res1, err := EvaluateTriggers(context.Background(), triageTestConfig(), req)
	require.NoError(t, err)
	assert.True(t, res1.Degraded)
	assert.Equal(t, int32(2), calls.Load(), "one retry on the first call")

	client.out = `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"fired","evidence":["commit abc"],"reasoning":"now it works"}]`
	res2, err := EvaluateTriggers(context.Background(), triageTestConfig(), req)
	require.NoError(t, err)
	assert.Equal(t, int32(3), calls.Load(), "the degraded verdict was never cached, so the model is asked again")
	assert.Equal(t, triggers.Fired, res2.Verdicts[0].Outcome)
	assert.False(t, res2.Verdicts[0].Cached)
}

// EVERY final, non-degraded verdict is cached — including a
// needs-investigation the model declined to attach queries to. Live
// regression: leaving those uncached meant an unchanged backlog still burned
// a model call every run (and, the model being nondeterministic, re-rolled a
// different verdict on identical evidence). The cache's contract is "same
// evidence => same answer, zero model calls"; refresh is the way to re-roll.
func TestEvaluateTriggers_UnescalatedNeedsInvestigationIsCached(t *testing.T) {
	tc := newTaskContext(t, "proj-cache-5")
	deferred, err := tasksops.AddTask(tc, "park me", "Deferred", "when something vague happens")
	require.NoError(t, err)

	client := &fullFakeClient{out: `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"needs-investigation","evidence":[],"reasoning":"unclear"}]`}
	factory, calls := countingClientFactory(client)
	req := EvaluateTriggersRequest{TaskContext: tc, Factory: factory}

	_, err = EvaluateTriggers(context.Background(), triageTestConfig(), req)
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load())

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), req)
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load(), "identical evidence must hit the cache and make ZERO further model calls")
	require.Len(t, res.Verdicts, 1)
	assert.True(t, res.Verdicts[0].Cached)
	assert.Equal(t, triggers.NeedsInvestigation, res.Verdicts[0].Outcome)
	assert.Equal(t, 1, res.CacheHits)
	assert.Equal(t, 0, res.CacheMisses)
}

// The whole-batch property the cache exists for: a second run over an
// unchanged backlog makes ZERO model calls, whatever mix of outcomes it holds.
func TestEvaluateTriggers_UnchangedBacklogSecondRunMakesZeroModelCalls(t *testing.T) {
	tc := newTaskContext(t, "proj-cache-8")
	d1, err := tasksops.AddTask(tc, "task one", "Deferred", "condition one")
	require.NoError(t, err)
	d2, err := tasksops.AddTask(tc, "task two", "Deferred", "condition two")
	require.NoError(t, err)
	d3, err := tasksops.AddTask(tc, "task three", "Deferred", "condition three")
	require.NoError(t, err)

	client := &fullFakeClient{out: `[` +
		`{"harp_id":"` + d1.Task.HarpID + `","outcome":"fired","evidence":["commit abc"],"reasoning":"a"},` +
		`{"harp_id":"` + d2.Task.HarpID + `","outcome":"not-fired","evidence":[],"reasoning":"b"},` +
		`{"harp_id":"` + d3.Task.HarpID + `","outcome":"needs-investigation","evidence":[],"reasoning":"c"}]`}
	factory, calls := countingClientFactory(client)
	req := EvaluateTriggersRequest{TaskContext: tc, Factory: factory}

	_, err = EvaluateTriggers(context.Background(), triageTestConfig(), req)
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load())

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), req)
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load(), "an unchanged backlog must make ZERO model calls on the second run")
	assert.Equal(t, 3, res.CacheHits)
	assert.Equal(t, 0, res.CacheMisses)
	for _, v := range res.Verdicts {
		assert.True(t, v.Cached)
	}
}

// A verdict SETTLED via escalation (round 2) is cached — a later call with
// identical evidence must reuse it and never re-run either round.
func TestEvaluateTriggers_EscalatedVerdictIsCachedAfterSettling(t *testing.T) {
	tc := newTaskContext(t, "proj-cache-6")
	deferred, err := tasksops.AddTask(tc, "wire the signing CLI", "Deferred", "when internal/signing/cli.go exists")
	require.NoError(t, err)

	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "internal", "signing"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "internal", "signing", "cli.go"), []byte("package signing\n"), 0o644))

	round1 := `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"needs-investigation","evidence":[],"reasoning":"unsure",` +
		`"queries":[{"type":"path_exists","path":"internal/signing/cli.go"}]}]`
	round2 := `[{"harp_id":"` + deferred.Task.HarpID + `","outcome":"fired","evidence":["commit abc"],"reasoning":"confirmed via query"}]`
	client := &fullFakeClient{outs: []string{round1, round2}}
	factory, calls := countingClientFactory(client)

	req := EvaluateTriggersRequest{TaskContext: tc, RepoDir: repo, Factory: factory}
	res1, err := EvaluateTriggers(context.Background(), triageTestConfig(), req)
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load())
	assert.Equal(t, triggers.Fired, res1.Verdicts[0].Outcome)

	res2, err := EvaluateTriggers(context.Background(), triageTestConfig(), req)
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load(), "the settled verdict is cached; a second identical call makes no further calls")
	assert.True(t, res2.Verdicts[0].Cached)
	assert.Equal(t, triggers.Fired, res2.Verdicts[0].Outcome)
}

// A mixed batch (one cache hit, one miss) must only call the model with the
// miss task's evidence, and the result must include both.
func TestEvaluateTriggers_MixedCacheHitAndMissOnlyCallsModelForTheMiss(t *testing.T) {
	tc := newTaskContext(t, "proj-cache-7")
	d1, err := tasksops.AddTask(tc, "task one", "Deferred", "condition one")
	require.NoError(t, err)

	client := &fullFakeClient{out: `[{"harp_id":"` + d1.Task.HarpID + `","outcome":"not-fired","evidence":[],"reasoning":"nothing yet"}]`}
	factory, calls := countingClientFactory(client)
	req := EvaluateTriggersRequest{TaskContext: tc, Factory: factory}

	_, err = EvaluateTriggers(context.Background(), triageTestConfig(), req)
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load())

	// A second task is deferred after the first call; only it should be a
	// cache miss on the next evaluation.
	d2, err := tasksops.AddTask(tc, "task two", "Deferred", "condition two")
	require.NoError(t, err)
	client.out = `[{"harp_id":"` + d2.Task.HarpID + `","outcome":"fired","evidence":["commit abc"],"reasoning":"it happened"}]`

	res, err := EvaluateTriggers(context.Background(), triageTestConfig(), req)
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load(), "only the new task should require a model call")
	require.Len(t, res.Verdicts, 2)
	assert.Equal(t, 1, res.CacheHits)
	assert.Equal(t, 1, res.CacheMisses)

	byHarp := map[string]triggers.Verdict{}
	for _, v := range res.Verdicts {
		byHarp[v.HarpID] = v
	}
	assert.True(t, byHarp[d1.Task.HarpID].Cached)
	assert.False(t, byHarp[d2.Task.HarpID].Cached)
	assert.Equal(t, triggers.Fired, byHarp[d2.Task.HarpID].Outcome)

	// Prompt sent for the miss call must only reference the miss task.
	require.Len(t, client.gotReqs, 2)
	assert.NotContains(t, client.gotReqs[1].Prompt.Content, d1.Task.HarpID)
	assert.Contains(t, client.gotReqs[1].Prompt.Content, d2.Task.HarpID)
}

// ---------------------------------------------------------------------------
// fillMissingVerdicts
// ---------------------------------------------------------------------------

// U088-F22: fillMissingVerdicts used to do `out := got` and append onto it —
// when got carries spare capacity, append reuses its backing array, so a
// later in-place rewrite through the returned slice (e.g.
// escalateNeedsInvestigation's verdicts[i] = ...) silently mutates the
// caller's own slice too. fillMissingVerdicts must return an independent
// copy.
func TestFillMissingVerdicts_DoesNotAliasCallersBackingArray(t *testing.T) {
	got := make([]triggers.Verdict, 1, 4) // spare capacity so append would reuse the backing array
	got[0] = triggers.Verdict{HarpID: "a", Outcome: triggers.Fired}
	deferred := []tasks.Task{{HarpID: "a"}, {HarpID: "b"}}

	out := fillMissingVerdicts(deferred, got, "reason")
	require.Len(t, out, 2)

	out[0].Outcome = triggers.NotFired
	assert.Equal(t, triggers.Fired, got[0].Outcome, "fillMissingVerdicts must not alias the caller's backing array")
}
