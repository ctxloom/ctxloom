package operations

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/agent/present"
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
	Surfaces map[agent.SurfaceKind]string `json:"-"`
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
	// WithheldByPremise names every fragment this materialization kept OUT of
	// the context because its premise did not match, and where it went instead.
	//
	// It is reported because the alternative is this project's signature
	// failure: a premise-withheld fragment was skipped silently, materialize
	// exited 0 reporting every surface written, and the content was simply
	// absent. Four acceptance fixtures gave fragments a description — which IS
	// the premise — and 22 scenarios across four features failed on a missing
	// marker with clean exits. Two agent runs were spent on it; the first could
	// not see the cause at all, because there was no signal to follow.
	//
	// SEPARATE FROM NotCarried, deliberately. NotCarried is a property of the
	// ENGINE ("this engine has no structural place for hooks"). A premise
	// withhold is a property of the CONTEXT ("this guidance did not apply
	// here"), and folding them together would make a reader unable to tell an
	// engine limitation from a premise that simply did not match.
	//
	// A withhold is NOT a loss on its own: where the engine has a skills
	// surface the fragment is re-delivered as a skill package, which Delivered
	// records. An empty Delivered is the case that actually costs content.
	WithheldByPremise []PremiseWithhold `json:"withheld_by_premise,omitempty"`
	Warnings          []string          `json:"warnings,omitempty"`
}

// PremiseWithhold is one fragment kept out of the assembled context by its
// premise, and the surface that carried it instead.
//
// Reported as STRUCTURED DATA and not as a warning, ruled by the human
// 2026-09-09. The accepted cost, recorded so it reads later as a choice: a
// person running materialize in a terminal sees nothing. Note the asymmetry
// this leaves and do not "fix" it by adding a warning — a TRUST-withheld
// fragment does warn, so the two withhold reasons report through different
// surfaces by decision.
type PremiseWithhold struct {
	// Name is the fragment's qualified ref, the same one the premise index
	// hands out, so a reader can ask for it by name.
	Name string `json:"name"`
	// Premise is the condition that did not match. Carried so the report says
	// WHY it was withheld rather than only that it was.
	Premise string `json:"premise"`
	// Delivered names the surface that carried it instead ("skills"), or is
	// EMPTY when nothing carried it. Empty is the case worth looking at: it
	// means the content reached the agent by no route at all.
	Delivered string `json:"delivered,omitempty"`
}

// resolveMaterializeTarget validates the request and resolves the backend whose
// native surfaces will be written, canonicalizing the requested name. "" means
// the default; anything unregistered is an error, as is a missing config,
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
	if req.Backend == "" {
		return registeredBackend(DefaultMaterializeBackend)
	}
	return registeredBackend(req.Backend)
}

// registeredBackend refuses a name no engine is registered under exactly.
// The registered name is what every backends.* lookup keys on and what
// results report.
func registeredBackend(name string) (string, error) {
	if !backends.Exists(name) {
		return "", fmt.Errorf("unknown backend %q", name)
	}
	return name, nil
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
	// A materialized surface is ctxloom OUT OF THE LOOP, so a premised fragment
	// withheld here cannot be pulled later — it is lost, not deferred. Where the
	// engine has its own Agent Skills surface we hand it the fragments as skill
	// packages instead (WithheldFragments, below), which is the same progressive
	// disclosure the premise index gives a live session, done by the engine's
	// own mechanism. Where it does not, they are written into the context so
	// nothing is ever lost. That fork is NOT decided here: this call states
	// what it is writing and ContextConsumer.static decides, the same way it
	// decides for every composition that must match this file.
	asm, err := AssembleContext(ctx, cfg, AssembleContextRequest{
		Profiles: req.Profiles,
		Consumer: MaterializedFor(backend),
	})
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

	// Select over the backend's OWN Declaration with the assembled pieces and deliver
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
	// Withheld fragments join the authored skills. A collision between two of
	// them is fatal rather than a silent overwrite — see PremisedFragmentSkills.
	withheld := make([]backends.PremisedFragment, 0, len(asm.WithheldFragments))
	for _, w := range asm.WithheldFragments {
		withheld = append(withheld, backends.PremisedFragment{Ref: w.Name, Premise: w.Premise, Content: w.Content})
	}
	fragmentSkills, err := backends.PremisedFragmentSkills(withheld)
	if err != nil {
		return nil, fmt.Errorf("materialize premised fragments as skills for %v: %w", req.Profiles, err)
	}
	skills = append(skills, fragmentSkills...)
	// Report the withholds. Built from what ACTUALLY happened rather than from
	// the capability flag: fragmentSkills is what was really produced, so a
	// fragment that failed to become a skill is reported as delivered nowhere
	// instead of being described by the branch we hoped we took.
	res.WithheldByPremise = describePremiseWithholds(asm.PremiseIndex, len(fragmentSkills) > 0)
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
	decl := backends.Declared(backend)

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
	// Context and MCP are pinned to the engine's native file. A materialized
	// tree must outlive ctxloom, so it cannot carry a surface whose only form
	// is a file some future launch names on argv: MCP's declared DEFAULT is now
	// exactly that, and WithEverything would otherwise take it and refuse here.
	sel := agent.Select(decl).WithEverything().
		With(agent.SurfaceContext, agent.ApproachUnsafeFile).
		With(agent.SurfaceMCP, agent.ApproachUnsafeFile)
	for kind, approach := range req.Surfaces {
		sel = sel.With(kind, approach)
	}
	_, kinds, errs := sel.DeliverUnder(inputs, fs, present.ProjectOnHost(req.Target))
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

// describePremiseWithholds turns the assembly's premise index into the result's
// withhold report. asSkills says whether the withheld bodies were actually
// re-delivered as skill packages.
//
// Returns nil for an empty index, and every caller relies on that: a
// materialization over a corpus authoring no premises must report no withholds
// rather than an empty list, so the JSON omitempty tag can keep the payload
// byte-identical to what it was before this field existed.
func describePremiseWithholds(index []PremiseIndexEntry, asSkills bool) []PremiseWithhold {
	if len(index) == 0 {
		return nil
	}
	delivered := ""
	if asSkills {
		delivered = "skills"
	}
	out := make([]PremiseWithhold, 0, len(index))
	for _, e := range index {
		out = append(out, PremiseWithhold{Name: e.Name, Premise: e.Premise, Delivered: delivered})
	}
	return out
}
