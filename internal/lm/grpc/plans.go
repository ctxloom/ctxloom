package grpc

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Plan retrieval: the agent server is co-located with the session files and
// serves a session's plan documents (the *.plan.md the agent wrote to its
// ctxloom session directory) so ctxloom can fold them into distilled output and
// carry them across a cross-agent handoff. Keyed by harp (location-independent);
// the server resolves its own ctxloom home, so no host path crosses the wire.

func planFilesToProto(in []agent.PlanFile) []*PlanFile {
	if in == nil {
		return nil
	}
	out := make([]*PlanFile, len(in))
	for i, p := range in {
		out[i] = &PlanFile{Name: p.Name, Content: p.Content}
	}
	return out
}

func planFilesFromProto(in []*PlanFile) []agent.PlanFile {
	if in == nil {
		return nil
	}
	out := make([]agent.PlanFile, 0, len(in))
	for _, p := range in {
		if p == nil {
			continue
		}
		out = append(out, agent.PlanFile{Name: p.GetName(), Content: p.GetContent()})
	}
	return out
}

// ReadPlanFiles reads the *.plan.md documents in a harp's ctxloom session
// directory, sorted by name for determinism. Fault tolerant: an unresolved home,
// a missing directory, or an unreadable file yields fewer (or no) plans rather
// than an error — distill degrades to "no plans" rather than failing.
func ReadPlanFiles(harp string) []agent.PlanFile {
	if harp == "" {
		return nil
	}
	dir, err := paths.HarpDir(harp)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []agent.PlanFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), paths.PlanFileExt) {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, agent.PlanFile{
			Name:    strings.TrimSuffix(e.Name(), paths.PlanFileExt),
			Content: string(content),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// GetPlans (server) serves the agent's own session-directory plan files for the
// requested harp.
func (s *GRPCServer) GetPlans(ctx context.Context, req *GetPlansRequest) (*PlansData, error) {
	return &PlansData{Plans: planFilesToProto(ReadPlanFiles(req.GetHarp()))}, nil
}

// GetPlans (client) fetches a harp's plan documents from the agent server.
func (c *GRPCClient) GetPlans(ctx context.Context, harp string) ([]agent.PlanFile, error) {
	resp, err := c.client.GetPlans(ctx, &GetPlansRequest{Harp: harp})
	if err != nil {
		return nil, err
	}
	return planFilesFromProto(resp.GetPlans()), nil
}

// GetPlans delegates to the underlying gRPC client.
func (p *LLMRunner) GetPlans(ctx context.Context, harp string) ([]agent.PlanFile, error) {
	return p.grpc.GetPlans(ctx, harp)
}
