//go:build mutation

package mutation

import (
	"encoding/json"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// gomutantsOnPath is the real gomutants, or a failure naming how to get it.
func gomutantsOnPath(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("gomutants")
	if err != nil {
		t.Fatalf("gomutants is not on PATH (%v) — `just test-mutation-install`", err)
	}
	return bin
}

// diffModule is a git module holding files at base under the project's own
// .gomutants.yml, committed as main.
func diffModule(t *testing.T, files map[string]string) string {
	t.Helper()
	// The planner is built and exec'd, so name its sources: an edit to them
	// must re-run this test, not replay a cached pass.
	repoInput(t, "scripts/mutshard/*.go", ".gomutants.yml")
	cfg, err := os.ReadFile(filepath.Join(repoRootFromTest(t), ".gomutants.yml"))
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	writeFile(t, src, "go.mod", "module lab\n\ngo 1.24\n")
	writeFile(t, src, ".gomutants.yml", string(cfg))
	for rel, body := range files {
		writeFile(t, src, rel, body)
	}
	mustGit(t, src, "init", "-q", "-b", "main")
	mustGit(t, src, "add", "-A")
	mustGit(t, src, "commit", "-q", "-m", "base")
	return src
}

// TestPackageDiff_FindsAKnownMutant drives the REAL gomutants through the plan
// `just test-mutation-pkg PKG --diff BASE` runs (mutshard -pkg) over a module
// whose diff adds one conditional in the package and one arithmetic change
// outside it. The package's mutants must be found, one at least killed, and
// nothing outside the package mutated.
func TestPackageDiff_FindsAKnownMutant(t *testing.T) {
	gomutants := gomutantsOnPath(t)
	src := diffModule(t, map[string]string{
		"sub/sub.go":          "package sub\n\nfunc Pos(x int) bool { return true }\n",
		"sub/sub_test.go":     "package sub\n\nimport \"testing\"\n\nfunc TestPos(t *testing.T) {\n\tif !Pos(1) || Pos(0) {\n\t\tt.Fatal(\"pos\")\n\t}\n}\n",
		"other/other.go":      "package other\n\nfunc Add(a, b int) int { return a }\n",
		"other/other_test.go": "package other\n",
	})
	writeFile(t, src, "sub/sub.go", "package sub\n\nfunc Pos(x int) bool {\n\tif x > 0 {\n\t\treturn true\n\t}\n\treturn false\n}\n")
	writeFile(t, src, "other/other.go", "package other\n\nfunc Add(a, b int) int { return a + b }\n")

	t.Setenv("TMPDIR", t.TempDir())
	mutshard := buildMutshard(t)
	reports := t.TempDir()
	runIn(t, src, mutshard, "run", "-scope", "diff:main", "-pkg", "sub", "-out", reports, "--", gomutants)
	verdict := runIn(t, src, mutshard, "aggregate", "-scope", "diff:main", "-pkg", "sub", "-reports", reports)

	m := regexp.MustCompile(`Killed: (\d+), Lived: (\d+)`).FindStringSubmatch(verdict)
	if m == nil || m[1] == "0" {
		t.Fatalf("the conditional the diff added in sub/ was not killed:\n%s", verdict)
	}
	lines := mutatedLines(t, filepath.Join(reports, "shard-0.json"))
	if len(lines) == 0 {
		t.Fatal("the shard report scored no mutant")
	}
	for f := range lines {
		if f != "sub/sub.go" {
			t.Errorf("gomutants mutated %s, outside the package", f)
		}
	}
}

// buildMutshard builds scripts/mutshard and returns the binary.
func buildMutshard(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "mutshard")
	build := exec.Command("go", "build", "-o", bin, "./scripts/mutshard")
	build.Dir = repoRootFromTest(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mutshard: %v\n%s", err, out)
	}
	return bin
}

// runIn runs bin in dir and returns its output, failing t on any error.
func runIn(t *testing.T, dir, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", filepath.Base(bin), args, err, out)
	}
	return string(out)
}

// mutatedLines is, per module-relative file, the set of lines on which the
// shard report's gomutants result tested a mutant. The file comes from the
// mutant's id, which leads with the module-relative path.
func mutatedLines(t *testing.T, shardReport string) map[string]map[int]bool {
	t.Helper()
	b, err := os.ReadFile(shardReport)
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Gomutants struct {
			Files []struct {
				Mutations []struct {
					ID   string `json:"id"`
					Line int    `json:"line"`
				} `json:"mutations"`
			} `json:"files"`
		} `json:"gomutants"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("%s: %v", shardReport, err)
	}
	lines := map[string]map[int]bool{}
	for _, f := range r.Gomutants.Files {
		for _, m := range f.Mutations {
			file, _, _ := strings.Cut(m.ID, ":")
			if path.Ext(file) != ".go" {
				t.Fatalf("mutant id %q does not lead with its file", m.ID)
			}
			if lines[file] == nil {
				lines[file] = map[int]bool{}
			}
			lines[file][m.Line] = true
		}
	}
	return lines
}

// TestDiff_MutatesExactlyTheChangedLines drives the REAL gomutants through the
// plan `just test-mutation-diff BASE` runs, over a diff whose two edits sit
// close enough for git's default three lines of context to merge them into one
// hunk. A tool that took one line range per hunk would score the unchanged
// line between the edits; gomutants diffs with --unified=0. Both edits must be
// mutated and the line between them must not.
func TestDiff_MutatesExactlyTheChangedLines(t *testing.T) {
	gomutants := gomutantsOnPath(t)
	src := diffModule(t, map[string]string{
		"sub/sub.go": "package sub\n\nfunc F(a, b int) []int {\n\treturn []int{\n\t\ta,\n\t\ta - b,\n\t\tb,\n\t\tb,\n\t}\n}\n",
		"sub/sub_test.go": "package sub\n\nimport (\n\t\"reflect\"\n\t\"testing\"\n)\n\n" +
			"func TestF(t *testing.T) {\n\tif got := F(3, 2); !reflect.DeepEqual(got, []int{4, 1, 2, 4}) {\n\t\tt.Fatal(got)\n\t}\n}\n",
	})
	// Lines 5 and 8 change; line 6 (mutable) and line 7 do not.
	const firstEdit, unchanged, secondEdit = 5, 6, 8
	writeFile(t, src, "sub/sub.go", "package sub\n\nfunc F(a, b int) []int {\n\treturn []int{\n\t\ta + 1,\n\t\ta - b,\n\t\tb,\n\t\tb * 2,\n\t}\n}\n")

	t.Setenv("TMPDIR", t.TempDir())
	mutshard := buildMutshard(t)
	reports := t.TempDir()
	runIn(t, src, mutshard, "run", "-scope", "diff:main", "-out", reports, "--", gomutants)

	lines := mutatedLines(t, filepath.Join(reports, "shard-0.json"))["sub/sub.go"]
	for _, l := range []int{firstEdit, secondEdit} {
		if !lines[l] {
			t.Errorf("changed line %d was not mutated (mutated lines: %v)", l, lines)
		}
	}
	if lines[unchanged] {
		t.Errorf("unchanged line %d between the edits was mutated (mutated lines: %v)", unchanged, lines)
	}
}
