package operations

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// DefaultMapConcurrency bounds how many member agents run at once, keeping
// parallel LLM cost predictable. Overridable per call.
const DefaultMapConcurrency = 4

// Part is one member agent's labeled output within a map/weave run. A failed
// member carries its error in Err (and empty Output) rather than aborting the
// set — fan-out is fault-tolerant (CLAUDE.md).
type Part struct {
	Profile string `json:"profile"`
	Label   string `json:"label,omitempty"`
	Backend string `json:"backend,omitempty"`
	Output  string `json:"output"`
	Err     string `json:"error,omitempty"`
}

// Failed reports whether this part is an error placeholder.
func (p Part) Failed() bool { return p.Err != "" }

// MapProfilesRequest fans one shared task across several profiles.
type MapProfilesRequest struct {
	Profiles    []string // member profiles to run in parallel
	Task        string   // shared task/prompt broadcast to every member
	LLM         string   // optional override applied to all members (wins over each profile's llm)
	WorkDir     string
	Verbosity   int
	Concurrency int // <=0 uses DefaultMapConcurrency

	Loader  *bundles.Loader  // optional shared bundle loader (test seam)
	Factory pb.ClientFactory // optional client factory (test seam)
}

// MapProfiles runs each profile as a parallel oneshot agent over the shared
// task, returning one Part per profile in input order. Concurrency is bounded;
// a member failure becomes an error Part and never fails the call. This is the
// "map" half of the weave primitive.
func MapProfiles(ctx context.Context, cfg *config.Config, req MapProfilesRequest) []Part {
	parts := make([]Part, len(req.Profiles))
	if len(req.Profiles) == 0 {
		return parts
	}

	conc := req.Concurrency
	if conc <= 0 {
		conc = DefaultMapConcurrency
	}
	if conc > len(req.Profiles) {
		conc = len(req.Profiles)
	}

	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for i, profile := range req.Profiles {
		wg.Add(1)
		sem <- struct{}{} // acquire (bounds concurrency)
		go func(i int, profile string) {
			defer wg.Done()
			defer func() { <-sem }()

			res, err := RunOneshot(ctx, cfg, RunOneshotRequest{
				Profile:   profile,
				Task:      req.Task,
				LLM:       req.LLM,
				WorkDir:   req.WorkDir,
				Verbosity: req.Verbosity,
				Loader:    req.Loader,
				Factory:   req.Factory,
			})
			if err != nil {
				parts[i] = Part{Profile: profile, Err: err.Error()}
				return
			}
			parts[i] = Part{
				Profile: profile,
				Label:   res.Label,
				Backend: res.Backend,
				Output:  res.Output,
			}
		}(i, profile)
	}
	wg.Wait()
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

// WeaveRequest fans members over a shared task, then synthesizes their outputs
// (plus any injected parts) with a high-power synthesis profile.
type WeaveRequest struct {
	Members       []string // member profiles run in parallel (may be empty if only injecting)
	Synthesize    string   // synthesis profile; empty or NoSynthesize skips the reduce
	Task          string   // shared task broadcast to members and shown to the synthesizer
	LLM           string   // optional override for MEMBERS only (synth keeps its own llm)
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
			Profiles:    req.Members,
			Task:        req.Task,
			LLM:         req.LLM,
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
