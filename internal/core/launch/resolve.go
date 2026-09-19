package launch

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// Resolve is the ONE constructor. Refuses (typed) a zero identity, an agent
// that does not resolve, a label that names no composed engine, a mode the
// Definition lacks, a posture that cannot be honoured, an ownership
// mismatch and — for a container binding — an engine whose container
// story refuses (the cells adapter's refusals pass through untouched).
// Order: Select → Assemble → the engine and its mode → the axes → the
// permission floored ONCE → the surfaces → Cells.Prepare (the roots) →
// Route (over those roots) → the endpoint once per harp (bound on the
// session record) → the Launch. Discard tears the cell down.
func Resolve(ctx context.Context, deps Deps, src Source) (Launch, error) {
	if src.Identity.Harp == "" {
		return Launch{}, ErrNoIdentity
	}
	if err := src.Identity.Validate(); err != nil {
		return Launch{}, fmt.Errorf("%w: %v", ErrNoIdentity, err)
	}
	cfg := deps.Snapshot.Config

	sel, err := selectSource(cfg, src)
	if err != nil {
		return Launch{}, err
	}
	// A selection with no profiles is context-free BY DECLARATION (an
	// internal one-shot, a binding that composes nothing): nothing is
	// assembled, so no default can be composed in its place. Naming a
	// profile set and delivering none of it is never what the caller asked
	// for: a set that assembled to nothing is a failed assembly, refused.
	var asm Assembled
	if len(sel.profiles) > 0 || len(sel.fragments) > 0 || len(sel.tags) > 0 {
		if asm, err = deps.Assembler.Assemble(ctx, deps.Snapshot, Selection{Profiles: sel.profiles, Fragments: sel.fragments, Tags: sel.tags}); err != nil {
			return Launch{}, err
		}
		if strings.TrimSpace(asm.Context) == "" {
			return Launch{}, fmt.Errorf("%w: profile set %v (check the profiles' fragments and bundles resolve, or drop the profile to run context-free)", ErrContextEmpty, sel.profiles)
		}
	}

	label := firstNonEmpty(src.Label, sel.llm, asm.ProfileLLM, cfg.PrimaryLabel())
	eng, labelCfg, labelPerm, err := selectEngine(deps, cfg, label)
	if err != nil {
		return Launch{}, err
	}
	def := eng.Root()
	if src.Model != "" {
		labelCfg.Model = src.Model
	}
	if alias, ok := def.ModelAliases[labelCfg.Model]; ok {
		labelCfg.Model = alias
	}
	if !slices.Contains(def.Modes, src.Mode) {
		return Launch{}, fmt.Errorf("%w: %s declares %v, not %v", ErrModeUnsupported, def.Name, def.Modes, src.Mode)
	}

	axes, err := resolveAxes(cfg, src, sel)
	if err != nil {
		return Launch{}, err
	}
	perm, err := floorPermission(src, sel, labelPerm, cfg, def.Permissions)
	if err != nil {
		return Launch{}, err
	}
	managed, err := deps.Assembler.Surfaces(ctx, deps.Snapshot, def.Name, src.WorkDir, asm.Profiles, sel.surfaces)
	if err != nil {
		return Launch{}, err
	}
	dirty, err := ParseDirtyTreeHandler(firstNonEmpty(string(src.DirtyTree), cfg.GetDirtyTreeHandler()))
	if err != nil {
		return Launch{}, err
	}

	env := sessions.HookEnv(src.Identity)
	maps.Copy(env, src.Env)
	cell, err := deps.Cells.Prepare(ctx, CellRequest{
		Axes:        axes,
		Engine:      eng,
		Identity:    src.Identity,
		ProjectRoot: src.WorkDir,
		SessionDir:  filepath.Join(deps.Host.CtxloomHome, paths.SessionsDir, src.Identity.Harp),
		DirtyTree:   dirty,
		Image:       ImageConfigFor(cfg, def.Name),
		Host:        deps.Host,
		Degraded:    src.Degraded,
		HomeMode:    sel.homeMode,
		Env:         env,
	})
	if err != nil {
		return Launch{}, err
	}
	pkg := Package{Context: asm.Context, Managed: managed, Profiles: asm.Profiles, Fragments: asm.Fragments}
	plan, err := delivery.Route(itemsOf(pkg), def, preference(def), cell.Paths.Paths())
	if err != nil {
		_ = Discard(ctx, Launch{Cell: cell})
		return Launch{}, err
	}
	ep, resume, err := endpoint(ctx, deps, src, axes)
	if err != nil {
		_ = Discard(ctx, Launch{Cell: cell})
		return Launch{}, err
	}
	if err := deps.Sessions.BindEngine(src.Identity.Harp, string(def.Name)); err != nil {
		_ = Discard(ctx, Launch{Cell: cell})
		return Launch{}, err
	}
	return Launch{
		Identity:   src.Identity,
		Engine:     def.Name,
		Label:      labelCfg,
		Mode:       src.Mode,
		Permission: perm,
		Axes:       axes,
		Cell:       cell,
		Home:       cell.Home,
		Package:    pkg,
		Plan:       plan,
		MCP:        ep,
		Prompt:     src.Prompt,
		Resume:     resume,
		Env:        src.Env,
	}, nil
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
	permissions string
	homeMode    HomeMode
	surfaces    map[string]string
}

