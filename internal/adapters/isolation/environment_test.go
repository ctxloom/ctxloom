package isolation

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// The Environment contract, through the exported calls (NewSpec, Prepare,
// Preview) and nothing else: whichever environment the axes select, the
// caller is handed the same shape.

var (
	hostAxes      = launch.Axes{Workspace: launch.WorkspaceNone, Runtime: launch.RuntimeHost}
	containerAxes = launch.Axes{Workspace: launch.WorkspaceNone, Runtime: launch.RuntimeRootless}
)

// containerRuntime is the fake the container environment runs over: its
// binary is `true`, so an image inspect succeeds and every other probe
// answers nothing.
var containerRuntime = fakeRuntime{name: "docker", binary: "true", available: true}

// withFakeContainerRuntime makes the run-path runtime probe answer rt and
// the shared-filesystem probe pass, so a container Prepare runs hermetically.
func withFakeContainerRuntime(t *testing.T, rt Runtime) {
	t.Helper()
	sel, fsCheck := selectRuntimeProbe, sharedFSCheck
	selectRuntimeProbe = func(string, RuntimeAxis) Runtime { return rt }
	sharedFSCheck = func(context.Context, Runtime, string, []string) error { return nil }
	t.Cleanup(func() { selectRuntimeProbe, sharedFSCheck = sel, fsCheck })
}

// envSpec is a Spec for eng under axes, in harp's session under home, with a
// run-as-is image so a container prepare builds nothing.
func envSpec(t *testing.T, axes launch.Axes, eng engine.Engine, home, project string) Spec {
	t.Helper()
	s, err := NewSpec(axes, eng).Project(project).
		Session(harpA, sessionDir(home, harpA), SessionState{Harp: harpA}).
		Image(ImageConfig{Image: "img"}).Home(agents.HomeModeSession).Build()
	require.NoError(t, err)
	return s
}

func prepared(t *testing.T, s Spec) Environment {
	t.Helper()
	env, err := Prepare(context.Background(), s)
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Cleanup() })
	return env
}

// S0: the host and a (fake-runtime) container environment give the SAME
// Placement shape through the SAME calls: both roots present on both sides,
// the home var bound to the session home's Engine side, and the bytes behind
// both at the same host paths. Only the Engine sides differ.
func TestEnvironment_HostAndContainerGiveTheSamePlacementShape(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	withFakeContainerRuntime(t, containerRuntime)
	project := t.TempDir()
	eng := claudeEngine(t)

	onHost := prepared(t, envSpec(t, hostAxes, eng, home, project)).Placement()
	strictness.Reset()
	inContainer := prepared(t, envSpec(t, containerAxes, eng, home, project)).Placement()

	for name, pl := range map[string]launch.Placement{"host": onHost, "container": inContainer} {
		paths := pl.Paths.Paths()
		assert.Equal(t, project, paths.ProjectRoot.Host, "%s: the project's bytes", name)
		assert.Equal(t, claudeHome(home, harpA), paths.SessionHome.Host, "%s: the session home's bytes", name)
		assert.NotEmpty(t, paths.ProjectRoot.Engine, name)
		assert.NotEmpty(t, paths.SessionHome.Engine, name)
		require.Len(t, pl.Home, 1, name)
		assert.Equal(t, claude.ConfigDirEnv, pl.Home[0].Var, name)
		assert.Equal(t, paths.SessionHome.Engine, pl.Home[0].Path, "%s: the home binding is the Engine side", name)
		assert.Equal(t, paths.SessionHome.Engine, pl.Env[claude.ConfigDirEnv], "%s: the home var names the Engine side", name)
	}
	assert.Equal(t, project, onHost.Paths.Paths().ProjectRoot.Engine, "the host presents in place")
	assert.Equal(t, "/ctr"+project, inContainer.Paths.Paths().ProjectRoot.Engine, "the container presents where its mapper routes")
	assert.Equal(t, defaultContainerInstanceHome+"/"+claude.HomeLeaf, inContainer.Paths.Paths().SessionHome.Engine)
}

