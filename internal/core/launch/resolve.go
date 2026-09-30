package launch

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// Resolve is the ONE constructor. Refuses (typed) a zero identity, an agent
// that does not resolve, a label that names no composed engine, a mode the
// Definition lacks, a posture that cannot be honoured, an ownership
// mismatch and — for a container binding — an engine whose container
// story refuses (the cells adapter's refusals pass through untouched).
// Order: Select → Assemble → the engine and its mode → the axes → the
// permission floored ONCE → Cells.Prepare (the roots) → Exports → Route
// (over those roots) → the endpoint once per harp (bound on the session
// record) → the catalog index → Encode → carry by size → the Launch.
// Discard tears the cell down.
func Resolve(ctx context.Context, deps Deps, src Source) (Launch, error) {
	if err := validateIdentity(src.Identity); err != nil {
		return Launch{}, err
	}
	cfg := deps.Snapshot.Config

	sel, err := selectWithHome(cfg, src)
	if err != nil {
		return Launch{}, err
	}
	pkg, err := assembleSelection(ctx, deps, src, sel)
	if err != nil {
		return Launch{}, err
	}
	pkg = pkg.WithLead(src.Extra...)
	// The binding's delivery preference rides the package as written, so
	// the runner validates it against the engine it hosts.
	pkg.Selection.Preference = sel.surfaces

	label := firstNonEmpty(src.Label, sel.llm, pkg.Selection.LLM, cfg.PrimaryLabel())
	eng, labelCfg, labelPerm, err := resolveEngineMode(deps, cfg, src, label)
	if err != nil {
		return Launch{}, err
	}
	def := eng.Root()

	declared, axes, err := resolveAxes(cfg, src, sel)
	if err != nil {
		return Launch{}, err
	}
	perm, err := resolvePolicy(report.To(deps.Reporter), src, declsFor(sel, label, labelPerm, cfg), eng, axes.Runtime)
	if err != nil {
		return Launch{}, err
	}
	dirty, err := resolveDirtyTree(cfg, src)
	if err != nil {
		return Launch{}, err
	}

	passthrough, cell, err := prepareCell(ctx, deps, src, eng, axes, dirty, sel, label)
	if err != nil {
		return Launch{}, err
	}
	l := Launch{
		Identity:   src.Identity,
		Engine:     def.Name,
		Label:      labelCfg,
		Mode:       src.Mode,
		Permission: perm,
		Declared:   declared,
		Axes:       axes,
		Cell:       cell,
		Home:       cell.Home,
		Prompt:     src.Prompt,
		Env:        passthrough,
	}
	if err := deliverLaunch(ctx, deps, src, eng, pkg, sel.roots, &l); err != nil {
		_ = Discard(ctx, Launch{Cell: cell})
		return Launch{}, err
	}
	return l, nil
}

// validateIdentity refuses a zero or malformed identity, as ErrNoIdentity.
func validateIdentity(id sessions.Identity) error {
	if id.Harp == "" {
		return ErrNoIdentity
	}
	if err := id.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrNoIdentity, err)
	}
	return nil
}

// selectWithHome is Select with the home mode settled. A launch with no
// binding (a profile set, an internal one-shot, a degraded bare launch) has
// no engine_home to read: it gets the default, the session home. The real
// home is only ever a binding's explicit selection.
func selectWithHome(cfg *config.Config, src Source) (selection, error) {
	sel, err := selectSource(cfg, src)
	if err != nil {
		return selection{}, err
	}
	if sel.homeMode == "" {
		sel.homeMode = HomeModeSession
	}
	return sel, nil
}

// assembleSelection is Assemble for the selection. A selection with no
// profiles is context-free BY DECLARATION (an internal one-shot, a binding
// that composes nothing): nothing is assembled, so no default can be composed
// in its place. Naming a profile set and delivering none of it is never what
// the caller asked for: a set that assembled to nothing is a failed assembly,
// refused.
func assembleSelection(ctx context.Context, deps Deps, src Source, sel selection) (composite.Package, error) {
	var pkg composite.Package
	if len(sel.profiles) == 0 && len(sel.fragments) == 0 && len(sel.tags) == 0 {
		return pkg, nil
	}
	pkg, err := deps.Assembler.Assemble(ctx, deps.Snapshot, Selection{Profiles: sel.profiles, Fragments: sel.fragments, Tags: sel.tags, WorkDir: src.WorkDir})
	if err != nil {
		return composite.Package{}, err
	}
	if strings.TrimSpace(pkg.Context.Text) == "" {
		return composite.Package{}, fmt.Errorf("%w: profile set %v (check the profiles' fragments and bundles resolve, or drop the profile to run context-free)", ErrContextEmpty, sel.profiles)
	}
	return pkg, nil
}

