//go:build mutation

package mutation

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

// TestPackageDiff_FindsAKnownMutant drives the REAL gremlins through the plan
// `just test-mutation-pkg PKG --diff BASE` runs (mutshard -pkg, from the module
// root) over a module whose diff adds one conditional in the package and one
// arithmetic change outside it. The package's mutant must be found and killed,
// and nothing outside the package mutated.
//
// It also holds the reason that path exists: gremlins handed the package as
// its target, with the same diff, finds no mutant at all. When a gremlins
// upgrade makes that half fail, the package target works again and the recipe
// can hand it the package directly.
func TestPackageDiff_FindsAKnownMutant(t *testing.T) {
	gremlins, err := exec.LookPath("gremlins")
	if err != nil {
		t.Fatalf("gremlins is not on PATH (%v) — `just test-mutation-install`", err)
	}
	src := packageDiffModule(t)
	t.Setenv("TMPDIR", t.TempDir())
	mutshard := buildMutshard(t)
	reports := t.TempDir()
	runIn(t, src, mutshard, "run", "-scope", "diff:main", "-pkg", "sub", "-out", reports, "--", gremlins)
	verdict := runIn(t, src, mutshard, "aggregate", "-scope", "diff:main", "-pkg", "sub", "-reports", reports)

	m := regexp.MustCompile(`Killed: (\d+), Lived: (\d+)`).FindStringSubmatch(verdict)
	if m == nil || m[1] == "0" {
		t.Fatalf("the conditional the diff added in sub/ was not killed:\n%s", verdict)
	}
	for _, f := range mutatedFiles(t, filepath.Join(reports, "shard-0.json")) {
		if f != "sub/sub.go" {
			t.Errorf("gremlins mutated %s, outside the package", f)
		}
	}
	if n := packageTargetMeasured(t, gremlins, src); n > 0 {
		t.Errorf("gremlins with the package as its target now measures %d mutant(s) in the diff: the package target works again, so test-mutation-pkg can hand it the package directly", n)
	}
}

// packageDiffModule is a git module whose working tree, against main, adds a
// conditional in sub/ (which TestPos kills) and an arithmetic change in
// other/, under the project's own .gremlins.yaml.
func packageDiffModule(t *testing.T) string {
	t.Helper()
	// The planner is built and exec'd, so name its sources: an edit to them
	// must re-run this test, not replay a cached pass.
	repoInput(t, "scripts/mutshard/*.go", ".gremlins.yaml")
	cfg, err := os.ReadFile(filepath.Join(repoRootFromTest(t), ".gremlins.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	src := fakeModule(t, map[string]string{
		"go.mod":              labGoMod,
		".gremlins.yaml":      string(cfg),
		"sub/sub.go":          "package sub\n\nfunc Pos(x int) bool { return true }\n",
		"sub/sub_test.go":     "package sub\n\nimport \"testing\"\n\nfunc TestPos(t *testing.T) {\n\tif !Pos(1) || Pos(0) {\n\t\tt.Fatal(\"pos\")\n\t}\n}\n",
		"other/other.go":      "package other\n\nfunc Add(a, b int) int { return a }\n",
		"other/other_test.go": "package other\n",
	})
	mustGit(t, src, "init", "-q", "-b", "main")
	mustGit(t, src, "add", "-A")
	mustGit(t, src, "commit", "-q", "-m", "base")
	writeFile(t, src, "sub/sub.go", "package sub\n\nfunc Pos(x int) bool {\n\tif x > 0 {\n\t\treturn true\n\t}\n\treturn false\n}\n")
	writeFile(t, src, "other/other.go", "package other\n\nfunc Add(a, b int) int { return a + b }\n")
	return src
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

// packageTargetMeasured is how many mutants gremlins judges (killed or lived)
// when handed the package as its target over src's diff. Measuring nothing
// scores 0% efficacy, so gremlins exits with its threshold status there; the
// report is what says it measured nothing.
func packageTargetMeasured(t *testing.T, gremlins, src string) int {
	t.Helper()
	out := filepath.Join(t.TempDir(), "pkg-target.json")
	trap := exec.Command(gremlins, "unleash", "./sub", "--diff", "main", "--output", out)
	trap.Dir = src
	b, err := trap.CombinedOutput()
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != gremlinsEfficacyThresholdExit) {
		t.Fatalf("gremlins unleash ./sub --diff main: %v\n%s", err, b)
	}
	rep, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("gremlins wrote no report: %v\n%s", err, b)
	}
	var o struct {
		Killed int `json:"mutants_killed"`
		Lived  int `json:"mutants_lived"`
	}
	if err := json.Unmarshal(rep, &o); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	return o.Killed + o.Lived
}

