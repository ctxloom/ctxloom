package taskstest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The environment route has to be drivable RED against directories this test
// owns: a stand-in temp root nested in a t.TempDir, so "outside the temp root"
// is expressible without naming anybody's real home.
func TestEnvAppDirEscapeError_CatchesAnInheritedAppDirPath(t *testing.T) {
	base := t.TempDir()
	tempRoot := filepath.Join(base, "tmp")
	realStore := filepath.Join(base, "realhome", appDirName, "cache", "companions", "digest")
	sandboxed := filepath.Join(tempRoot, "home", appDirName, "cache")
	pathList := func(entries ...string) string { return strings.Join(entries, string(os.PathListSeparator)) }

	err := envAppDirEscapeError([]string{"PATH=" + pathList(realStore, "/usr/bin")}, []string{tempRoot})
	if err == nil {
		t.Fatal("a PATH entry inside an app dir outside the temp root must be reported: a companion lookup through it reads the real store")
	}
	if !strings.Contains(err.Error(), "$PATH") || !strings.Contains(err.Error(), realStore) {
		t.Errorf("the message must name the variable and the offending entry; got %v", err)
	}

	for name, environ := range map[string][]string{
		"an app dir inside the temp root":      {"PATH=" + pathList(sandboxed, "/usr/bin")},
		"no app-dir component":                 {"PATH=/usr/bin", "GOTMPDIR=" + filepath.Join(base, "ctxloom-gotmp")},
		"a relative entry (the sandboxed cwd)": {"X=" + filepath.Join(appDirName, "cache")},
		"a variable with an empty value":       {"EMPTY="},
	} {
		if err := envAppDirEscapeError(environ, []string{tempRoot}); err != nil {
			t.Errorf("%s must be accepted: %v", name, err)
		}
	}
}

func TestScrubAppDirEnv_DropsOnlyTheEscapingEntries(t *testing.T) {
	// Never stat'd or opened: the scrub reads the string, nothing else.
	escaping := filepath.Join(string(filepath.Separator), "qhm-nonexistent", appDirName, "cache", "companions", "digest")
	keep := t.TempDir()
	t.Setenv("PATH", strings.Join([]string{escaping, keep}, string(os.PathListSeparator)))
	t.Setenv("QHM_APPDIR_SCALAR", escaping)

	if err := ScrubAppDirEnv(); err != nil {
		t.Fatal(err)
	}

	if got := os.Getenv("PATH"); got != keep {
		t.Errorf("PATH must keep its other directories and lose only the escaping one; got %q", got)
	}
	if v, ok := os.LookupEnv("QHM_APPDIR_SCALAR"); ok {
		t.Errorf("a variable left with nothing must be unset; still %q", v)
	}
	if err := EnvAppDirEscapeError(); err != nil {
		t.Errorf("after the scrub the guard must hold: %v", err)
	}
}
