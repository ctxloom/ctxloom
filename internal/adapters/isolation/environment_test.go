package isolation

import (
	"context"
	"fmt"
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

			env, err := Preview(context.Background(), envSpec(t, axes, claudeEngine(t), home, project))
			require.NoError(t, err)

			assert.Equal(t, before, treeOf(t, home, project), "a preview wrote to disk")
			assert.Equal(t, claudeHome(home, harpA), env.Placement().Paths.Paths().SessionHome.Host,
				"the preview still shows the session home the run WOULD create")
			_, err = env.Start(context.Background(), RunnerRequest{Engine: "claude-code"})
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
	env, err := Preview(context.Background(), envSpec(t, containerAxes, claudeEngine(t), home, t.TempDir()))
	require.NoError(t, err)
	assert.Equal(t, Description{Workspace: string(WorkspaceShared), Runtime: "docker", Reach: "loopback"}, env.Describe())
}

// S0 / the SCRATCH ruling: a non-relocating engine in a container gets its
// session home, created on the host and mounted as the container's $HOME —
// and the runner is started with exactly that mount and that HOME.
func TestPrepare_NonRelocatingEngineInAContainerHasItsHomeAtHOME(t *testing.T) {
	home := fakeHostHome(t, "")
	withFakeContainerRuntime(t, containerRuntime)
	eng := mock.New(mock.WithContainer())
	stageEngineFacts(t, string(mock.Name), func(f *EngineFacts) { *f = FactsOf(eng) })

	env := prepared(t, envSpec(t, containerAxes, eng, home, t.TempDir()))
	sessionHome := env.Placement().Paths.Paths().SessionHome
	assert.Equal(t, filepath.Join(sessionDir(home, harpA), "home", string(mock.Name)), sessionHome.Host)
	assert.Equal(t, defaultContainerHome, sessionHome.Engine)
	assert.DirExists(t, sessionHome.Host)

	ce, ok := env.(*containerEnvironment)
	require.True(t, ok, "a container axis with a reachable runtime prepares the container environment")
	spec := ce.c.buildRunnerSpec(string(mock.Name), "name", ce.cw, nil)
	assert.Equal(t, defaultContainerHome, spec.Home)
	assert.Contains(t, spec.Mounts, mount{Host: sessionHome.Host, Container: defaultContainerHome})
}

// S0: a root the runtime cannot route is refused at Prepare with
// present.ErrUnreachableRoot, recorded as a non-degradable finding, and the
// workspace it prepared is torn down.
func TestPrepare_UnreachableRootIsRefused(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	project := t.TempDir()
	withFakeContainerRuntime(t, unroutableRuntime{fakeRuntime: containerRuntime, m: unroutableMapper{under: project}})

	env, err := Prepare(context.Background(), envSpec(t, containerAxes, claudeEngine(t), home, project))
	require.ErrorIs(t, err, present.ErrUnreachableRoot)
	assert.Nil(t, env)
	found := strictness.All()
	require.NotEmpty(t, found)
	assert.True(t, found[len(found)-1].NonDegradable, "an unpresentable root is not a thinner run")
	matches, _ := filepath.Glob(filepath.Join(sessionDir(home, harpA), "*", "ctxloom-iso-*"))
	assert.Empty(t, matches, "the refused environment's scratch was torn down")
}

// uncMapper refuses every path the way driveLetterMapper refuses a share.
type uncMapper struct{}

func (uncMapper) toContainer(host string) (string, error) {
	return "", fmt.Errorf("%w: %s", errUNCPath, host)
}

// A share-path root is refused with the remedy that fits it: the Linux build
// inside the WSL distro, not "move it where the daemon sees it". Any other
// unroutable root keeps the general remedy.
func TestPrepare_UnreachableShareRootNamesWSLRemedy(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	project := t.TempDir()
	withFakeContainerRuntime(t, unroutableRuntime{fakeRuntime: containerRuntime, m: uncMapper{}})

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
