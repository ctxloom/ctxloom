package operations

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
)

// DefaultMapConcurrency bounds how many member agents run at once, keeping
// parallel LLM cost predictable. Overridable per call.
const DefaultMapConcurrency = 4

// Part is one member agent's labeled output within a map/weave run. A failed
// member carries its error in Err (and empty Output) rather than aborting the
// set — fan-out is fault-tolerant (CLAUDE.md). Profile holds the member
// identifier — an agent name or a bare profile name (sugar) — as written.
type Part struct {
	Profile string `json:"profile"`
	Label   string `json:"label,omitempty"`
	Backend string `json:"backend,omitempty"`
	Output  string `json:"output"`
	Err     string `json:"error,omitempty"`
}

// Failed reports whether this part is an error placeholder.
func (p Part) Failed() bool { return p.Err != "" }

// MapProfilesRequest fans one shared task across several members. Each member is
// an agent-or-profile identifier (see resolveMember): a configured agent
// runs with its own engine + composed profiles; a bare profile name is sugar for
// a default-engine agent.
type MapProfilesRequest struct {
	Members     []string // members (agent or bare-profile names) to run in parallel
	Task        string   // shared task/prompt broadcast to every member
	LLM         string   // optional override applied to all members (wins over each member's engine)
	Workspace   string   // SESSION-level workspace axis for every member session (none | worktree; empty uses the project `workspace:` default)
	WorkDir     string
	Verbosity   int
	Concurrency int // <=0 uses DefaultMapConcurrency

	Loader  *bundles.Loader  // optional shared bundle loader (test seam)
	Factory pb.ClientFactory // optional client factory (test seam)
}

// MapProfiles runs each member as a parallel oneshot agent over the shared task,
// returning one Part per member in input order. Each member resolves to its own
// engine + composed context (a configured agent, or a bare profile as
// default-engine sugar) and runs on THAT transport — the fan is no longer single
// engine. Concurrency is bounded; a member failure (resolution or run) becomes an
// error Part and never fails the call. This is the "map" half of the weave
// primitive.
func MapProfiles(ctx context.Context, cfg *config.Config, req MapProfilesRequest) []Part {
	parts := make([]Part, len(req.Members))
	if len(req.Members) == 0 {
		return parts
	}

	conc := req.Concurrency
	if conc <= 0 {
		conc = DefaultMapConcurrency
	}
	if conc > len(req.Members) {
		conc = len(req.Members)
	}

	// Resolve every member SEQUENTIALLY on this goroutine first, before the
	// parallel run fan. Resolution assembles context (a default-profile member
	// composes the default agent's profiles via DefaultAgentProfiles); doing it
	// here turns a member's resolution failure into a fault-tolerant error Part
	// instead of a hard abort.
	resolved := make([]*ResolvedAgent, len(req.Members))
	for i, member := range req.Members {
		rs, err := resolveMember(ctx, cfg, member, req.LLM, req.Loader)
		if err != nil {
			parts[i] = Part{Profile: member, Err: err.Error()}
			continue
		}
		resolved[i] = rs
	}

	// The fan's SESSION-level workspace axis: the invocation's --workspace wins,
	// else the project `workspace:` default. One value for every member session
	// — the workspace is a property of how this fan launches its sessions, not
	// of any agent binding.
	workspace := req.Workspace
	if workspace == "" {
		workspace = cfg.Workspace
	}

	// Build the shared executable trust gate ONCE for this fan, but only when a
	// member actually has isolation on some axis — an all-defaults fan writes no
	// per-member config and must stay byte-identical to pre-P3, so it never touches
	// the gate (which runs the trust baseline + opens the store). The gate's allow()
	// is concurrency-safe, so the parallel members below share one.
	var execGate *ExecutableTrustGate
	var gate bundles.ContentGate
	for _, rs := range resolved {
		if rs != nil && !memberAxes(workspace, rs).Zero() {
			execGate = NewExecutableTrustGate(cfg)
			gate = execGate.Gate()
			break
		}
	}

	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for i := range req.Members {
		if resolved[i] == nil {
			continue // resolution failed; the error Part is already recorded
		}
		wg.Add(1)
		sem <- struct{}{} // acquire (bounds concurrency)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			// A panic inside the backend client or plugin would otherwise crash
			// the whole process and every other in-flight member. Recover and
			// record it as an error Part so one bad member degrades like a
			// returned error — honoring the documented "never fails the call"
			// contract (CLAUDE.md).
			defer func() {
				if r := recover(); r != nil {
					parts[i] = Part{Profile: req.Members[i], Err: fmt.Sprintf("panic: %v", r)}
				}
			}()

			rs := resolved[i]
			res, err := runResolvedAgent(ctx, resolvedRunRequest{
				Context:   rs.Context,
				Task:      req.Task,
				WorkDir:   req.WorkDir,
				Verbosity: req.Verbosity,
				Label:     rs.Label,
				Backend:   rs.Backend,
				Model:     rs.Model,
				// The member's posture: its agent binding, else the engine label's
				// config; resolved for headless in runResolvedAgent.
				Permissions: memberPermissions(rs, cfg),
				// The member's axes: the fan's session-level workspace x the
				// agent-resolved runtime, plus the user-provided agent image
				// for the member's backend when one is configured; AgentID
				// scopes this member's per-agent workspace by its identifier.
				Axes:           memberAxes(workspace, rs),
				IsolationImage: IsolationImageConfig(cfg, rs.Backend),
				AgentID:        req.Members[i],
				// The member's composed profile set + shared gate scope the
				// per-member managed config written when the member is isolated.
				Profiles: rs.Profiles,
				Gate:     gate,
				Factory:  req.Factory,
			})
			if err != nil {
				parts[i] = Part{Profile: req.Members[i], Err: err.Error()}
				return
			}
			parts[i] = Part{
				Profile: req.Members[i],
				Label:   res.Label,
				Backend: res.Backend,
				Output:  res.Output,
			}
		}(i)
	}
	wg.Wait()
	// Advisory: name how many bundle executables the shared gate withheld across
	// isolated members (content-free; nil-safe when no gate was built).
	execGate.WarnWithheld()
	return parts
}

