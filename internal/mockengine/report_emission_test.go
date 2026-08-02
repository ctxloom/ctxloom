package mockengine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

func emissionRuntime(t *testing.T, reportFile string, stderr, stdout *strings.Builder) *Runtime {
	t.Helper()
	return &Runtime{
		CLI: agent.EngineCLI{
			Engine:  "codex",
			Surface: agent.CLISurfaceOneshot,
			Prompt:  agent.PromptStdin,
		},
		Res: Resolver{Cwd: t.TempDir(), Home: t.TempDir()},
		Getenv: func(k string) string {
			if k == EnvReportFile {
				return reportFile
			}
			return ""
		},
		Stdin:  strings.NewReader("hello"),
		Stdout: stdout,
		Stderr: stderr,
	}
}

// The discovery report is the entire deliverable of this instrument. A run that
// could not put it on the channel the caller ASKED for — CTXLOOM_MOCK_REPORT_FILE
// is set precisely when stderr is not trusted to survive — and still exited 0
// would be a success message over zero evidence.
func TestRuntime_ReportFileWriteFailureFailsTheRun(t *testing.T) {
	dir := t.TempDir()
	// A path whose PARENT is a regular file: the write fails with ENOTDIR.
	// Deterministic, and unlike a chmod it does not evaporate when the suite
	// runs as root.
	blocker := filepath.Join(dir, "notadir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr, stdout strings.Builder
	rt := emissionRuntime(t, filepath.Join(blocker, "report.json"), &stderr, &stdout)

	if code := rt.Run(); code == 0 {
		t.Error("the report file could not be written and the run still exited 0 — zero evidence, clean exit")
	}
	if !strings.Contains(stderr.String(), "report file write error") {
		t.Errorf("the failure must be named on stderr, got:\n%s", stderr.String())
	}
	// The channel that DID work still carries the evidence: a broken file
	// channel must not cost the caller the stderr report as well.
	if !strings.Contains(stderr.String(), ReportBegin) {
		t.Error("the stderr report channel was dropped along with the file channel")
	}
}

// The converse: a report file that writes cleanly must leave the exit code to
// the engine outcome, not to the emission.
func TestRuntime_WritableReportFileLeavesExitCodeAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")

	var stderr, stdout strings.Builder
	rt := emissionRuntime(t, path, &stderr, &stdout)

	if code := rt.Run(); code != 0 {
		t.Errorf("exit code = %d, want the engine outcome 0", code)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report file: %v", err)
	}
	if len(b) == 0 {
		t.Error("the report file was created empty")
	}
}
