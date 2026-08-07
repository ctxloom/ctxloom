package operations

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/git"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	tasksops "github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/triggers"
)

// Trigger evaluation bounds: a batch call must never blow the fast model's
// context budget, even on a project with a long-Deferred task and heavy churn
// since, or one with hundreds of other tasks to cross-reference.
const (
	defaultMaxCommitsPerTask = 20
	maxOtherTasksInPack      = 200
	maxFilesPerTask          = 40

	// repoDirsScanCap and workingChangesScanCap bound the repo-global "what
	// exists NOW" evidence. They are stated here, not left to the git seam's
	// own defaults, because whoever owns the bound is the only one who can
	// notice a listing that HIT it — and a listing that hit it must be
	// declared truncated in the prompt rather than shown as complete.
	repoDirsScanCap       = 400
	workingChangesScanCap = 100

	// defaultTriageChunkSize caps how many cache-miss tasks ride in ONE
	// batch-triage model call. A single call carrying a large enough batch of
	// tasks' evidence packs makes the fast model silently DROP tasks from its
	// JSON response — never a wrong verdict (fillMissingVerdicts always
	// backstops an omission to cannot-determine), but a useless answer for an
	// arbitrary subset, and a project's Deferred backlog only grows over
	// time.
	//
	// Live-measured against this project's own Deferred backlog (39 tasks,
	// haiku as the fast model) with refresh=true so every task was a fresh
	// model call: one unchunked call over all 39 dropped 9 of them (23%) from
	// its response. Chunk size 20 (2 calls) and chunk size 10 (4 calls) both
	// came back with zero drops on the same backlog. 10 is chosen over 20
	// deliberately for headroom: it isn't the largest size observed to work,
	// it is comfortably under it, because a batch big enough to fail once
	// will keep failing as the backlog grows and there is no per-project
	// re-tuning of this value.
	defaultTriageChunkSize = 10

	// triageConcurrency bounds how many chunk calls run in parallel, mirroring
	// internal/memory/compactor.go's distillConcurrency — each chunk spawns
	// its own LLM plugin subprocess, so this caps concurrent subprocesses
	// (and provider rate pressure) instead of firing every chunk at once.
	triageConcurrency = 4
)

// EvaluateTriggersRequest configures one batch trigger-triage run.
type EvaluateTriggersRequest struct {
	// TaskContext resolves the project's task store (see
	// internal/shared/tasks/operations.TaskContext).
	TaskContext tasksops.TaskContext

	// RepoDir is the git working tree to gather commit evidence from. Empty
	// skips git evidence entirely (the model still judges the trigger text
	// and cross-references other tasks).
	RepoDir string

	// LLMLabel is the config label to use for the batch call. Empty resolves
	// to cfg.FastLabel() — trigger triage is a cheap batch judgment, not a
	// coding task, so it defaults to the fast role rather than primary.
	LLMLabel string

	// MaxCommits caps how many commits are gathered per task. <=0 uses
	// defaultMaxCommitsPerTask.
	MaxCommits int

	// ChunkSize caps how many cache-miss tasks ride in one batch-triage model
	// call. <=0 uses defaultTriageChunkSize. A test seam primarily — the
	// default is chosen from live evidence (see defaultTriageChunkSize) and
	// production callers should not normally need to override it.
	ChunkSize int

	// Refresh bypasses the verdict cache: every Deferred task is treated as a
	// cache miss (re-evaluated by the model), and the cache is overwritten
	// with the fresh results. Use it when the evidence gathered here isn't
	// what actually changed (e.g. the caller knows something out-of-band
	// happened) or to force a second opinion.
	Refresh bool

	// Factory/Git are test seams; nil selects the real backend (
	// pb.DefaultClientFactory) and the real git binary (git.NewExec).
	Factory pb.ClientFactory
	Git     git.Git
}

