package mutation

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// notMutating names the test-mutation* recipes that run no mutant, and why.
// Every other test-mutation* recipe compiles the tree, so it must take
// _mutation-prereqs: a tree that cannot build scores every mutant as a kill.
var notMutating = map[string]string{
	"test-mutation-install": "installs the gremlins binary",
	"test-mutation-entries": "lists the target tables",
	"test-mutation-entry":   "delegates to test-mutation-acceptance, which takes the prerequisite",
}

// TestMutationRecipes_AllTakeTheBuildPrerequisite fails when a mutation lane
// can start without generating and compiling the tree first — in either
// justfile, since build/ci.justfile's lanes run in the dev container too.
func TestMutationRecipes_AllTakeTheBuildPrerequisite(t *testing.T) {
	root := repoRootFromTest(t)
	repoInput(t, "justfile", "justfile.container", "build/*.justfile")
	just, err := exec.LookPath("just")
	if err != nil {
		t.Fatalf("just is not on PATH (%v); this test reads the real recipes", err)
	}
	for _, jf := range []string{"justfile", "justfile.container"} {
		out, err := exec.Command(just, "--justfile", filepath.Join(root, jf), "--working-directory", root,
			"--dump", "--dump-format", "json").Output()
		if err != nil {
			t.Fatalf("%s: just --dump: %v", jf, err)
		}
		var dump struct {
			Recipes map[string]struct {
				Dependencies []struct {
					Recipe string `json:"recipe"`
				} `json:"dependencies"`
			} `json:"recipes"`
		}
		if err := json.Unmarshal(out, &dump); err != nil {
			t.Fatalf("%s: parse dump: %v", jf, err)
		}
		var names []string
		for name := range dump.Recipes {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if !strings.HasPrefix(name, "test-mutation") {
				continue
			}
			if _, ok := notMutating[name]; ok {
				continue
			}
			found := false
			for _, d := range dump.Recipes[name].Dependencies {
				found = found || d.Recipe == "_mutation-prereqs"
			}
			if !found {
				t.Errorf("%s: recipe %s runs mutants but does not depend on _mutation-prereqs", jf, name)
			}
		}
	}
}
