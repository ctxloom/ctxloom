package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/plans"
)

// Plan retrieval: a session's plan documents (the *.plan.md the agent wrote
// to its ctxloom session directory), read so ctxloom can fold them into
// distilled output and carry them across a cross-agent handoff. Keyed by
// harp (location-independent); the reader resolves its own ctxloom home.

// ReadPlanFiles reads the *.plan.md documents belonging to a harp's ctxloom
// session, sorted by name for determinism. WHERE it looks is not this file's
// decision: plans.SessionPlanPaths owns that, so this reader and the location
// mcp.sessionInstructions hands to the agent cannot drift apart. Fault
// tolerant: an unresolved home, a missing directory, or an unreadable file
// yields fewer (or no) plans rather than an error — distill degrades to "no
// plans" rather than failing.
//
// The tolerance is kept, but it is no longer SILENT: every degraded path is
// warned about. A distill or cross-agent handoff that omitted plan documents
// which exist on disk used to be indistinguishable from a session that has no
// plans, and the consumers (GetPlans, internal/adapters/memory's compactor) fold the
// empty result straight into distilled output where the omission is invisible
// forever after. EngineReader.GetPlans and the compactor's PlansSource
// consumers read through here.
func ReadPlanFiles(harp string) []agent.PlanFile {
	out, problems := readPlanFiles(harp)
	for _, err := range problems {
		clidiag.Warn("ctxloom", "%v", err)
	}
	return out
}

// readPlanFiles is ReadPlanFiles' body, returning the degraded paths as values
// instead of writing them to stderr, so the "what got dropped" contract is
// testable without capturing process-wide diagnostics.
//
// An empty harp and a genuinely absent session directory are the two cases that
// stay quiet: both are legitimately "no plans", not "plans we failed to read".
func readPlanFiles(harp string) ([]agent.PlanFile, []error) {
	if harp == "" {
		return nil, nil
	}
	candidates, problems := plans.SessionPlanPaths(harp)
	var out []agent.PlanFile
	for _, path := range candidates {
		base := filepath.Base(path)
		content, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, fmt.Errorf("plan file %s omitted, unreadable: %w", base, err))
			continue
		}
		out = append(out, agent.PlanFile{
			Name:    strings.TrimSuffix(base, paths.PlanFileExt),
			Content: string(content),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, problems
}
