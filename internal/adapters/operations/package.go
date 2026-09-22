package operations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/operations/managedhooks"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/resources"
)

// ONE assembly for every consumer. AssemblePackage resolves what the config
// holds — the profile set, the trust, the catalog, the surfaces the
// config-level resolvers compose — into composite's
// inputs, calls composite.Assemble ONCE, and voices its findings. Every
// consumer of an assembled package (a run's context and managed surfaces,
// the MCP assemble tool, the SessionStart context file, `profile
// materialize`, the skill-mates hook) reads the one Package; none assembles
// for itself.

// PackageRequest names what a package is assembled for.
type PackageRequest struct {
	// Profiles is the profile set. Empty with no explicit arm means the
	// default agent's composed profiles, resolved fault-tolerantly; an
	// explicit set's resolution failures are hard errors.
	Profiles []string
	// Fragments and Tags are the caller's explicit arm.
	Fragments []string
	Tags      []string
	// Consumer states what the bytes are for (a live session or a
	// materialized surface); it decides whether premised fragments are held
	// back or written.
	Consumer ContextConsumer
	// WorkDir is the project root the managed hooks are composed for.
	WorkDir string
	// Pipeline is the injected-stage seam: a pre-built process stage in
	// place of the generation's gated one (tests).
	Pipeline *bundles.Pipeline
	// ProfileLoaderFunc is the profile-resolution seam (tests).
	ProfileLoaderFunc func() ProfileLoader
}

// AssemblePackage is the one place a Package is assembled from a config.
func AssemblePackage(ctx context.Context, cfg *config.Config, req PackageRequest) (composite.Package, error) {
	static, err := req.Consumer.static()
	if err != nil {
		return composite.Package{}, fmt.Errorf("resolve who consumes the assembled context: %w", err)
	}
	profileNames := packageProfileNames(cfg, req)
	fromDefaults := len(req.Profiles) == 0

	var (
		tr   composite.Trust
		gate bundles.Authorizer
		cat  bundles.Catalog
		opts = composite.Options{DropWithheld: true, Static: static}
	)
	var versions bundles.BundleVersionResolver
	if req.Pipeline != nil {
		// An injected stage carries its own gate, links, form and versions.
		opts.Pipeline = req.Pipeline
		opts.PreferDistilled = req.Pipeline.PreferDistilled()
		gate = req.Pipeline.Authorizer()
		tr = composite.Gated(gate)
		cat = req.Pipeline.Loader().Catalog()
		versions = req.Pipeline.Loader().VersionResolver()
	} else {
		// A generation with no gate cannot deliver: refused at entry, by
		// sentinel.
		tr, err = cfg.RequireTrust()
		if err != nil {
			return composite.Package{}, fmt.Errorf("assemble context: %w", err)
		}
		gate = tr.Authorizer()
		cat = cfg.Catalog()
		opts.PreferDistilled = cfgPreferDistilled(cfg)
		versions = cfg.VersionResolver()
		opts.Versions = versions
	}

	resolved, err := resolveProfiles(cfg, profileNames, fromDefaults, req.ProfileLoaderFunc)
	if err != nil {
		return composite.Package{}, err
	}
	if req.Pipeline == nil {
		// The run's granted MCP set decides the link groups: a linked
		// fragment or skill is delivered exactly when its server is. Both
		// resolvers take the set THIS assembly resolved, so a profile that
		// did not resolve is reported once, here.
		opts.MCP = cfg.ResolveBundleMCPServersFor(resolved)
		opts.Hooks = *managedhooks.AssembleFor(terminalReporter(), cfg, req.WorkDir, "", resolved).Wire()
		opts.Statusline = managedStatuslineEnabled(cfg)
	}
	sel, err := composite.Select(resolved, cat, composite.SelectRequest{Fragments: req.Fragments, Tags: req.Tags, Versions: versions})
	if err != nil {
		return composite.Package{}, err
	}
	opts.DenyTools = sel.DenyTools
	// ctxloom's embedded commands are always present. Companion loadout
	// fragments need no input here: Assemble reads them off the catalog
	// (composite assembly's companionAsks) through the same gate as every
	// selected fragment.
	opts.Commands = builtinCommands()

	pkg, err := composite.Assemble(ctx, cat, sel, tr, opts)
	if err != nil {
		return composite.Package{}, err
	}
	voiceFindings(pkg)
	// Surface (content-free) any items the trust gate withheld during this
	// assembly so the user knows content was hidden, WHY, and how to review
	// it; and name any SELECTED profile the gate emptied out completely.
	warnWithheld(gate)
	warnGuttedProfiles(sel.Declared, pkg.Loaded, gate)
	return pkg, nil
}

// packageProfileNames picks the profiles to assemble from: the explicit set,
// else (when nothing at all is selected) the default agent's composed
// profiles. When no default agent is configured, assembly degrades to an
// empty context. No synthetic profile is ever created.
func packageProfileNames(cfg *config.Config, req PackageRequest) []string {
	if len(req.Profiles) > 0 {
		return req.Profiles
	}
	if len(req.Fragments) == 0 && len(req.Tags) == 0 {
		return cfg.DefaultAgentProfiles()
	}
	return nil
}

