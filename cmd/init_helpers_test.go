package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/paths"
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
// (carrying the chosen engine) and remotes.yaml (default remotes).
func TestWriteInitialConfig(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")

	if err := writeInitialConfig(appDir, "antigravity"); err != nil {
		t.Fatalf("writeInitialConfig: %v", err)
	}

	// Directory tree exists.
	for _, dir := range []string{appDir, filepath.Join(appDir, paths.ProfilesDir), paths.BundlesPath(appDir)} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Errorf("expected directory %s to exist (err=%v)", dir, err)
		}
	}

	// config.yaml exists and reflects the chosen engine.
	cfg, err := os.ReadFile(paths.ConfigPath(appDir))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if !strings.Contains(string(cfg), "antigravity") {
		t.Errorf("config.yaml should mention chosen engine; got:\n%s", cfg)
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

func TestWriteInitialConfig_IsIdempotent(t *testing.T) {
	// Re-running over an existing dir must not error (MkdirAll + overwrite).
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	if err := writeInitialConfig(appDir, "claude-code"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writeInitialConfig(appDir, "antigravity"); err != nil {
		t.Fatalf("second write should succeed: %v", err)
	}
	cfg, err := os.ReadFile(paths.ConfigPath(appDir))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if !strings.Contains(string(cfg), "antigravity") {
		t.Errorf("second write should have overwritten engine to antigravity; got:\n%s", cfg)
	}
}