// S0 (Q5): a Preview creates NOTHING on disk — no checkout, no scratch, no
// session home — on either environment, and starts nothing.
func TestPreview_CreatesNothingOnDisk(t *testing.T) {
	for name, axes := range map[string]launch.Axes{
		"host":      hostAxes,
		"worktree":  {Workspace: launch.WorkspaceWorktree, Runtime: launch.RuntimeHost},
		"container": containerAxes,
	} {
		t.Run(name, func(t *testing.T) {
			home := fakeHostHome(t, tokenFixture)
			withFakeContainerRuntime(t, containerRuntime)
			project := gitRepo(t)
			before := treeOf(t, home, project)

			env := Preview(context.Background(), envSpec(t, axes, claudeEngine(t), home, project))

			assert.Equal(t, before, treeOf(t, home, project), "a preview wrote to disk")
			assert.Equal(t, claudeHome(home, harpA), env.Placement().Paths.Paths().SessionHome.Host,
				"the preview still shows the session home the run WOULD create")
			_, err := env.Start(context.Background(), RunnerRequest{Engine: "claude-code"})
			assert.ErrorIs(t, err, ErrPreviewEnvironment)
			_, err = env.Interactive(context.Background(), RunnerRequest{Engine: "claude-code"})
			assert.ErrorIs(t, err, ErrPreviewEnvironment)
			assert.NoError(t, env.Cleanup())
		})
	}
}

// A container preview describes the runtime it probed, not the axis.
func TestPreview_ContainerDescribesTheProbedRuntime(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	withFakeContainerRuntime(t, containerRuntime)
	env := Preview(context.Background(), envSpec(t, containerAxes, claudeEngine(t), home, t.TempDir()))
	assert.Equal(t, Description{Workspace: string(WorkspaceShared), Runtime: "docker", Reach: "loopback"}, env.Describe())
}

// errNoHomeRoute is a runtime with no route home, as settleReach refuses.
var errNoHomeRoute = errors.New("no default route to the host")

// homelessRuntime is a mapperRuntime over an unroutableMapper with no route home either: two
// independent problems a run would refuse on, one at relocation and one at
// the reach probe.
type homelessRuntime struct{ mapperRuntime }

func (homelessRuntime) reachRoute(context.Context) (hostRoute, error) {
	return hostRoute{}, errNoHomeRoute
}

// The preview does NOT stop at the first problem: an unroutable root and a
// runtime with no route home are BOTH recorded — each as the finding the real
// run raises — and the preview still returns its best-effort outcome: the
// unroutable root marked unreachable (no Engine side, never a guessed path)
// and the reach unknown.
func TestPreview_ReportsEveryProblemAndContinues(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	project := t.TempDir()
	withFakeContainerRuntime(t, homelessRuntime{mapperRuntime{fakeRuntime: containerRuntime, m: unroutableMapper{under: project}}})

	mark := strictness.Checkpoint()
	defer strictness.Close(mark)
	env := Preview(context.Background(), envSpec(t, containerAxes, claudeEngine(t), home, project))
	found := strictness.Since(mark)

	texts := make([]string, 0, len(found))
	for _, f := range found {
		assert.True(t, f.NonDegradable, "a finding the run refuses on is non-degradable in the preview too: %s", f.Text)
		assert.NotEmpty(t, f.Remedy, "every finding names its fix: %s", f.Text)
		texts = append(texts, f.Text)
	}
	require.Len(t, found, 3, "every problem, not the first: %q", texts)
	assert.Contains(t, found[0].Text, "identity contract cannot be verified", "the container gate: the fake runtime's run-as-is image has no config to verify")
	assert.Contains(t, found[1].Text, project, "the unroutable root, by name")
	assert.Contains(t, found[2].Text, errNoHomeRoute.Error(), "the missing route home")

	root := env.Placement().Paths.Paths().ProjectRoot
	assert.Equal(t, present.Root{Host: project}, root, "an unreachable root has no Engine side — never a guessed path")
	assert.Equal(t, "docker", env.Describe().Runtime)
	assert.Equal(t, ReachUnknown, env.Describe().Reach)
}