// EvaluateTriggersResult is a batch triage's outcome: proposed verdicts for a
// human to confirm. See EvaluateTriggers — this is never used to mutate task
// status.
type EvaluateTriggersResult struct {
	Verdicts  []triggers.Verdict
	Evaluated int // number of Deferred tasks considered

	// CacheHits/CacheMisses partition Evaluated: a hit reused a cached
	// verdict (no model call for that task, see triggers.Verdict.Cached); a
	// miss went through the model this run. CacheHits == Evaluated means this
	// call made ZERO model calls.
	CacheHits   int
	CacheMisses int

	// Degraded is true when at least one CHUNK's LLM call/parse failed even
	// after its one retry (a cache hit can never be degraded). The batch is
	// chunked (see defaultTriageChunkSize) and each chunk succeeds or fails
	// independently — a bad chunk degrades only its own tasks to a
	// cannot-determine fallback, never the whole batch. Warning summarizes
	// every degraded chunk's failure, joined, so Degraded/Warning stay
	// meaningful even when most of the batch came back fine. Degraded/
	// cannot-determine fallback verdicts are never written to the cache — a
	// transient model failure must not freeze a task's verdict until its
	// evidence happens to change.
	Degraded bool
	Warning  string

	// Omitted counts task-verdicts that fell back to cannot-determine
	// because a chunk's model response was otherwise well-formed but simply
	// DID NOT MENTION that task — the live-observed failure mode this
	// feature exists to make visible. It is disjoint from Degraded (a chunk
	// whose call/parse failed outright): Omitted only counts individual
	// tasks silently dropped from an answer the model DID produce, in either
	// round 1 or the round-2 escalation. Zero is the target; a nonzero value
	// means the model is still dropping tasks even at the current chunk
	// size, and the caller should surface it rather than let it hide inside
	// an undifferentiated pile of cannot-determine verdicts.
	Omitted int

	// QueriesRejected counts the follow-up evidence queries a round-1
	// needs-investigation verdict asked for and ctxloom REFUSED, because they
	// failed the whitelist/containment gate (an unsupported query type, an
	// absolute or escaping path, a grep with no pattern). It is disjoint from
	// both Degraded and Omitted: nothing failed and nothing was dropped from a
	// response — ctxloom declined to run something the model asked for.
	//
	// TasksRefusedEveryQuery is the subset that matters most: verdicts that
	// asked for at least one query and had every one refused. Those stay
	// needs-investigation, which is exactly what a verdict that asked for
	// nothing does, so without this count the two are indistinguishable and a
	// model reliably requesting malformed queries looks like a model
	// declining to escalate.
	QueriesRejected        int
	TasksRefusedEveryQuery int
}

