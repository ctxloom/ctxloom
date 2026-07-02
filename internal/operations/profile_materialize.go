package operations

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// DefaultMaterializeBackend is the backend whose native on-disk agent surface a
// profile materializes to by default.
const DefaultMaterializeBackend = "claude-code"

// MaterializeProfileRequest asks to write a profile's assembled context to an
// external target directory as a backend's NATIVE agent surface — the inverse of
// runtime injection — so an externally-launched agent on that backend inherits
// the profile with ctxloom out of the loop.
type MaterializeProfileRequest struct {
	Profiles []string `json:"profiles"`
	Target   string   `json:"target"`
	Backend  string   `json:"backend,omitempty"` // "" or "claude" → claude-code
	FS       afero.Fs `json:"-"`
}

// MaterializeProfileResult reports which managed surfaces were written under
// Target, plus any per-surface warnings (a partial export still succeeds).
type MaterializeProfileResult struct {
	Target   string   `json:"target"`
	Backend  string   `json:"backend"`
	Profiles []string `json:"profiles"`
	Wrote    []string `json:"wrote"`
	Warnings []string `json:"warnings,omitempty"`
}

// MaterializeProfile writes the assembled profile(s) into Target as the backend's
// native files, OVERWRITING each managed surface every run (the export is the
// source of truth for the target's managed files):
//   - context → CLAUDE.md (the assembled fragment block — the same payload the
//     SessionStart hook injects, but STATIC, so no context-injection hook is added)
//   - mcp     → the backend MCP config (.mcp.json / settings)
//   - hooks   → the backend settings hooks (config + profile + bundle hooks, gated)
//   - skills  → the backend slash-command dir
//
// Fault tolerant (CLAUDE.md philosophy): a per-surface write failure warns and
// continues rather than aborting the whole export; only bad arguments and a
// failed context assembly (the core payload) are hard errors.
func MaterializeProfile(ctx context.Context, cfg *config.Config, req MaterializeProfileRequest) (*MaterializeProfileResult, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if req.Target == "" {
		return nil, fmt.Errorf("target dir is required")
	}
	if len(req.Profiles) == 0 {
		return nil, fmt.Errorf("at least one profile is required")
	}
	backend := req.Backend
	if backend == "" || backend == "claude" {
		backend = DefaultMaterializeBackend
	}
	if !backends.Exists(backend) {
		return nil, fmt.Errorf("unknown backend %q", backend)
	}
	fs := getFS(req.FS)
	// The target is ours to create: materialize's whole point is standing up a
	// fresh native surface, so a nonexistent --target dir is expected input,
	// not an error.
	if err := fs.MkdirAll(req.Target, 0o755); err != nil {
		return nil, fmt.Errorf("create target dir %s: %w", req.Target, err)
	}
	res := &MaterializeProfileResult{Target: req.Target, Backend: backend, Profiles: req.Profiles}

	// Gate the executable surfaces (bundle MCP / hooks / skill exports) at their
	// own choke, exactly as ApplyHooks does. Set before resolving any of them.
	execGate := NewExecutableTrustGate(cfg)
	cfg.SetExecutableTrustGate(execGate.Gate())

	// context → CLAUDE.md. An explicit profile set makes resolution failures hard
	// errors (the caller named these profiles), so this is the one fatal surface.
	asm, err := AssembleContext(ctx, cfg, AssembleContextRequest{Profiles: req.Profiles})
	if err != nil {
		return nil, fmt.Errorf("assemble context for %v: %w", req.Profiles, err)
	}
	if err := afero.WriteFile(fs, filepath.Join(req.Target, "CLAUDE.md"), []byte(asm.Context), 0o644); err != nil {
		return nil, fmt.Errorf("write CLAUDE.md: %w", err)
	}
	res.Wrote = append(res.Wrote, "CLAUDE.md")

	// mcp + hooks → the backend's settings + MCP config. contextHash "" omits the
	// SessionStart context-injection hook — the context is STATIC in CLAUDE.md, so
	// re-injecting it at launch would double it. WriteSettings reconciles (removes
	// prior ctxloom-managed entries, preserves foreign ones) → overwrite semantics.
	//
	// AssembleManagedMCP (not &cfg.MCP) so a profile's OWN inline mcp: block reaches
	// the exported .mcp.json — it merges config-level MCP then the selected
	// profiles' inline/directory servers (directory ones pass the exec gate set
	// above), mirroring what a live `run` assembles. bundleMCP is passed separately.
	hooks := backends.AssembleManagedHooks(cfg, req.Target, "", req.Profiles)
	mcp := backends.AssembleManagedMCP(cfg, req.Profiles)
	bundleMCP := cfg.ResolveBundleMCPServers(req.Profiles)
	if err := backends.WriteSettings(backend, hooks, mcp, bundleMCP, req.Target, backends.WithSettingsFS(fs)); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("settings/mcp: %v", err))
	} else {
		res.Wrote = append(res.Wrote, "settings", "mcp")
	}

	// skills → the backend's slash-command surface (manifest-scoped overwrite).
	prompts := backends.LoadSkillExports(cfg, req.Profiles)
	if len(prompts) > 0 {
		if err := backends.WriteCommandFilesFor(backend, req.Target, prompts, agent.WithCommandFS(fs)); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("skills: %v", err))
		} else {
			res.Wrote = append(res.Wrote, "skills")
		}
	}

	// Surface (content-free) any executable the trust gate withheld.
	execGate.WarnWithheld()
	return res, nil
}
