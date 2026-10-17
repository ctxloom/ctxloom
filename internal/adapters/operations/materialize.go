package operations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// SurfaceSpec is one `--surface KIND[=MECHANISM][:DEST]`: the kind to
// deliver, its mechanism ("" = the engine's own approach, or
// agent.ApproachFile, which at rest is the same thing) and, for context
// only, the destination ("" = the engine's own file).
type SurfaceSpec struct {
	Kind      present.Kind `json:"kind"`
	Mechanism string       `json:"mechanism,omitempty"`
	Dest      string       `json:"dest,omitempty"`
}

// MaterializeRequest is one at-rest delivery: which profiles, into which
// directory, for which engines, which surfaces.
type MaterializeRequest struct {
	// Profiles is the profile set; nil is the default agent's.
	Profiles []string `json:"profiles,omitempty"`
	// Target is REQUIRED and explicit: the CLI resolves the project-root
	// default and applies its guard; an internal caller passes its own.
	Target string `json:"target"`
	// Explicit says the user named Target rather than taking the CLI's
	// project-root default; the CLI's project-directory guard (R14) keys on
	// it. Engine resolution does not: with no engine named or configured,
	// every target is delivered for the registry's default engine (R6,
	// ruling 16).
	Explicit bool `json:"explicit,omitempty"`
	// Engines is the engine set; nil is the configured engines.
	Engines []string `json:"engines,omitempty"`
	// Surfaces selects kinds; nil is every kind through its default. A kind
	// not named is untouched: neither written nor released.
	Surfaces []SurfaceSpec `json:"surfaces,omitempty"`
	// Release delivers the empty plan for the selected kinds.
	Release bool `json:"release,omitempty"`
	// DryRun plans and reports, and writes (and creates) nothing.
	DryRun bool `json:"dry_run,omitempty"`
	// Force overrides the refusal to write an engine's user-global scope.
	Force bool `json:"force,omitempty"`
	// LeaveLaunchContext drops the context kind for every engine whose
	// context rides the launch (contextRidesTheLaunch): its context is
	// neither written nor released. The post-sync refresh sets it — a
	// launched session carries that context itself, so a copy at rest would
	// double it — while hooks, MCP, settings and commands still refresh.
	LeaveLaunchContext bool `json:"leave_launch_context,omitempty"`
	// Strictness is the posture the assembly gate judges by (the caller's
	// App.Strictness; the zero value is strict). Every engine's package is
	// assembled before anything is written, and a finding raised during that
	// assembly which this mode acts on (strictness.Mode.Actionable) refuses
	// the whole delivery — MaterializeRefusal — with nothing written, not even
	// the target directory. Under Degraded the ordinary findings are waived
	// and the delivery writes as it always did, the findings still streamed
	// and recorded. A dry run and a release write nothing assembled, and are
	// not gated here: the caller's own gate lists the findings.
	Strictness strictness.Mode `json:"-"`
	Root       safefs.Root     `json:"-"`
}

// MaterializeResult is what one materialize did, per engine.
type MaterializeResult struct {
	Target string `json:"target"`
	// Created says the target directory did not exist and was created.
	Created  bool            `json:"created,omitempty"`
	Profiles []string        `json:"profiles,omitempty"`
	Engines  []EngineOutcome `json:"engines"`
	// Status is applied | partial | failed | released | planned (a dry run)
	// | nothing (nothing to release).
	Status   string   `json:"status"`
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// EngineOutcome is one engine's part of a materialize.
type EngineOutcome struct {
	Engine string `json:"engine"`
	// Wrote names the kinds delivered (or, in a dry run, planned).
	Wrote []string `json:"wrote,omitempty"`
	// Released names the selected kinds this run left with nothing: their
	// earlier claims, if any, were taken out.
	Released []string `json:"released,omitempty"`
	// ContextFile is the absolute path the context was written to.
	ContextFile       string              `json:"context_file,omitempty"`
	NotCarried        []agent.SurfaceLoss `json:"not_carried,omitempty"`
	WithheldByPremise []PremiseWithhold   `json:"withheld_by_premise,omitempty"`
	// Skipped names each command or skill the engine's writers skipped or
	// refused (an unsafe name, a failed render, a vendor constraint): the
	// rest was delivered, these were not.
	Skipped []string `json:"skipped,omitempty"`
	// Refused names, in a dry run, each kind whose delivery would be
	// refused (a value that is not ctxloom's, a declared file missing, a
	// file it cannot read), with the fix. Nothing of such a kind would land.
	Refused  []KindRefusal `json:"refused,omitempty"`
	Warnings []string      `json:"warnings,omitempty"`
}

// KindRefusal is one kind a dry run found its delivery would refuse: why,
// and what the human changes so it can land.
type KindRefusal struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	Fix    string `json:"fix"`
}

