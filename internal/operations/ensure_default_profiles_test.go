package operations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// writeHomeConfig points $HOME at a temp dir and writes ~/.ctxloom/config.yaml
// with the given defaults.profiles (none when nil). Returns the home dir.
func writeHomeConfig(t *testing.T, profiles []string) string {
	t.Helper()
	home := testsupport.Isolate(t)

	ctxDir := filepath.Join(home, ".ctxloom")
	if err := os.MkdirAll(ctxDir, 0o755); err != nil {
		t.Fatal(err)
	}

	body := "defaults: {}\n"
	if len(profiles) > 0 {
		body = "defaults:\n  profiles:\n"
		for _, p := range profiles {
			body += "    - " + p + "\n"
		}
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// TestEnsureDefaultProfiles_RespectsProjectDefaults verifies that an explicit
// project defaults.profiles list is left untouched (home is not consulted).
func TestEnsureDefaultProfiles_RespectsProjectDefaults(t *testing.T) {
	// Home defines a different list that must NOT override the project's.
	writeHomeConfig(t, []string{"home/ignored"})

	cfg := &config.Config{
		AppPaths: []string{t.TempDir()},
		Profiles: config.ProfilesConfig{Defaults: []string{"proj/dev"}},
	}

	EnsureDefaultProfiles(cfg)

	got := cfg.ExplicitDefaultProfiles()
	if len(got) != 1 || got[0] != "proj/dev" {
		t.Fatalf("project defaults mutated: got %v, want [proj/dev]", got)
	}
}

// TestEnsureDefaultProfiles_InheritsFromHome verifies that when the project has
// no defaults.profiles, the home config's list becomes the cfg's RUN-ONLY
// inherited defaults: GetDefaultProfiles serves them, but they never appear
// explicit (so first-profile auto-promotion stays armed), nothing is written
// to the project config, and no synthetic profile is created.
func TestEnsureDefaultProfiles_InheritsFromHome(t *testing.T) {
	writeHomeConfig(t, []string{"home/dev", "home/sec"})

	baseDir := t.TempDir()
	cfg := &config.Config{AppPaths: []string{baseDir}}

	EnsureDefaultProfiles(cfg)

	got := cfg.GetDefaultProfiles()
	if len(got) != 2 || got[0] != "home/dev" || got[1] != "home/sec" {
		t.Fatalf("home defaults not inherited: got %v, want [home/dev home/sec]", got)
	}
	// Inherited defaults are run-only: not explicit, so a later cfg.Save()
	// cannot materialize them and auto-promotion is not suppressed.
	if explicit := cfg.ExplicitDefaultProfiles(); len(explicit) != 0 {
		t.Fatalf("inherited defaults leaked into explicit defaults: %v", explicit)
	}

	// The project config dir must NOT have been written (no silent mutation,
	// no synthetic profile file).
	if _, err := os.Stat(filepath.Join(baseDir, "config.yaml")); err == nil {
		t.Fatal("project config.yaml was written; inherit must be in-memory only")
	}
	if entries, err := os.ReadDir(filepath.Join(baseDir, "profiles")); err == nil && len(entries) > 0 {
		t.Fatalf("synthetic profile created: %v", entries)
	}
}

// TestEnsureDefaultProfiles_InheritedDefaultsSurviveUnrelatedSave verifies the
// other half of the run-only contract end to end: after inheriting, an
// unrelated cfg.Save() (e.g. SetDefaultProfile on a different name, sync
// pinning, etc.) must not persist the inherited names into config.yaml.
func TestEnsureDefaultProfiles_InheritedDefaultsSurviveUnrelatedSave(t *testing.T) {
	writeHomeConfig(t, []string{"home/dev"})

	baseDir := t.TempDir()
	cfg := &config.Config{AppPaths: []string{baseDir}}

	EnsureDefaultProfiles(cfg)
	if got := cfg.GetDefaultProfiles(); len(got) != 1 || got[0] != "home/dev" {
		t.Fatalf("home defaults not inherited: got %v", got)
	}

	if err := cfg.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(baseDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if strings.Contains(string(data), "home/dev") {
		t.Fatalf("run-only inherited defaults were persisted:\n%s", data)
	}
}

// TestEnsureDefaultProfiles_ResolvesOnce verifies the lookup runs at most once
// per cfg: after the first call, subsequent calls are read-only fast paths
// (MapProfiles relies on this to keep concurrent member assembly race-free).
func TestEnsureDefaultProfiles_ResolvesOnce(t *testing.T) {
	writeHomeConfig(t, nil) // home exists, no defaults

	cfg := &config.Config{AppPaths: []string{t.TempDir()}}
	EnsureDefaultProfiles(cfg)
	if !cfg.Profiles.InheritedDefaultsResolved() {
		t.Fatal("first call must mark inheritance resolved (even when none found)")
	}
	// Second call must be a no-op (no panic, no re-resolution).
	EnsureDefaultProfiles(cfg)
}

// TestEnsureDefaultProfiles_NoHomeDefaultsDegradesGracefully verifies that when
// neither project nor home defines defaults.profiles, no profile is invented
// and the function returns cleanly (degraded, not crashed).
func TestEnsureDefaultProfiles_NoHomeDefaultsDegradesGracefully(t *testing.T) {
	writeHomeConfig(t, nil) // home config exists but has no defaults.profiles

	baseDir := t.TempDir()
	cfg := &config.Config{AppPaths: []string{baseDir}}

	EnsureDefaultProfiles(cfg)

	if got := cfg.ExplicitDefaultProfiles(); len(got) != 0 {
		t.Fatalf("expected no default profiles, got %v", got)
	}
	// No synthetic "auto"/"general" profile must have been created.
	if entries, err := os.ReadDir(filepath.Join(baseDir, "profiles")); err == nil && len(entries) > 0 {
		t.Fatalf("synthetic profile created: %v", entries)
	}
}

// TestEnsureDefaultProfiles_NilConfigSafe ensures a nil config is a no-op.
func TestEnsureDefaultProfiles_NilConfigSafe(t *testing.T) {
	EnsureDefaultProfiles(nil) // must not panic
}
