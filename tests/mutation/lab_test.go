// No build tag, deliberately — same reasoning as invalid_mutant_test.go. The
// laboratory helpers here are what the mutation-tagged harness uses to refuse a
// run that cannot build the UNMUTATED tree, and the runners' embed handling is
// what decides whether any mutant builds at all. Both must be checked by an
// ordinary gate, not only during the hour-long run they guard.
package mutation

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// linkLab reproduces ooze's laboratory (fsrepository.LinkAllToTemporaryRepository,
// which is internal to ooze and cannot be imported): every non-directory file
// under src becomes a symlink at the same relative path under lab. That is the
// tree a runner script sees as its cwd, so a runner proven here is proven
// against the shape it actually meets.
func linkLab(src, lab string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		link := filepath.Join(lab, rel)
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			return err
		}
		return os.Symlink(path, link)
	})
}

// preflightJudge runs testCommand — split exactly as ooze's WithTestCommand
// splits it — once, in a laboratory of the UNMUTATED tree at root, and returns
// its output and an error unless it exits 0.
//
// WHY: ooze has no baseline run. It scores every mutant by the runner's exit
// status alone, so a judge that fails on the unmutated tree — a lab that cannot
// build, a suite that is already red — kills every mutant, and the run reports
// a perfect score over nothing. Checking the unmutated tree first is the only
// place that failure can be told apart from a real kill.
func preflightJudge(root, testCommand string) (string, error) {
	return runInLab(root, testCommand, nil)
}

// runInLab builds a laboratory of root, overwrites each overlay path (slash,
// relative) with real bytes the way ooze writes a mutated file, and runs
// testCommand there.
func runInLab(root, testCommand string, overlay map[string]string) (string, error) {
	lab, err := os.MkdirTemp("", "ooze-lab-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(lab)

	if err := linkLab(root, lab); err != nil {
		return "", fmt.Errorf("building the laboratory: %w", err)
	}
	for rel, body := range overlay {
		p := filepath.Join(lab, filepath.FromSlash(rel))
		if err := os.Remove(p); err != nil {
			return "", fmt.Errorf("overlay %s: %w", rel, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return "", fmt.Errorf("overlay %s: %w", rel, err)
		}
	}
	parts := strings.Split(testCommand, " ")
	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Dir = lab
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%q in the laboratory: %w", testCommand, err)
	}
	return string(out), nil
}

// fakeModule writes files (slash paths relative to the module root) into a
// fresh directory, and copies the real runner scripts in at their real
// relative path, so a laboratory of it runs them exactly as ooze would.
func fakeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	src := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	scripts, err := filepath.Glob(filepath.Join(repoRootFromTest(t), "tests", "mutation", "*.sh"))
	if err != nil || len(scripts) == 0 {
		t.Fatalf("no runner scripts found: %v", err)
	}
	dst := filepath.Join(src, "tests", "mutation")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, s := range scripts {
		b, err := os.ReadFile(s)
		if err != nil {
			t.Fatalf("read %s: %v", s, err)
		}
		if err := os.WriteFile(filepath.Join(dst, filepath.Base(s)), b, 0o755); err != nil {
			t.Fatalf("copy %s: %v", s, err)
		}
	}
	return src
}

// Embeds in packages no hand-kept list would name: the binary's own package, a
// directory embed ("all:") nested under internal/, and a package reached only
// through an import. go:embed refuses a symlink, so every one of these must be
// materialized in the laboratory or nothing — mutant or not — builds.
const labGoMod = "module lab\n\ngo 1.24\n"

var embeddingModule = map[string]string{
	"go.mod": labGoMod,
	"cmd/ctxloom/main.go": "package main\n\nimport (\n\t_ \"embed\"\n\n\t\"lab/internal/deep/nest\"\n)\n\n" +
		"//go:embed loadout.yaml\nvar loadout string\n\nfunc main() { _ = loadout; _ = nest.FS }\n",
	"cmd/ctxloom/loadout.yaml":            "x: 1\n",
	"internal/deep/nest/nest.go":          "package nest\n\nimport \"embed\"\n\n//go:embed all:data\nvar FS embed.FS\n",
	"internal/deep/nest/data/a.txt":       "a\n",
	"internal/deep/nest/data/b/c.txt":     "c\n",
	"internal/deep/nest/nest_test.go":     "package nest\n\nimport \"testing\"\n\nfunc TestNest(t *testing.T) {\n\tif _, err := FS.ReadFile(\"data/b/c.txt\"); err != nil {\n\t\tt.Fatal(err)\n\t}\n}\n",
	"tests/acceptance/acceptance_test.go": "//go:build acceptance\n\npackage acceptance\n\nimport \"testing\"\n\nfunc TestAcceptance(t *testing.T) {}\n",
}

