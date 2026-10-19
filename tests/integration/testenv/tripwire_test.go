package testenv

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tripRoots stand in for a suite's temp roots: a name prefix and a directory.
var tripRoots = []string{"/tmp/ctxloom-integration-", "/var/tmp/ctxloom-gotmp/"}

// tripSecret sits in the fake claude config wherever a credential would, so a
// test can prove no value ever reaches a report.
const tripSecret = "sk-tripwire-secret-never-printed"

func writeTripFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeClaudeConfig writes a claude config at path whose projects map holds
// keys, beside an account block carrying tripSecret.
func writeClaudeConfig(t *testing.T, path string, keys ...string) {
	t.Helper()
	projects := map[string]any{}
	for _, k := range keys {
		projects[k] = map[string]any{"allowedTools": []string{}, "note": tripSecret}
	}
	raw, err := json.Marshal(map[string]any{
		"oauthAccount": map[string]any{"accessToken": tripSecret},
		"projects":     projects,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeTripFile(t, path, string(raw))
}

func writeRegistry(t *testing.T, home string, projectPaths ...string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("schema_version: 1\nprojects:\n")
	for i, p := range projectPaths {
		b.WriteString("  - project_id: id" + string(rune('a'+i)) + "\n    path: " + p + "\n")
	}
	writeTripFile(t, filepath.Join(home, ".ctxloom", "projects", "index.yaml"), b.String())
}

func writeSession(t *testing.T, home, harp, projectDir string) {
	t.Helper()
	writeTripFile(t, filepath.Join(home, ".ctxloom", "sessions", harp, "session.yaml"),
		"schema_version: 1\nbackend: claude-code\nproject_dir: "+projectDir+"\n")
}

func mkdirTrip(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// seededHome is a home that already holds one entry in every source, including
// an OLD trace under a temp root — the shape of a developer's real home after
// the leaks this tripwire exists for — so only what a run ADDS can count.
func seededHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	old := "/tmp/ctxloom-integration-1/project"
	writeClaudeConfig(t, filepath.Join(home, ".claude.json"), "/home/dev/work", old)
	writeClaudeConfig(t, filepath.Join(home, ".claude", ".claude.json"), "/home/dev/work")
	mkdirTrip(t, filepath.Join(home, ".claude", "projects", claudeDirName(old)))
	writeSession(t, home, "old-test-harp", old)
	writeSession(t, home, "real-harp", "/home/dev/work")
	mkdirTrip(t, filepath.Join(home, ".ctxloom", "coord", "old-test-harp"))
	writeRegistry(t, home, "/home/dev/work", old)
	return home
}

// allHomeSources is every source readHomeState reads.
var allHomeSources = []homeSource{srcClaudeProjects, srcClaudeConfigProjects, srcClaudeTranscripts, srcSessions, srcCoord, srcProjectRegistry}

// tripChanges are one new entry per source, made with the given project path.
func tripChanges(t *testing.T, home string) map[homeSource]func(projectPath string) {
	return map[homeSource]func(string){
		srcClaudeProjects: func(p string) {
			writeClaudeConfig(t, filepath.Join(home, ".claude.json"), "/home/dev/work", "/tmp/ctxloom-integration-1/project", p)
		},
		srcClaudeConfigProjects: func(p string) {
			writeClaudeConfig(t, filepath.Join(home, ".claude", ".claude.json"), "/home/dev/work", p)
		},
		srcClaudeTranscripts: func(p string) {
			mkdirTrip(t, filepath.Join(home, ".claude", "projects", claudeDirName(p)))
		},
		srcSessions: func(p string) { writeSession(t, home, "new-harp", p) },
		srcCoord: func(p string) {
			// The coordinator root is new; its session record says where it
			// ran (written as part of the same run).
			writeSession(t, home, "coord-harp", p)
			mkdirTrip(t, filepath.Join(home, ".ctxloom", "coord", "coord-harp"))
		},
		srcProjectRegistry: func(p string) {
			writeRegistry(t, home, "/home/dev/work", "/tmp/ctxloom-integration-1/project", p)
		},
	}
}

func TestHomeLeaks_ANewEntryUnderATempRootIsALeak(t *testing.T) {
	for _, leaked := range []string{"/tmp/ctxloom-integration-42/project", "/var/tmp/ctxloom-gotmp/TestX123/001"} {
		for _, src := range allHomeSources {
			t.Run(string(src)+" "+leaked, func(t *testing.T) {
				home := seededHome(t)
				before := readHomeState(home)
				tripChanges(t, home)[src](leaked)
				// A new coordinator root comes with its session record, so
				// that change reports under both; what matters is that the
				// source changed is among them.
				leaks := homeLeaks(home, before, readHomeState(home), tripRoots)
				attributed := false
				for _, l := range leaks {
					attributed = attributed || strings.HasPrefix(l, string(src)+": ")
				}
				if !attributed {
					t.Fatalf("a new %s entry for %s was not reported; leaks: %v", src, leaked, leaks)
				}
			})
		}
	}
}

// Real sessions run beside a suite: what they add is not the suite's.
func TestHomeLeaks_ANewUnrelatedEntryIsNotALeak(t *testing.T) {
	for _, src := range allHomeSources {
		t.Run(string(src), func(t *testing.T) {
			home := seededHome(t)
			before := readHomeState(home)
			tripChanges(t, home)[src]("/home/dev/another-project")
			if leaks := homeLeaks(home, before, readHomeState(home), tripRoots); len(leaks) != 0 {
				t.Fatalf("an unrelated new %s entry was reported: %v", src, leaks)
			}
		})
	}
}

// CI has no real home state at all; nothing to read is nothing leaked.
func TestHomeLeaks_MissingFilesAreNotALeak(t *testing.T) {
	for name, home := range map[string]string{
		"an empty home":      t.TempDir(),
		"a home that is not": filepath.Join(t.TempDir(), "absent"),
	} {
		before := readHomeState(home)
		after := readHomeState(home)
		if len(before.unreadable)+len(after.unreadable) != 0 {
			t.Errorf("%s: missing files were reported unreadable: %v %v", name, before.unreadable, after.unreadable)
		}
		if leaks := homeLeaks(home, before, after, tripRoots); len(leaks) != 0 {
			t.Errorf("%s: reported %v", name, leaks)
		}
	}
}

// A source that appears only during the run is compared against nothing: a
// first test trace on a clean machine is still a leak.
func TestHomeLeaks_ASourceCreatedDuringTheRunIsChecked(t *testing.T) {
	home := t.TempDir()
	before := readHomeState(home)
	writeClaudeConfig(t, filepath.Join(home, ".claude.json"), "/tmp/ctxloom-integration-9/project")
	if leaks := homeLeaks(home, before, readHomeState(home), tripRoots); len(leaks) != 1 {
		t.Fatalf("leaks = %v, want the one new key", leaks)
	}
}

// A source the first reading could not parse is skipped, not compared: every
// name in it would look new, and the old traces a real home already holds
// would fail every run.
func TestHomeLeaks_ASourceUnreadableBeforeIsSkipped(t *testing.T) {
	home := seededHome(t)
	path := filepath.Join(home, ".claude.json")
	writeTripFile(t, path, "{not json "+tripSecret)
	before := readHomeState(home)
	if before.unreadable[srcClaudeProjects] == nil {
		t.Fatal("a corrupt claude config was not recorded as unreadable")
	}
	if strings.Contains(before.unreadable[srcClaudeProjects].Error(), tripSecret) {
		t.Fatal("the unreadable error quotes the file's contents")
	}
	writeClaudeConfig(t, path, "/home/dev/work", "/tmp/ctxloom-integration-1/project")
	if leaks := homeLeaks(home, before, readHomeState(home), tripRoots); len(leaks) != 0 {
		t.Fatalf("an unreadable-before source was compared: %v", leaks)
	}
}

// The wiring: readings either side of the run, and a passing run that leaked
// becomes a failing one whose report names the entry and never a value.
func TestGuardHome_FailsAPassingRunThatLeaked(t *testing.T) {
	home := seededHome(t)
	var out bytes.Buffer
	code := guardHome(home, tripRoots, func() int {
		writeClaudeConfig(t, filepath.Join(home, ".claude.json"), "/home/dev/work", "/tmp/ctxloom-integration-1/project", "/tmp/ctxloom-integration-77/project")
		return 0
	}, &out)
	if code == 0 {
		t.Fatalf("a run that leaked into the home exited 0; report:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "/tmp/ctxloom-integration-77/project") {
		t.Errorf("the report does not name the leaked entry:\n%s", out.String())
	}
	if strings.Contains(out.String(), tripSecret) {
		t.Errorf("the report printed a value from the config:\n%s", out.String())
	}
}

func TestGuardHome_KeepsTheRunsOwnCodeWhenNothingLeaked(t *testing.T) {
	for _, want := range []int{0, 3} {
		home := seededHome(t)
		var out bytes.Buffer
		got := guardHome(home, tripRoots, func() int {
			writeSession(t, home, "concurrent-real-harp", "/home/dev/work")
			return want
		}, &out)
		if got != want {
			t.Errorf("guardHome = %d, want the run's own %d; report:\n%s", got, want, out.String())
		}
	}
}

// The roots cover where the suites really put things: a TestEnvironment's
// Root and a test's t.TempDir().
func TestSuiteTempRoots_CoverWhereTheSuitesWrite(t *testing.T) {
	root, err := os.MkdirTemp("", envRootPattern)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	roots := suiteTempRoots()
	for name, dir := range map[string]string{"a TestEnvironment root": filepath.Join(root, "project"), "t.TempDir()": t.TempDir()} {
		if !underTempRoot(dir, roots) {
			t.Errorf("%s %s is under none of %v", name, dir, roots)
		}
	}
	if underTempRoot("/home/dev/work", roots) {
		t.Errorf("an ordinary project path counts as a temp root: %v", roots)
	}
}
