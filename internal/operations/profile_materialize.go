package operations

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
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
	// Surfaces overrides WHERE a surface kind is delivered, for the kinds named.
	// An absent kind keeps the engine's own default, so an empty map is exactly
	// today's behaviour. Overriding is how a caller asks for a portable artifact
	// an engine would not otherwise leave behind: claude-code delivers context
	// through a SessionStart hook by default — deliberately, since a hook
	// reflects the profile as composed at launch — so a user who wants their
	// assembled context as a file on disk has to say so.
	//
	// An unsupported (kind, approach) pair is REFUSED by the builder's Build(),
	// naming what the engine does support. It is not silently downgraded to the
	// default: a caller who asked for a file and received a hook would have no
	// file and no error.
	Surfaces map[agent.SurfaceKind]agent.Approach `json:"-"`
}

// MaterializeProfileResult reports which managed surfaces were written under
// Target, which parts of the profile the chosen engine could not carry at all,
// plus any per-surface warnings (a partial export still succeeds).
type MaterializeProfileResult struct {
	Target   string   `json:"target"`
	Backend  string   `json:"backend"`
	Profiles []string `json:"profiles"`
	Wrote    []string `json:"wrote"`
	// NotCarried is the loss half of the report: everything the profile
	// declared that this engine has no structural place for. Wrote alone is
	// true and incomplete — see agent.SurfaceLoss.
	NotCarried []agent.SurfaceLoss `json:"not_carried,omitempty"`
	Warnings   []string            `json:"warnings,omitempty"`
}

// resolveMaterializeTarget validates the request and resolves the backend whose
// native surfaces will be written. "" and the "claude" alias both mean
// claude-code; anything unregistered is an error, as is a missing config,
// target or profile set.
func resolveMaterializeTarget(cfg *config.Config, req MaterializeProfileRequest) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("config is required")
	}
	if req.Target == "" {
		return "", fmt.Errorf("target dir is required")
	}
	if len(req.Profiles) == 0 {
		return "", fmt.Errorf("at least one profile is required")
	}
	backend := req.Backend
	if backend == "" || backend == "claude" {
		backend = DefaultMaterializeBackend
	}
	if !backends.Exists(backend) {
		return "", fmt.Errorf("unknown backend %q", backend)
	}
	return backend, nil
}