// resolveEngineMode is the engine the label names, its label config with the
// source's model override and the engine's model aliases applied, and its
// permission — refused when the engine does not declare the source's mode.
func resolveEngineMode(deps Deps, cfg *config.Config, src Source, label string) (engine.Engine, engine.LabelConfig, agents.LabelPermissions, error) {
	eng, labelCfg, labelPerm, err := selectEngine(deps, cfg, label)
	if err != nil {
		return nil, engine.LabelConfig{}, agents.LabelPermissions{}, err
	}
	def := eng.Root()
	if src.Model != "" {
		labelCfg.Model = src.Model
	}
	if alias, ok := def.ModelAliases[labelCfg.Model]; ok {
		labelCfg.Model = alias
	}
	if !slices.Contains(def.Modes, src.Mode) {
		return nil, engine.LabelConfig{}, agents.LabelPermissions{}, fmt.Errorf("%w: %s declares %v, not %v", ErrModeUnsupported, def.Name, def.Modes, src.Mode)
	}
	return eng, labelCfg, labelPerm, nil
}

// prepareCell is Cells.Prepare for the launch, returning the engine
// passthrough env (the label's env overlaid by the source's) with the cell.
func prepareCell(ctx context.Context, deps Deps, src Source, eng engine.Engine, axes Axes, dirty DirtyTreeHandler, sel selection, label string) (map[string]string, Cell, error) {
	def := eng.Root()
	passthrough := map[string]string{}
	maps.Copy(passthrough, deps.Assembler.LabelEnv(deps.Snapshot, label))
	maps.Copy(passthrough, src.Env)
	env := sessions.HookEnv(src.Identity)
	maps.Copy(env, passthrough)
	cell, err := deps.Cells.Prepare(ctx, CellRequest{
		Axes:        axes,
		Engine:      eng,
		Identity:    src.Identity,
		ProjectRoot: src.WorkDir,
		SessionDir:  filepath.Join(deps.Host.CtxloomHome, paths.SessionsDir, src.Identity.Harp),
		DirtyTree:   dirty,
		Image:       ImageConfigFor(deps.Snapshot.Config, def.Name),
		Host:        deps.Host,
		Degraded:    src.Degraded,
		HomeMode:    sel.homeMode,
		Auth:        sel.auth,
		Env:         env,
	})
	if err != nil {
		return nil, Cell{}, err
	}
	return passthrough, cell, nil
}

// deliverLaunch fills l's delivery over its prepared cell: Exports, Route
// (over the cell's roots), the endpoint once per harp (bound on the session
// record), the catalog index, and the package encoded and carried by size.
// The caller discards the cell on error.
func deliverLaunch(ctx context.Context, deps Deps, src Source, eng engine.Engine, pkg composite.Package, roots map[string]string, l *Launch) error {
	def := eng.Root()
	exports, err := eng.Exports(pkg.EngineItems(def.Name))
	if err != nil {
		return fmt.Errorf("%s exports: %w", def.Name, err)
	}
	pref, err := preference(def, roots)
	if err != nil {
		return err
	}
	plan, err := delivery.Route(itemsOf(pkg, def.Name), def, pref, l.Cell.Paths.Paths())
	if err != nil {
		return err
	}
	ep, resume, err := endpoint(ctx, deps, src, l.Axes)
	if err != nil {
		return err
	}
	if err := deps.Sessions.BindEngine(src.Identity.Harp, string(def.Name)); err != nil {
		return err
	}
	index, err := deps.Assembler.Index(ctx, deps.Snapshot)
	if err != nil {
		return err
	}
	enc, err := composite.Encode(pkg)
	if err != nil {
		return err
	}
	carrier, err := carry(ctx, deps, enc)
	if err != nil {
		return err
	}
	l.Exports, l.Plan, l.MCP, l.Resume, l.Index, l.Package = exports, plan, ep, resume, index, carrier
	return nil
}

