//go:build acceptance

package acceptance

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// The record names every surface the run delivered, most of them under the
// session store. Only the named file, and only under that store, is a
// delivery of THAT file into a session home — the scenario alone cannot
// tell, because its run delivers the named file there too.
func TestRecordedSessionDeliveries_OnlyTheNamedFileUnderTheSessionStore(t *testing.T) {
	root := t.TempDir()
	w := &World{
		env:  &testenv.TestEnvironment{HomeDir: filepath.Join(root, "home")},
		mock: &testenv.MockLM{RecordedInputPath: filepath.Join(root, "record.txt")},
	}
	store := sessionStoreRoot(w)
	rec := "=== Arguments ===\n" +
		mock.RecordContextFile + "=" + filepath.Join(store, "harp-a", "MOCK_CONTEXT.md") + "\n" +
		mock.RecordMCPFile + "=" + filepath.Join(store, "harp-a", "mcp.json") + "\n" +
		mock.RecordSettingsFile + "=" + filepath.Join(root, "project", "MOCK_CONTEXT.md") + "\n" +
		"cwd=" + filepath.Join(store, "harp-a", "MOCK_CONTEXT.md") + "\n"
	if err := os.WriteFile(w.mock.RecordedInputPath, []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := recordedSessionDeliveries(w, "MOCK_CONTEXT.md")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join("harp-a", "MOCK_CONTEXT.md")}
	if !slices.Equal(got, want) {
		t.Fatalf("deliveries = %v, want %v", got, want)
	}
}

// A mock that never ran delivered nothing — that is the dry run's honest
// state, not an error.
func TestRecordedSessionDeliveries_NeverInvokedIsNone(t *testing.T) {
	root := t.TempDir()
	w := &World{
		env:  &testenv.TestEnvironment{HomeDir: root},
		mock: &testenv.MockLM{RecordedInputPath: filepath.Join(root, "absent.txt")},
	}
	got, err := recordedSessionDeliveries(w, "MOCK_CONTEXT.md")
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want none, nil", got, err)
	}
}
