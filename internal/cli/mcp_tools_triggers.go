package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/operations"
	tasksops "github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/triggers"
)

// evaluateTriggersDesc is shared verbatim with the runner's host-relay
// registration (mcp_runner.go's relayEvaluateTriggersDesc) — the parity test
// TestRunnerServer_HostRelayDescriptionsMatchStdio pins them equal.
const evaluateTriggersDesc = "Evaluate every Deferred task's revive trigger against gathered evidence (git history since the task was deferred, changed files, and the status of other tasks) and return a machine verdict per trigger: fired, not-fired, needs-investigation, or cannot-determine. The batch is triaged in bounded chunks against the fast model, not one call for everything, so a large Deferred backlog does not silently lose tasks off the end of an oversized response. A needs-investigation verdict may resolve itself internally — ctxloom can run one bounded, whitelisted follow-up look (file existence, a read-only grep, recent commits on a path, another task's status) before settling a final verdict, without any extra turns on your part. This is TRIAGE ONLY — it proposes verdicts for a human to confirm and never changes any task's status itself. Use it before check-triggers' own judgement, or to get a second, evidence-grounded opinion on a Deferred task's trigger. A \"cannot-determine\" verdict means the trigger genuinely cannot be judged from evidence available inside this system (e.g. it depends on something a person has to say) — treat it the same as \"not yet\", never as \"fired\". If the model dropped any task from its response despite chunking, the \"omitted\" count says so explicitly — those tasks still get a cannot-determine verdict, but the count tells you it was a drop, not a genuine judgment; rerun with refresh=true to retry them. Verdicts are cached against the evidence that produced them (see the \"cached\" field per verdict); pass refresh=true to force a fresh look."

type evaluateTriggersInput struct {
	MaxCommits int  `json:"max_commits,omitempty" jsonschema:"Per-task cap on how many commits (since the task was deferred) are gathered as evidence. Default 20."`
	Refresh    bool `json:"refresh,omitempty" jsonschema:"Bypass the verdict cache and re-evaluate every Deferred task's trigger against the model, even if nothing about its evidence changed since the last call."`
}

// evaluateTriggersResult mirrors operations.EvaluateTriggersResult; Verdicts
// rides the pure triggers.Verdict shape directly rather than a duplicate
// wire type, since its JSON tags are already the wire contract (including
// each verdict's own "cached" flag).
type evaluateTriggersResult struct {
	Evaluated   int    `json:"evaluated"`
	CacheHits   int    `json:"cache_hits,omitempty"`
	CacheMisses int    `json:"cache_misses,omitempty"`
	Degraded    bool   `json:"degraded,omitempty"`
	Warning     string `json:"warning,omitempty"`
	// Omitted surfaces the live-observed silent-drop failure mode: tasks
	// whose chunk gave a well-formed response that simply didn't mention
	// them. It is NOT folded into Degraded/Warning (a chunk call/parse
	// failure) — a caller must be able to tell "the model dropped N tasks"
	// apart from "N chunks failed outright" rather than reading both as an
	// undifferentiated pile of cannot-determine verdicts.
	Omitted  int                `json:"omitted,omitempty"`
	Verdicts []triggers.Verdict `json:"verdicts"`
}

func (s *ctxServer) registerTriggerTools(server *mcp.Server) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "evaluate_triggers",
			Description: evaluateTriggersDesc,
		},
		s.handleEvaluateTriggers)
}

func (s *ctxServer) handleEvaluateTriggers(ctx context.Context, _ *mcp.CallToolRequest, in evaluateTriggersInput) (*mcp.CallToolResult, *evaluateTriggersResult, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve working directory: %w", err)
	}
	tc := tasksops.TaskContext{
		WorkDir:     cwd,
		ProjectID:   os.Getenv("CTXLOOM_PROJECT_ID"),
		SessionHarp: os.Getenv("CTXLOOM_SESSION_HARP"),
	}
	res, err := operations.EvaluateTriggers(ctx, s.cfg, operations.EvaluateTriggersRequest{
		TaskContext: tc,
		RepoDir:     cwd,
		MaxCommits:  in.MaxCommits,
		Refresh:     in.Refresh,
	})
	if err != nil {
		return nil, nil, err
	}
	return nil, &evaluateTriggersResult{
		Evaluated:   res.Evaluated,
		CacheHits:   res.CacheHits,
		CacheMisses: res.CacheMisses,
		Degraded:    res.Degraded,
		Warning:     res.Warning,
		Omitted:     res.Omitted,
		Verdicts:    res.Verdicts,
	}, nil
}