// selectSource is Select: an agent binding by name (refused by name when
// absent), the default binding for a bare launch, or the caller's profile
// set. The binding is ungated config; nothing here touches trust.
func selectSource(cfg *config.Config, src Source) (selection, error) {
	switch {
	case src.Internal:
		return selection{}, nil
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
		home = HomeModeHost
	}
	return selection{
		agent:       name,
		profiles:    slices.Clone(binding.Profiles),
		llm:         binding.LLM,
		runtime:     binding.Runtime,
		permissions: binding.Permissions,
		homeMode:    home,
		surfaces:    maps.Clone(binding.Surfaces),
	}, nil
}

// parseHomeMode is the one conversion of the binding's `engine_home`
// spelling; empty is the host default, an unknown spelling is refused.
func parseHomeMode(s string) (HomeMode, error) {
	switch HomeMode(strings.TrimSpace(s)) {
	case "", HomeModeHost:
		return HomeModeHost, nil
	case HomeModeSession:
		return HomeModeSession, nil
	default:
		return "", fmt.Errorf("unknown engine_home %q (known: %s|%s)", s, HomeModeHost, HomeModeSession)
	}
}

// selectEngine maps the label to a composed engine: a configured entry's
// type, or a bare registered name (the ad-hoc `--llm <engine>` form). A label
// that names neither is refused by name, listing what would have resolved;
// an empty label is the registry's default engine. The third result is the
// label's declared permission posture, one rung of the floor.
func selectEngine(deps Deps, cfg *config.Config, label string) (engine.Engine, engine.LabelConfig, string, error) {
	if label == "" {
		eng, err := deps.Engines.Default()
		if err != nil {
			return nil, engine.LabelConfig{}, "", fmt.Errorf("%w: %v", ErrNoEngine, err)
		}
		return eng, engine.LabelConfig{}, "", nil
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
		return nil, engine.LabelConfig{}, "", fmt.Errorf("%w: %q (configured labels: %s; engines: %v)", ErrNoEngine, label, strings.Join(known, ", "), deps.Engines.Names(nil))
	}
	return eng, engine.LabelConfig{Label: label, Model: model}, entry.Permissions, nil
}

// resolveAxes settles the two isolation axes: the workspace is a SESSION
// trait (the invocation, else the project default); the runtime is the
// binding's, else the project default. Each string is parsed exactly once;
// silence on either axis is the host and the shared checkout.
func resolveAxes(cfg *config.Config, src Source, sel selection) (Axes, error) {
	ws, err := ParseWorkspaceAxis(firstNonEmpty(string(src.Workspace), cfg.GetWorkspace()))
	if err != nil {
		return Axes{}, err
	}
	rt, err := ParseRuntimeAxis(firstNonEmpty(sel.runtime, cfg.GetRuntime()))
	if err != nil {
		return Axes{}, err
	}
	if ws == "" {
		ws = WorkspaceNone
	}
	if rt == "" {
		rt = RuntimeHost
	}
	return Axes{Workspace: ws, Runtime: rt}, nil
}

