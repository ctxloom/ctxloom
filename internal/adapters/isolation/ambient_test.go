package isolation

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/spf13/afero"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// recordingInstanceConfig is a stand-in engine config writer: it records every
// request it is handed so a test can prove the ENGINE was actually reached with
// the right instance home and working directory — the engine write-config
// directive's whole point being that isolation orchestrates and the engine
// edits.
type recordingInstanceConfig struct {
	mu       sync.Mutex
	requests []engine.InstanceConfigRequest
	// inFlight/maxInFlight measure overlap, so the serialization test observes
	// serialization rather than asserting a lock file exists.
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
	hold        time.Duration
	report      engine.InstanceConfigReport
	err         error
}

func (r *recordingInstanceConfig) WriteInstanceConfig(req engine.InstanceConfigRequest, _ afero.Fs) (engine.InstanceConfigReport, error) {
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

func (r *recordingInstanceConfig) seen() []engine.InstanceConfigRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]engine.InstanceConfigRequest(nil), r.requests...)
}

// withInstanceConfigWriter declares w as engine's config generator for the
// duration of the test (nil: none), leaving the engine's other facts as they
// are; the accessor is restored on cleanup.
func withInstanceConfigWriter(t *testing.T, name string, w engine.InstanceConfigWriter) {
	t.Helper()
	stageEngineFacts(t, name, func(f *EngineFacts) { f.Home.InstanceConfig = w })
}

// TestAmbientSet_IsAnExplicitAllowListPerEngine is the roster guard the plan
// asks for: every engine with a declared ambient set names its files ONE BY
// ONE, at owner-only mode, and every registered backend appears in the roster
// by declaration rather than by omission.
//
// The allow-list shape is what makes D4/D5 decisions rather than accidents: a
// deny-list would copy each new engine file by default, and the default
// direction of a mistake there is a confidentiality leak.
func TestAmbientSet_IsAnExplicitAllowListPerEngine(t *testing.T) {
	home := withFakeHome(t)

	// An engine that declares NO seed is in the roster with its reason, and
	// has an empty set: the difference between "declared nothing to seed"
	// and "nobody registered it" is that the former can be read back.
	const unseeded = "unseeded-fixture"
	stageEngineFacts(t, unseeded, func(f *EngineFacts) {
		f.Home = engine.HomeSpec{} // relocates nothing: nothing to seed
	})

	names := AmbientEngineNames()
	sort.Strings(names)
	assert.Equal(t, []string{"claude-code", unseeded}, names,
		"every registered backend needs an EXPLICIT ambient declaration, provided or absent")

	want := map[string][]AmbientFile{
		"claude-code": {{HostRel: ".claude/.credentials.json", DestRel: "claude/.credentials.json", Mode: 0o600, Required: true}},
	}
	for engine, files := range want {
		assert.Equal(t, files, AmbientSet(engine), "%s's ambient set", engine)
	}
	assert.Nil(t, AmbientSet(unseeded), "a declared absence has no ambient set")
	declared, ok := credentialSeedDeclared(unseeded)
	require.True(t, ok, "a declared absence is still a registered declaration")
	assert.NotEmpty(t, declared.AbsentReason())
	assert.Nil(t, AmbientSet("never-registered"), "an unregistered engine has no ambient set either")
	_, ok = credentialSeedDeclared("never-registered")
	assert.False(t, ok, "…but it is not a declaration, and that is the readable difference")

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
	rec := &recordingInstanceConfig{report: engine.InstanceConfigReport{
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

// TestCopyAmbient_SerializesTwoRunsSharingOneInstance is the S5-flagged
// lock, the plan-1.6 item S5 skipped: two runs WITHIN one session (a
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

	// The real instance shape: the session's home member under the ctxloom
	// home, which is what the lock is keyed on.
	project := t.TempDir()
	instance, err := paths.HarpSessionHome("ugly-icy-squid")
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

// TestCopyAmbient_InstanceCredentialKeepsItsRefreshToken is the INVERSE of the
// stripping test it replaces, and the inversion is the whole point of this
// work.
//
// The old seed removed the refresh half of claude's OAuth token on the way
// into the instance, because a COPY that refreshed would consume the host's
// single-use token and invalidate the user's own login. The price was that the
// instance could authenticate until its access token expired and then had no
// way back — a credential that provably could not renew.
//
// The instance's credential is no longer a copy: it is the host's material,
// delivered by a mechanism that keeps the two in step. So the refresh token
// must be THERE, because refreshing is now the correct thing for the instance
// to do.
//
// MUTATION TARGET: reintroduce any projection of the credential bytes and this
// goes red.
func TestCopyAmbient_InstanceCredentialKeepsItsRefreshToken(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))
	hostBytes := []byte(`{"claudeAiOauth":{"accessToken":"acc","refreshToken":"ref","refreshTokenExpiresAt":2,"subscriptionType":"max"}}`)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), hostBytes, 0o600))
	withInstanceConfigWriter(t, "claude-code", &recordingInstanceConfig{})

	instance := t.TempDir()
	report, err := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = report.Close() })

	placed, err := os.ReadFile(filepath.Join(instance, "claude", ".credentials.json"))
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(placed, &cfg))
	oauth := cfg["claudeAiOauth"].(map[string]any)
	assert.Equal(t, "ref", oauth["refreshToken"], "the instance must be able to RENEW; a stripped copy provably cannot")
	assert.Equal(t, float64(2), oauth["refreshTokenExpiresAt"])
	assert.Equal(t, "acc", oauth["accessToken"])
	assert.Equal(t, hostBytes, placed, "the instance gets the host's material, unprojected")
}