// EvaluateTriggers reads Deferred tasks, assembles a bounded evidence pack
// per task (git history since it was deferred, changed files, other tasks'
// status), and asks the fast model for a triage verdict on every trigger in
// ONE call. It is READ-ONLY: it proposes verdicts and never calls
// tasksops.SetTaskStatus itself. Reviving a task on a model's say-so with no
// human in the loop risks a FALSE REVIVE — worse than leaving a fired
// trigger parked one cycle longer than necessary — so confirming and moving
// the task is left entirely to the caller (the check-triggers command, or
// whatever calls the evaluate_triggers MCP tool).
func EvaluateTriggers(ctx context.Context, cfg *config.Config, req EvaluateTriggersRequest) (*EvaluateTriggersResult, error) {
	listRes, err := tasksops.ListTasks(req.TaskContext, tasksops.ListOptions{IncludeDone: true})
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}

	var deferred, other []tasks.Task
	allByHarp := make(map[string]tasks.Task, len(listRes.Tasks))
	for _, t := range listRes.Tasks {
		allByHarp[t.HarpID] = t
		if t.Status == tasks.StatusDeferred {
			deferred = append(deferred, t)
		} else {
			other = append(other, t)
		}
	}
	if len(deferred) == 0 {
		return &EvaluateTriggersResult{}, nil
	}

	// Best-effort: a DeferredSince failure degrades to "unknown since" for
	// every task rather than failing the whole evaluation — the model still
	// judges the trigger text, just without a bounded git window.
	since, sinceErr := tasksops.DeferredSince(req.TaskContext)
	if sinceErr != nil {
		since = map[string]time.Time{}
	}

	// Evidence is gathered for EVERY Deferred task up front, cache hit or
	// not: the fingerprint that decides hit/miss is a hash of exactly this
	// evidence (see fingerprintTask), so it has to exist before the split.
	// This costs git reads even on a cache hit, but never a model call — the
	// cache's entire point is to save the LLM round-trip, not the local I/O.
	batch := buildBatch(ctx, deferred, other, since, req)
	deferredByHarp := make(map[string]triggers.TaskInput, len(batch.Tasks))
	for _, ti := range batch.Tasks {
		deferredByHarp[ti.HarpID] = ti
	}

	gitClient := req.Git
	if gitClient == nil {
		gitClient = git.NewExec()
	}

	cache := loadTriggerCache(listRes.ProjectID)
	var cachedVerdicts []triggers.Verdict
	var missInputs []triggers.TaskInput
	var missTasks []tasks.Task
	for _, t := range deferred {
		ti := deferredByHarp[t.HarpID]
		fp := fingerprintTask(ti, batch.Repo)
		if !req.Refresh {
			if entry, ok := cache.Tasks[t.HarpID]; ok && entry.Fingerprint == fp {
				v := entry.Verdict
				v.Cached = true
				cachedVerdicts = append(cachedVerdicts, v)
				continue
			}
		}
		missInputs = append(missInputs, ti)
		missTasks = append(missTasks, t)
	}

	result := &EvaluateTriggersResult{
		Evaluated:   len(deferred),
		CacheHits:   len(cachedVerdicts),
		CacheMisses: len(missInputs),
	}

	freshByHarp := make(map[string]triggers.Verdict, len(missInputs))
	if len(missInputs) > 0 {
		factory := req.Factory
		if factory == nil {
			factory = pb.DefaultClientFactory()
		}
		label := req.LLMLabel
		if label == "" {
			label = cfg.FastLabel()
		}
		backendName, model := cfg.ResolveLLM(label)

		chunkSize := req.ChunkSize
		if chunkSize <= 0 {
			chunkSize = defaultTriageChunkSize
		}

		// The batch is split into bounded chunks — one model call per chunk,
		// run with bounded concurrency — rather than one call for the whole
		// miss set. See defaultTriageChunkSize for why: a big-enough single
		// call makes the model silently drop tasks from its JSON response.
		chunkResults := runTriageChunks(ctx, chunkMissTasks(missTasks, missInputs, chunkSize), batch, factory, backendName, label, model, cfg.LabelEnv(label))

		// A verdict is cacheable once it is a GENUINE model answer — anything
		// the model actually returned this round, including a
		// needs-investigation it declined to attach queries to. Only
		// degraded/fallback verdicts are withheld (see below).
		//
		// Caching needs-investigation is deliberate: the fingerprint already
		// pins the evidence, so re-asking on identical evidence buys nothing
		// but a model call and a re-roll of a nondeterministic answer. This
		// is what makes "unchanged backlog => ZERO model calls" true rather
		// than nearly-true. `refresh` is the way to force another look.
		cacheable := map[string]bool{}
		var verdicts []triggers.Verdict
		var degradedWarnings []string
		omitted := 0
		for _, cr := range chunkResults {
			verdicts = append(verdicts, cr.verdicts...)
			omitted += cr.omitted
			if cr.degraded {
				degradedWarnings = append(degradedWarnings, cr.warning)
				continue
			}
			for _, v := range cr.parsed {
				cacheable[v.HarpID] = true
			}
		}
		result.Degraded = len(degradedWarnings) > 0
		result.Warning = strings.Join(degradedWarnings, "; ")

		// Escalation runs once, across the WHOLE merged set (not per input
		// chunk): round-1 chunking is about the input evidence pack size, but
		// how many tasks land in needs-investigation is independent of which
		// chunk they came from, and re-chunking on THAT count (bounded the
		// same way) keeps the round-2 prompt from hitting the identical
		// failure mode on a backlog with many ambiguous triggers.
		esc := escalateNeedsInvestigation(ctx, escalationParams{
			factory:        factory,
			backendName:    backendName,
			label:          label,
			model:          model,
			env:            cfg.LabelEnv(label),
			gitClient:      gitClient,
			repoDir:        req.RepoDir,
			otherByHarp:    allByHarp,
			deferredByHarp: deferredByHarp,
			repo:           batch.Repo,
			otherTasks:     batch.OtherTasks,
			cacheable:      cacheable,
			chunkSize:      chunkSize,
		}, verdicts)
		verdicts = esc.verdicts
		result.Omitted = omitted + esc.omitted
		result.QueriesRejected = esc.queriesRejected
		result.TasksRefusedEveryQuery = esc.tasksRefusedEveryQuery
		if esc.degraded {
			result.Degraded = true
			if result.Warning != "" {
				result.Warning += "; " + esc.warning
			} else {
				result.Warning = esc.warning
			}
		}

		// The outward verdict contract never carries a query request — that's
		// an internal round-1↔round-2 signal, not something the caller (or a
		// human reading the result) needs.
		for i := range verdicts {
			verdicts[i].Queries = nil
			v := verdicts[i]
			freshByHarp[v.HarpID] = v
			if cacheable[v.HarpID] {
				stored := v
				stored.Cached = false
				cache.Tasks[v.HarpID] = triggerCacheEntry{
					Fingerprint: fingerprintTask(deferredByHarp[v.HarpID], batch.Repo),
					Verdict:     stored,
				}
			}
		}
		saveTriggerCache(listRes.ProjectID, cache)
	}

	// Assemble the result in the Deferred tasks' original order — stable
	// regardless of model response order or which tasks hit the cache.
	cachedByHarp := make(map[string]triggers.Verdict, len(cachedVerdicts))
	for _, v := range cachedVerdicts {
		cachedByHarp[v.HarpID] = v
	}
	for _, t := range deferred {
		if v, ok := cachedByHarp[t.HarpID]; ok {
			result.Verdicts = append(result.Verdicts, v)
			continue
		}
		if v, ok := freshByHarp[t.HarpID]; ok {
			result.Verdicts = append(result.Verdicts, v)
		}
	}

	return result, nil
}

