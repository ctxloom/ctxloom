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
