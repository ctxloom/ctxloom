package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"

	"github.com/ctxloom/ctxloom/internal/core/paths"
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
		{"explicit selection wins", "mock", []string{"claude-code"}, "mock"},
		{"explicit wins even with empty primary", "mock", nil, "mock"},
		{"first primary when none selected", "", []string{"claude-code", "mock"}, "claude-code"},
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
		{"sole secondary engine", nil, []string{"mock"}, "mock"},
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

	if err := writeInitialConfig(appDir, "mock", "copy", ""); err != nil {
		t.Fatalf("writeInitialConfig: %v", err)
	}

	// Directory tree exists.
	for _, dir := range []string{appDir, bundletree.ProjectProfilesDir(t, appDir), authoredV1(appDir)} {
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
	if !strings.Contains(string(cfg), "mock") {
		t.Errorf("config.yaml should mention chosen engine; got:\n%s", cfg)
	}
	if !strings.Contains(string(cfg), "dirty_tree_handler: copy") {
		t.Errorf("config.yaml should carry the interview's dirty_tree_handler answer; got:\n%s", cfg)
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

// TestWriteInitialConfig_WritesTheCommitHandler: init writes the commit
// handler into config.yaml, which is the whole authorization.
func TestWriteInitialConfig_WritesTheCommitHandler(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	if err := writeInitialConfig(appDir, "claude-code", "commit", ""); err != nil {
		t.Fatalf("writeInitialConfig: %v", err)
	}
	cfg, err := os.ReadFile(paths.ConfigPath(appDir))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if !strings.Contains(string(cfg), "dirty_tree_handler: commit") {
		t.Errorf("config.yaml should carry dirty_tree_handler: commit; got:\n%s", cfg)
	}
}

func TestWriteInitialConfig_IsIdempotent(t *testing.T) {
	// Re-running over an existing dir must not error (MkdirAll + overwrite).
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	if err := writeInitialConfig(appDir, "claude-code", "", ""); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writeInitialConfig(appDir, "mock", "", ""); err != nil {
		t.Fatalf("second write should succeed: %v", err)
	}
	cfg, err := os.ReadFile(paths.ConfigPath(appDir))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if !strings.Contains(string(cfg), "mock") {
		t.Errorf("second write should have overwritten engine to mock; got:\n%s", cfg)
	}
}

// authoredV1 is where a fixture must write a FORMAT-V1 authored bundle for the
// project's reader to find it.
//
// paths.LocalBundlesPath is the bundles ROOT — the parent every format root is
// a sibling under — and the reader searches the format roots, never the root
// itself. A fixture that writes straight to the root writes somewhere nothing
// looks: the bundle resolves to nothing, and the command reports success.
func authoredV1(appPath string) string {
	return paths.LocalBundlesPathFor(appPath, paths.LayoutV2)
}

// TestWriteInitialConfig_HeadlessPosture: the interview's headless-posture
// answer reaches config.yaml as the default seed agent's permissions.
func TestWriteInitialConfig_HeadlessPosture(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	if err := writeInitialConfig(appDir, "mock", "", "plan"); err != nil {
		t.Fatalf("writeInitialConfig: %v", err)
	}
	cfg, err := os.ReadFile(paths.ConfigPath(appDir))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if !strings.Contains(string(cfg), "mode: plan") {
		t.Errorf("config.yaml should carry the headless posture on the seed agent; got:\n%s", cfg)
	}
}