// Materialize statuses.
const (
	MaterializeApplied  = "applied"
	MaterializePartial  = "partial"
	MaterializeFailed   = "failed"
	MaterializeReleased = "released"
	MaterializePlanned  = "planned"
	MaterializeNothing  = "nothing"
)

// ErrMaterializeNoTarget refuses a request with no Target: a programming
// error, since the CLI and every internal caller resolve their own.
var ErrMaterializeNoTarget = errors.New("materialize: a target directory is required")

// ErrMaterializeRefused is the assembly gate's refusal (MaterializeRefusal):
// a finding the request's strictness acts on was raised while assembling,
// and nothing was written.
var ErrMaterializeRefused = errors.New("materialize: refused over fatal findings raised while assembling; nothing was written")

// MaterializeRefusal is the assembly gate's refusal: the actionable findings
// raised while every engine's package was assembled. Each was already
// streamed (warning and fix line) when it was raised, and stays on the
// strictness ledger, so a caller with a phase gate of its own closes it to
// render the abort; Error lists them for any other caller.
type MaterializeRefusal struct {
	Findings report.Findings
}

func (r MaterializeRefusal) Error() string {
	return strictness.Mode{}.Listing(ErrMaterializeRefused.Error()+":", r.Findings)
}

// Is makes a MaterializeRefusal an ErrMaterializeRefused.
func (r MaterializeRefusal) Is(target error) bool { return target == ErrMaterializeRefused }

// ErrMaterializeRequest refuses an incoherent request (a release with
// profiles or a destination, a destination on a kind other than context, an
// unknown mechanism, one kind given two specs).
var ErrMaterializeRequest = errors.New("materialize: invalid request")

// Materialize delivers the assembled profile set AT REST into req.Target as
// each engine's native files: the at-rest delivery the CLI's
// `ctxloom materialize` and the post-sync apply share. It runs in three
// phases. ASSEMBLE: every engine's package is assembled out of the loop
// (premised fragments become skills where skills are delivered and the
// engine exports them, and are inlined otherwise), once each, before
// anything is written. GATE: a finding raised by that assembly which
// req.Strictness acts on refuses the whole delivery (MaterializeRefusal),
// so a strict run never leaves a degraded delivery behind it. DELIVER: each
// package goes to the placement core at the target, under the engine's
// per-kind project writers, for exactly the selected kinds.
//
// A per-engine write failure is a fatal-class strictness finding and an
// error on the result (partial success is success); bad arguments, an
// unresolvable target, a failed assembly and the gate's refusal are hard
// errors.
func Materialize(ctx context.Context, reg engine.Registry, cfg *config.Config, req MaterializeRequest) (*MaterializeResult, error) {
	m, err := newMaterializeRun(reg, cfg, req)
	if err != nil {
		return nil, err
	}
	if req.Release {
		return materializeRelease(ctx, reg, m.root, m.names, m.kinds, m.res)
	}
	staged, err := assembleEngines(ctx, reg, cfg, m)
	if err != nil {
		return nil, err
	}
	if err := m.createTarget(reg); err != nil {
		return nil, err
	}
	for _, s := range staged {
		out, err := materializeEngine(ctx, cfg, m, s)
		if err != nil {
			return nil, err
		}
		m.res.Engines = append(m.res.Engines, out)
		m.res.Errors = append(m.res.Errors, outcomeErrors(out)...)
	}
	m.res.Status = materializeStatus(req, m.res)
	return m.res, nil
}