// Synthesizer records which profile (and resolved transport) produced a weave's
// synthesis report.
type Synthesizer struct {
	Profile string `json:"profile"`
	Label   string `json:"label,omitempty"`
	Backend string `json:"backend,omitempty"`
}

// WeaveResult is a weave run: the synthesized report plus the member/injected
// parts it was built from. Report is empty when synthesis was skipped or failed.
type WeaveResult struct {
	Report      string       `json:"report,omitempty"`
	Parts       []Part       `json:"parts"`
	Synthesizer *Synthesizer `json:"synthesizer,omitempty"`
}

// MapProfilesResult is the CLI-facing envelope for a `map` run's member parts —
// MapProfiles itself returns the bare []Part; this wraps them for structured
// (--format json) output and JSON-schema generation.
type MapProfilesResult struct {
	Parts []Part `json:"parts"`
}

// WeaveRequest fans members over a shared task, then synthesizes their outputs
// (plus any injected parts) with a high-power synthesis profile.
type WeaveRequest struct {
	Members       []string // members (agent or bare-profile names) run in parallel (may be empty if only injecting)
	Synthesize    string   // synthesis profile; empty or NoSynthesize skips the reduce
	Task          string   // shared task broadcast to members and shown to the synthesizer
	LLM           string   // optional override for MEMBERS only (synth keeps its own llm)
	Workspace     string   // SESSION-level workspace axis for member sessions (none | worktree; empty uses the project default)
	InjectedParts []Part   // externally-supplied parts (e.g. non-ctxloom outputs)
	WorkDir       string
	Verbosity     int
	Concurrency   int
	NoSynthesize  bool // emit parts only; skip the synthesis pass

	Loader  *bundles.Loader
	Factory pb.ClientFactory
}

