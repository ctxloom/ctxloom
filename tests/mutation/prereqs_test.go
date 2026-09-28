package mutation

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// notMutating names the test-mutation* recipes that run no mutant, and why.
// Every other test-mutation* recipe compiles the tree, so it must take
// _mutation-prereqs: a tree that cannot build scores every mutant as a kill.
var notMutating = map[string]string{
	"test-mutation-install":   "installs the gremlins binary",
	"test-mutation-entries":   "lists the target tables",
	"test-mutation-entry":     "delegates to test-mutation-acceptance, which takes the prerequisite",
	"test-mutation-aggregate": "judges the shards' reports; the shards took the prerequisite",
}

// TestMutationRecipes_AllTakeTheBuildPrerequisite fails when a mutation lane
// can start without generating and compiling the tree first — in either
// justfile, since build/ci.justfile's lanes run in the dev container too.
func TestMutationRecipes_AllTakeTheBuildPrerequisite(t *testing.T) {
	root := repoRootFromTest(t)
	repoInput(t, "justfile", "justfile.container", "build/*.justfile")
	for _, jf := range []string{"justfile", "justfile.container"} {
		for _, name := range mutatingWithoutPrereqs(recipeDeps(t, root, jf)) {
			t.Errorf("%s: recipe %s runs mutants but does not depend on _mutation-prereqs", jf, name)
		}
	}
}

// recipeDeps maps every recipe in one justfile to the recipes it depends on,
// as `just --dump` reports them.
func recipeDeps(t *testing.T, root, justfile string) map[string][]string {
	t.Helper()
	just, err := exec.LookPath("just")
	if err != nil {
		t.Fatalf("just is not on PATH (%v); this test reads the real recipes", err)
	}
	out, err := exec.Command(just, "--justfile", filepath.Join(root, justfile), "--working-directory", root,
		"--dump", "--dump-format", "json").Output()
	if err != nil {
		t.Fatalf("%s: just --dump: %v", justfile, err)
	}
	var dump struct {
		Recipes map[string]struct {
			Dependencies []struct {
				Recipe string `json:"recipe"`
			} `json:"dependencies"`
		} `json:"recipes"`
	}
	if err := json.Unmarshal(out, &dump); err != nil {
		t.Fatalf("%s: parse dump: %v", justfile, err)
	}
	deps := make(map[string][]string, len(dump.Recipes))
	for name, r := range dump.Recipes {
		deps[name] = []string{} // a recipe with no dependencies must still be listed
		for _, d := range r.Dependencies {
			deps[name] = append(deps[name], d.Recipe)
		}
	}
	return deps
}

// mutatingWithoutPrereqs returns, sorted, the test-mutation* recipes that run
// mutants yet do not depend on _mutation-prereqs.
func mutatingWithoutPrereqs(deps map[string][]string) []string {
	var missing []string
	for name, ds := range deps {
		if _, exempt := notMutating[name]; exempt || !strings.HasPrefix(name, "test-mutation") {
			continue
		}
		if !slices.Contains(ds, "_mutation-prereqs") {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}
