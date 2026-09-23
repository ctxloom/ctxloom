//go:build docker_integration

// Docker-gated proof that a container cell's ENGINE HOME is delivered where
// the engine reads it: the in-container runner (the container's foreground
// process) writes the claude surfaces that live beneath CLAUDE_CONFIG_DIR —
// the system-prompt file, the MCP config, settings.json — at the container
// side of the engine-home mount, not at the host path the mount is bound
// from (which does not exist inside the container).
//
// The cell is the PRODUCTION cell: launch.Resolve over
// operations.LaunchDepsFor (the real Cells: BindAgentHome →
// isolation.MountEngineHome → present.Containerize), started through the
// production isolation.StarterForWorkspace. Only the package (a fixed
// assembler) and the image (the config's isolation_images override) are the
// test's. The image puts mockengine in claude's place: this proves delivery,
// never that the real claude runs in any image.
//
//	GOWORK=off just test-pkg ./internal/core/coord/... -tags docker_integration -run CoordContainerEngineHome
package coord_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
)

const claudeShimImage = "ctxloom-coord-claudeshim-itest:latest"

// buildClaudeShimImage layers mockengine over the bus image as `claude`, so
// the claude-code runner's exec resolves without a vendor binary.
func buildClaudeShimImage(t *testing.T) string {
	t.Helper()
	base := buildBusIntegrationImage(t)
	dir := t.TempDir()
	root, err := sourcedir.RepoRoot()
	require.NoError(t, err)
	build := exec.Command("go", "build", "-buildvcs=false", "-o", filepath.Join(dir, "mockengine"), "github.com/ctxloom/ctxloom/cmd/mockengine")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH, "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build static mockengine: %v\n%s", err, out)
	}
	shim := "#!/bin/sh\nexec /usr/local/bin/mockengine --" + claude.EngineName + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "claude"), []byte(shim), 0o755))
	dockerfile := "FROM " + base + "\nCOPY mockengine /usr/local/bin/mockengine\nCOPY claude /usr/local/bin/claude\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644))
	if out, err := exec.Command("docker", "build", "-t", claudeShimImage, dir).CombinedOutput(); err != nil {
		t.Fatalf("docker build claude shim image: %v\n%s", err, out)
	}
	return claudeShimImage
}

// engineHomeAssembler composes a package that lands a surface beneath the
// engine home for every kind claude roots there: context (the system-prompt
// file), MCP servers (.mcp.json) and a deny list (settings.json).
type engineHomeAssembler struct{}

func (engineHomeAssembler) Assemble(_ context.Context, _ *config.Snapshot, sel launch.Selection) (composite.Package, error) {
	text := "# guidance\nENGINE-HOME-MARKER-5c1e"
	return composite.Package{
		Context:   composite.Context{Text: text, Hash: fmt.Sprintf("%x", sha256.Sum256([]byte(text)))},
		MCP:       map[string]wire.MCPServer{"deploy-tool": {Command: "deploy", Args: []string{"--serve"}}},
		DenyTools: []string{"Task"},
		Selection: composite.Selection{Profiles: sel.Profiles},
	}, nil
}

func (engineHomeAssembler) Index(context.Context, *config.Snapshot) (composite.Index, error) {
	return composite.Index{}, nil
}
func (engineHomeAssembler) LabelEnv(*config.Snapshot, string) map[string]string { return nil }

