package backends

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// nonTUIBackends names registered backends this completeness bar deliberately
// exempts, with WHY each is not a native-TUI local-CLI engine:
//   - "acp": a protocol-only driver. SupportedModes deliberately omits
//     ModeInteractive (see acp.SupportedModes' own doc) — an interactive
//     session belongs to the TARGET agent's own backend (claude-code/codex/
//     kiro direct-CLI), not this generic ACP client.
//   - "mock": a test double, never launched for a real session.
var nonTUIBackends = map[string]string{
	"acp":  "protocol-only ACP driver; interactive belongs to the target agent's own backend",
	"mock": "test double, not a real engine",
}

// launcherInjector is satisfied by every concrete backend via its embedded
// agent.BaseBackend — asserted against agent.Backend so this test never
// imports a concrete engine package (the registry already does that).
type launcherInjector interface {
	SetLauncher(agent.Launcher)
}

// TestAllEngines_LaunchNativeTUI is the completeness bar: "ctxloom run
// must launch every engine's native TUI/CLI" is not best-effort. For every
// backend the registry (internal/lm/backends/registry.go) knows about — other
// than the documented exemptions above — an INTERACTIVE Execute must:
//
//  1. declare ModeInteractive in SupportedModes (else `ctxloom run --llm
//     <engine>` could never select an interactive session for it at all);
//  2. accept ctxloom's injected pty launcher (SetLauncher, promoted from
//     agent.BaseBackend — a backend that doesn't embed it can never receive
//     RunLaunchSpec);
//  3. reach that launcher with a LaunchSpec that actually requests a pty
//     (Interactive: true) and names a binary to spawn (BinaryPath != "").
//
// A fake launcher captures the LaunchSpec instead of exec'ing the real
// binary, so this never depends on claude/codex/kiro-cli/opencode
// actually being installed — it stays hermetic and fast. Registering a new
// engine without wiring SetLauncher, without declaring ModeInteractive, or
// whose buildArgs collapses interactive mode onto a headless flag is exactly
// the gap this test exists to fail on.
func TestAllEngines_LaunchNativeTUI(t *testing.T) {
	names := List()
	require.NotEmpty(t, names, "the registry has no backends registered at all")

	tested := 0
	for _, name := range names {
		name := name
		if reason, excluded := nonTUIBackends[name]; excluded {
			t.Logf("%s excluded from the native-TUI bar: %s", name, reason)
			continue
		}
		tested++
		t.Run(name, func(t *testing.T) {
			b := Get(name)
			require.NotNil(t, b, "registry.Get(%q) returned nil", name)

			assert.Contains(t, b.SupportedModes(), agent.ModeInteractive,
				"%s must support ModeInteractive to launch its native TUI under `ctxloom run`", name)

			injector, ok := b.(launcherInjector)
			require.True(t, ok, "%s must embed agent.BaseBackend (SetLauncher) to accept ctxloom's pty launcher", name)

			var captured *agent.LaunchSpec
			injector.SetLauncher(func(_ context.Context, spec agent.LaunchSpec, _ io.Reader, _, _ io.Writer, _ <-chan agent.WindowSize) (int32, error) {
				captured = &spec
				return 0, nil
			})

			workDir := t.TempDir()
			_, err := b.Execute(context.Background(), &agent.ExecuteRequest{
				Mode:      agent.ModeInteractive,
				WorkDir:   workDir,
				SkipSetup: true, // mirrors a real run: Setup never ran, only Execute's own launch path is under test
			}, io.Discard, io.Discard)
			require.NoError(t, err, "%s: interactive Execute must not error before ever reaching the launcher", name)

			require.NotNil(t, captured, "%s: interactive Execute never reached the injected launcher — its native TUI would never spawn", name)
			assert.True(t, captured.Interactive, "%s: interactive run must request a pty (Interactive: true), else the native TUI never gets a terminal", name)
			assert.NotEmpty(t, captured.BinaryPath, "%s: interactive LaunchSpec named no binary to spawn", name)
		})
	}
	// A gap in the exemption map (a new backend name neither tested nor
	// excluded) would otherwise silently pass this test by looping over zero
	// engines — guard the completeness bar itself.
	assert.Positive(t, tested, "no backend was actually exercised by the native-TUI completeness bar")
}
