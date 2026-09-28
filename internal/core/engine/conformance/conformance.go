package conformance

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// Run asserts the port's properties for one engine. The DECLARATIVE half:
// the constructor's Validate holds; the Definition is a value (equal on
// every call); the derived views agree with the typed fields; every
// declared approach, the dynamic one included, is named and offers a root;
// every declared Mode has a grammar. The INSTANCE half: Instance binds a
// session in every declared mode; the Exec it composes parses against that
// mode's grammar (the anti-drift property); Structured ∈ Modes exactly when
// the instance has a driver; every home var the engine declares points
// under the session home it was handed; Home validates; Container is a real
// spec or ErrUnsupported naming the engine; Hooks is never nil.
func Run(t *testing.T, eng engine.Engine) {
	t.Helper()
	def := eng.Root()
	require.NoError(t, def.Validate(), "the constructor's Validate must hold for every registered engine")
	require.Equal(t, def.Name, eng.Root().Name, "the Definition is a value, equal on every call")
	require.Equal(t, def.Modes, eng.Root().Modes)
	checkDerivedViews(t, def)
	checkContainerArgv(t, def)

	// The instance half.
	checkEngineFacts(t, eng, def)
	structured := false
	for _, m := range def.Modes {
		structured = structured || m == engine.Structured
		checkModeInstance(t, eng, def, m)
	}
	if !structured {
		inst, err := eng.Instance(SessionFor(t, eng, def.Modes[0]))
		require.NoError(t, err)
		require.Empty(t, inst.Drivers(), "a driver exists but Structured is not declared")
	}
}

// checkDerivedViews asserts the declarative half: the derived views agree
// with the typed fields, every declared approach (the dynamic one included)
// is named and offers a root, and every declared Mode has a grammar.
func checkDerivedViews(t *testing.T, def engine.Base) {
	t.Helper()
	s := def.Surfaces()
	require.Equal(t, len(def.Static()), len(s), "Static() and Surfaces() walk the same typed fields")
	for _, k := range def.Static() {
		a, ok := s[k]
		require.True(t, ok, "Static lists %v but Surfaces omits it", k)
		require.True(t, def.Carries(k))
		require.NotEmpty(t, a.Name(), "the %v approach is nameless", k)
		require.NotEmpty(t, a.Traits().Roots, "the %v approach offers no root", k)
	}
	for _, k := range []present.Kind{present.Context, present.MCP, present.Settings, present.Hooks, present.Commands, present.Skills} {
		if !def.Carries(k) {
			_, has := s[k]
			require.False(t, has, "%v is not carried yet appears in Surfaces", k)
		}
	}
	if def.Dynamic != nil {
		require.NotNil(t, def.MCP, "a dynamic approach needs an MCP approach to name the endpoint")
		require.NotEmpty(t, def.Dynamic.Name())
		require.NotEmpty(t, def.Dynamic.Traits().Roots)
	}
	for _, m := range def.Modes {
		g, ok := engine.CLIFor(def.CLI, m)
		require.True(t, ok, "engine declares Mode %v but no CLI grammar for it", m)
		require.NotEmpty(t, g.Binary, "the %v grammar names no binary", m)
	}
}

// checkContainerArgv delivers every kind at its default root into a cell
// whose roots are relocated, as a container relocates them, and requires
// every path the engine is told on argv to be the ENGINE side: a host path
// there names a directory the engine cannot open. It is not vacuous — some
// argv value must name a relocated root, or the check proved nothing.
func checkContainerArgv(t *testing.T, def engine.Base) {
	t.Helper()
	dir := t.TempDir()
	host := present.Paths{
		ProjectRoot: present.Root{Host: filepath.Join(dir, "project")},
		SessionHome: present.Root{Host: filepath.Join(dir, "home")},
	}
	fs := afero.NewOsFs()
	for _, r := range []present.Root{host.ProjectRoot, host.SessionHome} {
		require.NoError(t, fs.MkdirAll(r.Host, 0o700))
	}
	mapped := present.Advised(present.Paths{
		ProjectRoot: present.Root{Host: host.ProjectRoot.Host, Engine: "/conformance-engine/project"},
		SessionHome: present.Root{Host: host.SessionHome.Host, Engine: "/conformance-engine/home"},
	})
	start := present.New(mapped)
	engineSide := 0
	for _, k := range def.Static() {
		a := def.Surfaces()[k]
		d, err := deliverMinimal(def, k, start, a.Traits().Roots[0], fs)
		require.NoError(t, err, "deliver %v through %s", k, a.Name())
		for _, arg := range d.Presented.Args {
			for _, r := range []present.Root{host.ProjectRoot, host.SessionHome} {
				require.False(t, present.Under(arg, r.Host), "%v announces the HOST path %q on argv; the engine opens the engine side", k, arg)
			}
			for _, r := range []present.Root{mapped.Paths().ProjectRoot, mapped.Paths().SessionHome} {
				if present.Under(arg, r.Engine) {
					engineSide++
				}
			}
		}
	}
	require.Positive(t, engineSide, "no argv value names a relocated root, so this check proved nothing")
}