// TestCopyAmbient_ReportsTheDeliveryItGot pins that the delivery reaches the
// caller. Mounted and replicated FAIL DIFFERENTLY — replication has a rotation
// window a mount does not — so "which one did this run get?" has to be
// answerable without reading the selection code.
func TestCopyAmbient_ReportsTheDeliveryItGot(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, false)
	withInstanceConfigWriter(t, "claude-code", &recordingInstanceConfig{})

	report, err := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: t.TempDir(), WorkDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = report.Close() })

	seed, ok := credentialSeedFor("claude-code")
	require.True(t, ok)
	assert.Contains(t, seed.Accept, report.Delivery,
		"the delivery must be one the engine DECLARED it accepts, never one it was handed")
	assert.NotEmpty(t, report.Mechanism, "the implementation that placed it must be nameable")
}

// TestCopyAmbient_RefusesWhenNoDeclaredDeliveryCanBeHonoured is the fail-loud
// contract, and the reason there is no fallback left to catch it.
//
// A platform honouring none of the declared acceptances must REFUSE, naming
// every mechanism it tried and why each was rejected — never quietly hand back
// something the engine did not agree to. The stripped copy that used to be the
// silent last resort is gone, and this is what stands in its place.
func TestCopyAmbient_RefusesWhenNoDeclaredDeliveryCanBeHonoured(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, false)
	withInstanceConfigWriter(t, "claude-code", &recordingInstanceConfig{})

	// Force the failure rather than hunt for a host that has it: the probes
	// are injectable precisely so the "nothing works here" path is reachable.
	// A capability probe that can only ever answer YES gets believed.
	restore := seedProvisionOptions
	seedProvisionOptions = func() []ProvisionOption {
		return []ProvisionOption{
			WithNamespaceBindsPerformed(),
			WithNamespaceProbe(func(context.Context, string) error {
				return errors.New("unprivileged user namespaces are disabled by policy on this host")
			}),
			WithReplicationProbe(func(context.Context, string) error {
				return errors.New("the inotify instance limit is exhausted")
			}),
		}
	}
	t.Cleanup(func() { seedProvisionOptions = restore })

	instance := t.TempDir()
	_, err := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: t.TempDir()})
	require.Error(t, err, "a run that cannot get its declared delivery must refuse, not degrade")

	msg := err.Error()
	assert.Contains(t, msg, "no declared provisioner can deliver shared material")
	assert.Contains(t, msg, "container-mount", "every candidate tried must be named")
	assert.Contains(t, msg, "namespace-mount")
	assert.Contains(t, msg, "replication")
	assert.Contains(t, msg, "disabled by policy", "…each with the reason it was rejected")
	assert.Contains(t, msg, "inotify instance limit")

	assert.NoFileExists(t, filepath.Join(instance, "claude", ".credentials.json"),
		"a refused run leaves no material behind to be mistaken for a working credential")
}