// MaterializeProfile writes the assembled profile(s) into Target as the backend's
// native files, OVERWRITING each managed surface every run (the export is the
// source of truth for the target's managed files):
//   - context → CLAUDE.md (the assembled fragment block — the same payload the
//     SessionStart hook injects, but STATIC, so no context-injection hook is added)
//   - mcp     → the backend MCP config (.mcp.json / settings)
//   - hooks   → the backend settings hooks (config + profile + bundle hooks, gated)
//   - commands → the backend slash-command dir
//   - skills   → the backend's Agent Skills dir (claude only today — Part
//     B3-seam; codex/opencode/kiro are the next parallel wave)
//
// Fail-loudly (CLAUDE.md philosophy): a surface-write failure is a fatal-class
// finding recorded through strictness — the `profile materialize` choke owner
// aborts on it (exit 3) so no half-materialized target ships silently. --degraded
// downgrades every finding to a loud warning and keeps the partial target
// ("partial success is success"). Bad arguments and a failed context assembly
// (the core payload) stay hard errors regardless of mode.
func MaterializeProfile(ctx context.Context, cfg *config.Config, req MaterializeProfileRequest) (*MaterializeProfileResult, error) {
	backend, err := resolveMaterializeTarget(cfg, req)
	if err != nil {
		return nil, err
	}
	fs := getFS(req.FS)
	// The target is ours to create: materialize's whole point is standing up a
	// fresh native surface, so a nonexistent --target dir is expected input,
	// not an error.
	if err := fs.MkdirAll(req.Target, 0o755); err != nil {
		return nil, fmt.Errorf("create target dir %s: %w", req.Target, err)
	}
	res := &MaterializeProfileResult{Target: req.Target, Backend: backend, Profiles: req.Profiles}

	// Gate the executable surfaces (bundle MCP / hooks / command exports) at their
	// own choke, exactly as ApplyHooks does. Set before resolving any of them.
	// Scoped to THIS call: cfg belongs to the caller, and a gate left installed
	// on it silently governs every later consumer of that config.
	execGate := NewExecutableTrustGate(cfg)
	callersGate := cfg.ExecutableTrustGate()
	cfg.SetExecutableTrustGate(execGate.Authorizer())
	defer cfg.SetExecutableTrustGate(callersGate)

	// context is the one HARD-error surface: an explicit profile set makes
	// resolution failures fatal (the caller named these profiles), and the
	// assembled context is the core payload every native surface is built from.
	asm, err := AssembleContext(ctx, cfg, AssembleContextRequest{Profiles: req.Profiles})
	if err != nil {
		return nil, fmt.Errorf("assemble context for %v: %w", req.Profiles, err)
	}
	// An assembly that RESOLVED but carries nothing is a failed assembly too.
	// The caller NAMED these profiles, and a target built from an empty payload
	// gets no native context file at all while the result still reports the
	// context surface as written — a success message over zero delivered bytes.
	if strings.TrimSpace(asm.Context) == "" {
		return nil, fmt.Errorf("empty context: profile set %v assembled to nothing — refusing to materialize %s into %s (check the profile's fragments/bundles resolve, and that none are withheld pending review)",
			req.Profiles, backend, req.Target)
	}

	// Build the backend's OWN SurfaceSet from the assembled pieces and deliver
	// every native surface into the target as an isolated cell — the single,
	// per-provider-correct delivery path (claude → CLAUDE.md + .mcp.json +
	// .claude/settings.json + .claude/commands; kiro →
	// .kiro/…). codex opts out of a NATIVE context file, writing only its
	// config/cache surfaces. The orchestrator holds no per-backend file knowledge:
	// correctness comes from routing through backends.BuildSurfaces.
	//
	// contextHash "" omits the SessionStart context-injection hook — the context is
	// STATIC in the native file, so re-injecting it at launch would double it.
	// Each write reconciles (managed entries overwritten, foreign ones
	// preserved).
	hooks := backends.AssembleManagedHooks(cfg, req.Target, "", req.Profiles).WireDeclared()
	bundleMCP := cfg.ResolveBundleMCPServers(req.Profiles)
	commands := backends.CommandExportsFor(backend, backends.LoadCommandExports(cfg, req.Profiles))
	skills := backends.SkillExportsFor(backend, backends.LoadSkillExports(cfg, req.Profiles))
	denyTools := backends.AssembleManagedDenyTools(cfg, req.Profiles)
	settings := cfg.GetSettings()

	inputs := agent.SurfaceInputs{
		Context:          asm.Context,
		BundleMCP:        bundleMCP,
		Hooks:            hooks,
		ManageStatusline: settings.ShouldManageStatusline(),
		Commands:         commands,
		// The --target tree is a PORTABLE, self-contained artifact meant to be
		// launched on a DIFFERENT machine (an externally-launched agent, CI) with
		// ctxloom out of the loop. Deduping commands against THIS (materializing)
		// machine's ~/.claude/commands would silently drop any command that
		// happens to already exist here — wrong, since the launch environment
		// won't have it. So materialize alone opts out of that dedup.
		SelfContainedCommands: true,
		Skills:                skills,
		SelfContainedSkills:   true,
		DenyTools:             denyTools,
	}
	set := backends.BuildSurfaces(backend, inputs, fs)

	// The LOSS half of the report, read from the SAME inputs the delivery is
	// built from. res.Wrote can only ever list what landed — every line true —
	// so a surface this engine has no place for is invisible in it by
	// construction: materializing a team profile onto opencode dropped the
	// team's session_start guardrail and said nothing.
	// Reported, not fatal: the rest of the tree is still worth having, and the
	// decision to ship it anyway belongs to whoever now knows.
	if !cfg.ShouldSilenceUnsupported() {
		res.NotCarried = backends.UncarriedSurfaces(backend, inputs)
	}
	// The OTHER half of the same report, for the other kind of absence: a
	// surface this engine delivers only at launch, into a per-session engine
	// home, which THIS harpless call has no way to name (codex — see
	// backends.LaunchOnlySurfaces and internal/codex/declared_absence.go). The
	// surfaces themselves skip and warn; without this line the structured
	// result would list four true "wrote" entries and stay silent about the
	// settings, MCP servers, prompts and skills that went nowhere.
	res.NotCarried = append(res.NotCarried, backends.LaunchOnlySurfaces(backend, inputs)...)

	// Materialize delivers EVERY native surface (the full opt-in selection). Fail
	// loud, fail early (CLAUDE.md): each surface is attempted and each write failure
	// is a fatal-class finding the `profile materialize` choke owner aborts on;
	// --degraded downgrades every finding to a loud warning (strictness.record
	// no-ops) and keeps the partial target. Collecting ALL failures (not stopping at
	// the first) is what lets degraded mode produce maximal output. res.Wrote lists
	// the kinds that ACTUALLY delivered — codex's context surface is a no-op here (no
	// fragments → no native context file), so codex reports settings + commands only.
	// Materialize writes a tree for a launch with ctxloom OUT of the loop, so it
	// names the context approach rather than inheriting the engine's live
	// default. Those defaults are ordered for a RUNNING session — an out-of-cwd
	// scratch file first, a SessionStart hook next — and neither survives
	// ctxloom's absence: the scratch is passed by flag on a command line nobody
	// here will issue, and the hook invokes a ctxloom that may not be installed.
	// The native file is the only context an engine reads unaided, which is the
	// whole product of this command.
	//
	// Every engine declares unsafe-file for context, so this request is always
	// honourable: claude, kiro and opencode have always had it, and
	// codex gained it when its native AGENTS.md route stopped being folded
	// invisibly into the hook approach.
	sel := agent.Select(set).WithEverything().WithApproach(agent.SurfaceContext, agent.ApproachUnsafeFile)
	for kind, approach := range req.Surfaces {
		sel = sel.WithApproach(kind, approach)
	}
	_, kinds, errs := sel.DeliverUnder(req.Target)
	for _, e := range errs {
		strictness.Fail(strictness.ClassApply,
			"fix the write failure, then re-run (ctxloom profile materialize)",
			"materialize %s surface: %v", backend, e)
		res.Warnings = append(res.Warnings, e.Error())
	}
	for _, k := range kinds {
		res.Wrote = append(res.Wrote, k.String())
	}

	// Surface (content-free) any executable the trust gate withheld.
	execGate.WarnWithheld()
	return res, nil
}