// buildBatch assembles the pure triggers.Batch from the resolved task lists
// and gathered evidence — the only I/O-adjacent step in EvaluateTriggers;
// everything past this point (prompt construction, parsing) is pure.
func buildBatch(ctx context.Context, deferred, other []tasks.Task, since map[string]time.Time, req EvaluateTriggersRequest) triggers.Batch {
	gitClient := req.Git
	if gitClient == nil {
		gitClient = git.NewExec()
	}
	maxCommits := req.MaxCommits
	if maxCommits <= 0 {
		maxCommits = defaultMaxCommitsPerTask
	}

	batch := triggers.Batch{Now: time.Now().UTC()}

	// Repo-global "what exists NOW" evidence, gathered once for the batch.
	// Best-effort: a git failure degrades to no repo state rather than
	// failing the evaluation (the prompt's absence-of-evidence rule then
	// keeps the model off a confident not-fired).
	if req.RepoDir != "" {
		// The bounds are passed EXPLICITLY rather than left to the git seam's
		// defaults: a caller that does not own the bound cannot tell a
		// complete listing from one that stopped at it, and both bounds cut
		// alphabetically. Same shape as gitLogPathScanCap.
		if dirs, derr := gitClient.RepoDirs(ctx, req.RepoDir, repoDirsScanCap); derr == nil {
			batch.Repo.Dirs = dirs
			batch.Repo.DirsTruncated = len(dirs) >= repoDirsScanCap
		}
		if changes, cerr := gitClient.WorkingChanges(ctx, req.RepoDir, workingChangesScanCap); cerr == nil {
			batch.Repo.WorkingChanges = changes
			batch.Repo.WorkingChangesTruncated = len(changes) >= workingChangesScanCap
		}
	}

	for _, t := range deferred {
		ti := triggers.TaskInput{
			HarpID:     t.HarpID,
			Text:       t.Text,
			Trigger:    t.Trigger,
			DeferredAt: since[t.HarpID],
		}
		if req.RepoDir != "" {
			if entries, gerr := gitClient.LogSince(ctx, req.RepoDir, since[t.HarpID], maxCommits); gerr == nil {
				ti.CommitsSince, ti.ChangedFiles = summarizeCommits(entries, maxFilesPerTask)
			}
		}
		batch.Tasks = append(batch.Tasks, ti)
	}
	for i, t := range other {
		if i >= maxOtherTasksInPack {
			break
		}
		batch.OtherTasks = append(batch.OtherTasks, triggers.OtherTask{HarpID: t.HarpID, Text: t.Text, Status: t.Status})
	}
	return batch
}

// summarizeCommits shapes git.LogEntry evidence into the pure package's
// CommitSummary/changed-files pair, deduplicating and capping the file list
// (a task with many small commits shouldn't blow the per-task evidence
// budget on a repeated filename).
func summarizeCommits(entries []git.LogEntry, maxFiles int) ([]triggers.CommitSummary, []string) {
	summaries := make([]triggers.CommitSummary, 0, len(entries))
	seen := map[string]bool{}
	var files []string
	for _, e := range entries {
		summaries = append(summaries, triggers.CommitSummary{SHA: e.SHA, Date: e.Date, Subject: e.Subject})
		for _, f := range e.Files {
			if seen[f] {
				continue
			}
			seen[f] = true
			if len(files) < maxFiles {
				files = append(files, f)
			}
		}
	}
	return summaries, files
}

// runTriageWithRetry makes the batch LLM call and retries ONCE on a parse
// failure — the only retry in the LLM path (there is no general
// retry/backoff policy anywhere else in this codebase; this stays local to
// trigger evaluation rather than introducing one). Two consecutive failures
// (LLM error or unparseable output) degrade gracefully: the caller falls
// back to cannot-determine verdicts rather than surfacing a hard error, since
// a check-triggers run finding nothing conclusive is still a valid outcome.
func runTriageWithRetry(ctx context.Context, factory pb.ClientFactory, backendName, label, model string, env map[string]string, prompt string) (verdicts []triggers.Verdict, degraded bool, warning string) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		out, err := runTriageCall(ctx, factory, backendName, label, model, env, prompt)
		if err != nil {
			lastErr = err
			continue
		}
		parsed, perr := triggers.ParseVerdicts(out)
		if perr == nil {
			return parsed, false, ""
		}
		lastErr = perr
	}
	return nil, true, fmt.Sprintf("trigger evaluation degraded after retry: %v", lastErr)
}

