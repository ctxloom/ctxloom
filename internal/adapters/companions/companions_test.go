package companions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/companions/loadout"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/cliversion"
)

func TestProbeCompanions_ReportsVersionFromJSONProbe(t *testing.T) {
	admitEveryDiscoveredCompanion(t)
	restoreLook := SetLookPathForTesting(func(bin string) (string, error) {
		return "/usr/bin/" + bin, nil
	})
	defer restoreLook()
	// Probes run concurrently, so guard the collection.
	var probedMu sync.Mutex
	var probed []string
	restoreProbe := SetCompanionVersionOutputForTesting(func(path string) ([]byte, error) {
		probedMu.Lock()
		probed = append(probed, path)
		probedMu.Unlock()
		return []byte(`{"name":"x","version":"v1.2.3"}`), nil
	})
	defer restoreProbe()

	statuses := Prober{}.ProbeCompanions(nil)
	require.Len(t, statuses, 3)
	for _, st := range statuses {
		assert.Equal(t, "/usr/bin/"+st.Bin, st.Path)
		assert.Equal(t, "v1.2.3", st.Version)
		assert.NoError(t, st.Err)
	}
	assert.ElementsMatch(t, []string{"/usr/bin/ltk", "/usr/bin/reprise", "/usr/bin/taskloom"}, probed)
}

func TestProbeCompanions_MissingBinaryYieldsEmptyPathAndNoProbe(t *testing.T) {
	restoreLook := SetLookPathForTesting(func(string) (string, error) {
		return "", exec.ErrNotFound
	})
	defer restoreLook()
	restoreProbe := SetCompanionVersionOutputForTesting(func(string) ([]byte, error) {
		t.Fatal("version probe must not run for a missing binary")
		return nil, nil
	})
	defer restoreProbe()

	for _, st := range (Prober{}).ProbeCompanions(nil) {
		assert.Empty(t, st.Path)
		assert.Empty(t, st.Version)
		assert.NoError(t, st.Err, "missing is a state, not a probe error")
	}
}

func TestProbeCompanions_ProbeFailureIsNonFatal(t *testing.T) {
	admitEveryDiscoveredCompanion(t)
	restoreLook := SetLookPathForTesting(func(bin string) (string, error) {
		return "/usr/bin/" + bin, nil
	})
	defer restoreLook()

	t.Run("exec error", func(t *testing.T) {
		restore := SetCompanionVersionOutputForTesting(func(string) ([]byte, error) {
			return nil, errors.New("boom")
		})
		defer restore()
		for _, st := range (Prober{}).ProbeCompanions(nil) {
			assert.NotEmpty(t, st.Path, "binary still counts as present")
			assert.Error(t, st.Err)
		}
	})

	t.Run("unparseable output", func(t *testing.T) {
		restore := SetCompanionVersionOutputForTesting(func(string) ([]byte, error) {
			return []byte("not json"), nil
		})
		defer restore()
		for _, st := range (Prober{}).ProbeCompanions(nil) {
			assert.Error(t, st.Err)
			assert.Empty(t, st.Version)
		}
	})

	t.Run("missing version field", func(t *testing.T) {
		restore := SetCompanionVersionOutputForTesting(func(string) ([]byte, error) {
			return []byte(`{"name":"ltk"}`), nil
		})
		defer restore()
		for _, st := range (Prober{}).ProbeCompanions(nil) {
			assert.Error(t, st.Err)
		}
	})
}

// writeFakeCompanion drops an executable shell script named name into dir and
// returns its path.
func writeFakeCompanion(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755))
	return path
}

// shrinkProbeDurations shortens the probe timeout and wait-delay so the
// stall-path tests run in milliseconds, restoring them on cleanup.
//
// BOTH pairs, because the two probes are bounded in different packages: the
// loadout probe by this package's vars, the version probe by cliversion's
// (which owns that exec). Shrinking only one leaves whichever test drives the
// other waiting the full production timeout.
func shrinkProbeDurations(t *testing.T, timeout, waitDelay time.Duration) {
	t.Helper()
	origTimeout, origDelay := companionProbeTimeout, companionProbeWaitDelay
	origProbe, origProbeDelay := cliversion.ProbeTimeout, cliversion.ProbeWaitDelay
	companionProbeTimeout, companionProbeWaitDelay = timeout, waitDelay
	cliversion.ProbeTimeout, cliversion.ProbeWaitDelay = timeout, waitDelay
	t.Cleanup(func() {
		companionProbeTimeout, companionProbeWaitDelay = origTimeout, origDelay
		cliversion.ProbeTimeout, cliversion.ProbeWaitDelay = origProbe, origProbeDelay
	})
}

// TestCompanionVersionOutput_RealExec below drives cliversion.Probe directly:
// it is the production body ProbeCompanions runs, and every other test in this
// package replaces the seam over it.