// TestCoordContainerEngineHome_DeliveredAtTheContainerSidePath is the gate.
func TestCoordContainerEngineHome_DeliveredAtTheContainerSidePath(t *testing.T) {
	dockergate.RequireRuntime(t, (isolation.Docker{}).Available(), "the container engine-home delivery integration test")
	coord.ResetStrictness(t)
	// claude-code's container auth and its session-home seed both accept an
	// env token; the mock in claude's place never reads it.
	t.Setenv("ANTHROPIC_API_KEY", "itest-not-a-key")

	image := buildClaudeShimImage(t)
	projectDir := testsupport.ProjectDir(t)
	coord.TeeHome(t)

	cfg := config.NewFixture(config.Fixture{
		LM: config.LMConfig{
			Configs:  map[string]config.LLMConfig{"primary": {Type: claude.EngineName}},
			Defaults: config.RoleDefaults{Primary: "primary"},
		},
		Agents: map[string]agents.Agent{"x": {
			Name: "x", Profiles: []string{"base"}, Permissions: "bypass",
			Runtime: string(launch.RuntimeRootless),
		}},
		DefaultAgent:    "x",
		IsolationImages: map[string]string{claude.EngineName: image},
	})
	deps, err := operations.LaunchDepsFor(engines.Registry(), &config.Snapshot{Config: cfg}, strictness.Mode{Prog: "ctxloom"})
	require.NoError(t, err)
	deps.Assembler = engineHomeAssembler{}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	entry, err := operations.OpenedApp(nil, operations.Handed{Engines: engines.Registry()}).AssignSession(ctx, projectDir, claude.EngineName)
	require.NoError(t, err)
	id := coord.OwnerIdentity()
	id.Harp = entry.HarpName
	l, err := launch.Resolve(ctx, deps, launch.Source{
		Identity: id, Agent: "x", Mode: engine.Structured, Prompt: "go", WorkDir: projectDir,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = launch.Discard(context.Background(), l) })
	require.NotNil(t, l.Cell.Container, "the binding asked for a container cell")
	home := l.Cell.Paths.Paths().EngineHome
	require.NotEqual(t, home.Host, home.Engine, "the engine home must be relocated for this test to mean anything: %+v", home)
	prepared, ok := l.Cell.Handle.(operations.PreparedCell)
	require.True(t, ok, "the production cell carries its prepared workspace: %T", l.Cell.Handle)

	c, err := coord.New(coord.Options{ProjectDir: projectDir, ProjectID: "enginehome-itest", Spawner: coord.NewFakeSpawner(nil, nil), OwnerHarp: entry.HarpName})
	require.NoError(t, err)
	require.NoError(t, coordgrpc.Serve(c))
	t.Cleanup(c.Close)
	token, err := c.RegisterSessionOwner(entry.HarpName)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)

	var container string
	start := func(ctx context.Context, spawnEnv map[string]string) (func(), string, error) {
		handle, err := isolation.StarterForWorkspace(prepared.Policy, prepared.Workspace, claude.EngineName, l.Label.Label, 0, spawnEnv)(ctx)
		if err != nil {
			return nil, "", err
		}
		container = handle.Name
		return handle.Kill, handle.Name, nil
	}
	t.Cleanup(func() {
		if t.Failed() && container != "" {
			logs, _ := exec.Command("docker", "logs", container).CombinedOutput()
			t.Logf("container %s logs:\n%s", container, logs)
		}
	})
	_, err = c.StartOwnedRun(ctx, owner, coord.OwnedRunOf(l, false), start, "")
	require.NoError(t, err)
	require.NotEmpty(t, container)

	// Seen from INSIDE the container: every engine-home surface exists at the
	// path claude is told (CLAUDE_CONFIG_DIR, --append-system-prompt-file,
	// --mcp-config), which is the Engine side of the root.
	inside := func(script string) (string, error) {
		out, err := exec.Command("docker", "exec", container, "sh", "-c", script).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	want := []string{
		path.Join(home.Engine, claude.SettingsFileName),
		path.Join(home.Engine, claude.MCPFileName),
	}
	present := func() bool {
		for _, f := range want {
			if _, err := inside("test -s " + f); err != nil {
				return false
			}
		}
		out, err := inside("ls " + home.Engine + "/*" + agent.SCMFramedContextSuffix)
		return err == nil && out != ""
	}
	deadline := time.Now().Add(60 * time.Second)
	for !present() {
		if time.Now().After(deadline) {
			listing, _ := inside("ls -la " + home.Engine)
			t.Fatalf("engine-home surfaces %v and *%s missing at the container-side path %s; inside the container it holds:\n%s",
				want, agent.SCMFramedContextSuffix, home.Engine, listing)
		}
		time.Sleep(250 * time.Millisecond)
	}
	body, err := inside("cat " + home.Engine + "/*" + agent.SCMFramedContextSuffix)
	require.NoError(t, err)
	require.Contains(t, body, "ENGINE-HOME-MARKER-5c1e", "the system-prompt file must carry the composed context")

}
