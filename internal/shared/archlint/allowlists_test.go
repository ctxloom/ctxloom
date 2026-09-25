package archlint

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/archrules"
)

// TestAllowlists_NameFilesThatExist fails when an allowlist or scope entry
// names a file or directory that is gone.
//
// Every rule's liveness half judges an entry only in a pass that analyzes the
// file it names, and no pass is ever handed a file that does not exist. An
// entry naming a deleted file is therefore invisible to the analyzers: it
// outlives what it exempted, silently, and exempts whatever next takes that
// path. This is the one question about an allowlist that needs the file tree
// rather than a package, so it is asked here rather than by a rule.
func TestAllowlists_NameFilesThatExist(t *testing.T) {
	root := moduleRoot(t)
	for _, list := range []struct {
		name  string
		paths []string
	}{
		{"testSupportImporters", keysOf(testSupportImporters)},
		{"bindSessionAllowedCallers", keysOf(bindSessionAllowedCallers)},
		{"generatedFrameEncoders", keysOf(generatedFrameEncoders)},
		{"frameDeclarers", keysOf(frameDeclarers)},
		{"lockDisciplineScopes", lockDisciplineScopes},
		{"lockDisciplineExemptFiles", keysOf(lockDisciplineExemptFiles)},
		{"writeDisciplineExemptDirs", writeDisciplineExemptDirs},
		{"vocabConversionAllowed", filesOf(vocabConversionAllowed)},
		{"archrules.LockDisciplineAllowed", filesOf(archrules.LockDisciplineAllowed)},
		{"archrules.LedgerDisciplineAllowed", filesOf(archrules.LedgerDisciplineAllowed)},
		{"archrules.WriteDisciplineAllowed", filesOf(archrules.WriteDisciplineAllowed)},
		{"archrules.TestWriteDisciplineAllowed", filesOf(archrules.TestWriteDisciplineAllowed)},
	} {
		for _, rel := range list.paths {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
				t.Errorf("%s names %q, which does not exist — delete the entry, or it will silently "+
					"exempt whatever next takes that path: %v", list.name, rel, err)
			}
		}
	}
}

// moduleRoot walks up from the working directory to the directory holding
// go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

// keysOf returns a map's keys, sorted.
func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// filesOf returns the distinct files named by "file#..." allowlist keys.
func filesOf(m map[string]string) []string {
	seen := map[string]bool{}
	for k := range m {
		file, _, _ := strings.Cut(k, "#")
		seen[file] = true
	}
	return keysOf(seen)
}