// checkEngineFacts asserts the engine-level instance facts: Home validates,
// Container is a real spec or ErrUnsupported naming the engine, Hooks is
// never nil, and an interactive engine decides its wake.
func checkEngineFacts(t *testing.T, eng engine.Engine, def engine.Base) {
	t.Helper()
	require.NoError(t, eng.Home().Validate(), "Home() must validate: the zero spec is the null object, a non-zero one is complete")
	if spec, err := eng.Container(); err != nil {
		var unsupported engine.ErrUnsupported
		require.True(t, errors.As(err, &unsupported), "Container refuses only with ErrUnsupported")
		require.Equal(t, def.Name, unsupported.Engine)
	} else {
		require.NoError(t, spec.Validate(), "a provided ContainerSpec must validate")
	}
	require.NotNil(t, eng.Hooks(), "Hooks() is a codec on every engine; one that fires none refuses on Decode")
	for _, m := range def.Modes {
		if m == engine.Interactive {
			require.True(t, eng.Wake().Decided(), "an interactive engine must declare its wake, or declare it absent with the reason")
		}
	}
	if spec, ok := eng.Wake().Get(); ok {
		require.NotNil(t, spec, "a provided wake is a spec")
	}
}

// checkModeInstance asserts one mode's instance: Instance binds a session,
// the Exec it composes parses against the mode's grammar (the anti-drift
// property), a pty exactly for interactive, every declared home var points
// under the session home, and Structured has a driver.
func checkModeInstance(t *testing.T, eng engine.Engine, def engine.Base, m engine.Mode) {
	t.Helper()
	sess := SessionFor(t, eng, m)
	inst, err := eng.Instance(sess)
	require.NoError(t, err, "Instance must bind a session in every declared mode")
	require.NotNil(t, inst)
	ex, err := inst.Exec(PresentAll(t, eng, sess))
	require.NoError(t, err)
	g, _ := engine.CLIFor(def.CLI, m)
	_, err = g.ParseArgv(ex.Args)
	require.NoError(t, err, "Exec emitted an argv the engine's own %v grammar refuses: %v", m, ex.Args)
	require.Equal(t, m == engine.Interactive, ex.Interactive, "a pty exactly for the interactive mode")
	for _, v := range eng.Home().Vars {
		require.Contains(t, ex.Env, v.Name, "home var %s is declared but Exec does not set it", v.Name)
		require.True(t, present.Under(ex.Env[v.Name], sess.Roots.SessionHome.Engine),
			"home var %s = %q is not under the session home the engine was handed", v.Name, ex.Env[v.Name])
	}
	if m == engine.Structured {
		require.NotEmpty(t, inst.Drivers(), "Structured is declared but the instance has no driver")
	}
}

// SessionFor builds the engine-facing Session a test hands to Instance: a
// throwaway identity, the mode asked for, the engine's declared host
// default posture, roots under a temp dir (the project root and the session
// home, host side equal to engine side), and each home
// var the engine declares bound under the session home.
func SessionFor(t *testing.T, eng engine.Engine, mode engine.Mode) engine.Session {
	t.Helper()
	dir := t.TempDir()
	def := eng.Root()
	home := filepath.Join(dir, "home")
	s := engine.Session{
		Identity:   sessions.Identity{Harp: "conformance-" + string(def.Name)},
		Label:      engine.LabelConfig{Label: string(def.Name)},
		Mode:       mode,
		Permission: def.Permissions.HostDefault,
		Roots: present.Paths{
			ProjectRoot: present.Root{Host: filepath.Join(dir, "project"), Engine: filepath.Join(dir, "project")},
			SessionHome: present.Root{Host: home, Engine: home},
		},
		WorkDir: filepath.Join(dir, "project"),
		Prompt:  "conformance",
	}
	for _, v := range eng.Home().Vars {
		s.Home = append(s.Home, engine.HomeBinding{Var: v.Name, Path: filepath.Join(home, v.Subdir)})
	}
	return s
}

