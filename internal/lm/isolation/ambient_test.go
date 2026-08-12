package isolation

import (
	"context"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// recordingInstanceConfig is a stand-in engine config writer: it records every
// request it is handed so a test can prove the ENGINE was actually reached with
// the right instance home and working directory — the engine write-config
// directive's whole point being that isolation orchestrates and the engine
// edits.
type recordingInstanceConfig struct {
	mu       sync.Mutex
	requests []agent.InstanceConfigRequest
	// inFlight/maxInFlight measure overlap, so the filelock test observes
	// serialization rather than asserting a lock file exists.
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
	hold        time.Duration
	report      agent.InstanceConfigReport
	err         error
}

func (r *recordingInstanceConfig) WriteInstanceConfig(req agent.InstanceConfigRequest) (agent.InstanceConfigReport, error) {
	n := r.inFlight.Add(1)
	for {
		max := r.maxInFlight.Load()
		if n <= max || r.maxInFlight.CompareAndSwap(max, n) {
			break
		}
	}
	if r.hold > 0 {
		time.Sleep(r.hold)
	}
	r.inFlight.Add(-1)

	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()
	return r.report, r.err
}

func (r *recordingInstanceConfig) seen() []agent.InstanceConfigRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agent.InstanceConfigRequest(nil), r.requests...)
}

// withInstanceConfigWriter installs w as engine's generator for the duration of
// the test and restores whatever was registered before. The registry is
// process-global (internal/lm/backends populates it at init, and its external
// test package links backends into this test binary), so a test that replaced
// an entry and left it replaced would silently reshape every later test.
func withInstanceConfigWriter(t *testing.T, engine string, w agent.InstanceConfigWriter) {
	t.Helper()
	instanceConfigMu.Lock()
	prev, had := instanceConfigWriters[engine]
	instanceConfigMu.Unlock()
	RegisterInstanceConfigWriter(engine, w)
	t.Cleanup(func() {
		if had {
			RegisterInstanceConfigWriter(engine, prev)
			return
		}
		RegisterInstanceConfigWriter(engine, nil)
	})
}

// TestAmbientSet_IsAnExplicitAllowListPerEngine is the roster guard the plan
// asks for: every engine with a declared ambient set names its files ONE BY
// ONE, at owner-only mode, and kiro's set is empty by declaration rather than
// by being missing from the roster.
//
// The allow-list shape is what makes D4/D5 decisions rather than accidents: a
// deny-list would copy each new engine file by default, and the default
// direction of a mistake there is a confidentiality leak.
func TestAmbientSet_IsAnExplicitAllowListPerEngine(t *testing.T) {
	home := withFakeHome(t)

	names := AmbientEngineNames()
	sort.Strings(names)
	assert.Equal(t, []string{"claude-code", "codex", "kiro", "opencode"}, names,
		"every registered backend needs an EXPLICIT ambient declaration, empty or not")

	want := map[string][]AmbientFile{
		"claude-code": {{HostRel: ".claude/.credentials.json", DestRel: "claude/.credentials.json", Mode: 0o600, Required: true}},
		"codex":       {{HostRel: ".codex/auth.json", DestRel: ".codex/auth.json", Mode: 0o600, Required: true}},
		"kiro":        nil,
		"opencode": {
			{HostRel: ".local/share/opencode/auth.json", DestRel: "xdg-data/opencode/auth.json", Mode: 0o600, Required: true},
			{HostRel: ".local/share/opencode/mcp-auth.json", DestRel: "xdg-data/opencode/mcp-auth.json", Mode: 0o600, Required: false},
		},
	}
	for engine, files := range want {
		assert.Equal(t, files, AmbientSet(engine), "%s's ambient set", engine)
	}
	assert.Nil(t, AmbientSet("mock"), "an engine with no declaration has no ambient set")

	// The sets are resolved against the REAL host home seam, not a literal.
	claude := AmbientSet("claude-code")
	require.Len(t, claude, 1)
	assert.NotContains(t, claude[0].HostRel, home, "HostRel is home-RELATIVE, never an absolute host path")
}

// TestCopyAmbient_ReachesTheEngineWithTheInstanceAndWorkDir pins the engine
// write-config directive at the seam: CopyAmbient copies the allow-listed files
// itself and then hands the ENGINE the host home, the instance home and the
// working directory, performing no byte-level edit of the engine's format on
// its own.
func TestCopyAmbient_ReachesTheEngineWithTheInstanceAndWorkDir(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, true)
	rec := &recordingInstanceConfig{report: agent.InstanceConfigReport{
		Wrote:    []string{"/generated/.claude.json"},
		Warnings: []string{"the host .claude.json carries no \"hasCompletedOnboarding\""},
	}}
	withInstanceConfigWriter(t, "claude-code", rec)

	instance := t.TempDir()
	workDir := t.TempDir()
	report, err := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: workDir})
	require.NoError(t, err)

	require.Len(t, rec.seen(), 1, "the engine must be asked exactly once")
	got := rec.seen()[0]
	assert.Equal(t, home, got.HostHome, "the engine reads the real host home through the caller's one resolution")
	assert.Equal(t, instance, got.InstanceHome)
	assert.Equal(t, workDir, got.WorkDir, "the trust target is the run's working directory")

	assert.Equal(t, []string{"/generated/.claude.json"}, report.Generated)
	assert.Len(t, report.Warnings, 1, "the engine's fail-loud notices reach the caller, not just stderr")
	assert.Equal(t, 1, report.Copied)
	assert.False(t, report.NoSource)
}