// stagedEngine is one engine's assembled package, held between the
// assembly and the delivery.
type stagedEngine struct {
	name     string
	kind     engine.Engine
	kinds    []present.Kind
	pkg      composite.Package
	withheld []PremiseWithhold
}

// assembleEngines assembles every engine's package, then gates: a finding
// raised during the assembly that m.req.Strictness acts on refuses with a
// MaterializeRefusal (a dry run is not gated: it writes nothing). The window
// is this call's own checkpoint, so a finding raised before it — or by a
// concurrent caller on another goroutine — never refuses this delivery, and
// a FailOnce finding an earlier window already recorded is recorded again in
// this one rather than slipping past it.
func assembleEngines(ctx context.Context, reg engine.Registry, cfg *config.Config, m *materializeRun) ([]stagedEngine, error) {
	mark := strictness.Checkpoint()
	defer strictness.Close(mark)
	staged := make([]stagedEngine, 0, len(m.names))
	for _, name := range m.names {
		kind, ok := reg.Lookup(engine.Name(name))
		if !ok {
			return nil, fmt.Errorf("materialize: no engine kind is composed for %s", name)
		}
		kinds := engineKinds(m.req, kind.Root(), m.kinds)
		pkg, withheld, err := materializePackage(ctx, reg, cfg, m.req, name, kinds)
		if err != nil {
			return nil, err
		}
		staged = append(staged, stagedEngine{name: name, kind: kind, kinds: kinds, pkg: pkg, withheld: withheld})
	}
	if m.req.DryRun {
		return staged, nil
	}
	if found := m.req.Strictness.Actionable(strictness.Since(mark)); len(found) > 0 {
		return nil, MaterializeRefusal{Findings: found}
	}
	return staged, nil
}

// materializeRun is one Materialize's resolved inputs.
type materializeRun struct {
	req         MaterializeRequest
	root        safefs.Root
	kinds       []present.Kind
	target      string
	created     bool
	contextDest string
	contextFile string
	names       []string
	res         *MaterializeResult
}

// newMaterializeRun validates req and resolves its target, kinds, context
// destination and engines, and refuses an engine whose user-global scope
// the target is (unless req.Force).
func newMaterializeRun(reg engine.Registry, cfg *config.Config, req MaterializeRequest) (*materializeRun, error) {
	if cfg == nil {
		return nil, fmt.Errorf("materialize: a config generation is required")
	}
	if req.Target == "" {
		return nil, ErrMaterializeNoTarget
	}
	kinds, contextDest, err := materializeKinds(req)
	if err != nil {
		return nil, err
	}
	m := &materializeRun{req: req, root: rootOf(req.Root), kinds: kinds, contextDest: contextDest}
	// Resolved, never created, here: the target is created only once the
	// assembly gate has passed (createTarget), so a refused delivery leaves
	// no directory behind.
	if err := m.resolveTarget(false); err != nil {
		return nil, err
	}
	m.res = &MaterializeResult{Target: m.target, Engines: []EngineOutcome{}, Profiles: materializeProfiles(cfg, req)}
	if m.names, err = materializeEngines(reg, cfg, req); err != nil {
		return nil, err
	}
	if err := m.checkScope(reg); err != nil {
		return nil, err
	}
	return m, nil
}

// resolveTarget resolves the target (ResolveTarget, creating it when create
// is set and it is missing) and the context file inside it, noting whether
// it created the directory.
func (m *materializeRun) resolveTarget(create bool) error {
	target, created, err := ResolveTarget(m.root.Fs, m.req.Target, create, symlinkResolver(m.root.Fs))
	if err != nil {
		return err
	}
	m.target, m.created = target, created
	m.contextFile, err = ResolveContextFile(m.target, m.contextDest)
	return err
}

// checkScope refuses an engine whose user-global scope the target is
// (unless req.Force).
func (m *materializeRun) checkScope(reg engine.Registry) error {
	for _, name := range m.names {
		if err := checkHookTargetScopeOf(reg, name, m.target, m.req.Force); err != nil {
			return err
		}
	}
	return nil
}