// selection is what Select decided from the config and the Source: the
// binding (if any) and the axes and postures it declared.
type selection struct {
	agent       string
	profiles    []string
	fragments   []string
	tags        []string
	llm         string
	runtime     string
	permissions agents.Permissions
	homeMode    HomeMode
	auth        string
	surfaces    map[string]string
	roots       map[string]string
}

// selectSource is Select: an agent binding by name (refused by name when
// absent), the default binding for a bare launch, or the caller's profile
// set. The binding is ungated config; nothing here touches trust.
func selectSource(cfg *config.Config, src Source) (selection, error) {
	switch {
	case src.Internal:
		return selection{auth: src.Auth}, nil
	case src.Agent != "":
		return bindingSelection(cfg, src.Agent, src.Degraded)
	case len(src.Profiles) == 0 && len(src.Fragments) == 0 && len(src.Tags) == 0:
		return bindingSelection(cfg, cfg.GetDefaultAgent(), src.Degraded)
	default:
		return selection{profiles: slices.Clone(src.Profiles), fragments: slices.Clone(src.Fragments), tags: slices.Clone(src.Tags)}, nil
	}
}

// bindingSelection reads one agent binding. A bare launch whose default
// agent is missing is refused with the remedy; under --degraded it launches
// context-free at the project defaults.
func bindingSelection(cfg *config.Config, name string, degraded bool) (selection, error) {
	binding, ok := cfg.Agent(name)
	if !ok {
		if degraded && name == cfg.GetDefaultAgent() {
			return selection{}, nil
		}
		return selection{}, fmt.Errorf("%w: %q (declare it with `ctxloom agent set %s`, or `ctxloom agent default <name>` for a bare launch)", ErrNoAgent, name, name)
	}
	home, err := parseHomeMode(binding.HomeMode)
	if err != nil {
		if !degraded {
			return selection{}, fmt.Errorf("agent %q: %w", name, err)
		}
		home = HomeModeSession
	}
	// The declared auth travels as written: the cells adapter checks it
	// against the engine it binds (engine.CheckAuth), the one check config
	// load and `agent create/edit` also run.
	auth := binding.Auth
	return selection{
		agent:       name,
		profiles:    slices.Clone(binding.Profiles),
		llm:         binding.LLM,
		runtime:     binding.Runtime,
		permissions: binding.Permissions,
		homeMode:    home,
		auth:        auth,
		surfaces:    maps.Clone(binding.Surfaces),
		roots:       maps.Clone(binding.Roots),
	}, nil
}

// parseHomeMode is the one conversion of the binding's `engine_home`
// spelling; empty is the session default, "host" is the unsafe selection
// of the real home, an unknown spelling is refused.
func parseHomeMode(s string) (HomeMode, error) {
	switch HomeMode(strings.TrimSpace(s)) {
	case "", HomeModeSession:
		return HomeModeSession, nil
	case HomeModeHost:
		return HomeModeHost, nil
	default:
		return "", fmt.Errorf("unknown engine_home %q (known: %s|%s)", s, HomeModeHost, HomeModeSession)
	}
}

// selectEngine maps the label to a composed engine: a configured entry's
// type, or a bare registered name (the ad-hoc `--llm <engine>` form). A label
// that names neither is refused by name, listing what would have resolved;
// an empty label is the registry's default engine. The third result is the
// label's declared permissions block, one rung of the policy.
func selectEngine(deps Deps, cfg *config.Config, label string) (engine.Engine, engine.LabelConfig, agents.LabelPermissions, error) {
	if label == "" {
		eng, err := deps.Engines.Default()
		if err != nil {
			return nil, engine.LabelConfig{}, agents.LabelPermissions{}, fmt.Errorf("%w: %v", ErrNoEngine, err)
		}
		return eng, engine.LabelConfig{}, agents.LabelPermissions{}, nil
	}
	entry, configured := cfg.GetLLMEntry(label)
	name := engine.Name(label)
	var model string
	if configured {
		backend, m := cfg.ResolveLLM(label)
		name, model = engine.Name(backend), m
	}
	eng, ok := deps.Engines.Lookup(name)
	if !ok {
		known := slices.Sorted(maps.Keys(cfg.GetLMConfig().Configs))
		return nil, engine.LabelConfig{}, agents.LabelPermissions{}, fmt.Errorf("%w: %q (configured labels: %s; engines: %v)", ErrNoEngine, label, strings.Join(known, ", "), deps.Engines.Names(nil))
	}
	return eng, engine.LabelConfig{Label: label, Model: model, Body: entry.Body}, entry.Permissions, nil
}

