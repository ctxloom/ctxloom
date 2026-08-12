package paths

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestEngineStateHome_JoinsUnderTheStateTier pins the ONE location the
// engine-home policy names: when ctxloom points an engine's config home at a
// project-scoped path, that home is <appPath>/state/engines/<engine>. Both the
// run path (codex's resolveCodexProjectDir) and every static writer resolve
// through this function, so the join is the single fact they agree on.
func TestEngineStateHome_JoinsUnderTheStateTier(t *testing.T) {
	got := EngineStateHome("/proj/.ctxloom", "codex")
	want := filepath.Join("/proj/.ctxloom", "state", "engines", "codex")
	if got != want {
		t.Errorf("EngineStateHome() = %q, want %q", got, want)
	}
}

// TestEngineStateHome_IsUnderStatePath keeps the helper anchored to the tier it
// documents rather than to a string that merely happens to spell it today: a
// home holding seeded credentials must stay inside the gitignored,
// unrebuildable tier, which is what makes the single .ctxloom/state/ ignore
// rule cover a seeded auth.json.
func TestEngineStateHome_IsUnderStatePath(t *testing.T) {
	app := "/proj/.ctxloom"
	if got, want := EngineStateHome(app, "codex"), filepath.Join(StatePath(app), EnginesDir, "codex"); got != want {
		t.Errorf("EngineStateHome() = %q, want it under StatePath(): %q", got, want)
	}
}

// TestEngineStateHome_SeparatesEngines proves the engine name is a real
// component and not decoration: two engines must not share one home, or a
// second home-keyed engine would inherit the first's credentials and config.
func TestEngineStateHome_SeparatesEngines(t *testing.T) {
	app := "/proj/.ctxloom"
	if EngineStateHome(app, "codex") == EngineStateHome(app, "somefutureengine") {
		t.Errorf("EngineStateHome() returns the same path for two different engines")
	}
}

// TestLayout_EngineStateHomeIsTierLocal pins the doctor-walked classification
// row: a seeded engine home is gitignored AND unrebuildable (no command
// reconstructs a credential copy or a user's hand-edited config.toml), so it is
// TierLocal with an empty Rebuild.
func TestLayout_EngineStateHomeIsTierLocal(t *testing.T) {
	want := filepath.Join(AppDirName, StateDir, EnginesDir)
	for _, e := range Layout() {
		if e.Rel != want {
			continue
		}
		if e.Tier != TierLocal {
			t.Errorf("%q has tier %s, want %s", want, e.Tier, TierLocal)
		}
		if e.Rebuild != "" {
			t.Errorf("%q declares Rebuild %q; nothing rebuilds a seeded engine home", want, e.Rebuild)
		}
		return
	}
	t.Errorf("Layout() does not list %q at all", want)
}

// TestLayout_EngineStateHomeRowCoversEveryEngine is the check that keeps the
// classification row a PARENT rather than a per-engine enumeration: Layout()
// lists state/engines once, so a newly home-controlled engine (claude-code and
// kiro joined codex) inherits the TierLocal classification with no new row. If
// Layout ever starts enumerating engines individually, this goes red and the
// missing rows have to be added deliberately instead of being silently
// unclassified — an unclassified state directory is one doctor never warns
// about losing.
func TestLayout_EngineStateHomeRowCoversEveryEngine(t *testing.T) {
	row := filepath.Join(AppDirName, StateDir, EnginesDir)
	for _, engine := range []string{"codex", "claude-code", "kiro"} {
		home := EngineStateHome(AppDirName, engine)
		if !strings.HasPrefix(home, row+string(filepath.Separator)) {
			t.Errorf("EngineStateHome(%q, %q) = %q is not covered by the Layout row %q", AppDirName, engine, home, row)
		}
	}
}
