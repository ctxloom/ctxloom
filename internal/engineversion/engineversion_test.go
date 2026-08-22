package engineversion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeBinary writes an executable-looking file and returns its path. The
// Prober only ever STATS it (the exec itself is stubbed), so the contents are
// irrelevant — what matters is that a real path with a real mtime/size exists
// for the fingerprint to be taken from.
func fakeBinary(t *testing.T, contents string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "engine-bin")
	require.NoError(t, os.WriteFile(p, []byte(contents), 0o755))
	return p
}

// probeAtToken0 builds a Prober over one fake binary whose version command
// returns whatever the caller queues, parsed as "the first token is the
// version". runs counts executions so a test can prove the cache did (or did
// not) prevent one.
func probeAtToken0(t *testing.T, binary string, out string, runErr error) (*Prober, *int) {
	t.Helper()
	runs := 0
	p := NewProber(func(string) (string, Command, error) {
		return binary, Command{
			Args:  []string{"--version"},
			Parse: func(o string) (string, error) { return TokenAt(o, 0) },
		}, nil
	})
	p.run = func(context.Context, string, []string) (string, error) {
		runs++
		return out, runErr
	}
	return p, &runs
}

// The whole reason the cache is keyed by binary FINGERPRINT rather than engine
// name: an engine upgrade in place (npm i -g) must invalidate it. A name-keyed
// cache hands back the pre-upgrade version for the rest of the process's life,
// and on this path a wrong version selects a wrong reader — the silent
// mis-parse this machinery exists to prevent.
func TestProber_CachesUntilTheBinaryChanges(t *testing.T) {
	bin := fakeBinary(t, "v1")
	p, runs := probeAtToken0(t, bin, "1.18.4", nil)

	v, err := p.Probe(context.Background(), "opencode")
	require.NoError(t, err)
	assert.Equal(t, "1.18.4", v)
	assert.Equal(t, 1, *runs, "first probe must actually execute the binary")

	_, err = p.Probe(context.Background(), "opencode")
	require.NoError(t, err)
	assert.Equal(t, 1, *runs,
		"a second probe of the SAME binary must be served from cache — ctxloom's CLI is invoked constantly and probing every time is the startup cost this cache exists to avoid")

	// Upgrade in place: same path, different bytes and a moved mtime.
	require.NoError(t, os.WriteFile(bin, []byte("v2-longer-contents"), 0o755))
	require.NoError(t, os.Chtimes(bin, time.Now().Add(time.Minute), time.Now().Add(time.Minute)))

	assert.Equal(t, 1, *runs, "sanity: nothing has re-probed yet")
	_, err = p.Probe(context.Background(), "opencode")
	require.NoError(t, err)
	assert.Equal(t, 2, *runs,
		"an upgraded binary must re-probe: a cache that survives the upgrade reports the OLD version and selects the OLD reader for a NEW format")
}

// An absent binary is an ordinary state of the world (kiro is
// not installed on this project's dev host), so it gets its own type rather
// than being folded into "something went wrong" — but it is still a REFUSAL,
// never a version.
func TestProber_AbsentBinaryRefusesWithItsOwnType(t *testing.T) {
	p := NewProber(func(engine string) (string, Command, error) {
		return "", Command{}, &BinaryAbsentError{Engine: engine, Err: errors.New("kiro-cli not found in $PATH")}
	})

	v, err := p.Probe(context.Background(), "kiro")
	assert.Empty(t, v, "an unfindable engine must yield NO version, not a guess")

	var absent *BinaryAbsentError
	require.ErrorAs(t, err, &absent, "an uninstalled engine must be distinguishable from an installed one that misbehaved")
	assert.Equal(t, "kiro", absent.Engine)
	assert.Contains(t, err.Error(), "cannot be determined",
		"the refusal must name what could not be determined, not just fail")
}

// A binary that vanishes between resolution and the stat is the same fact as
// one that was never there. This is the path an engine uninstalled mid-process
// takes.
func TestProber_UnstattableBinaryRefusesAsAbsent(t *testing.T) {
	p, runs := probeAtToken0(t, filepath.Join(t.TempDir(), "never-created"), "1.0.0", nil)

	_, err := p.Probe(context.Background(), "opencode")
	var absent *BinaryAbsentError
	require.ErrorAs(t, err, &absent)
	assert.Zero(t, *runs, "a binary that cannot be stat'd must never be executed")
}