// TestCompanionVersionOutput_RealExec exercises the production seam body —
// the actual `version --format json` argv, the probe timeout, and the
// WaitDelay pipe bound — against fake companion binaries. Every other test
// replaces the seam, so without this the real exec path has zero coverage.
func TestCompanionVersionOutput_RealExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake companions are sh scripts")
	}
	dir := t.TempDir()

	t.Run("valid JSON output", func(t *testing.T) {
		// The script proves the argv contract too: it only answers the
		// version when called exactly as `version --format json`.
		bin := writeFakeCompanion(t, dir, "goodtool",
			`[ "$1 $2 $3" = "version --format json" ] || exit 2
echo '{"name":"goodtool","version":"v9.9.9"}'`)
		version, err := cliversion.Probe(bin)
		require.NoError(t, err)
		assert.Equal(t, "v9.9.9", version)
	})

	t.Run("wedged companion hits the probe timeout", func(t *testing.T) {
		shrinkProbeDurations(t, 200*time.Millisecond, 100*time.Millisecond)
		bin := writeFakeCompanion(t, dir, "wedged", "sleep 30")
		start := time.Now()
		_, err := cliversion.Probe(bin)
		require.Error(t, err, "a wedged companion must fail the probe, not hang it")
		assert.Less(t, time.Since(start), 5*time.Second, "probe must be bounded by timeout+WaitDelay")
	})

	t.Run("grandchild holding stdout is bounded by WaitDelay", func(t *testing.T) {
		shrinkProbeDurations(t, 200*time.Millisecond, 100*time.Millisecond)
		// The direct child exits immediately but leaves a background
		// grandchild holding the inherited stdout pipe open. Without
		// cmd.WaitDelay, Output blocks until that pipe closes (30s here;
		// forever for a daemonized companion) and startup stalls.
		bin := writeFakeCompanion(t, dir, "spawner", "sleep 30 &\nexit 0")
		start := time.Now()
		_, err := cliversion.Probe(bin)
		require.Error(t, err)
		assert.Less(t, time.Since(start), 5*time.Second, "WaitDelay must cut the pipe wait short")
	})
}

// ==========================================================================
// Companion disable switch (--no-companions / CTXLOOM_NO_COMPANIONS)
// ==========================================================================

// TestProbeCompanions_DisabledYieldsNothing proves ProbeCompanions itself
// honours the switch at the exec boundary — the prior gate
// (TestCompanionsDisabled_SkipsProbeEntirely) only proved
// companionBundleSeed's OWN check short-circuits before ever calling the
// probe; it never proved ProbeCompanions would refuse to run if something
// else called it directly, which is exactly what reportCompanions
// (cli/startup_helpers.go, called unconditionally from `ctxloom run`/`ctxloom
// mcp`) did.
func TestProbeCompanions_DisabledYieldsNothing(t *testing.T) {
	restoreLook := SetLookPathForTesting(func(bin string) (string, error) {
		t.Fatal("lookPath must not run when companions are disabled")
		return "", nil
	})
	defer restoreLook()

	assert.Empty(t, Prober{Disabled: true}.ProbeCompanions(nil), "no companion binary may be probed when disabled")
}

// The reader source of a disabled prober contributes nothing — no exec, no
// loadout — while an enabled one reads through the probe.
func TestProberReaderSource_DisabledContributesNothing(t *testing.T) {
	restoreLook := SetLookPathForTesting(func(bin string) (string, error) {
		t.Fatal("lookPath must not run when companions are disabled")
		return "", nil
	})
	defer restoreLook()
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{t.TempDir()}})

	readers := Prober{Disabled: true}.ReaderSource()(cfg)
	require.Len(t, readers, 1)
	reads, err := readers[0].Read(context.Background())
	require.NoError(t, err)
	assert.Empty(t, reads)
}

// TestProbeCompanionLoadouts_DisabledYieldsNothing is ProbeCompanionLoadouts'
// half of the same fix: the loadout probe execs companion binaries too, and
// had no gate of its own either.
func TestProbeCompanionLoadouts_DisabledYieldsNothing(t *testing.T) {
	restoreLook := SetLookPathForTesting(func(bin string) (string, error) {
		t.Fatal("lookPath must not run when companions are disabled")
		return "", nil
	})
	defer restoreLook()

	got, err := Prober{Disabled: true}.ProbeCompanionLoadouts(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, got, "no loadout may be probed when disabled")
}

// syncBuffer is a mutex-guarded bytes.Buffer: ProbeCompanionLoadouts fans its
// per-companion probes out across goroutines, so a plain bytes.Buffer as the
// clidiag sink races when more than one probe warns concurrently.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestProbeCompanionLoadouts_WedgedCompanionWarns proves a loadout probe
// failure that is NOT the ordinary "no loadout subcommand" case (a wedged
// companion timing out, or any other exec failure) is reported — previously
// it vanished with zero diagnostic and the run reported success having
// delivered nothing from that companion.
func TestProbeCompanionLoadouts_WedgedCompanionWarns(t *testing.T) {
	admitEveryDiscoveredCompanion(t)
	restoreLook := SetLookPathForTesting(func(bin string) (string, error) {
		return "/usr/bin/" + bin, nil
	})
	defer restoreLook()
	restoreLoadout := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	})
	defer restoreLoadout()

	buf := &syncBuffer{}
	restoreSink := clidiag.SetSink(buf)
	defer restoreSink()

	out, err := Prober{}.ProbeCompanionLoadouts(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, out.Loadouts, "a wedged companion still contributes nothing")
	assert.Contains(t, buf.String(), "loadout probe failed", "a non-benign failure must be diagnosed, not silent")
}

