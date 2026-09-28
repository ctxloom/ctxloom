// No build tag, deliberately — same reasoning as mutation_driver_test.go: the
// gremlins recipes are wired to their verdict here, by an ordinary gate,
// rather than only during the hours-long run the verdict is about.
package mutation

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// gremlinsReport is a gremlins --output report scoring killed and lived
// mutants in file, with the counters gremlins would compute from them.
func gremlinsReport(t *testing.T, file string, killed, lived int) string {
	t.Helper()
	type mutation struct {
		Type   string `json:"type"`
		Status string `json:"status"`
		Line   int    `json:"line"`
		Column int    `json:"column"`
	}
	var muts []mutation
	for i := 0; i < killed+lived; i++ {
		status := "KILLED"
		if i >= killed {
			status = "LIVED"
		}
		muts = append(muts, mutation{Type: "CONDITIONALS_NEGATION", Status: status, Line: i + 1, Column: 1})
	}
	b, err := json.Marshal(map[string]any{
		"files":          []map[string]any{{"file_name": file, "mutations": muts}},
		"mutants_total":  killed + lived,
		"mutants_killed": killed,
		"mutants_lived":  lived,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fakeWithReport is a fake toolchain whose gremlins reports report.
func fakeWithReport(t *testing.T, report string) string {
	t.Helper()
	fake := fakeGoDir(t, "gremlins ran\n", "0")
	if err := os.WriteFile(filepath.Join(fake, "report.json"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	return fake
}

// shardFiles is the real plan for shard k of n over the whole tree.
func shardFiles(t *testing.T, k, n string) []string {
	t.Helper()
	root := repoRootFromTest(t)
	cmd := exec.Command("just", "--justfile", filepath.Join(root, "justfile"), "--working-directory", root, "mutation-shard-plan", "tree", k, n)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("mutation-shard-plan tree %s %s: %v", k, n, err)
	}
	files := strings.Fields(string(out))
	if len(files) == 0 {
		t.Fatalf("shard %s of %s over the tree has no files", k, n)
	}
	return files
}

type gremlinsYAML struct {
	Unleash struct {
		ExcludeFiles []string `yaml:"exclude-files"`
		Threshold    struct {
			Efficacy float64 `yaml:"efficacy"`
		} `yaml:"threshold"`
	} `yaml:"unleash"`
}

func readGremlinsYAML(t *testing.T, path string) gremlinsYAML {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var g gremlinsYAML
	if err := yaml.Unmarshal(b, &g); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return g
}

// The unsharded recipe runs gremlins and then the AGGREGATE, which decides:
// a run under .gremlins.yaml's efficacy threshold fails with gremlins' own
// exit code, one over it passes. gremlins itself is handed the project's
// config with every exclusion intact and no threshold — the verdict must come
// from one place, over the whole run.
func TestGremlinsRecipes_TheAggregateJudgesTheRun(t *testing.T) {
	project := readGremlinsYAML(t, filepath.Join(repoRootFromTest(t), ".gremlins.yaml"))
	if project.Unleash.Threshold.Efficacy <= 50 || project.Unleash.Threshold.Efficacy >= 90 {
		t.Fatalf("this test assumes an efficacy threshold between 50 and 90, the project's is %v", project.Unleash.Threshold.Efficacy)
	}
	file := shardFiles(t, "0", "1")[0]
	cases := []struct {
		name           string
		killed, lived  int
		wantCode       int
		wantOutputHave string
	}{
		{"under the threshold", 5, 5, 10, "test efficacy is not above the threshold"},
		{"over the threshold", 9, 1, 0, "Test efficacy: 90.00%"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := runRecipeWithFake(t, fakeWithReport(t, gremlinsReport(t, file, c.killed, c.lived)), "test-mutation")
			if r.code != c.wantCode || !strings.Contains(r.out, c.wantOutputHave) {
				t.Fatalf("exit %d (want %d), output must contain %q:\n%s", r.code, c.wantCode, c.wantOutputHave, r.out)
			}
			argv := strings.Fields(r.argv)
			if len(argv) == 0 || argv[0] != "unleash" || strings.Contains(r.argv, "--diff") {
				t.Errorf("gremlins argv = %q, want a whole-tree `unleash`", argv)
			}
			handed := readGremlinsYAML(t, filepath.Join(r.fake, "config.yaml"))
			if strings.Join(handed.Unleash.ExcludeFiles, "\n") != strings.Join(project.Unleash.ExcludeFiles, "\n") {
				t.Errorf("gremlins was handed exclude-files %q, the project's are %q", handed.Unleash.ExcludeFiles, project.Unleash.ExcludeFiles)
			}
			if handed.Unleash.Threshold.Efficacy != 0 {
				t.Errorf("gremlins was handed efficacy threshold %v: the verdict is the aggregate's", handed.Unleash.Threshold.Efficacy)
			}
		})
	}
}

// CI's shape: each shard on its own machine, then one aggregate over the
// reports. A shard that scored a file the plan gave another shard would be
// counted twice; the aggregate refuses that union, and passes a clean one.
func TestGremlinsRecipes_TheAggregateRefusesAShardThatStrayed(t *testing.T) {
	shard0, shard1 := shardFiles(t, "0", "2"), shardFiles(t, "1", "2")
	cases := []struct {
		name       string
		shard1File string
		wantCode   int
		want       string
	}{
		{"shard 1 scored shard 0's file", shard0[0], 1, "a shard mutated a file the plan gave another shard"},
		{"each shard scored its own", shard1[0], 0, "Test efficacy: 90.00%"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reports := t.TempDir()
			for k, file := range []string{shard0[0], c.shard1File} {
				r := runRecipeWithFake(t, fakeWithReport(t, gremlinsReport(t, file, 9, 1)),
					"test-mutation-shard", "tree", []string{"0", "1"}[k], "2", reports)
				if r.code != 0 {
					t.Fatalf("shard %d of 2: exit %d — a shard passes whatever it measured:\n%s", k, r.code, r.out)
				}
				if !strings.Contains(r.argv, "--config") {
					t.Fatalf("shard %d never ran gremlins; argv %q\n%s", k, r.argv, r.out)
				}
			}
			r := runRecipeWithFake(t, fakeGoDir(t, "", "0"), "test-mutation-aggregate", "tree", reports)
			if r.code != c.wantCode || !strings.Contains(r.out, c.want) {
				t.Errorf("aggregate exit %d (want %d), output must contain %q:\n%s", r.code, c.wantCode, c.want, r.out)
			}
		})
	}
}