// TestCopyAmbient_NoSourceSkipsGeneration: when there is nothing to
// authenticate with, the caller is about to refuse this instance outright — so
// generating a config for it would be work thrown away and a directory created
// for a home nobody will use.
func TestCopyAmbient_NoSourceSkipsGeneration(t *testing.T) {
	withFakeHome(t) // no host creds at all
	t.Setenv("ANTHROPIC_API_KEY", "")
	rec := &recordingInstanceConfig{}
	withInstanceConfigWriter(t, "claude-code", rec)

	report, err := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: t.TempDir(), WorkDir: t.TempDir()})
	require.NoError(t, err)
	require.True(t, report.NoSource)
	assert.Empty(t, rec.seen(), "no config is generated for an instance the caller will refuse")
}

// TestCopyAmbient_EnvTriggerStillGeneratesTheEngineConfig: an ANTHROPIC_API_KEY
// run needs no credential copy and STILL meets claude's onboarding and trust
// dialogs. The two halves of the copy-in are independent, and treating
// "skipped the credential" as "skipped everything" would re-prompt every
// API-key session.
func TestCopyAmbient_EnvTriggerStillGeneratesTheEngineConfig(t *testing.T) {
	withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	rec := &recordingInstanceConfig{}
	withInstanceConfigWriter(t, "claude-code", rec)

	report, err := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: t.TempDir(), WorkDir: t.TempDir()})
	require.NoError(t, err)
	assert.True(t, report.SkippedEnv)
	assert.Len(t, rec.seen(), 1, "auth riding the env says nothing about onboarding or trust")
}

// TestCopyAmbient_UnregisteredEngineIsAnError: an engine with no declared
// ambient set cannot be copied for. Silently succeeding would report a prepared
// instance that had nothing done to it.
func TestCopyAmbient_UnregisteredEngineIsAnError(t *testing.T) {
	_, err := CopyAmbient(AmbientRequest{Engine: "acp", InstanceHome: t.TempDir()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no declared ambient set")
}

// TestCopyAmbient_Kiro_DeclaredEmptyCopiesNothing: kiro's ambient set is empty
// BY DESIGN (its credentials live in a global sqlite no home var relocates), so
// the copy-in moves no bytes and reports no fail-loud "nothing seedable" —
// there was never anything to seed.
func TestCopyAmbient_Kiro_DeclaredEmptyCopiesNothing(t *testing.T) {
	withFakeHome(t)
	t.Setenv("KIRO_API_KEY", "")
	instance := t.TempDir()

	report, err := CopyAmbient(AmbientRequest{Engine: "kiro", InstanceHome: instance, WorkDir: t.TempDir()})
	require.NoError(t, err)
	assert.Zero(t, report.Copied)
	assert.False(t, report.NoSource, "an empty set is not a failed seed")
	assert.Nil(t, AmbientSet("kiro"))
}

// TestCopyAmbient_SerializesTwoRunsSharingOneInstance is the S5-flagged
// filelock, the plan-1.6 item S5 skipped: two runs WITHIN one session (a
// coordinator and its in-tree delegated child, which inherits the harp) share
// ONE instance home, and both load-modify-write the same config file. Without
// the lock their reads and writes interleave and one run's generated content is
// lost.
//
// MUTATION TARGET: drop the lockInstanceHome call in CopyAmbient and this goes
// red — the recorder observes two generations in flight at once.
func TestCopyAmbient_SerializesTwoRunsSharingOneInstance(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, false)
	rec := &recordingInstanceConfig{hold: 60 * time.Millisecond}
	withInstanceConfigWriter(t, "claude-code", rec)

	// An instance home INSIDE a .ctxloom tree, which is what
	// filelock.ProjectPathFor keys on — the real in-tree shape,
	// <project>/.ctxloom/state/<harp>/home.
	project := t.TempDir()
	instance, err := paths.SessionHomePath(filepath.Join(project, paths.AppDirName), "ugly-icy-squid")
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, cerr := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: project})
			assert.NoError(t, cerr)
		}()
	}
	wg.Wait()

	require.Len(t, rec.seen(), 2, "both runs must have prepared the shared instance")
	assert.Equal(t, int32(1), rec.maxInFlight.Load(),
		"two runs sharing one session instance must serialize; %d were generating at once", rec.maxInFlight.Load())
}

// TestWorktreeAxis_RoutesThroughCopyAmbient is D8 made testable: the worktree
// axis's provisionConfigHome reaches the SAME mechanism the in-tree axis does,
// so claude's field-scoped .claude.json and codex's section elision apply to a
// fan-out member too. The LOCATION stays split — the instance home handed over
// is this axis's own per-agent config home, and the working directory is this
// member's own checkout, never the shared project root.
func TestWorktreeAxis_RoutesThroughCopyAmbient(t *testing.T) {
	resetStrictness(t)
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, false)
	rec := &recordingInstanceConfig{}
	withInstanceConfigWriter(t, "claude-code", rec)

	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f, "claude-code").PrepareWorkspace(context.Background(), "/proj", "member-ambient")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Cleanup() })

	seen := rec.seen()
	require.Len(t, seen, 1, "the worktree axis must reach the engine through CopyAmbient, not a second seeding path")
	assert.Equal(t, home, seen[0].HostHome)
	assert.Equal(t, ws.Dir(), seen[0].WorkDir,
		"the member's own checkout is what an engine-generated trust answer must name, never the shared project root")

	configDir := WorkspaceEnv(ws)["CLAUDE_CONFIG_DIR"]
	require.NotEmpty(t, configDir)
	assert.Equal(t, filepath.Dir(configDir), seen[0].InstanceHome,
		"the engine is handed the config-home ROOT and appends its own leaf, exactly as on the in-tree axis")
	assert.NotContains(t, filepath.ToSlash(seen[0].InstanceHome), "/"+paths.AppDirName+"/state/",
		"D8 shares the MECHANISM, not the location: a worktree member's home must not migrate into the project state tier")
}