// resolveProfiles loads each profile (with inheritance). Profiles picked up
// from configured defaults degrade per fault-tolerance: a default that
// fails to resolve is fatal-class in strict mode (the default IS an
// explicit ask, just a persisted one) and skipped so degraded mode still
// assembles what is left. An explicit set is the user's ask, so its
// failures stay hard errors. A later profile that disagrees about the
// engine label is noted; the first non-empty wins.
func resolveProfiles(cfg *config.Config, names []string, fromDefaults bool, loaderFunc func() ProfileLoader) ([]profiles.ResolvedProfile, error) {
	var pLoader ProfileLoader
	if loaderFunc != nil {
		pLoader = loaderFunc()
	} else {
		pLoader = cfg.GetProfileLoader()
	}
	out := make([]profiles.ResolvedProfile, 0, len(names))
	effectiveLLM := ""
	for _, name := range names {
		resolved, err := pLoader.ResolveProfile(name, nil)
		if err != nil {
			if fromDefaults {
				strictness.Fail(strictness.ClassRef, "fix the default agent's profiles in .ctxloom/config.yaml (agents.<name>.profiles), or install the missing content (ctxloom deps pull)",
					"skipping default profile %s: %v", name, err)
				continue
			}
			return nil, fmt.Errorf("failed to resolve profile %s: profile %s: %w", name, name, err)
		}
		if resolved.LLM != "" {
			if effectiveLLM == "" {
				effectiveLLM = resolved.LLM
			} else if resolved.LLM != effectiveLLM {
				clidiag.Warn("ctxloom", "profile %q declares llm %q but %q is already in effect; keeping %q",
					name, resolved.LLM, effectiveLLM, effectiveLLM)
			}
		}
		p := *resolved
		p.Name = name
		out = append(out, p)
	}
	return out, nil
}

// builtinCommands are ctxloom's own slash commands, embedded in the binary
// and always present; the frontmatter description is the authored help
// text. An embedded file that fails to read is warned about and skipped —
// a dropped builtin would otherwise vanish from the next materialize with
// no signal at all — never blocking a launch over one file.
func builtinCommands() []composite.Command {
	names, err := resources.ListBuiltinCommands()
	if err != nil {
		clidiag.Warn("ctxloom", "builtin commands unavailable: %v", err)
		return nil
	}
	var out []composite.Command
	for _, name := range names {
		content, err := resources.GetBuiltinCommand(name)
		if err != nil {
			clidiag.Warn("ctxloom", "builtin command %q unavailable: %v", name, err)
			continue
		}
		description, body := resources.SplitCommandFrontmatter(string(content))
		out = append(out, composite.Command{Name: name, Description: description, Body: body})
	}
	return out
}

// voiceFindings says what the assembly found, content-free, through the
// channels each finding always used: an unresolvable ref is fatal-class in
// strict mode (the startup choke owner aborts on the finding) and a warning
// in degraded mode; a pinned version that failed says so; an undefined
// variable and a collapsed duplicate warn once per process, because the
// assembly runs once per turn and an unchanged fragment would otherwise
// re-warn every time.
func voiceFindings(pkg composite.Package) {
	for _, f := range pkg.Findings {
		switch f.Kind {
		case composite.FindingLoadFailed:
			if f.Version != "" {
				strictness.Fail(strictness.ClassRef, "fix the pinned version in the referencing profile, or ctxloom deps pull",
					"withholding %s@%s: %s", f.Ref, f.Version, f.Message)
				continue
			}
			strictness.Fail(strictness.ClassRef, "fix the fragment ref in the referencing profile, or install its bundle (ctxloom deps pull)",
				"fragment %s failed to load (%s); skipping", f.Ref, f.Message)
		case composite.FindingSubstitution:
			clidiag.WarnOnce("ctxloom", "%s (fragment %q)", f.Message, f.Ref)
		case composite.FindingDuplicate:
			ingestWarn("%s", f.Message)
		case composite.FindingCuratedSkipped:
			clidiag.Warn("ctxloom", "skipping curated item %q: %s", f.Ref, f.Message)
		}
	}
}

// ingestWarn is the sink for the collapsed-duplicate diagnostic: a variable
// so a test can observe it deterministically (WarnOnce dedups on the
// formatted line for the whole PROCESS).
var ingestWarn = func(format string, args ...any) {
	clidiag.WarnOnce("ctxloom", format, args...)
}

// managedStatuslineEnabled reports whether ctxloom manages the HUD
// statusline, via the config accessor (ShouldManageStatusline has a pointer
// receiver, so it needs an addressable copy).
func managedStatuslineEnabled(cfg *config.Config) bool {
	settings := cfg.GetSettings()
	return settings.ShouldManageStatusline()
}

// ManagedConfigOf projects a package onto the managed surfaces for one
// engine: its command and skill exports as that engine decides them from
// its own blocks (Engine.Exports over EngineItems), the hooks, the servers,
// the deny list and the statusline. An engine nobody registered, or a block
// its schema refuses, is an error naming it.
func ManagedConfigOf(pkg composite.Package, engineName string) (*agent.ManagedConfig, error) {
	exports, err := ExportsFor(pkg, engineName)
	if err != nil {
		return nil, err
	}
	return agent.ManagedConfigFor(ManagedSurfacesOf(pkg), exports), nil
}