// createTarget creates a missing target for a delivery that will write,
// once the assembly gate has passed. A directory that did not exist was
// resolved unresolved (ResolveTarget resolves links only through an existing
// path), so the created one is resolved again and the user-global scope
// re-checked against what it resolved to.
func (m *materializeRun) createTarget(reg engine.Registry) error {
	if m.req.DryRun {
		return nil
	}
	if err := m.resolveTarget(true); err != nil {
		return err
	}
	m.res.Target, m.res.Created = m.target, m.created
	return m.checkScope(reg)
}

// materializeProfiles is the profile set a result reports: none for a
// release, else the request's, else the default agent's.
func materializeProfiles(cfg *config.Config, req MaterializeRequest) []string {
	switch {
	case req.Release:
		return nil
	case req.Profiles != nil:
		return req.Profiles
	default:
		return cfg.DefaultAgentProfiles()
	}
}

// materializeKinds is the selected kinds (every kind when none is named)
// and the context destination, refusing an incoherent selection.
func materializeKinds(req MaterializeRequest) ([]present.Kind, string, error) {
	if req.Release && len(req.Profiles) > 0 {
		return nil, "", fmt.Errorf("%w: --release takes no profiles", ErrMaterializeRequest)
	}
	if len(req.Surfaces) == 0 {
		return delivery.AllKinds(), "", nil
	}
	seen := map[present.Kind]SurfaceSpec{}
	var kinds []present.Kind
	dest := ""
	for _, s := range req.Surfaces {
		if err := checkSurfaceSpec(s, req.Release); err != nil {
			return nil, "", err
		}
		if prev, ok := seen[s.Kind]; ok {
			if prev != s {
				return nil, "", fmt.Errorf("%w: surface %s is named twice, differently", ErrMaterializeRequest, s.Kind)
			}
			continue
		}
		seen[s.Kind] = s
		kinds = append(kinds, s.Kind)
		if s.Kind == present.Context {
			dest = s.Dest
		}
	}
	// The plan's order, whatever order the flags came in.
	slices.SortFunc(kinds, func(a, b present.Kind) int {
		return slices.Index(delivery.AllKinds(), a) - slices.Index(delivery.AllKinds(), b)
	})
	return kinds, dest, nil
}

// checkSurfaceSpec refuses one spec at rest: an unknown kind, a mechanism
// other than the native file, a destination off context or on a release.
func checkSurfaceSpec(s SurfaceSpec, release bool) error {
	switch {
	case !slices.Contains(delivery.AllKinds(), s.Kind):
		return fmt.Errorf("%w: %v is not a surface kind", ErrMaterializeRequest, s.Kind)
	case s.Mechanism != "" && s.Mechanism != agent.ApproachFile:
		return fmt.Errorf("%w: %s=%s: at rest the only mechanism is %q", ErrMaterializeRequest, s.Kind, s.Mechanism, agent.ApproachFile)
	case s.Dest != "" && s.Kind != present.Context:
		return fmt.Errorf("%w: %s takes no destination; only context does", ErrMaterializeRequest, s.Kind)
	case s.Dest != "" && release:
		return fmt.Errorf("%w: --release takes no destination", ErrMaterializeRequest)
	}
	return nil
}