// resolveAxes settles the two isolation axes and keeps what was declared
// apart from what they settled to: the workspace is a SESSION trait (the
// invocation, else the project default); the runtime is the binding's,
// else the project default. A project default is not a declaration — it
// fills the settled pair, never the declared one. Each string is parsed
// exactly once; silence on either axis is the host and the shared checkout.
func resolveAxes(cfg *config.Config, src Source, sel selection) (declared, settled Axes, err error) {
	if declared.Workspace, err = ParseWorkspaceAxis(string(src.Workspace)); err != nil {
		return Axes{}, Axes{}, err
	}
	if declared.Runtime, err = ParseRuntimeAxis(sel.runtime); err != nil {
		return Axes{}, Axes{}, err
	}
	settled = declared
	if settled.Workspace == "" {
		if settled.Workspace, err = ParseWorkspaceAxis(cfg.GetWorkspace()); err != nil {
			return Axes{}, Axes{}, err
		}
	}
	if settled.Runtime == "" {
		if settled.Runtime, err = ParseRuntimeAxis(cfg.GetRuntime()); err != nil {
			return Axes{}, Axes{}, err
		}
	}
	if settled.Workspace == "" {
		settled.Workspace = WorkspaceNone
	}
	if settled.Runtime == "" {
		settled.Runtime = RuntimeHost
	}
	return declared, settled, nil
}

// resolveDirtyTree settles what a worktree cell does about a dirty parent
// tree, ONCE: the invocation's handler, else the project default, else the
// built-in. The request arrives typed (its edge parsed it); the project
// default is parsed here and an unusable spelling REFUSES rather than
// falling through — the built-in is the member that auto-commits the user's
// branch, and reaching it through a spelling nobody recognised routes
// around the consent that handler is gated on.
func resolveDirtyTree(cfg *config.Config, src Source) (DirtyTreeHandler, error) {
	if src.DirtyTree != "" {
		return src.DirtyTree, nil
	}
	handler, err := ParseDirtyTreeHandler(cfg.GetDirtyTreeHandler())
	if err != nil {
		return "", fmt.Errorf("this project's dirty_tree_handler default is unusable: %w — fix `dirty_tree_handler:` in .ctxloom/config.yaml, or pass a valid one on this call", err)
	}
	if handler == "" {
		return DirtyTreeHandlerCommit, nil
	}
	return handler, nil
}

// ImageConfigFor is the user's container-image configuration for the
// engine's isolated runs, read off the generation: the per-engine prebuilt
// image override, the base Containerfile local builds layer the agent stage
// onto, the project root devcontainer auto-detection resolves against with
// its opt-out and service pick, and the composable engine set. Resolve reads
// it for the cell; the container commands read it to build ahead of a run.
func ImageConfigFor(cfg *config.Config, eng engine.Name) ImageConfig {
	return ImageConfig{
		Image:               cfg.IsolationImageFor(string(eng)),
		BaseContainerfile:   cfg.IsolationBaseContainerfilePath(),
		AppRoot:             cfg.GetAppRoot(),
		NoDevcontainerBase:  !cfg.IsolationDevcontainerBaseEnabled(),
		DevcontainerService: cfg.GetIsolationDevcontainerService(),
		Engines:             cfg.GetIsolationEngines(),
	}
}

// itemsOf is the engine-facing projection of the package: the assembled
// context as one unconditional fragment ahead of the package's own
// projection. The engine's Delegate decides over it; nothing here decides.
func itemsOf(pkg composite.Package, name engine.Name) engine.Items {
	items := pkg.EngineItems(name)
	if pkg.Context.Text != "" {
		items.Fragments = append([]engine.FragmentItem{{Ref: "context", Body: []byte(pkg.Context.Text)}}, items.Fragments...)
	}
	return items
}