// ManagedSurfacesOf is the package's surfaces as the writers' payload names
// them.
func ManagedSurfacesOf(pkg composite.Package) agent.ManagedSurfaces {
	return agent.ManagedSurfaces{Hooks: pkg.Hooks, MCP: pkg.MCP, DenyTools: pkg.DenyTools, Statusline: pkg.Statusline}
}

// ExportsFor is what the named engine says about the package: its own
// Exports over the engine-facing projection of the package.
func ExportsFor(pkg composite.Package, engineName string) (engine.Exports, error) {
	eng, ok := engines.Registry().Lookup(engine.Name(engineName))
	if !ok {
		return engine.Exports{}, fmt.Errorf("unknown backend %q", engineName)
	}
	exports, err := eng.Exports(pkg.EngineItems(engine.Name(engineName)))
	if err != nil {
		return engine.Exports{}, fmt.Errorf("%s exports: %w", engineName, err)
	}
	return exports, nil
}

// CommandExportsOf and SkillExportsOf are the writers' shapes of the
// engine's exports, written once in core/agent beside the payload they fill.
var (
	CommandExportsOf = agent.CommandExportsOf
	SkillExportsOf   = agent.SkillExportsOf
)

// LoadedSkills is the package's skills in the loaded shape, for the
// surfaces that read a skill's link tags beside its name.
func LoadedSkills(pkg composite.Package) []*bundles.LoadedSkill {
	out := make([]*bundles.LoadedSkill, 0, len(pkg.Skills))
	for _, s := range pkg.Skills {
		files := make([]bundles.LoadedSkillFile, 0, len(s.Value.Files))
		for _, f := range s.Value.Files {
			files = append(files, bundles.LoadedSkillFile{RelPath: f.Path, Content: f.Bytes, Mode: f.Mode})
		}
		out = append(out, &bundles.LoadedSkill{
			Name:        s.Value.Name,
			Bundle:      s.Value.Bundle,
			Item:        s.Value.Item,
			Tags:        s.Value.Tags,
			Frontmatter: bundles.SkillFrontmatter{Name: s.Value.Name, Description: s.Value.Description},
			Files:       files,
			Exports:     engineBlocks(s.Value.Exports),
			Curated:     s.Value.Curated,
			TrustRef:    s.Ref,
			Signer:      s.Signer,
		})
	}
	return out
}

func engineBlocks(m map[string][]byte) bundles.EngineBlocks {
	if len(m) == 0 {
		return nil
	}
	out := make(bundles.EngineBlocks, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// guttedProfiles names the selected profiles that declared fragments but
// contributed NONE of them to the assembled context, in name order. A
// profile that declared nothing was never going to contribute and is not
// "gutted"; a partially-loaded one still carries some of its role.
func guttedProfiles(declared map[string][]string, loaded []string) []string {
	if len(declared) == 0 {
		return nil
	}
	loadedSet := make(map[string]bool, len(loaded))
	for _, n := range loaded {
		loadedSet[n] = true
	}
	names := make([]string, 0, len(declared))
	for p := range declared {
		names = append(names, p)
	}
	sort.Strings(names)

	var out []string
	for _, p := range names {
		refs := declared[p]
		if len(refs) == 0 {
			continue
		}
		contributed := false
		for _, r := range refs {
			if loadedSet[r] {
				contributed = true
				break
			}
		}
		if !contributed {
			out = append(out, p)
		}
	}
	return out
}

// warnGuttedProfiles surfaces a profile the user explicitly SELECTED whose
// content the trust gate withheld in full: assembly then produces a stub
// and exits 0 — the agent still answers, with its whole role missing — so
// the profile is named. It stays a WARNING rather than a startup fault:
// trust withholding is never a startup fault.
func warnGuttedProfiles(declared map[string][]string, loaded []string, gate bundles.Authorizer) {
	warnGuttedProfilesTo(os.Stderr, declared, loaded, gate)
}

// warnGuttedProfilesTo is warnGuttedProfiles with the sink injected.
func warnGuttedProfilesTo(w io.Writer, declared map[string][]string, loaded []string, gate bundles.Authorizer) {
	var withheld []string
	for _, it := range composite.WithheldBy(gate) {
		withheld = append(withheld, it.Ref)
	}
	if len(withheld) == 0 {
		// The profile may be empty for reasons that are not the gate's doing
		// (an empty bundle, an exclusion filter). Only speak to withholding.
		return
	}
	for _, p := range guttedProfiles(declared, loaded) {
		clidiag.Fwarn(w, "ctxloom",
			"profile %q contributed NO content to this context: every fragment it declares was withheld. "+
				"Withheld item(s): %s — run 'ctxloom review' to see and accept them",
			p, strings.Join(withheld, ", "))
	}
}

// errNoFragments is the refusal for an explicit selection that matched
// nothing.
var errNoFragments = errors.New("no fragments loaded")
