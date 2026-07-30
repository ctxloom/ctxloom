package mockengine

import (
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// U079-F12 reads Runtime.getenv and Resolver.getenv as two readers of ONE
// concept with contradictory nil policies, and asks for them to be unified.
// They are two readers of two DIFFERENT concepts, and unifying them would
// re-open a defect this package already fixed, so these tests pin the
// difference in both directions.
//
// Runtime.getenv reads the MOCK'S OWN control knobs — the sentinel overrides,
// the report-file path. The mock IS the process, so an absent injection means
// "read the real process environment", exactly as a production default should.
const envReaderProbeVar = "CTXLOOM_MOCKENGINE_ENVREADER_PROBE"

func TestRuntimeGetenv_NilReadsTheRealProcessEnvironment(t *testing.T) {
	t.Setenv(envReaderProbeVar, "from-process")

	r := &Runtime{} // no Getenv injected
	if got := r.getenv(envReaderProbeVar); got != "from-process" {
		t.Errorf("Runtime.getenv with no injection = %q, want the process value: the mock's own control knobs are read from the environment it was launched in", got)
	}
}

// Resolver.getenv resolves PROBE ROOTS — observed data, not control. Its nil
// policy is the opposite ON PURPOSE: a walk that quietly read the developer's
// own environment would stat the developer's own ~/.codex and report
// present:true with a hash of their personal config for a surface ctxloom never
// delivered (U079-F04). "Never reach past the seam" is the whole point.
func TestResolverGetenv_NilNeverReachesTheProcessEnvironment(t *testing.T) {
	t.Setenv(envReaderProbeVar, "from-process")

	var res Resolver // no Getenv injected
	if got := res.getenv(envReaderProbeVar); got != "" {
		t.Errorf("Resolver.getenv with no injection = %q, want empty: unifying it with Runtime.getenv's os.Getenv default re-opens U079-F04", got)
	}
}

// The observable consequence of that policy, at the level a test asserts on: an
// env-dir probe with no injected reader records the $HOME fallback and MARKS it,
// even when the real process environment does define the variable.
func TestEnvDirProbe_NilResolverFallsBackAndSaysSo(t *testing.T) {
	home := t.TempDir()
	elsewhere := t.TempDir()
	t.Setenv("CODEX_HOME", elsewhere)

	cli := agent.EngineCLI{
		Engine:  "probe-test",
		Surface: agent.CLISurfaceOneshot,
		Probes: []agent.CLIProbe{{
			Kind:           agent.ProbeKindContext,
			Scope:          agent.ScopeEnvDir,
			EnvVar:         "CODEX_HOME",
			EnvHomeDefault: ".codex",
			Rel:            "config.toml",
		}},
	}

	recs := Walk(cli, agent.ParsedArgv{}, Resolver{Home: home}) // Getenv nil
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	rec := recs[0]
	if !rec.Fallback {
		t.Error("a nil Resolver.Getenv must record the $HOME fallback, not silently adopt the process's CODEX_HOME")
	}
	if want := filepath.Join(home, ".codex"); rec.Root != want {
		t.Errorf("probe root = %q, want %q — the walk reached past its seam into the real environment", rec.Root, want)
	}
}