// A version command that RUNS and FAILS is a different fact from one that was
// never there, and its output is usually the only clue why — so it is carried.
func TestProber_FailedVersionCommandRefusesAndCarriesItsOutput(t *testing.T) {
	bin := fakeBinary(t, "x")
	p, _ := probeAtToken0(t, bin, "panic: cannot load config", errors.New("exit status 1"))

	v, err := p.Probe(context.Background(), "codex")
	assert.Empty(t, v)

	var failed *CommandFailedError
	require.ErrorAs(t, err, &failed)
	assert.Equal(t, "panic: cannot load config", failed.Output,
		"whatever the failing command printed is the diagnosis; dropping it leaves a user with nothing to act on")
	assert.ErrorContains(t, err, "exit status 1")
}

// The signal that a vendor reshaped its `--version` banner. It must surface as
// its OWN refusal — not as a silently-empty version, and never as a fallback
// to some default adapter.
func TestProber_UnparseableOutputRefusesWithItsOwnType(t *testing.T) {
	bin := fakeBinary(t, "x")
	p, _ := probeAtToken0(t, bin, "Claude Code, build 2026.08", nil)

	v, err := p.Probe(context.Background(), "claude-code")
	assert.Empty(t, v)

	var unparseable *UnparseableVersionError
	require.ErrorAs(t, err, &unparseable)
	assert.Equal(t, "Claude Code, build 2026.08", unparseable.Output,
		"the unrecognised output belongs in the error: it is the evidence that the vendor's shape moved")
}

// A failure must NOT be cached: the failures here are environment-shaped
// (binary mid-install, a transient exec error) and pinning one until the
// binary's mtime moves would outlast its cause.
func TestProber_DoesNotCacheAFailure(t *testing.T) {
	bin := fakeBinary(t, "x")
	runs := 0
	out, runErr := "", errors.New("exit status 1")
	p := NewProber(func(string) (string, Command, error) {
		return bin, Command{Args: []string{"--version"}, Parse: func(o string) (string, error) { return TokenAt(o, 0) }}, nil
	})
	p.run = func(context.Context, string, []string) (string, error) {
		runs++
		return out, runErr
	}

	_, err := p.Probe(context.Background(), "codex")
	require.Error(t, err)

	out, runErr = "0.144.4", nil
	v, err := p.Probe(context.Background(), "codex")
	require.NoError(t, err, "a transient failure must not poison the probe until the binary changes")
	assert.Equal(t, "0.144.4", v)
	assert.Equal(t, 2, runs)
}

// An engine that declares no version command cannot be probed. This is a gap
// in ctxloom's OWN descriptor table, not a fact about the user's machine, so
// it is its own type — and it still refuses.
func TestProber_NoVersionCommandRefuses(t *testing.T) {
	bin := fakeBinary(t, "x")
	p := NewProber(func(string) (string, Command, error) { return bin, Command{}, nil })

	_, err := p.Probe(context.Background(), "mock")
	var missing *NoVersionCommandError
	require.ErrorAs(t, err, &missing)
	assert.Equal(t, "mock", missing.Engine)
}

// TokenAt is positional ON PURPOSE. A scan-for-anything parser keeps
// "succeeding" when a vendor reshapes its banner, absorbing a build number or
// a bundled runtime's version as if it were the engine's — which is precisely
// the drift this mechanism exists to make visible.
func TestTokenAt_IsPositionalAndRefusesOffShape(t *testing.T) {
	v, err := TokenAt("codex-cli 0.144.4", 1)
	require.NoError(t, err)
	assert.Equal(t, "0.144.4", v)

	_, err = TokenAt("codex-cli 0.144.4", 0)
	assert.Error(t, err, "the name token is not a version and must not be accepted as one")

	_, err = TokenAt("1.18.4", 1)
	assert.ErrorContains(t, err, "only 1 token",
		"asking past the end of the line must say so, not return empty")
}

