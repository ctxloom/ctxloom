package paths

import "testing"

// TestLogPathParityWithModeSwitch pins the two mode-specific resolvers to the
// mode switch they replace, over the inputs each one guards: a valid value, an
// empty one, and a crafted id that must never become a path.
func TestLogPathParityWithModeSwitch(t *testing.T) {
	for _, repoRoot := range []string{"/proj", "", "rel/root"} {
		wantPath, wantErr := TasksLogPath(ModeRepo, repoRoot, "")
		gotPath, gotErr := RepoTasksLogPath(repoRoot)
		if gotPath != wantPath || (gotErr == nil) != (wantErr == nil) {
			t.Errorf("RepoTasksLogPath(%q) = %q,%v; switch = %q,%v", repoRoot, gotPath, gotErr, wantPath, wantErr)
		}
	}
	for _, id := range []string{"swift-amber-falcon", "", "../../escape", "a/b"} {
		wantPath, wantErr := TasksLogPath(ModeHome, "", id)
		gotPath, gotErr := HomeTasksLogPath(id)
		if gotPath != wantPath || (gotErr == nil) != (wantErr == nil) {
			t.Errorf("HomeTasksLogPath(%q) = %q,%v; switch = %q,%v", id, gotPath, gotErr, wantPath, wantErr)
		}
		// The zero Mode value must behave exactly like ModeHome — every
		// caller that predates homing-mode selection relies on it.
		emptyPath, emptyErr := TasksLogPath("", "", id)
		if emptyPath != wantPath || (emptyErr == nil) != (wantErr == nil) {
			t.Errorf("TasksLogPath(\"\", %q) = %q,%v; ModeHome = %q,%v", id, emptyPath, emptyErr, wantPath, wantErr)
		}
	}
}
