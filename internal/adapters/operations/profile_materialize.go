package operations

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines"
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
// The registered name is what every registry lookup keys on and what
// results report.
func registeredBackend(name string) (string, error) {
	if !EngineExists(name) {
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
//   - skills   → the backend's Agent Skills dir
//
// Fail-loudly (CLAUDE.md philosophy): a surface-write failure is a fatal-class
// finding recorded through strictness — the `profile materialize` choke owner
// aborts on it (exit 3) so no half-materialized target ships silently. --degraded
// downgrades every finding to a loud warning and keeps the partial target
// ("partial success is success"). Bad arguments and a failed context assembly
// (the core payload) stay hard errors regardless of mode.
func MaterializeProfile(ctx context.Context, cfg *config.Config, req MaterializeProfileRequest) (*MaterializeProfileResult, error) {
	if _, err := cfg.RequireTrust(); err != nil {
		return nil, fmt.Errorf("materialize: %w", err)
	}
	backend, err := resolveMaterializeTarget(cfg, req)
	if err != nil {
		return nil, err
	}
	fs := getFS(req.FS)
	// The target is ours to create: materialize's whole point is standing up a
	// fresh native surface, so a nonexistent --target dir is expected input,
	// not an error. It is delivered to by its absolute path: the ownership
	// record keys every file by path, and a relative one records nothing
	// anyone could find again.
	target, err := filepath.Abs(req.Target)
	if err != nil {
		return nil, fmt.Errorf("resolve target dir %s: %w", req.Target, err)
	}
	req.Target = target
	if err := fs.MkdirAll(req.Target, 0o755); err != nil {
		return nil, fmt.Errorf("create target dir %s: %w", req.Target, err)
	}
	res := &MaterializeProfileResult{Target: req.Target, Backend: backend, Profiles: req.Profiles}

	// The executable surfaces (bundle MCP / hooks / command exports) decide
	// at their own choke with the generation's Trust, exactly as ApplyHooks.

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
	pkg, err := AssemblePackage(ctx, cfg, PackageRequest{
		Profiles: req.Profiles,
		Consumer: MaterializedFor(backend),
		WorkDir:  req.Target,
	})
	if err != nil {
		return nil, fmt.Errorf("assemble context for %v: %w", req.Profiles, err)
	}
	asm := contextResultOf(pkg)
	// An assembly that RESOLVED but carries nothing is a failed assembly too.
	// The caller NAMED these profiles, and a target built from an empty payload
	// gets no native context file at all while the result still reports the
	// context surface as written — a success message over zero delivered bytes.
	if strings.TrimSpace(asm.Context) == "" {
		return nil, fmt.Errorf("empty context: profile set %v assembled to nothing — refusing to materialize %s into %s (check the profile's fragments/bundles resolve, and that none are withheld pending review)",
			req.Profiles, backend, req.Target)
	}

	// Withheld fragments join the package as skill packages: a materialized
	// tree is ctxloom OUT of the loop, so a premised fragment withheld here
	// cannot be pulled later — as a skill the engine's own progressive
	// disclosure carries it. A collision between two of them is fatal rather
	// than a silent overwrite — see PremisedFragmentSkills.
	withheld := make([]PremisedFragment, 0, len(asm.WithheldFragments))
	for _, w := range asm.WithheldFragments {
		withheld = append(withheld, PremisedFragment{Ref: w.Name, Premise: w.Premise, Content: w.Content})
	}
	fragmentSkills, err := PremisedFragmentSkills(withheld)
	if err != nil {
		return nil, fmt.Errorf("materialize premised fragments as skills for %v: %w", req.Profiles, err)
	}
	for _, sk := range fragmentSkills {
		skill := composite.Skill{Name: sk.Name, Description: sk.Description}
		for _, f := range sk.Files {
			skill.Files = append(skill.Files, engine.SkillFile{Path: f.RelPath, Bytes: f.Content, Size: int64(len(f.Content)), Mode: uint32(f.Mode.Perm())})
		}
		pkg.Skills = append(pkg.Skills, composite.Item[composite.Skill]{Ref: "materialize#skill/" + sk.Name, Value: skill})
	}
	// Built from what ACTUALLY happened rather than from the capability
	// flag: fragmentSkills is what was really produced.
	res.WithheldByPremise = describePremiseWithholds(asm.PremiseIndex, len(fragmentSkills) > 0)
	settings := cfg.GetSettings()
	pkg.Statusline = settings.ShouldManageStatusline()

	// Materialize is the ONE sanctioned human-invoked project-root writer:
	// the same static delivery a session gets, with the project root as the
	// target and the project writer's record. Every kind the engine offers
	// at the project root lands there; a kind it does not is reported as
	// not carried, never routed elsewhere. The context is the engine's
	// native file — a materialized tree must be readable with ctxloom out
	// of the loop, so no injection hook is written.
	kind, ok := engines.Registry().Lookup(engine.Name(backend))
	if !ok {
		return nil, fmt.Errorf("materialize: no engine kind is composed for %s", backend)
	}
	delivered, plan, err := DeliverProject(ctx, fs, kind, pkg, req.Target)
	if err != nil {
		strictness.Fail(strictness.ClassApply,
			"fix the write failure, then re-run (ctxloom profile materialize)",
			"materialize %s: %v", backend, err)
		res.Warnings = append(res.Warnings, err.Error())
	}
	for _, k := range delivered.Wrote {
		res.Wrote = append(res.Wrote, k.String())
	}
	if !cfg.ShouldSilenceUnsupported() {
		res.NotCarried = notCarried(backend, kind, pkg, plan)
	}

	// Surface (content-free) any executable the trust gate withheld.
	WarnWithheldBy(cfg.ExecutableTrustGate())
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

// notCarried is the LOSS half of the materialize report, read from the same
// plan the delivery was built from: a kind the engine does not carry at all
// (the plan's accepted losses), and a hook event the engine fires no hook
// for (absent from its exported event table — the port's own statement of
// an uncarried event). res.Wrote can only ever list what landed, so a loss
// is invisible in it by construction; this names it.
func notCarried(backend string, kind engine.Engine, pkg composite.Package, plan delivery.Plan) []agent.SurfaceLoss {
	var out []agent.SurfaceLoss
	for _, loss := range plan.Losses {
		out = append(out, agent.SurfaceLoss{
			Surface: loss.Kind.String(),
			Detail:  fmt.Sprintf("%s has no %s surface; nothing was written for it", backend, loss.Kind),
			Reason:  fmt.Sprintf("%s declares no approach for %s", backend, loss.Kind),
		})
	}
	exports, err := kind.Exports(pkg.EngineItems(kind.Root().Name))
	if err != nil {
		return out
	}
	for _, event := range wire.HookEvents() {
		n := len(pkg.Hooks.Unified.Event(event))
		if n == 0 || exports.HookEvent[event] != "" {
			continue
		}
		out = append(out, agent.SurfaceLoss{
			Surface: "hooks",
			Detail:  fmt.Sprintf("%d %s", n, event),
			Reason:  fmt.Sprintf("%s has no native %s event", backend, event),
		})
	}
	return out
}