// materializeEngines resolves the engine set (R6): the named engines, each
// registered; else the configured engines, which already fall back to the
// registry's default (ruling 16) — so the tail below is reached only by a
// registry that ships no default, and refuses with its error.
func materializeEngines(reg engine.Registry, cfg *config.Config, req MaterializeRequest) ([]string, error) {
	if len(req.Engines) > 0 {
		var out []string
		for _, name := range req.Engines {
			if _, err := namedBackend(reg, name); err != nil {
				return nil, err
			}
			if !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
		return out, nil
	}
	if names := ConfiguredEngines(reg, cfg); len(names) > 0 {
		return names, nil
	}
	def, err := reg.Default()
	if err != nil {
		return nil, err
	}
	return []string{string(def.Root().Name)}, nil
}

// materializeEngine delivers (or, in a dry run, plans) one engine's
// assembled package at the target.
func materializeEngine(ctx context.Context, cfg *config.Config, m *materializeRun, s stagedEngine) (EngineOutcome, error) {
	name, kind, kinds, pkg := s.name, s.kind, s.kinds, s.pkg
	out := EngineOutcome{Engine: name, WithheldByPremise: s.withheld}
	p := atRestPlacement(m.target, engine.Name(name), kinds)
	p.ContextFile = m.contextFile
	var plan delivery.Plan
	var err error
	if m.req.DryRun {
		plan, err = previewMaterializedAt(ctx, m.root, kind, pkg, p, &out)
		if err != nil {
			return out, err
		}
	} else {
		plan = deliverMaterializedAt(ctx, m.root, kind, pkg, p, &out)
		for _, k := range kinds {
			if !slices.Contains(out.Wrote, k.String()) {
				out.Released = append(out.Released, k.String())
			}
		}
	}
	if !cfg.ShouldSilenceUnsupported() {
		out.NotCarried = selectedLosses(notCarried(name, kind, pkg, plan), kinds)
	}
	return out, nil
}

// engineKinds is the kinds delivered for one engine: the selected kinds,
// less context when req.LeaveLaunchContext is set and the engine's context
// rides the launch.
func engineKinds(req MaterializeRequest, root engine.Base, kinds []present.Kind) []present.Kind {
	if !req.LeaveLaunchContext || !contextRidesTheLaunch(root) {
		return kinds
	}
	return slices.DeleteFunc(slices.Clone(kinds), func(k present.Kind) bool { return k == present.Context })
}

// contextRidesTheLaunch decides, from the engine's declaration alone, how
// the context reaches it with ctxloom IN the loop: an engine whose context
// approach is told on argv (a launch-time channel, nothing it opens at rest)
// takes the context at launch, so nothing at rest need carry it; an engine
// that opens a context file reads the file written at rest.
func contextRidesTheLaunch(root engine.Base) bool {
	a, ok := root.Surfaces()[present.Context]
	return ok && a.Traits().Channel == present.ChannelArgv
}

// previewMaterializedAt is a dry run at p: the delivery previewed against the
// project as it stands (Preview), writing nothing. Only a kind that differs
// is recorded — a planned one as what would be written, one the plan no
// longer routes as what would be released — so a project already current
// records nothing. A kind whose delivery would be refused is recorded with
// its fix (KindRefusal) and fails nothing: the refusal is the dry run's
// finding, not its error. The writers' skips are recorded as a delivery's
// are.
func previewMaterializedAt(ctx context.Context, root safefs.Root, kind engine.Engine, pkg composite.Package, p Placement, out *EngineOutcome) (delivery.Plan, error) {
	var skipped report.Findings
	pv, plan, err := Preview(ctx, root, kind, pkg, delivery.Loadout{Report: &skipped}, p)
	if err != nil {
		return delivery.Plan{}, err
	}
	for _, f := range skipped {
		out.Skipped = append(out.Skipped, f.Text)
	}
	planned := map[present.Kind]bool{}
	for _, it := range plan.Static {
		planned[it.Kind] = true
	}
	for _, k := range pv.Kinds {
		switch {
		case k.Refused != nil:
			out.Refused = append(out.Refused, KindRefusal{Kind: k.Kind.String(), Reason: k.Refused.Error(), Fix: refusalFix(k.Kind, k.Refused)})
		case !k.Differs():
		case planned[k.Kind]:
			out.Wrote = append(out.Wrote, k.Kind.String())
		default:
			out.Released = append(out.Released, k.Kind.String())
		}
	}
	return plan, nil
}

// refusalFix is what the human changes so a refused kind can land: the
// refusal's own remedy when it carries one, else the change its cause asks
// for.
func refusalFix(k present.Kind, err error) string {
	var r report.Remediable
	switch {
	case errors.As(err, &r) && r.Remedy() != "":
		return r.Remedy()
	case errors.Is(err, fsstatic.ErrNotOurs):
		return "ctxloom will not overwrite a value it did not put there: move or remove yours at the place named, then apply"
	case errors.Is(err, fsstatic.ErrDeclaredMissing), errors.Is(err, fsstatic.ErrDeclaredUnclaimed):
		return fmt.Sprintf("release the kind with 'ctxloom materialize --release --yes --surface %s', then apply it again", k)
	}
	return "correct the file the reason names (it must parse, and ctxloom must be able to read it), then apply"
}

// deliverMaterializedAt delivers pkg at p and records on out what landed,
// the context file written, the writers' skips, and a write failure (a
// fatal-class finding and a warning, not an error: the choke owner decides).
func deliverMaterializedAt(ctx context.Context, root safefs.Root, kind engine.Engine, pkg composite.Package, p Placement, out *EngineOutcome) delivery.Plan {
	var skipped report.Findings
	delivered, plan, err := Deliver(ctx, root, kind, pkg, delivery.Loadout{Report: &skipped}, p)
	for _, f := range skipped {
		out.Skipped = append(out.Skipped, f.Text)
	}
	if err != nil {
		strictness.Fail(report.KindApply, "fix the write failure, then re-run (ctxloom materialize)", "materialize %s: %v", out.Engine, err)
		out.Warnings = append(out.Warnings, err.Error())
	}
	for i, k := range delivered.Wrote {
		out.Wrote = append(out.Wrote, k.String())
		if k == present.Context && i < len(delivered.Presented) {
			out.ContextFile = delivered.Presented[i].HostPath
		}
	}
	return plan
}

// materializePackage assembles the profile set for one engine, out of the
// loop (R7): with skills selected, premised fragments the engine can carry
// as skills become skills; otherwise they are inlined. The empty-context
// refusal applies only when context is selected.
func materializePackage(ctx context.Context, reg engine.Registry, cfg *config.Config, req MaterializeRequest, name string, kinds []present.Kind) (composite.Package, []PremiseWithhold, error) {
	consumer := MaterializedFor(reg, name)
	skills := slices.Contains(kinds, present.Skills)
	if !skills {
		consumer = consumer.WithoutSkills()
	}
	pkg, err := AssemblePackage(ctx, cfg, PackageRequest{Profiles: req.Profiles, Consumer: consumer})
	if err != nil {
		return composite.Package{}, nil, fmt.Errorf("assemble context for %v: %w", req.Profiles, err)
	}
	asm := contextResultOf(pkg)
	if slices.Contains(kinds, present.Context) && strings.TrimSpace(asm.Context) == "" {
		return composite.Package{}, nil, fmt.Errorf("empty context: profile set %v assembled to nothing — refusing to materialize %s (check the profile's fragments/bundles resolve)", req.Profiles, name)
	}
	var fragmentSkills []composite.Item[composite.Skill]
	if skills {
		fragmentSkills, err = withheldFragmentSkillItems(asm)
		if err != nil {
			return composite.Package{}, nil, fmt.Errorf("materialize premised fragments as skills for %v: %w", req.Profiles, err)
		}
		pkg.Skills = append(pkg.Skills, fragmentSkills...)
	}
	settings := cfg.GetSettings()
	pkg.Statusline = settings.ShouldManageStatusline()
	pkg.ShellTimeout = cfg.GetShellTimeout()
	return pkg, describePremiseWithholds(asm.PremiseIndex, len(fragmentSkills) > 0), nil
}

// selectedLosses keeps the losses of the selected kinds.
func selectedLosses(losses []agent.SurfaceLoss, kinds []present.Kind) []agent.SurfaceLoss {
	var out []agent.SurfaceLoss
	for _, l := range losses {
		if k, ok := present.ParseKind(l.Surface); ok && slices.Contains(kinds, k) {
			out = append(out, l)
		}
	}
	return out
}

// materializeRelease delivers the empty plan for the selected kinds of each
// engine at the target. A target that does not exist holds nothing to
// release.
func materializeRelease(ctx context.Context, reg engine.Registry, root safefs.Root, names []string, kinds []present.Kind, res *MaterializeResult) (*MaterializeResult, error) {
	if exists, err := afero.DirExists(root.Fs, res.Target); err != nil || !exists {
		res.Status = MaterializeNothing
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s does not exist; there is nothing to release", res.Target))
		return res, nil
	}
	for _, name := range names {
		out := EngineOutcome{Engine: name}
		kind, ok := reg.Lookup(engine.Name(name))
		if !ok {
			return nil, fmt.Errorf("materialize: no engine kind is composed for %s", name)
		}
		if err := Release(ctx, root, kind, atRestPlacement(res.Target, engine.Name(name), kinds)); err != nil {
			strictness.Fail(report.KindApply, "fix the failure, then re-run (ctxloom materialize --release)", "release %s: %v", name, err)
			out.Warnings = append(out.Warnings, err.Error())
		} else {
			for _, k := range kinds {
				out.Released = append(out.Released, k.String())
			}
		}
		res.Engines = append(res.Engines, out)
		res.Errors = append(res.Errors, outcomeErrors(out)...)
	}
	res.Status = MaterializeReleased
	if len(res.Errors) > 0 {
		res.Status = MaterializePartial
		if len(res.Errors) == len(names) {
			res.Status = MaterializeFailed
		}
	}
	return res, nil
}

// outcomeErrors is one engine's failures, prefixed with its name.
func outcomeErrors(out EngineOutcome) []string {
	var errs []string
	for _, w := range out.Warnings {
		errs = append(errs, out.Engine+": "+w)
	}
	return errs
}

// materializeStatus is the run's status from its engines' outcomes.
func materializeStatus(req MaterializeRequest, res *MaterializeResult) string {
	switch {
	case req.DryRun:
		return MaterializePlanned
	case len(res.Errors) == 0:
		return MaterializeApplied
	case len(res.Errors) < len(res.Engines):
		return MaterializePartial
	default:
		return MaterializeFailed
	}
}

// TargetNotDirectoryError refuses a --target that names an existing file.
type TargetNotDirectoryError struct{ Path string }

func (e TargetNotDirectoryError) Error() string {
	return fmt.Sprintf("materialize: target %s exists and is not a directory", e.Path)
}

// ResolveTarget is the target as the ownership record keys it (R11): made
// absolute against the working directory, refused when it names an
// existing non-directory, created when create is true and it is missing,
// and then symlink-resolved through resolve (filepath.EvalSymlinks in
// production; the identity on a memory filesystem, which has no links). A
// target that does not exist (and was not created) is returned absolute and
// unresolved.
func ResolveTarget(fs afero.Fs, target string, create bool, resolve func(string) (string, error)) (abs string, created bool, err error) {
	abs, err = filepath.Abs(target)
	if err != nil {
		return "", false, fmt.Errorf("resolve target dir %s: %w", target, err)
	}
	exists, err := targetDirExists(fs, abs)
	if err != nil {
		return "", false, err
	}
	if !exists {
		if !create {
			return abs, false, nil
		}
		if err := fs.MkdirAll(abs, 0o755); err != nil {
			return "", false, fmt.Errorf("create target dir %s: %w", abs, err)
		}
		created = true
	}
	resolved, err := resolve(abs)
	if err != nil {
		return "", false, fmt.Errorf("resolve target dir %s: %w", abs, err)
	}
	return resolved, created, nil
}

// targetDirExists reports whether abs is an existing directory, refusing
// an existing non-directory (TargetNotDirectoryError).
func targetDirExists(fs afero.Fs, abs string) (bool, error) {
	info, err := fs.Stat(abs)
	switch {
	case os.IsNotExist(err):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("resolve target dir %s: %w", abs, err)
	case !info.IsDir():
		return false, TargetNotDirectoryError{Path: abs}
	}
	return true, nil
}

// symlinkResolver is how a target's links are resolved on fs: the
// operating system's for an OS filesystem, the identity for any other (a
// memory filesystem has no links to follow).
func symlinkResolver(fs afero.Fs) func(string) (string, error) {
	if _, ok := fs.(*afero.OsFs); ok {
		return filepath.EvalSymlinks
	}
	return func(p string) (string, error) { return p, nil }
}

// ErrProjectTargetUnconfirmed is the CLI's project-directory refusal (R14):
// no --target was given, so the target defaults to the project directory,
// and --yes was not given. Internal callers pass their root explicitly and
// never see it.
var ErrProjectTargetUnconfirmed = errors.New("materialize: refusing to write the project directory without --target or --yes")

// MaterializeEngines is the engine set req resolves to (named, configured,
// or the explicit-target default), without delivering: what the CLI's
// project-directory warning names.
func MaterializeEngines(reg engine.Registry, cfg *config.Config, req MaterializeRequest) ([]string, error) {
	return materializeEngines(reg, cfg, req)
}