// A container requested with no runtime reachable: the preview records the
// run's refusal and describes the runtime as unavailable — never as the host
// the degrade chain would fall back to — with every root unreachable and the
// reach unknown.
func TestPreview_NoRuntimeDescribesTheRuntimeUnavailable(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	project := t.TempDir()
	withFakeContainerRuntime(t, Host{})

	mark := strictness.Checkpoint()
	defer strictness.Close(mark)
	env := Preview(context.Background(), envSpec(t, containerAxes, claudeEngine(t), home, project))
	found := strictness.Since(mark)

	require.Len(t, found, 1)
	assert.True(t, found[0].NonDegradable)
	assert.Equal(t, Description{Workspace: string(WorkspaceShared), Runtime: RuntimeUnavailable, Reach: ReachUnknown}, env.Describe())
	paths := env.Placement().Paths.Paths()
	assert.Equal(t, present.Root{Host: project}, paths.ProjectRoot, "no runtime routes the project: no Engine side")
	assert.Equal(t, present.Root{Host: claudeHome(home, harpA)}, paths.SessionHome, "nor the session home")
	assert.NotContains(t, env.Placement().Env, claude.ConfigDirEnv, "no home var names a path nothing presents")
}

// Prepare is unchanged by the preview's accumulation: the relocator now
// hands back a partial Placement beside its error, and Prepare must still
// REFUSE on it — an error, no environment, the one refusal finding — rather
// than run on the roots that did route.
func TestPrepare_StillRefusesAPartialRelocation(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	project := t.TempDir()
	withFakeContainerRuntime(t, mapperRuntime{fakeRuntime: containerRuntime, m: unroutableMapper{under: project}})

	mark := strictness.Checkpoint()
	defer strictness.Close(mark)
	env, err := Prepare(context.Background(), envSpec(t, containerAxes, claudeEngine(t), home, project))
	require.ErrorIs(t, err, present.ErrUnreachableRoot)
	assert.Nil(t, env, "no environment over a partial relocation")
	var refusals int
	for _, f := range strictness.Since(mark) {
		if f.Remedy == unreachableRootRemedy(errNoRoute) {
			refusals++
			assert.True(t, f.NonDegradable)
		}
	}
	assert.Equal(t, 1, refusals, "one relocation refusal")
}

// uncMapper refuses every path the way driveLetterMapper refuses a share.
type uncMapper struct{}

func (uncMapper) toContainer(host string) (string, error) {
	return driveLetterMapper{}.toContainer(`\\srv\share` + host)
}

// A share-path root is refused with the remedy that fits it: the Linux build
// inside the WSL distro, not "move it where the daemon sees it". Any other
// unroutable root keeps the general remedy.
func TestPrepare_UnreachableShareRootNamesWSLRemedy(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	project := t.TempDir()
	withFakeContainerRuntime(t, mapperRuntime{fakeRuntime: containerRuntime, m: uncMapper{}})

	_, err := Prepare(context.Background(), envSpec(t, containerAxes, claudeEngine(t), home, project))
	require.ErrorIs(t, err, errUNCPath)
	found := strictness.All()
	require.NotEmpty(t, found)
	assert.Contains(t, found[len(found)-1].Remedy, "Linux build inside the WSL distro")
	assert.NotContains(t, unreachableRootRemedy(errNoRoute), "WSL")
}

// A worktree's Placement carries what the worktree provisioned (scratch dir,
// git identity) and the engine's home var exactly once, naming the SESSION
// home — never a home the workspace chose.
func TestPrepare_WorktreePlacementCarriesItsProvisionsAndTheSessionHome(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	project := gitRepo(t)
	axes := launch.Axes{Workspace: launch.WorkspaceWorktree, Runtime: launch.RuntimeHost}

	env := prepared(t, envSpec(t, axes, claudeEngine(t), home, project))
	pl := env.Placement()
	assert.NotEqual(t, project, pl.Paths.Paths().ProjectRoot.Host, "a worktree runs in its checkout")
	assert.Equal(t, claudeHome(home, harpA), pl.Env[claude.ConfigDirEnv], "the home var names the session home")
	assert.Contains(t, pl.Env, "TMPDIR", "the scratch dir the worktree provisioned")
	assert.Contains(t, pl.Env, "GIT_AUTHOR_NAME", "the git identity for the checkout it created")
	assert.NotContains(t, pl.Env, "HOME", "no blanket HOME override")
	assert.Equal(t, Description{Workspace: string(WorkspaceWorktree), Runtime: "host", Reach: "loopback"}, env.Describe())
}

