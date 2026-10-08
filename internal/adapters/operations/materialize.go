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
	// Explicit says the user named Target: with no engine configured, an
	// explicit target is delivered for the registry's default engine and the
	// project-root default is not (R6).
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
	Force bool        `json:"force,omitempty"`
	Root  safefs.Root `json:"-"`
}

// MaterializeResult is what one materialize did, per engine.
type MaterializeResult struct {
	Target string `json:"target"`
	// Created says the target directory did not exist and was created.
	Created  bool            `json:"created,omitempty"`
	Profiles []string        `json:"profiles,omitempty"`
	Engines  []EngineOutcome `json:"engines"`
	// Status is applied | partial | failed | released | planned (a dry run)
	// | nothing (no engine to deliver for, or nothing to release).
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
	Skipped  []string `json:"skipped,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
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

// ErrMaterializeRequest refuses an incoherent request (a release with
// profiles or a destination, a destination on a kind other than context, an
// unknown mechanism, one kind given two specs).
var ErrMaterializeRequest = errors.New("materialize: invalid request")

// Materialize delivers the assembled profile set AT REST into req.Target as
// each engine's native files: the at-rest delivery the CLI's
// `ctxloom materialize` and the post-sync apply share. Per engine it
// assembles out of the loop (premised fragments become skills where skills
// are delivered and the engine exports them, and are inlined otherwise) and
// hands the package to the placement core at the target, under the
// engine's per-kind project writers, for exactly the selected kinds.
//
// A per-engine write failure is a fatal-class strictness finding and an
// error on the result (partial success is success); bad arguments, an
// unresolvable target and a failed assembly are hard errors.
func Materialize(ctx context.Context, reg engine.Registry, cfg *config.Config, req MaterializeRequest) (*MaterializeResult, error) {
	m, err := newMaterializeRun(reg, cfg, req)
	if err != nil {
		return nil, err
	}
	if len(m.names) == 0 {
		m.res.Status = MaterializeNothing
		m.res.Warnings = append(m.res.Warnings, "no engine is configured, so nothing was delivered; name one with --backend, or give --target to deliver for the default engine")
		return m.res, nil
	}
	if req.Release {
		return materializeRelease(ctx, reg, m.root, m.names, m.kinds, m.res)
	}
	for _, name := range m.names {
		out, err := materializeEngine(ctx, reg, cfg, m, name)
		if err != nil {
			return nil, err
		}
		m.res.Engines = append(m.res.Engines, out)
		m.res.Errors = append(m.res.Errors, outcomeErrors(out)...)
	}
	m.res.Status = materializeStatus(req, m.res)
	return m.res, nil
}

// materializeRun is one Materialize's resolved inputs.
type materializeRun struct {
	req         MaterializeRequest
	root        safefs.Root
	kinds       []present.Kind
	target      string
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
	m := &materializeRun{req: req, root: rootOf(req.Root), kinds: kinds}
	var created bool
	m.target, created, err = ResolveTarget(m.root.Fs, req.Target, !req.DryRun && !req.Release, symlinkResolver(m.root.Fs))
	if err != nil {
		return nil, err
	}
	if m.contextFile, err = ResolveContextFile(m.target, contextDest); err != nil {
		return nil, err
	}
	m.res = &MaterializeResult{Target: m.target, Created: created, Engines: []EngineOutcome{}, Profiles: materializeProfiles(cfg, req)}
	if m.names, err = materializeEngines(reg, cfg, req); err != nil {
		return nil, err
	}
	for _, name := range m.names {
		if err := checkHookTargetScopeOf(reg, name, m.target, req.Force); err != nil {
			return nil, err
		}
	}
	return m, nil
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
// registered; else the configured engines; else, for an explicit target
// only, the registry's default.
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
	if !req.Explicit {
		return nil, nil
	}
	def, err := reg.Default()
	if err != nil {
		return nil, err
	}
	return []string{string(def.Root().Name)}, nil
}

// materializeEngine assembles for one engine and delivers (or, in a dry
// run, plans) its package at the target.
func materializeEngine(ctx context.Context, reg engine.Registry, cfg *config.Config, m *materializeRun, name string) (EngineOutcome, error) {
	out := EngineOutcome{Engine: name}
	kind, ok := reg.Lookup(engine.Name(name))
	if !ok {
		return out, fmt.Errorf("materialize: no engine kind is composed for %s", name)
	}
	pkg, withheld, err := materializePackage(ctx, reg, cfg, m.req, name, m.kinds)
	if err != nil {
		return out, err
	}
	out.WithheldByPremise = withheld
	p := Placement{Start: present.ProjectOnHost(m.target), Family: delivery.ProjectWriterFor(engine.Name(name)), Kinds: m.kinds, ContextFile: m.contextFile}
	var plan delivery.Plan
	if m.req.DryRun {
		plan, err = planMaterialized(kind, pkg, p, &out)
		if err != nil {
			return out, err
		}
	} else {
		plan = deliverMaterializedAt(ctx, m.root, kind, pkg, p, &out)
	}
	for _, k := range m.kinds {
		if !slices.Contains(out.Wrote, k.String()) {
			out.Released = append(out.Released, k.String())
		}
	}
	if !cfg.ShouldSilenceUnsupported() {
		out.NotCarried = selectedLosses(notCarried(name, kind, pkg, plan), m.kinds)
	}
	return out, nil
}

// planMaterialized is a dry run's plan at p, its kinds recorded as what
// would be written.
func planMaterialized(kind engine.Engine, pkg composite.Package, p Placement, out *EngineOutcome) (delivery.Plan, error) {
	plan, err := delivery.PlanFor(kind.Root(), pkg.EngineItems(kind.Root().Name), p.Start.Paths(), nil, p.Kinds, false)
	if err != nil {
		return delivery.Plan{}, err
	}
	for _, it := range plan.Static {
		out.Wrote = append(out.Wrote, it.Kind.String())
	}
	return plan, nil
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
		p := Placement{Start: present.ProjectOnHost(res.Target), Family: delivery.ProjectWriterFor(engine.Name(name)), Kinds: kinds}
		if err := Release(ctx, root, kind, p); err != nil {
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