// The acceptance runner must build the unmutated tree in a laboratory whatever
// the module embeds, without being told where the embeds are.
func TestRunScopedSuite_BuildsALaboratoryWhateverTheModuleEmbeds(t *testing.T) {
	t.Setenv("CTXLOOM_VERSION_LDFLAG", "-X main.stamp=lab")
	src := fakeModule(t, embeddingModule)
	out, err := preflightJudge(src, "sh tests/mutation/run_scoped_suite.sh")
	if err != nil {
		t.Fatalf("the acceptance runner could not build an UNMUTATED laboratory: %v\n"+
			"Every mutant would fail the same way and be scored as a kill.\noutput:\n%s", err, out)
	}
	if strings.Contains(out, "ooze-invalid-mutant:") {
		t.Errorf("the unmutated tree was marked an invalid mutant:\n%s", out)
	}
}

// An unstamped ctxloom refuses to start, so a laboratory built without the
// justfile's stamp fails every scenario — each one scored as a kill. The runner
// must refuse to build at all rather than produce that binary.
func TestRunScopedSuite_RefusesToBuildWithoutTheVersionStamp(t *testing.T) {
	t.Setenv("CTXLOOM_VERSION_LDFLAG", "")
	src := fakeModule(t, embeddingModule)
	out, err := preflightJudge(src, "sh tests/mutation/run_scoped_suite.sh")
	if err == nil {
		t.Fatalf("the runner built a laboratory with no version stamp; output:\n%s", out)
	}
	if !strings.Contains(out, "CTXLOOM_VERSION_LDFLAG") {
		t.Errorf("the refusal must name the missing CTXLOOM_VERSION_LDFLAG; output:\n%s", out)
	}
}

// The unit judge compiles the named package AND its tests, so its embeds —
// and every dependency's — must be materialized too.
func TestRunUnitJudge_BuildsALaboratoryWhateverTheModuleEmbeds(t *testing.T) {
	src := fakeModule(t, embeddingModule)
	t.Setenv("MUT_PKG", "./internal/deep/nest")
	t.Setenv("MUT_RUN", "^TestNest$")
	out, err := preflightJudge(src, "sh tests/mutation/run_unit_judge.sh")
	if err != nil {
		t.Fatalf("the unit judge could not build an UNMUTATED laboratory: %v\noutput:\n%s", err, out)
	}
}

// The pre-flight must actually refuse: a judge that fails on the unmutated
// tree is reported, with its output, rather than released.
func TestPreflightJudge_RefusesAJudgeThatFailsOnTheUnmutatedTree(t *testing.T) {
	src := fakeModule(t, map[string]string{
		"go.mod":  labGoMod,
		"fail.sh": "echo 'cannot embed irregular file loadout.yaml'\nexit 3\n",
	})
	out, err := preflightJudge(src, "sh fail.sh")
	if err == nil {
		t.Fatalf("a judge exiting 3 on the unmutated tree was accepted; ooze would score every mutant as killed")
	}
	if !strings.Contains(out, "cannot embed irregular file") {
		t.Errorf("the judge's own output must be returned so the failure is diagnosable; got %q", out)
	}
}

// And it runs in a LABORATORY, not the source tree: the files it sees are the
// symlinks ooze would give it, which is exactly what broke the embeds.
func TestPreflightJudge_RunsInASymlinkedLaboratory(t *testing.T) {
	src := fakeModule(t, map[string]string{
		"go.mod":   labGoMod,
		"check.sh": "[ -L go.mod ] && [ \"$(pwd -P)\" != \"$SRC\" ]\n",
	})
	t.Setenv("SRC", src)
	if out, err := preflightJudge(src, "sh check.sh"); err != nil {
		t.Fatalf("the pre-flight did not run in a symlinked laboratory: %v\n%s", err, out)
	}
}