// carry is the size conditional: measure, then Inline at or under InlineMax,
// ClaimCheck above it. The consumer never learns which answered. A launch
// whose package needs a claim check its caller did not compose is refused,
// never carried inline past the ceiling the frame is bounded by.
func carry(ctx context.Context, deps Deps, enc composite.Encoded) (composite.Carrier, error) {
	if len(enc.Bytes) <= deps.InlineMax {
		return deps.Inline.Carry(ctx, enc)
	}
	if deps.ClaimCheck == nil {
		return composite.Carrier{}, fmt.Errorf("%w: %d bytes over %d", ErrNoClaimCheck, len(enc.Bytes), deps.InlineMax)
	}
	return deps.ClaimCheck.Carry(ctx, enc)
}

// preference is the binding's delivery preference as Route reads it: the
// root the binding selects per kind (validated when the binding was
// written; a label that no longer parses is refused here by name), and the
// losses it accepts. No binding records a loss acceptance yet, so every
// kind the Definition does not carry is accepted: a run that delivers the
// rest is better than none until a binding can say otherwise.
func preference(def engine.Base, roots map[string]string) (delivery.Preference, error) {
	pref := delivery.Preference{Root: map[present.Kind]present.RootKind{}, AcceptLoss: map[present.Kind]bool{}}
	for name, label := range roots {
		k, ok := present.ParseKind(name)
		if !ok {
			return delivery.Preference{}, fmt.Errorf("%w: the binding selects a root for %q, which is not a surface kind", ErrBindingRoots, name)
		}
		r, ok := present.ParseRootKind(label)
		if !ok {
			return delivery.Preference{}, fmt.Errorf("%w: the binding selects root %q for %s, which is not a root", ErrBindingRoots, label, name)
		}
		pref.Root[k] = r
	}
	for _, k := range []present.Kind{present.Context, present.MCP, present.Settings, present.Hooks, present.Commands, present.Skills} {
		if !def.Carries(k) {
			pref.AcceptLoss[k] = true
		}
	}
	return pref, nil
}

// endpoint mints the session's MCP endpoint once per harp. A resume of the
// same harp reuses the bound endpoint unless it asks for a rebind; the
// result is bound on the session record either way. The resume ref rides
// through: the caller's native key, else the one the record holds.
func endpoint(ctx context.Context, deps Deps, src Source, axes Axes) (sessions.Endpoint, sessions.ResumeRef, error) {
	var resume sessions.ResumeRef
	if src.Resume.Ref.Harp != "" {
		resume = src.Resume.Ref
		entry, err := deps.Sessions.Find(src.Resume.Ref.Harp)
		if err != nil {
			return sessions.Endpoint{}, resume, err
		}
		if entry != nil {
			if resume.NativeKey == "" {
				resume.NativeKey = entry.SessionID
			}
			if !src.Resume.RebindEndpoint && entry.MCP.URL != "" {
				return entry.MCP, resume, nil
			}
		}
	}
	ep, err := mintEndpoint(ctx, deps, src.Identity, axes)
	return ep, resume, err
}

// mintEndpoint mints a fresh session endpoint and binds it on the session
// record, so a later resume of the harp finds the address the runner serves.
func mintEndpoint(ctx context.Context, deps Deps, id sessions.Identity, axes Axes) (sessions.Endpoint, error) {
	ep, err := deps.Endpoints.MintMCP(ctx, id, axes)
	if err != nil {
		return sessions.Endpoint{}, err
	}
	if err := deps.Sessions.BindMCP(id.Harp, ep); err != nil {
		return sessions.Endpoint{}, err
	}
	return ep, nil
}

// RebindEndpoint is l with a NEWLY minted session endpoint, bound on the
// session record in place of the one l carried. It answers the runner's
// delivery.ErrEndpointUnavailable for a launch its caller already holds: a
// minted address is only reserved until the mint returns, so another process
// can take the port before the runner binds it. The endpoint is the only
// part of a launch that names the address (the runner renders the engine's
// MCP config from it), so nothing else is re-resolved.
func RebindEndpoint(ctx context.Context, deps Deps, l Launch) (Launch, error) {
	ep, err := mintEndpoint(ctx, deps, l.Identity, l.Axes)
	if err != nil {
		return Launch{}, err
	}
	l.MCP = ep
	return l, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
