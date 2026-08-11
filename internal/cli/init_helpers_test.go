package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// pickDefaultEngine is the shared fallback used wherever runInit needs a
// concrete engine: an explicit selection wins; otherwise the first available
// primary engine; otherwise the hardcoded "claude-code" so init never dead-ends.
func TestPickDefaultEngine(t *testing.T) {
	tests := []struct {
		name     string
		selected string
		primary  []string
		want     string
	}{
		{"explicit selection wins", "antigravity", []string{"claude-code"}, "antigravity"},
		{"explicit wins even with empty primary", "antigravity", nil, "antigravity"},
		{"first primary when none selected", "", []string{"claude-code", "antigravity"}, "claude-code"},
		{"hardcoded fallback when nothing available", "", nil, "claude-code"},
		{"hardcoded fallback with empty slice", "", []string{}, "claude-code"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pickDefaultEngine(tt.selected, tt.primary); got != tt.want {
				t.Fatalf("pickDefaultEngine(%q, %v) = %q, want %q", tt.selected, tt.primary, got, tt.want)
			}
		})
	}
}

// selectSoleEngine announces and returns the single available engine. It must
// consult primary before indexing secondary: when the only engine is a primary
// one (the common claude-code-only setup) secondary is empty, and indexing it
// unconditionally panicked.
func TestSelectSoleEngine(t *testing.T) {
	tests := []struct {
		name      string
		primary   []string
		secondary []string
		want      string
	}{
		{"sole primary engine", []string{"claude-code"}, nil, "claude-code"},
		{"sole secondary engine", nil, []string{"codex"}, "codex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectSoleEngine(tt.primary, tt.secondary); got != tt.want {
				t.Fatalf("selectSoleEngine(%v, %v) = %q, want %q", tt.primary, tt.secondary, got, tt.want)
			}
		})
	}
}

// writeInitialConfig creates the .ctxloom skeleton: the dir tree plus config.yaml
// (carrying the chosen engine and the interview's dirty-tree answer) and
// remotes.yaml (default remotes).
func TestWriteInitialConfig(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")

	if err := writeInitialConfig(appDir, "codex", "copy", false); err != nil {
		t.Fatalf("writeInitialConfig: %v", err)
	}

	// Directory tree exists.
	for _, dir := range []string{appDir, filepath.Join(appDir, paths.ProfilesDir), paths.LocalBundlesPath(appDir)} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Errorf("expected directory %s to exist (err=%v)", dir, err)
		}
	}

	// config.yaml exists and reflects the chosen engine and dirty-tree answer.
	cfg, err := os.ReadFile(paths.ConfigPath(appDir))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if !strings.Contains(string(cfg), "codex") {
		t.Errorf("config.yaml should mention chosen engine; got:\n%s", cfg)
	}
	if !strings.Contains(string(cfg), "dirty_tree_handler: copy") {
		t.Errorf("config.yaml should carry the interview's dirty_tree_handler answer; got:\n%s", cfg)
	}
	if strings.Contains(string(cfg), "dirty_tree_commit_ack") {
		t.Errorf("dirty_tree_commit_ack must never appear in config.yaml at all — it moved to its own state-store file; got:\n%s", cfg)
	}
	if config.DirtyTreeCommitAcknowledged(nil, appDir) {
		t.Error("no acknowledgement was granted (dirty-tree answer wasn't \"commit\"), so DirtyTreeCommitAcknowledged must report false")
	}

	// remotes.yaml exists and is non-empty.
	rem, err := os.ReadFile(paths.RemotesPath(appDir))
	if err != nil {
		t.Fatalf("read remotes.yaml: %v", err)
	}
	if len(rem) == 0 {
		t.Error("remotes.yaml should not be empty")
	}
}