// TestProbeCompanionLoadouts_UnknownSubcommandStaysQuiet is the contrast: a
// companion that hasn't adopted the `loadout` protocol yet (ordinary
// *exec.ExitError, e.g. "unknown command") must NOT warn — that is the
// common, benign case the fix above must not turn noisy.
func TestProbeCompanionLoadouts_UnknownSubcommandStaysQuiet(t *testing.T) {
	admitEveryDiscoveredCompanion(t)
	restoreLook := SetLookPathForTesting(func(bin string) (string, error) {
		return "/usr/bin/" + bin, nil
	})
	defer restoreLook()
	restoreLoadout := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) {
		return nil, &exec.ExitError{}
	})
	defer restoreLoadout()

	buf := &syncBuffer{}
	restoreSink := clidiag.SetSink(buf)
	defer restoreSink()

	out, err := Prober{}.ProbeCompanionLoadouts(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, out.Loadouts)
	assert.Empty(t, buf.String(), "an unadopted loadout subcommand is the ordinary case, not a warning")
}

// TestCompanionLoadoutOutput_ArgvMatchesTheEmitterSide bridges the two ends
// of the cross-process wire contract this probe's argv and
// loadout.NewCommand's cobra dispatch both have to agree on
// (subcommand name, flag name, format value) — previously duplicated as
// bare string literals with no shared constant and no test exercising both
// real sides, so renaming any of the three passed the whole suite while
// silently breaking every companion in production. Drives the REAL
// loadout.NewCommand (the emitter side) with the EXACT argv
// companionLoadoutOutput builds (the consumer side) and checks the output
// round-trips through the real signing.DecodeLoadoutEnvelope decoder.
func TestCompanionLoadoutOutput_ArgvMatchesTheEmitterSide(t *testing.T) {
	bundleYAML := []byte("version: \"1.0.0\"\nfragments:\n  x:\n    content: hi\n")
	// loadout.NewCommand returns the "loadout" command itself (it has no
	// subcommands), so only the flag portion of companionLoadoutOutput's
	// argv applies here — the loadout.Subcommand constant is what a real companion
	// binary's root command would dispatch ON to reach this command in the
	// first place.
	cmd := loadout.NewCommand("acme", bundleYAML, nil)
	require.Equal(t, loadout.Subcommand, cmd.Use, "the emitter side's command name must still match loadout.Subcommand")
	cmd.SetArgs([]string{"--" + loadout.FormatFlag, loadout.FormatJSON})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	require.NoError(t, cmd.Execute())

	decoded, _, err := signing.DecodeLoadoutEnvelope(buf.Bytes(), nil, time.Now())
	require.NoError(t, err)
	assert.Equal(t, bundleYAML, decoded)
}

// TestCompanionVersion_ReadsTheCliversionContract pins the cross-binary
// version contract from BOTH ends at once: the payload is produced the way
// every companion actually produces it — by marshalling a cliversion.Info,
// which is what cmd/ltk, cmd/taskloom, cmd/harp and internal/adapters/cli all hand to
// their renderer — and consumed by the reader ctxloom boots with. The
// package doc on cliversion calls Info "the single source of truth rather
// than being re-declared per binary"; this test is what makes that true of
// the READER, so a field rename or json-tag change on Info can no longer
// break companion probing silently.
func TestCompanionVersion_ReadsTheCliversionContract(t *testing.T) {
	payload, err := json.Marshal(cliversion.Info{Name: "taskloom", Version: "v1.2.3"})
	require.NoError(t, err)

	restore := SetCompanionVersionOutputForTesting(func(string) ([]byte, error) {
		return payload, nil
	})
	defer restore()

	got, err := cliversion.Probe("/fake/taskloom")
	require.NoError(t, err)
	assert.Equal(t, "v1.2.3", got)
}

// TestCompanionVersion_RejectsAContractWithNoVersion pins the other half of
// the reader's contract: a well-formed envelope carrying no version is an
// ERROR, never an empty version string reported as a successful probe.
func TestCompanionVersion_RejectsAContractWithNoVersion(t *testing.T) {
	payload, err := json.Marshal(cliversion.Info{Name: "taskloom"})
	require.NoError(t, err)

	restore := SetCompanionVersionOutputForTesting(func(string) ([]byte, error) {
		return payload, nil
	})
	defer restore()

	_, err = cliversion.Probe("/fake/taskloom")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no version field")
}