// PresentAll delivers every kind the engine carries through its typed
// approach, at the approach's default root, with minimal inputs, into a
// throwaway filesystem rooted at the session's own roots, and returns the
// presentations in Kind order — what a full launch hands Exec.
func PresentAll(t *testing.T, eng engine.Engine, s engine.Session) []present.Presentation {
	t.Helper()
	def := eng.Root()
	fs := afero.NewOsFs()
	for _, r := range []present.Root{s.Roots.ProjectRoot, s.Roots.SessionHome} {
		if r.Host != "" {
			require.NoError(t, fs.MkdirAll(r.Host, 0o700))
		}
	}
	start := present.New(present.OnHost(s.Roots))
	var out []present.Presentation
	for _, k := range def.Static() {
		a := def.Surfaces()[k]
		d, err := deliverMinimal(def, k, start, a.Traits().Roots[0], fs)
		require.NoError(t, err, "deliver %v through %s", k, a.Name())
		out = append(out, d.Presented)
	}
	return out
}

// deliverMinimal delivers kind through the engine's typed approach with
// minimal inputs: a fixed context, and the dynamic approach's endpoint entry
// when the engine has one.
func deliverMinimal(def engine.Base, k present.Kind, start present.Start, root present.RootKind, fs afero.Fs) (present.Delivered, error) {
	switch k {
	case present.Context:
		return def.Context.DeliverContext(start, root, engine.ContextInputs{Text: []byte("conformance context"), Hash: "conformance"}, fs)
	case present.MCP:
		servers := map[string]wire.MCPServer{}
		if def.Dynamic != nil {
			servers[def.Dynamic.Name()] = def.Dynamic.Endpoint(sessions.Endpoint{URL: "http://127.0.0.1:1/mcp", Credential: "conformance"})
		}
		return def.MCP.DeliverMCP(start, root, engine.MCPInputs{Servers: servers}, fs)
	case present.Settings:
		return def.Settings.DeliverSettings(start, root, engine.SettingsInputs{}, fs)
	case present.Hooks:
		return def.Hooks.DeliverHooks(start, root, engine.HooksInputs{}, fs)
	case present.Commands:
		return def.Commands.DeliverCommands(start, root, engine.CommandsInputs{}, fs)
	case present.Skills:
		return def.Skills.DeliverSkills(start, root, engine.SkillsInputs{}, fs)
	}
	return present.Delivered{}, nil
}

// PackageFixture is the package the two-name proof routes: an unpremised
// fragment, a premised one, a command and a skill.
func PackageFixture(t *testing.T) composite.Package {
	t.Helper()
	return compositetest.Fixture(t,
		compositetest.WithFragment("always", "always body"),
		compositetest.WithPremised("when-go", "go body", "the task touches Go"),
		compositetest.WithCommand("go", "run go"),
		compositetest.WithSkill("greet"),
	)
}

// RouteFor routes pkg for eng with the binding's default preference over
// host roots: the plan Resolve would carry, computed the way Resolve
// computes it (delivery.Route over the engine's Base).
func RouteFor(t *testing.T, pkg composite.Package, eng engine.Engine) (delivery.Plan, error) {
	t.Helper()
	dir := t.TempDir()
	roots := present.Paths{
		ProjectRoot: present.Root{Host: filepath.Join(dir, "project"), Engine: filepath.Join(dir, "project")},
		SessionHome: present.Root{Host: filepath.Join(dir, "home"), Engine: filepath.Join(dir, "home")},
	}
	return delivery.Route(pkg.EngineItems(eng.Root().Name), eng.Root(), delivery.Preference{}, roots)
}

// Canonical renders a plan deterministically, engine name elided: two
// engines with identical definitions render identically.
func Canonical(plan delivery.Plan) string {
	var b strings.Builder
	for _, s := range plan.Static {
		fmt.Fprintf(&b, "static %v %s root=%v channel=%v\n", s.Kind, s.Approach, s.Root, s.Traits.Channel)
	}
	dyn := append([]string(nil), plan.Dynamic...)
	sort.Strings(dyn)
	for _, ref := range dyn {
		fmt.Fprintf(&b, "dynamic %s\n", ref)
	}
	for _, l := range plan.Losses {
		fmt.Fprintf(&b, "loss %v\n", l.Kind)
	}
	return b.String()
}