// TestWriteInitialConfig_DirtyTreeCommitAnswerWritesAckTrue is
// TestWriteInitialConfig's counterpart for the "commit" answer specifically:
// it is the only one of the four that must also record the dirty-tree-commit
// acknowledgement — in its OWN state-store file, never in config.yaml (see
// config.SetDirtyTreeCommitAck).
func TestWriteInitialConfig_DirtyTreeCommitAnswerWritesAckTrue(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	if err := writeInitialConfig(appDir, "claude-code", "commit", true); err != nil {
		t.Fatalf("writeInitialConfig: %v", err)
	}
	cfg, err := os.ReadFile(paths.ConfigPath(appDir))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if !strings.Contains(string(cfg), "dirty_tree_handler: commit") {
		t.Errorf("config.yaml should carry dirty_tree_handler: commit; got:\n%s", cfg)
	}
	if strings.Contains(string(cfg), "dirty_tree_commit_ack") {
		t.Errorf("dirty_tree_commit_ack must never appear in config.yaml, even for the commit answer; got:\n%s", cfg)
	}
	if !config.DirtyTreeCommitAcknowledged(nil, appDir) {
		t.Error("the commit answer should have recorded the acknowledgement in its own state store")
	}
	ackData, err := os.ReadFile(paths.DirtyTreeCommitAckPath(appDir))
	if err != nil {
		t.Fatalf("read dirty-tree-commit ack store: %v", err)
	}
	if len(ackData) == 0 {
		t.Error("dirty-tree-commit ack store exists but is empty — the silent-zero-bytes shape this project is characteristically buggy about")
	}
}

func TestWriteInitialConfig_IsIdempotent(t *testing.T) {
	// Re-running over an existing dir must not error (MkdirAll + overwrite).
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	if err := writeInitialConfig(appDir, "claude-code", "", false); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writeInitialConfig(appDir, "codex", "", false); err != nil {
		t.Fatalf("second write should succeed: %v", err)
	}
	cfg, err := os.ReadFile(paths.ConfigPath(appDir))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if !strings.Contains(string(cfg), "codex") {
		t.Errorf("second write should have overwritten engine to codex; got:\n%s", cfg)
	}
}

// TestApplyInitHooks_EmptyBackendListIsNotSuccess pins the PAYLOAD
// of init's hook-apply report. `Applied hooks for: []` is this project's
// signature silent no-op: a success sentence whose payload is empty. Registering
// zero backends means no engine settings surface was written at all, so nothing
// will ever reach ctxloom's MCP server or context hook — the one outcome init
// must not report as done.
func TestApplyInitHooks_EmptyBackendListIsNotSuccess(t *testing.T) {
	appDir := filepath.Join(testsupport.Isolate(t), ".ctxloom")

	orig := applyHooksFn
	applyHooksFn = func(context.Context, operations.ApplyHooksRequest) (*operations.ApplyHooksResult, error) {
		return &operations.ApplyHooksResult{Status: "ok", Backends: nil}, nil
	}
	t.Cleanup(func() { applyHooksFn = orig })

	var warnings strings.Builder
	t.Cleanup(clidiag.SetSink(&warnings))

	out := captureStdout(t, func() { applyInitHooks(&cobra.Command{}, appDir) })

	assert.NotContains(t, out, "Applied hooks for",
		"an apply that touched no backend must not print a success line")
	assert.Contains(t, warnings.String(), "no backends",
		"an apply that touched no backend must say so on the diagnostic channel")
}

// TestApplyInitHooks_ReportsTheBackendsItWrote is the counterpart: a real
// payload still gets the success line, unchanged.
func TestApplyInitHooks_ReportsTheBackendsItWrote(t *testing.T) {
	appDir := filepath.Join(testsupport.Isolate(t), ".ctxloom")

	orig := applyHooksFn
	applyHooksFn = func(context.Context, operations.ApplyHooksRequest) (*operations.ApplyHooksResult, error) {
		return &operations.ApplyHooksResult{Status: "ok", Backends: []string{"claude-code", "codex"}}, nil
	}
	t.Cleanup(func() { applyHooksFn = orig })

	var warnings strings.Builder
	t.Cleanup(clidiag.SetSink(&warnings))

	out := captureStdout(t, func() { applyInitHooks(&cobra.Command{}, appDir) })

	assert.Contains(t, out, "Applied hooks for: [claude-code codex]")
	assert.Empty(t, warnings.String())
}
