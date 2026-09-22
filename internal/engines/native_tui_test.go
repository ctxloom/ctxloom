package engines

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// launcherInjector is satisfied by every concrete backend via its embedded
// agent.BaseBackend — asserted against agent.Backend so this test never
// imports a concrete engine package.
type launcherInjector interface {
	SetLauncher(agent.Launcher)
}

// TestAllEngines_LaunchNativeTUI is the completeness bar: "ctxloom run must
// launch every engine's native TUI/CLI" is not best-effort. For every
// shippable engine the composition root builds, an INTERACTIVE Execute of
// its Backend (agent.Hosted) must declare ModeInteractive, accept the
// runner's injected launcher (SetLauncher), and reach it with a LaunchSpec
// that requests a pty and names a binary. A fake launcher captures the spec,
// so no engine's CLI has to be installed. A double declares that it is one
// where it is built, so a new double is excluded automatically rather than
// silently required to spawn a TUI.
func TestAllEngines_LaunchNativeTUI(t *testing.T) {
	names := Registry().Names(func(d engine.Definition) bool { return d.Distribution != engine.DistributionTestOnly })
	require.NotEmpty(t, names, "no shippable engine was composed; the completeness bar would range over nothing")
	for _, name := range names {
		t.Run(string(name), func(t *testing.T) {
			h, ok := Hosted(string(name))
			require.True(t, ok, "%s is not agent.Hosted", name)
			b := h.Backend(nil)
			assert.Contains(t, b.SupportedModes(), agent.ModeInteractive,
				"%s must support ModeInteractive to launch its native TUI under `ctxloom run`", name)
			injector, ok := b.(launcherInjector)
			require.True(t, ok, "%s must embed agent.BaseBackend (SetLauncher) to accept the runner's launcher", name)

			var captured *agent.LaunchSpec
			injector.SetLauncher(func(_ context.Context, spec agent.LaunchSpec, _ io.Reader, _, _ io.Writer, _ <-chan agent.WindowSize) (int32, error) {
				captured = &spec
				return 0, nil
			})
			_, err := b.Execute(context.Background(), &agent.ExecuteRequest{Mode: agent.ModeInteractive, WorkDir: t.TempDir()}, io.Discard, io.Discard)
			require.NoError(t, err, "%s: interactive Execute must not error before ever reaching the launcher", name)
			require.NotNil(t, captured, "%s: interactive Execute never reached the injected launcher — its native TUI would never spawn", name)
			assert.True(t, captured.Interactive, "%s: interactive run must request a pty (Interactive: true)", name)
			assert.NotEmpty(t, captured.BinaryPath, "%s: interactive LaunchSpec named no binary to spawn", name)
		})
	}
}

// Every shippable engine whose vendor transcripts ctxloom READS must be
// askable for its version (engine.Definition.Version) — otherwise reader
// selection has nothing to select on. Read off the registry, so a new engine
// cannot declare a reader without one; the mock provides a degenerate reader
// and no binary, and TestOnly is what exempts it, not a name.
func TestVersion_DeclaredForEveryShippableEngineWithAVendorReader(t *testing.T) {
	checked := 0
	for _, name := range Registry().Names(func(d engine.Definition) bool { return d.Distribution != engine.DistributionTestOnly }) {
		e, _ := Registry().Lookup(name)
		reads := false
		for _, r := range e.Transcripts() {
			if _, ok := r.(vendorreader.VersionedAdapter); ok {
				reads = true
			}
		}
		if !reads {
			continue
		}
		checked++
		assert.True(t, e.Root().Version.Declared(), "%s reads a vendor transcript, so it must declare a version command", name)
	}
	assert.GreaterOrEqual(t, checked, 1, "at least one shippable engine reads vendor transcripts")
}

// A double declares NO version command: it has no binary, so there is no
// single version that would mean anything, and a bogus one would put a
// meaningless string in a session index.
func TestVersion_AbsentOnEveryDouble(t *testing.T) {
	for _, name := range Registry().Names(func(d engine.Definition) bool { return d.Distribution == engine.DistributionTestOnly }) {
		e, _ := Registry().Lookup(name)
		assert.False(t, e.Root().Version.Declared(), "%s has no single binary whose version means anything", name)
	}
}

// Registry lookups resolve the registered name and NOTHING else: no alias,
// case or prefix resolution, so a typo is refused rather than rounded to a
// real engine.
func TestHosted_ResolvesTheExactNameOnly(t *testing.T) {
	for _, name := range Registry().Names(nil) {
		_, ok := Hosted(string(name))
		assert.True(t, ok, "%s", name)
		for _, other := range []string{string(name) + " ", string(name)[:len(name)-1], string(name) + "x"} {
			_, ok := Hosted(other)
			assert.False(t, ok, "%q must not resolve to %s", other, name)
		}
	}
	_, ok := Hosted("")
	assert.False(t, ok)
}