// mutatedFiles is every file the shard report's gremlins result scored.
func mutatedFiles(t *testing.T, shardReport string) []string {
	t.Helper()
	b, err := os.ReadFile(shardReport)
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Gremlins struct {
			Files []struct {
				Name string `json:"file_name"`
			} `json:"files"`
		} `json:"gremlins"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("%s: %v", shardReport, err)
	}
	var files []string
	for _, f := range r.Gremlins.Files {
		files = append(files, f.Name)
	}
	if len(files) == 0 {
		t.Fatalf("%s scored no file:\n%s", shardReport, b)
	}
	return files
}

// TestDiff_MutatesExactlyTheChangedLines drives the REAL gremlins through the
// plan `just test-mutation-diff BASE` runs, over a diff whose two edits sit
// close enough for git's default three lines of context to merge them into one
// hunk. gremlins derives ONE line range per hunk (start after the leading
// context, length the added-line count), so a merged hunk scores the unchanged
// line between the edits and never reaches the second edit. Both edits must be
// mutated and the line between them must not.
func TestDiff_MutatesExactlyTheChangedLines(t *testing.T) {
	gremlins, err := exec.LookPath("gremlins")
	if err != nil {
		t.Fatalf("gremlins is not on PATH (%v) — `just test-mutation-install`", err)
	}
	repoInput(t, "scripts/mutshard/*.go", ".gremlins.yaml")
	cfg, err := os.ReadFile(filepath.Join(repoRootFromTest(t), ".gremlins.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	src := fakeModule(t, map[string]string{
		"go.mod":         labGoMod,
		".gremlins.yaml": string(cfg),
		"sub/sub.go":     "package sub\n\nfunc F(a, b int) []int {\n\treturn []int{\n\t\ta,\n\t\ta - b,\n\t\tb,\n\t\tb,\n\t}\n}\n",
		"sub/sub_test.go": "package sub\n\nimport (\n\t\"reflect\"\n\t\"testing\"\n)\n\n" +
			"func TestF(t *testing.T) {\n\tif got := F(3, 2); !reflect.DeepEqual(got, []int{4, 1, 2, 4}) {\n\t\tt.Fatal(got)\n\t}\n}\n",
	})
	mustGit(t, src, "init", "-q", "-b", "main")
	mustGit(t, src, "add", "-A")
	mustGit(t, src, "commit", "-q", "-m", "base")
	// Lines 5 and 8 change; line 6 (mutable) and line 7 do not.
	const firstEdit, unchanged, secondEdit = 5, 6, 8
	writeFile(t, src, "sub/sub.go", "package sub\n\nfunc F(a, b int) []int {\n\treturn []int{\n\t\ta + 1,\n\t\ta - b,\n\t\tb,\n\t\tb * 2,\n\t}\n}\n")

	t.Setenv("TMPDIR", t.TempDir())
	mutshard := buildMutshard(t)
	reports := t.TempDir()
	runIn(t, src, mutshard, "run", "-scope", "diff:main", "-out", reports, "--", gremlins)

	lines := mutatedLines(t, filepath.Join(reports, "shard-0.json"), "sub/sub.go")
	for _, l := range []int{firstEdit, secondEdit} {
		if !lines[l] {
			t.Errorf("changed line %d was not mutated (mutated lines: %v)", l, lines)
		}
	}
	if lines[unchanged] {
		t.Errorf("unchanged line %d between the edits was mutated (mutated lines: %v)", unchanged, lines)
	}
}

// mutatedLines is the set of lines of file on which the shard report's
// gremlins result tested a mutant. A mutant outside the diff is listed too,
// SKIPPED, so it is not counted.
func mutatedLines(t *testing.T, shardReport, file string) map[int]bool {
	t.Helper()
	b, err := os.ReadFile(shardReport)
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Gremlins struct {
			Files []struct {
				Name      string `json:"file_name"`
				Mutations []struct {
					Line   int    `json:"line"`
					Status string `json:"status"`
				} `json:"mutations"`
			} `json:"files"`
		} `json:"gremlins"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("%s: %v", shardReport, err)
	}
	lines := map[int]bool{}
	for _, f := range r.Gremlins.Files {
		if f.Name == file {
			for _, m := range f.Mutations {
				if m.Status != "SKIPPED" {
					lines[m.Line] = true
				}
			}
		}
	}
	return lines
}