// The builder reports the FIRST missing fact, wrapped in ErrSpecIncomplete.
func TestSpecBuilder_FirstErrorWins(t *testing.T) {
	_, err := NewSpec(hostAxes, nil).Project("relative").Build()
	require.ErrorIs(t, err, ErrSpecIncomplete)
	assert.Contains(t, err.Error(), "engine", "the first failure is the one reported")

	_, err = NewSpec(hostAxes, mock.New()).Project("relative").Session("", "", SessionState{}).Build()
	require.ErrorIs(t, err, ErrSpecIncomplete)
	assert.Contains(t, err.Error(), "project")

	_, err = NewSpec(hostAxes, mock.New()).Project(t.TempDir()).Session(harpA, "rel", SessionState{}).Build()
	require.ErrorIs(t, err, ErrSpecIncomplete)
	assert.Contains(t, err.Error(), "session")
}

// gitRepo is a real repository with one commit, for a worktree to check out.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	return dir
}

// treeOf lists every path under the roots, for a before/after comparison.
func treeOf(t *testing.T, roots ...string) []string {
	t.Helper()
	var all []string
	for _, root := range roots {
		require.NoError(t, filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			all = append(all, p)
			return nil
		}))
	}
	sort.Strings(all)
	return all
}

// finding is what a dry run's gate lists of one strictness finding.
type finding struct {
	text, remedy  string
	nonDegradable bool
}

func findingsSince(mark strictness.Mark) []finding {
	var out []finding
	for _, f := range strictness.Since(mark) {
		out = append(out, finding{f.Text, f.Remedy, f.NonDegradable})
	}
	return out
}

// A container preview runs the run's container gate — inspecting, never
// pulling or building — and records exactly the refusals the run records on
// the same inputs, so a dry run is never clean where the run refuses: an
// image that is absent with no recipe to build it, and an engine that
// declares no container story (behind a user-owned image whose identity
// contract cannot be verified, which the run refuses on too).
func TestPreview_RecordsTheContainerGateARunRefusesOn(t *testing.T) {
	for _, tc := range []struct {
		name string
		rt   Runtime
		eng  func(t *testing.T) engine.Engine
	}{
		{"image absent, nothing to build it from", fakeRuntime{name: "docker", binary: "false", available: true}, claudeEngine},
		{"engine with no container story", containerRuntime, func(*testing.T) engine.Engine { return mock.NewNamed("storyless") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := fakeHostHome(t, tokenFixture)
			project := t.TempDir()
			withFakeContainerRuntime(t, tc.rt)
			s := envSpec(t, containerAxes, tc.eng(t), home, project)

			mark := strictness.Checkpoint()
			env, err := Prepare(context.Background(), s)
			if err == nil {
				require.NoError(t, env.Cleanup())
			}
			run := findingsSince(mark)
			strictness.Close(mark)
			require.NotEmpty(t, run, "precondition: the run refuses")

			mark = strictness.Checkpoint()
			defer strictness.Close(mark)
			_ = Preview(context.Background(), s)
			assert.Equal(t, run, findingsSince(mark), "the dry run records what the run refuses on")
		})
	}
}

// A worktree preview shows the cwd the run gets: the checkout it would
// create, at the path it creates it, and nothing on disk — or, for a project
// that is no git repository (the run degrades to the shared tree), the live
// project.
func TestPreview_WorktreeShowsTheCwdTheRunGets(t *testing.T) {
	axes := launch.Axes{Workspace: launch.WorkspaceWorktree, Runtime: launch.RuntimeHost}
	for _, tc := range []struct {
		name    string
		project func(t *testing.T) string
	}{
		{"a git project: the checkout", gitRepo},
		{"no git repository: the live project", func(t *testing.T) string { return t.TempDir() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := fakeHostHome(t, tokenFixture)
			project := tc.project(t)
			s := envSpec(t, axes, claudeEngine(t), home, project)

			previewed := Preview(context.Background(), s).Placement().Paths.Paths().ProjectRoot.Host
			require.NoDirExists(t, sessionDir(home, harpA), "a preview creates nothing on disk")

			ran := prepared(t, s).Placement().Paths.Paths().ProjectRoot.Host
			assert.Equal(t, ran, previewed, "the preview shows the cwd the run gets")
		})
	}
}