// TokenAt reads the FIRST NON-EMPTY line: engines print banners, update
// notices and blank leading lines, and a parser that took the whole output or
// the literal first line would refuse a working engine.
func TestTokenAt_UsesFirstNonEmptyLine(t *testing.T) {
	v, err := TokenAt("\n\n2.1.225 (Claude Code)\nsome trailing notice", 0)
	require.NoError(t, err)
	assert.Equal(t, "2.1.225", v)
}

// TokenAt returns the engine's OWN rendering unchanged. Canonicalizing it
// (2.1.225 -> v2.1.225, or 1.18 -> 1.18.0) would put a string in the session
// index that no `--version` output ever produced, and a human comparing the
// two would be chasing a difference ctxloom invented.
func TestTokenAt_ReturnsTheEnginesOwnRendering(t *testing.T) {
	v, err := TokenAt("1.18", 0)
	require.NoError(t, err)
	assert.Equal(t, "1.18", v, "the recorded version must be the characters the engine printed")
}

// FirstSemverToken is the deliberately tolerant parser, used ONLY for engines
// whose real output shape has never been measured. It still refuses when
// nothing on the line is a version.
func TestFirstSemverToken_ToleratesShapeButStillRefusesJunk(t *testing.T) {
	for _, out := range []string{"2.13.0", "kiro-cli 2.13.0", "2.13.0 (kiro)"} {
		v, err := FirstSemverToken(out)
		require.NoError(t, err, out)
		assert.Equal(t, "2.13.0", v, out)
	}

	_, err := FirstSemverToken("kiro-cli (development build)")
	assert.ErrorContains(t, err, "no semver-shaped token",
		"tolerant is not the same as credulous: output with no version in it must refuse")
}

// A version probe is an exec of somebody else's CLI, and the caller on the
// delegation spawn path handed down a context with no deadline at all — so a
// `--version` that never returned parked a whole agent spawn for as long as
// it hung, before the spawn was registered anywhere an operator could see it
// (task affected-yearly). The prober now carries its own budget, and blowing
// it is a refusal like every other failure here, not a guessed version.
func TestProber_AHungVersionCommandIsRefusedAtTheProbeBudget(t *testing.T) {
	bin := fakeBinary(t, "v1")
	p, _ := probeAtToken0(t, bin, "", nil)
	p.timeout = 20 * time.Millisecond
	// A binary that never answers on its own: it returns only when the
	// context it was handed is cut, which is precisely what the budget must
	// do and what nothing did before.
	p.run = func(ctx context.Context, _ string, _ []string) (string, error) {
		<-ctx.Done()
		return "", errors.New("signal: killed")
	}

	_, err := p.Probe(context.Background(), "claude-code")
	require.Error(t, err, "an unbounded probe must not be able to succeed by hanging")

	var failed *CommandFailedError
	require.ErrorAs(t, err, &failed, "a blown budget is a typed refusal, like every other failure here")
	assert.True(t, errors.Is(err, context.DeadlineExceeded),
		"and it names the budget as the cause — exec reports only 'signal: killed', which explains nothing")
	assert.Contains(t, err.Error(), "20ms", "the message states the budget it exceeded")
}

// The budget is only worth having if it is on by default: every production
// Prober comes from NewProber, and the callers this defect was found through
// pass a context that carries no deadline of its own.
func TestNewProber_CarriesTheProbeBudgetByDefault(t *testing.T) {
	p := NewProber(func(string) (string, Command, error) { return "", Command{}, nil })
	assert.Equal(t, DefaultProbeTimeout, p.timeout,
		"a prober built the production way must bound its own exec")
	assert.Positive(t, DefaultProbeTimeout, "and the bound must actually be a bound")
}

// The prober's budget must never LENGTHEN a caller's own deadline: a caller
// that has already decided how long it can wait keeps that answer.
func TestProber_ATighterCallerDeadlineStillWins(t *testing.T) {
	bin := fakeBinary(t, "v1")
	p, _ := probeAtToken0(t, bin, "", nil)
	p.timeout = time.Hour
	p.run = func(ctx context.Context, _ string, _ []string) (string, error) {
		<-ctx.Done()
		return "", errors.New("signal: killed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := p.Probe(ctx, "claude-code")
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded),
		"the caller's deadline cut the probe; the prober's hour-long backstop did not override it")
}
