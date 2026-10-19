package operations

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/toolchain"
)

// RequiredCommandsFor resolves the commands the named agent needs on its PATH,
// each with every reason it is needed. It is the ONE derivation of an agent's
// requirements; the doctor gap check and `agent show` both read it.
func RequiredCommandsFor(ctx context.Context, reg engine.Registry, cfg *config.Config, agent string) ([]toolchain.Requirement, error) {
	resolved, err := ResolveAgent(ctx, reg, cfg, agent, "")
	if err != nil {
		return nil, err
	}
	return RequiredCommandsForResolved(cfg, resolved)
}

// RequiredCommandsForResolved is RequiredCommandsFor for an agent the caller
// already resolved, so a caller holding the resolution does not repeat it.
//
// It unions two sources, de-duplicated by command with every source kept:
//   - the `requires.commands` of every bundle the agent's profiles compose,
//     inherited ones included (toolchain.KindBundle, named by the ref the
//     profile uses);
//   - the first word of each attached stdio MCP server's command
//     (toolchain.KindMCP, named by the server). A URL or session-endpoint
//     server has no command, a profile's `exclude_mcp` has already withheld its
//     servers, and a command ctxloom stages into the image itself
//     (isolation.StagedCommands) is no requirement of the base.
//
// Only the launcher is taken, never the package behind it: `npx foo` requires
// npx. A bundle that cannot be read is an error, not a silent omission: a
// requirement that vanished with its bundle would read as "nothing needed".
func RequiredCommandsForResolved(cfg *config.Config, resolved *ResolvedAgent) ([]toolchain.Requirement, error) {
	var col toolchain.Collector
	set := cfg.ResolveProfileSet(resolved.Profiles)

	cat := cfg.BundleLoader().Catalog()
	for _, p := range set {
		for _, ref := range p.Bundles {
			read, err := cat.Read(ref)
			if err != nil {
				return nil, fmt.Errorf("reading bundle %s for its requirements: %w", ref, err)
			}
			for _, command := range read.Bundle.Requires.Commands {
				col.Add(command, toolchain.RequirementSource{Kind: toolchain.KindBundle, Ref: ref})
			}
		}
	}

	staged := isolation.StagedCommands()
	for name, srv := range cfg.ResolveBundleMCPServersFor(set) {
		if srv.URL != "" || srv.ServedBy != "" {
			continue
		}
		fields := strings.Fields(srv.Command)
		if len(fields) == 0 || slices.Contains(staged, fields[0]) {
			continue
		}
		col.Add(fields[0], toolchain.RequirementSource{Kind: toolchain.KindMCP, Ref: name})
	}
	return col.Requirements(), nil
}