// floorPermission is THE floor, applied once. The first DECLARED source
// wins — the flag, the binding, the label, the project default — else the
// engine's declared host default; a declaration that does not parse is
// refused (--degraded narrows it to PermissionFloor, never widens). plan
// collapses to default on an engine with no read-only tier. A Structured
// run that would block on a prompt has no human at the engine: the
// originator's own run (depth 0) is widened to bypass — the human invoked it
// and owns the terminal — while a delegated child (depth > 0) is REFUSED
// rather than widened, because nothing answers its engine and elevating a
// posture nobody chose is worse than not launching; --degraded launches the
// child at PermissionFloor.
func floorPermission(src Source, sel selection, labelPerm string, cfg *config.Config, facts engine.PermissionFacts) (engine.PermissionMode, error) {
	flag := ""
	if src.Permission != engine.PermissionNotRequested {
		flag = src.Permission.String()
	}
	mode := facts.HostDefault
	if mode == engine.PermissionNotRequested {
		mode = engine.PermissionDefault
	}
	for _, s := range []string{flag, sel.permissions, labelPerm, cfg.GetPermissions()} {
		if strings.TrimSpace(s) == "" {
			continue
		}
		m, ok := engine.ParsePermissionMode(s)
		if !ok {
			if src.Degraded {
				return engine.PermissionFloor, nil
			}
			return 0, fmt.Errorf("%w: %q is not a posture (known: %s)", ErrPermissionUnhonoured, s, strings.Join(engine.PermissionModeNames(), "|"))
		}
		mode = m
		break
	}
	mode = mode.CollapsePlanIfUnenforced(facts.ReadOnlyPlan)
	if src.Mode == engine.Structured && !mode.SafeHeadless() {
		if !src.Identity.IsChild() {
			return engine.PermissionBypass, nil
		}
		if src.Degraded {
			return engine.PermissionFloor, nil
		}
		return 0, fmt.Errorf("%w: a delegated run has no human to answer an engine prompt and %q would block on one; declare permissions: %s|%s on agent %q",
			ErrPermissionUnhonoured, mode, engine.PermissionPlan, engine.PermissionBypass, sel.agent)
	}
	return mode, nil
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

// itemsOf is the engine-facing projection of today's package: the context
// as one unconditional fragment, plus the managed payload's own projection.
// The engine's Delegate decides over it; nothing here decides.
func itemsOf(pkg Package) engine.Items {
	var items engine.Items
	if pkg.Managed != nil {
		items = pkg.Managed.Items()
	}
	if pkg.Context != "" {
		items.Fragments = append([]engine.FragmentItem{{Ref: "context", Body: []byte(pkg.Context)}}, items.Fragments...)
	}
	return items
}

// preference is the binding's delivery preference as Route reads it. No
// binding records a root selection yet. Every kind's loss is accepted
// because the Definition's typed approaches are not yet what delivers —
// today's writers deliver from the hosting record, so a kind the Definition
// does not carry still lands; refusing on it would refuse launches that
// deliver. The static writers over this plan retire this acceptance.
func preference(def engine.Base) delivery.Preference {
	accept := map[present.Kind]bool{}
	for _, k := range []present.Kind{present.Context, present.MCP, present.Settings, present.Hooks, present.Commands, present.Skills} {
		if !def.Carries(k) {
			accept[k] = true
		}
	}
	return delivery.Preference{AcceptLoss: accept}
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
	ep, err := deps.Endpoints.MintMCP(ctx, src.Identity, axes)
	if err != nil {
		return sessions.Endpoint{}, resume, err
	}
	if err := deps.Sessions.BindMCP(src.Identity.Harp, ep); err != nil {
		return sessions.Endpoint{}, resume, err
	}
	return ep, resume, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