// runTriageCall makes one batch-triage LLM call: a fresh plugin client, run
// once in ONESHOT/SkipSetup mode with the whole prompt as the lead fragment.
// This mirrors internal/memory/compactor.go's runDistill — the established
// prompt-in/text-out seam for a headless, minimal-mode LLM call — rather than
// inventing a new transport.
func runTriageCall(ctx context.Context, factory pb.ClientFactory, backendName, label, model string, env map[string]string, prompt string) (string, error) {
	client, err := factory(backendName, label, 0)
	if err != nil {
		return "", fmt.Errorf("start plugin: %w", err)
	}
	defer client.Kill()

	req := &pb.RunStart{
		Prompt: &pb.Fragment{Content: prompt},
		Options: &pb.RunOptions{
			Mode:  pb.ExecutionMode_ONESHOT,
			Model: model,
			// The label's declared environment, which this call omitted
			// entirely while claiming to mirror runDistill — it copied that
			// seam's shape from BEFORE runDistill was fixed. Without it a
			// backend whose credentials live in llm.configs.<label>.env runs
			// unconfigured, which does not error: the model just answers
			// badly, and every trigger degrades to cannot-determine.
			Env:       env,
			SkipSetup: true, // headless triage: no hooks/commands/context writes
		},
	}
	var stdout, stderr bytes.Buffer
	exitCode, err := client.Run(ctx, req, nil, &stdout, &stderr, nil)
	if err != nil {
		return "", err
	}
	if exitCode != 0 {
		return "", fmt.Errorf("LLM exited with code %d: %s", exitCode, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// taskChunk is one round-1 chunk: a slice of the miss set's tasks.Task
// (needed for fillMissingVerdicts and, later, for keying the cache) paired
// with the matching triggers.TaskInput evidence (needed for the prompt).
// Built by index, not re-matched by harp id, so the two slices must stay in
// lockstep — chunkMissTasks is the only place that constructs one.
type taskChunk struct {
	tasks  []tasks.Task
	inputs []triggers.TaskInput
}

// chunkMissTasks splits the cache-miss set into groups of at most size,
// preserving order. missTasks and missInputs are built together, index for
// index, in EvaluateTriggers's cache-split loop, so slicing both at the same
// bounds keeps every chunk's two slices aligned.
func chunkMissTasks(missTasks []tasks.Task, missInputs []triggers.TaskInput, size int) []taskChunk {
	if size <= 0 || size >= len(missTasks) {
		if len(missTasks) == 0 {
			return nil
		}
		return []taskChunk{{tasks: missTasks, inputs: missInputs}}
	}
	var chunks []taskChunk
	for i := 0; i < len(missTasks); i += size {
		end := i + size
		if end > len(missTasks) {
			end = len(missTasks)
		}
		chunks = append(chunks, taskChunk{tasks: missTasks[i:end], inputs: missInputs[i:end]})
	}
	return chunks
}

// chunkResult is one round-1 chunk's outcome. parsed holds the model's
// GENUINE answers (empty when the chunk degraded) — kept separate from
// verdicts (parsed plus any cannot-determine fallback) because only a
// genuine answer is cache-eligible; see EvaluateTriggers's cacheable
// bookkeeping.
type chunkResult struct {
	parsed   []triggers.Verdict
	verdicts []triggers.Verdict
	degraded bool
	warning  string
	// omitted counts tasks in this chunk that the model's (non-degraded)
	// response simply didn't mention — the live-observed silent-drop defect
	// this feature exists to make visible, as opposed to a degraded chunk
	// (the whole call/parse failed).
	omitted int
}

// runTriageChunks runs every chunk's round-1 model call with bounded
// concurrency (mirroring internal/memory/compactor.go's distillChunks),
// writing each result into its own pre-sized slot by chunk index — so the
// returned slice is in the SAME order as chunks regardless of which
// goroutine finishes first. Chunks are independent (each is a disjoint task
// subset with no cross-chunk data dependency), so one chunk's failure never
// blocks or corrupts another's result.
func runTriageChunks(ctx context.Context, chunks []taskChunk, batch triggers.Batch, factory pb.ClientFactory, backendName, label, model string, env map[string]string) []chunkResult {
	results := make([]chunkResult, len(chunks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, triageConcurrency)
	for i, c := range chunks {
		wg.Add(1)
		sem <- struct{}{} // acquire a slot; blocks once triageConcurrency are in flight
		go func(i int, c taskChunk) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = runTriageChunk(ctx, c, batch, factory, backendName, label, model, env)
		}(i, c)
	}
	wg.Wait()
	return results
}

// runTriageChunk makes one round-1 model call scoped to c's tasks (batch
// supplies the shared repo state and other-tasks cross-reference, which are
// NOT chunked — every chunk sees the same repo-global evidence) and
// classifies the outcome: a degraded call falls every task in c back to
// cannot-determine with the failure as its reason (same as the pre-chunking
// behavior, just scoped to this chunk); a successful call falls back only
// the tasks the response omitted, and counts them.
func runTriageChunk(ctx context.Context, c taskChunk, batch triggers.Batch, factory pb.ClientFactory, backendName, label, model string, env map[string]string) chunkResult {
	chunkBatch := batch
	chunkBatch.Tasks = c.inputs
	prompt := triggers.BuildPrompt(chunkBatch)
	parsed, degraded, warning := runTriageWithRetry(ctx, factory, backendName, label, model, env, prompt)
	if degraded {
		return chunkResult{
			verdicts: fillMissingVerdicts(c.tasks, nil, warning),
			degraded: true,
			warning:  warning,
		}
	}
	// Keep only verdicts for harp ids this chunk actually asked about — the
	// model can hallucinate or cross-contaminate an id from another chunk's
	// evidence, and triggers.ParseVerdicts only checks that harp_id is
	// non-empty, never membership in the request. An out-of-chunk verdict
	// must never reach cacheable/the verdict cache; the task it
	// claims to answer for gets its normal cannot-determine fallback below,
	// same as any other omission.
	inChunk := make(map[string]bool, len(c.tasks))
	for _, t := range c.tasks {
		inChunk[t.HarpID] = true
	}
	kept := make([]triggers.Verdict, 0, len(parsed))
	for _, v := range parsed {
		if inChunk[v.HarpID] {
			kept = append(kept, v)
		}
	}
	seen := make(map[string]bool, len(kept))
	for _, v := range kept {
		seen[v.HarpID] = true
	}
	omitted := 0
	for _, t := range c.tasks {
		if !seen[t.HarpID] {
			omitted++
		}
	}
	return chunkResult{
		parsed:   kept,
		verdicts: fillMissingVerdicts(c.tasks, kept, "model response omitted this task"),
		omitted:  omitted,
	}
}

// escalationParams carries everything escalateNeedsInvestigation needs to
// run round 2 — grouped into a struct rather than a long positional
// parameter list, since several are shared with the round-1 call site.
type escalationParams struct {
	factory     pb.ClientFactory
	backendName string
	label       string
	model       string
	// env is the label's declared environment, carried into round 2 for the
	// same reason round 1 needs it: an escalation call that dropped the
	// backend's credentials would degrade every re-asked trigger while the
	// round-1 answers looked fine.
	env map[string]string

	gitClient git.Git
	repoDir   string

	// otherByHarp resolves task_status queries; deferredByHarp supplies the
	// original TaskInput evidence for any task that gets escalated, keyed by
	// harp id — both already built once per EvaluateTriggers call.
	otherByHarp    map[string]tasks.Task
	deferredByHarp map[string]triggers.TaskInput

	// repo and otherTasks are the batch's repo-global evidence, carried into
	// round 2 unchanged. Round 2 is the FINAL look: settling a trigger on
	// less evidence than the round that escalated it is a regression, and an
	// existence-style trigger is answerable from repo state alone.
	repo       triggers.RepoState
	otherTasks []triggers.OtherTask

	// cacheable is mutated in place: escalateNeedsInvestigation marks a
	// harp id true once its round-2 verdict is a genuine (non-degraded,
	// non-omitted) model answer, so the caller knows it's safe to cache.
	cacheable map[string]bool

	// chunkSize bounds how many followup tasks ride in one round-2 model
	// call, the same as round 1 — a backlog with many ambiguous triggers can
	// escalate more tasks than fit safely in one call too.
	chunkSize int
}

// escalateNeedsInvestigation runs ROUND 2 — the bounded, EXACTLY ONCE
// escalation — for every round-1 needs-investigation verdict that requested
// at least one valid, whitelisted query. It executes those queries
// deterministically (bounded, read-only; see task_triggers_query.go),
// re-asks the model for a FINAL verdict on just those tasks, and folds the
// result back into verdicts in place.
//
// A task that comes back needs-investigation again in round 2 — a model
// ignoring the round-2 instruction — is forced to cannot-determine here:
// that is the hard cap making escalation exactly one round, never a loop.
//
// A needs-investigation verdict with NO actionable queries (the model didn't
// ask for anything, or asked only for things SanitizeQueries rejected) is
// left untouched: there is nothing deterministic left to look up on its
// behalf, so it stays needs-investigation for a human to look at directly —
// same as before this feature existed.
//
// Query EXECUTION (the deterministic, read-only lookups) stays a single
// sequential pass over verdicts, sharing one budget — that part is local
// computation, not a model call, so there is nothing to chunk or race. Only
// the round-2 model CALL is chunked (mirroring round 1) once every
// followup's evidence has been gathered.
//
// Returns an escalationOutcome: the updated verdicts, how many were forced to
// cannot-determine specifically because a chunk's otherwise-valid round-2
// response omitted them (the same omission-visibility contract as round 1),
// whether/why at least one round-2 chunk's LLM call or parse failed outright
// — the escalation-round counterpart of chunkResult.degraded/warning, folded
// by the caller into EvaluateTriggersResult.Degraded/Warning — and
// how many follow-up queries were refused before they ever ran.
func escalateNeedsInvestigation(ctx context.Context, p escalationParams, verdicts []triggers.Verdict) escalationOutcome {
	budget := maxQueriesPerBatch
	var followups []triggers.FollowupTask
	escalated := map[string]bool{}
	out := escalationOutcome{verdicts: verdicts}

	for _, v := range verdicts {
		if v.Outcome != triggers.NeedsInvestigation {
			continue
		}
		qs, rejected := triggers.SanitizeQueries(v.Queries, maxQueriesPerTask)
		out.queriesRejected += len(rejected)
		if len(qs) == 0 {
			// A model that asked for nothing declined to escalate; a model
			// whose every request was refused asked for shapes outside the
			// whitelist. Both leave the task needs-investigation, so the
			// count is the only thing that tells them apart.
			if len(rejected) > 0 {
				out.tasksRefusedEveryQuery++
			}
			continue
		}
		ti, ok := p.deferredByHarp[v.HarpID]
		if !ok {
			continue
		}
		results := executeQueries(ctx, p.gitClient, p.repoDir, p.otherByHarp, qs, &budget)
		followups = append(followups, triggers.FollowupTask{TaskInput: ti, Results: results})
		escalated[v.HarpID] = true
	}
	if len(followups) == 0 {
		return out
	}

	finalByHarp, degradedByHarp, omittedCount := runFollowupChunks(ctx, followups, p, p.chunkSize)

	var warnings []string
	seenReasons := map[string]bool{}
	for i, v := range verdicts {
		if !escalated[v.HarpID] {
			continue
		}
		if reason, bad := degradedByHarp[v.HarpID]; bad {
			verdicts[i] = triggers.Verdict{
				HarpID:    v.HarpID,
				Outcome:   triggers.CannotDetermine,
				Reasoning: reason,
			}
			// A call/parse failure is a transient degradation, not a genuine
			// answer — it must never freeze the task's verdict in the cache
			// until the evidence changes. Round 1 may already have
			// marked this harp id cacheable (a needs-investigation with valid
			// queries IS a genuine round-1 answer); round 2 failing to settle
			// it must retract that.
			if p.cacheable != nil {
				delete(p.cacheable, v.HarpID)
			}
			if !seenReasons[reason] {
				seenReasons[reason] = true
				warnings = append(warnings, reason)
			}
			continue
		}
		fv, ok := finalByHarp[v.HarpID]
		if !ok {
			verdicts[i] = triggers.Verdict{
				HarpID:    v.HarpID,
				Outcome:   triggers.CannotDetermine,
				Reasoning: "round 2 response omitted this task",
			}
			// Same reasoning as the degraded branch above: an omission is the
			// model silently dropping this task from an otherwise-valid
			// response, not a genuine answer for it.
			if p.cacheable != nil {
				delete(p.cacheable, v.HarpID)
			}
			continue
		}
		if fv.Outcome == triggers.NeedsInvestigation {
			fv.Outcome = triggers.CannotDetermine
			fv.Reasoning = "round 2 still unsettled (capped at one escalation round): " + fv.Reasoning
		}
		verdicts[i] = fv
		if p.cacheable != nil {
			p.cacheable[v.HarpID] = true
		}
	}
	out.verdicts = verdicts
	out.omitted = omittedCount
	out.degraded = len(warnings) > 0
	out.warning = strings.Join(warnings, "; ")
	return out
}

// escalationOutcome is escalateNeedsInvestigation's result. queriesRejected and
// tasksRefusedEveryQuery are DIAGNOSTICS, disjoint from degraded (a round-2
// call/parse failure) exactly as EvaluateTriggersResult.Omitted is: a refused
// query is ctxloom declining to run something the model asked for, not the
// model or the transport failing, and reading the two as one pile is what
// makes a badly-shaped escalation request invisible.
type escalationOutcome struct {
	verdicts []triggers.Verdict
	omitted  int
	degraded bool
	warning  string

	// queriesRejected counts follow-up queries dropped by SanitizeQueries
	// because they failed the whitelist/containment gate. Queries dropped
	// merely for exceeding the per-task cap are valid and are not counted.
	queriesRejected int
	// tasksRefusedEveryQuery counts needs-investigation verdicts that asked
	// for at least one query and had EVERY one refused — the case that is
	// otherwise indistinguishable from a model that asked for nothing.
	tasksRefusedEveryQuery int
}

// followupChunkResult is one round-2 chunk's outcome: genuine verdicts keyed
// by harp id, a degrade reason keyed by harp id for every task in a chunk
// whose call/parse failed outright, and how many tasks that chunk's model
// response silently dropped (only meaningful when the chunk was not
// degraded — a degraded chunk's tasks are accounted for via degradedByHarp,
// not omitted, since the whole call failed rather than the model dropping
// individual tasks from an answer it did produce).
type followupChunkResult struct {
	finalByHarp    map[string]triggers.Verdict
	degradedByHarp map[string]string
	omitted        int
}

// runFollowupChunks splits followups into bounded chunks and runs their
// round-2 model calls with bounded concurrency (mirroring runTriageChunks),
// merging results by harp id so assembly is independent of chunk order or
// completion order.
func runFollowupChunks(ctx context.Context, followups []triggers.FollowupTask, p escalationParams, chunkSize int) (finalByHarp map[string]triggers.Verdict, degradedByHarp map[string]string, omitted int) {
	chunks := chunkFollowups(followups, chunkSize)
	results := make([]followupChunkResult, len(chunks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, triageConcurrency)
	for i, c := range chunks {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, c []triggers.FollowupTask) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = runFollowupChunk(ctx, c, p)
		}(i, c)
	}
	wg.Wait()

	finalByHarp = map[string]triggers.Verdict{}
	degradedByHarp = map[string]string{}
	for _, r := range results {
		for k, v := range r.finalByHarp {
			finalByHarp[k] = v
		}
		for k, reason := range r.degradedByHarp {
			degradedByHarp[k] = reason
		}
		omitted += r.omitted
	}
	return finalByHarp, degradedByHarp, omitted
}

// runFollowupChunk makes one round-2 model call for chunk and classifies its
// outcome: a degraded call marks every task in the chunk (nothing to key off
// but the request); a successful call marks every task the response didn't
// mention as omitted, leaving the rest for the caller to fold back into
// verdicts.
func runFollowupChunk(ctx context.Context, chunk []triggers.FollowupTask, p escalationParams) followupChunkResult {
	prompt := triggers.BuildFollowupPrompt(triggers.FollowupBatch{
		Tasks:      chunk,
		OtherTasks: p.otherTasks,
		Repo:       p.repo,
		Now:        time.Now().UTC(),
	})
	final, degraded, warning := runTriageWithRetry(ctx, p.factory, p.backendName, p.label, p.model, p.env, prompt)

	res := followupChunkResult{finalByHarp: map[string]triggers.Verdict{}, degradedByHarp: map[string]string{}}
	if degraded {
		reason := "escalation round degraded: " + warning
		for _, f := range chunk {
			res.degradedByHarp[f.HarpID] = reason
		}
		return res
	}
	seen := make(map[string]bool, len(final))
	for _, v := range final {
		res.finalByHarp[v.HarpID] = v
		seen[v.HarpID] = true
	}
	for _, f := range chunk {
		if !seen[f.HarpID] {
			res.omitted++
		}
	}
	return res
}

// chunkFollowups splits followups into groups of at most size, preserving
// order — the same shape as chunkMissTasks, one level down (round-2 items
// instead of round-1 tasks).
func chunkFollowups(followups []triggers.FollowupTask, size int) [][]triggers.FollowupTask {
	if size <= 0 || size >= len(followups) {
		if len(followups) == 0 {
			return nil
		}
		return [][]triggers.FollowupTask{followups}
	}
	var chunks [][]triggers.FollowupTask
	for i := 0; i < len(followups); i += size {
		end := i + size
		if end > len(followups) {
			end = len(followups)
		}
		chunks = append(chunks, followups[i:end])
	}
	return chunks
}

// fillMissingVerdicts adds a cannot-determine verdict, carrying reason, for
// every Deferred task the model's response didn't cover — a missing verdict
// must never be silently read as "not fired".
func fillMissingVerdicts(deferred []tasks.Task, got []triggers.Verdict, reason string) []triggers.Verdict {
	seen := make(map[string]bool, len(got))
	for _, v := range got {
		seen[v.HarpID] = true
	}
	// Copy rather than alias got's backing array: appending onto
	// `got` directly would let a later in-place rewrite through the returned
	// slice (e.g. escalateNeedsInvestigation's verdicts[i] = ...) silently
	// mutate the caller's own parsed/got slice too.
	out := append([]triggers.Verdict(nil), got...)
	for _, t := range deferred {
		if !seen[t.HarpID] {
			out = append(out, triggers.Verdict{
				HarpID:    t.HarpID,
				Outcome:   triggers.CannotDetermine,
				Reasoning: reason,
			})
		}
	}
	return out
}