// Weave runs the members in parallel (map), appends any injected parts, then
// reduces everything through the synthesis profile in-process — no shell, so the
// whole map→reduce is portable. A member failure is a fault-tolerant error Part;
// a synthesis failure returns the parts plus the error so the caller can still
// surface partial results. The synthesizer uses its own profile llm (the LLM
// override applies to members only).
func Weave(ctx context.Context, cfg *config.Config, req WeaveRequest) (*WeaveResult, error) {
	var parts []Part
	if len(req.Members) > 0 {
		parts = MapProfiles(ctx, cfg, MapProfilesRequest{
			Members:     req.Members,
			Task:        req.Task,
			LLM:         req.LLM,
			Workspace:   req.Workspace,
			WorkDir:     req.WorkDir,
			Verbosity:   req.Verbosity,
			Concurrency: req.Concurrency,
			Loader:      req.Loader,
			Factory:     req.Factory,
		})
	}
	parts = append(parts, req.InjectedParts...)

	result := &WeaveResult{Parts: parts}
	if req.NoSynthesize || req.Synthesize == "" {
		return result, nil
	}

	synth, err := RunOneshot(ctx, cfg, RunOneshotRequest{
		Profile:   req.Synthesize,
		Task:      buildSynthesisTask(req.Task, parts),
		WorkDir:   req.WorkDir,
		Verbosity: req.Verbosity,
		Loader:    req.Loader,
		Factory:   req.Factory,
	})
	if err != nil {
		return result, fmt.Errorf("synthesis: %w", err)
	}
	result.Report = synth.Output
	result.Synthesizer = &Synthesizer{Profile: req.Synthesize, Label: synth.Label, Backend: synth.Backend}
	return result, nil
}

// memberAxes is the launch-time meeting point of the two independently-owned
// isolation axes: the fan's SESSION-level workspace (one value per fan) and
// the member's AGENT-resolved runtime (rs.Runtime, already defaulted through
// the project `runtime:` key). Neither axis is ever derived from the other.
func memberAxes(workspace string, rs *ResolvedAgent) isolation.Axes {
	return isolation.Axes{
		Workspace: isolation.WorkspaceAxis(workspace),
		Runtime:   isolation.RuntimeAxis(rs.Runtime),
	}
}

// buildSynthesisTask frames the member/injected parts for the synthesis agent.
// The framing is deliberately generic (this is a general primitive); the
// domain-specific "how to combine" instructions live in the synthesis profile's
// own assembled context.
func buildSynthesisTask(originalTask string, parts []Part) string {
	var b strings.Builder
	if strings.TrimSpace(originalTask) != "" {
		fmt.Fprintf(&b, "## Task\n\n%s\n\n", originalTask)
	}
	b.WriteString("## Specialist outputs to synthesize\n\n")
	b.WriteString("The blocks below are independent agent outputs for the task above. ")
	b.WriteString("Combine them into a single, de-duplicated, prioritized result.\n\n")
	b.WriteString(FormatParts(parts))
	return b.String()
}

// PartHeaderPrefix opens every labeled part block in the map/weave stream.
const PartHeaderPrefix = "===== part: "

// FormatParts renders parts as the plain, labeled block stream that `ctxloom
// map` prints and `ctxloom weave` consumes — hand-authorable and easy to mix
// with non-ctxloom output.
func FormatParts(parts []Part) string {
	var b strings.Builder
	for _, p := range parts {
		header := p.Profile
		if p.Label != "" {
			header += " (llm: " + p.Label + ")"
		}
		fmt.Fprintf(&b, "%s%s =====\n", PartHeaderPrefix, header)
		if p.Failed() {
			fmt.Fprintf(&b, "[error: %s]\n", p.Err)
			continue
		}
		b.WriteString(p.Output)
		if !strings.HasSuffix(p.Output, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// memberPermissions is a fan member's declared posture, with the run precedence:
// the agent binding, else the engine label's configured permissions ("" when
// neither declares one). runResolvedAgent resolves it to an effective headless
// posture.
func memberPermissions(rs *ResolvedAgent, cfg *config.Config) string {
	if rs.Permissions != "" {
		return rs.Permissions
	}
	return cfg.LM.Configs[rs.Label].Permissions
}
