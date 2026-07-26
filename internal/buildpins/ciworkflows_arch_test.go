//go:build arch

package buildpins

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Architectural gates for the "every CI step is a `just` target" contract.
//
// The contract buys reproducibility (any CI step can be typed locally) and
// kills workflow-vs-recipe drift (CI ran a bare `gremlins unleash` for as long
// as it did precisely because that shell had no counterpart to be checked
// against). It also introduces two new ways to be wrong, and these are them.
//
// Both gates are class gates in this repo's sense — they fail when a NEW
// instance of a known-bad shape appears, not when one particular known bug
// regresses — so they carry `//go:build arch` and the TestArch_ naming that
// `just test-arch` selects on.

const workflowsDir = "../../.github/workflows"

// justfilesDefiningRecipes is every file a workflow's `just <target>` can
// resolve against: the two roots and the fragments they import. Listed rather
// than discovered because the import graph is three files deep and a
// discovery bug here would silently shrink the known-recipe set, which turns
// this gate into a source of false failures.
var justfilesDefiningRecipes = []string{
	"../../justfile",
	"../../justfile.container",
	"../../build/gates.justfile",
	"../../build/ci.justfile",
	"../../build/common.justfile",
}

// justInvocationRE finds a recipe name in a workflow.
//
// It deliberately anchors to the start of a line (after optional `run:`), which
// is what a step body looks like both inline (`run: just test-arch`) and folded
// (`run: >-` with `just engine-drift-alert ...` on the following line). Prose
// mentioning `just` in a `#` comment does not match, and neither does a flag
// (`just --version`), since the name must start with a lowercase letter.
var justInvocationRE = regexp.MustCompile(`(?m)^\s*(?:- )?(?:run:\s*)?just\s+(?:-f\s+\S+\s+)?([a-z][a-z0-9-]*)\b`)

// recipeDefinitionRE finds recipe names defined at column 0. Recipes may take
// parameters (`test-mutation-diff BASE`) and may be private (`_require-generated`).
var recipeDefinitionRE = regexp.MustCompile(`(?m)^([a-z_][a-z0-9_-]*)(\s+[^:\n]*)?:`)

func workflowFiles(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(workflowsDir, "*.yml"))
	if err != nil {
		t.Fatalf("glob %s: %v", workflowsDir, err)
	}
	if len(matches) == 0 {
		t.Fatalf("%s: no workflows found — this gate would pass vacuously", workflowsDir)
	}
	sort.Strings(matches)
	return matches
}

// TestArch_CIWorkflows_InvokeExistingJustTargets fails when a workflow step
// names a recipe that does not exist.
//
// Before the rework a step's shell was self-contained, so it was either
// correct or obviously broken. Now a step is a NAME, and a name can go stale
// silently: rename or delete a recipe and every workflow calling it keeps
// looking perfectly reasonable right up until a runner exits with just's
// "unknown recipe". That failure is cheap to catch here and expensive to catch
// there — a release-completer step, for instance, only ever runs on a tag.
func TestArch_CIWorkflows_InvokeExistingJustTargets(t *testing.T) {
	defined := map[string]bool{}
	for _, path := range justfilesDefiningRecipes {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, m := range recipeDefinitionRE.FindAllStringSubmatch(string(raw), -1) {
			defined[m[1]] = true
		}
	}
	if len(defined) < 20 {
		t.Fatalf("found only %d recipe definitions across %v — the definition regex is broken, and this gate would fail everything", len(defined), justfilesDefiningRecipes)
	}

	invocations := 0
	for _, wf := range workflowFiles(t) {
		raw, err := os.ReadFile(wf)
		if err != nil {
			t.Fatalf("read %s: %v", wf, err)
		}
		for _, m := range justInvocationRE.FindAllStringSubmatch(string(raw), -1) {
			invocations++
			if !defined[m[1]] {
				t.Errorf("%s runs `just %s`, but no justfile defines that recipe — this step would fail on the runner with just's \"unknown recipe\". Renamed? Deleted? Typo'd?", wf, m[1])
			}
		}
	}

	// Anti-vacuous. A `-run` pattern that matches nothing exits 0, and so does
	// a workflow scan that finds nothing: if the invocation regex ever stops
	// matching (a formatting change to the workflows, say), this gate would go
	// green having checked zero steps — the silent no-op it exists to prevent.
	if invocations < 15 {
		t.Fatalf("found only %d `just <target>` invocations across %d workflows — expected the CI steps to be just targets, so either the contract was abandoned or justInvocationRE stopped matching", invocations, len(workflowFiles(t)))
	}
	t.Logf("checked %d `just <target>` invocations against %d defined recipes", invocations, len(defined))
}

// TestArch_CIWorkflows_ContainerJobsSelectTheContainerJustfile fails when a job
// running inside a container image invokes `just` without pointing it at
// justfile.container.
//
// Both root justfiles define `build`, `proto`, `validate`, `test-default` and
// friends, and they mean DIFFERENT things: the host copies delegate into the
// devcontainer via `just dev-image` (a docker build), the container copies do
// the work directly. A containerized job that forgets JUST_JUSTFILE therefore
// resolves the host justfile and tries to build the devcontainer image from
// inside a container — a confusing failure a long way from its cause, in a job
// that may only run on a tag or a cron.
func TestArch_CIWorkflows_ContainerJobsSelectTheContainerJustfile(t *testing.T) {
	type job struct {
		Container any               `yaml:"container"`
		Env       map[string]string `yaml:"env"`
		Steps     []struct {
			Run string `yaml:"run"`
		} `yaml:"steps"`
	}
	type workflow struct {
		Jobs map[string]job `yaml:"jobs"`
	}

	checked := 0
	for _, wf := range workflowFiles(t) {
		raw, err := os.ReadFile(wf)
		if err != nil {
			t.Fatalf("read %s: %v", wf, err)
		}
		var doc workflow
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", wf, err)
		}
		names := make([]string, 0, len(doc.Jobs))
		for name := range doc.Jobs {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, name := range names {
			j := doc.Jobs[name]
			if j.Container == nil {
				continue
			}
			usesJust := false
			for _, s := range j.Steps {
				if justInvocationRE.MatchString(s.Run) {
					usesJust = true
					break
				}
			}
			if !usesJust {
				continue
			}
			checked++
			if got := j.Env["JUST_JUSTFILE"]; got != "justfile.container" {
				t.Errorf("%s: job %q runs in a container image and invokes `just`, but its job-level env has JUST_JUSTFILE=%q (want %q). Without it a bare `just` resolves the HOST justfile, whose same-named recipes delegate into `just dev-image` and would try to docker-build the devcontainer from inside a container.",
					wf, name, got, "justfile.container")
			}
		}
	}

	if checked == 0 {
		t.Fatal("no containerized job invoking `just` was found across the workflows — this gate checked nothing")
	}
	t.Logf("checked %d containerized jobs", checked)
}

// TestArch_CIWorkflows_KeepInlineShellOutOfSteps fails when a workflow step's
// `run:` grows shell that is not a `just` invocation.
//
// This is the contract itself, made executable. Every exemption is listed here
// by name with its reason, so adding one is a deliberate edit to this list
// rather than a step nobody notices. The exemptions are all bootstrap: shell
// that has to run before `just` is available to run anything.
func TestArch_CIWorkflows_KeepInlineShellOutOfSteps(t *testing.T) {
	// key: "<workflow basename>/<job>/<step name>"
	allowedInlineShell := map[string]string{
		"release-completer.yml/release/Install dependencies": "installs curl, which the `just` bootstrap on the next line needs; goreleaser-cross ships none of it",
	}

	type step struct {
		Name string `yaml:"name"`
		Run  string `yaml:"run"`
	}
	type job struct {
		Steps []step `yaml:"steps"`
	}
	type workflow struct {
		Jobs map[string]job `yaml:"jobs"`
	}

	seen := map[string]bool{}
	for _, wf := range workflowFiles(t) {
		raw, err := os.ReadFile(wf)
		if err != nil {
			t.Fatalf("read %s: %v", wf, err)
		}
		var doc workflow
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", wf, err)
		}
		names := make([]string, 0, len(doc.Jobs))
		for name := range doc.Jobs {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, jobName := range names {
			for _, s := range doc.Jobs[jobName].Steps {
				if strings.TrimSpace(s.Run) == "" {
					continue
				}
				key := filepath.Base(wf) + "/" + jobName + "/" + s.Name
				seen[key] = true
				if _, ok := allowedInlineShell[key]; ok {
					continue
				}
				if justInvocationRE.MatchString(s.Run) {
					continue
				}
				t.Errorf("%s: step %q in job %q runs shell that is not a `just` target:\n%s\n\nMove it into build/ci.justfile and invoke it by name, so it can be run locally and cannot drift from the recipe doing the same job. If it genuinely cannot be a recipe (bootstrap only), add it to allowedInlineShell with the reason.",
					wf, s.Name, jobName, strings.TrimSpace(s.Run))
			}
		}
	}

	// A stale exemption is a hole that looks like a decision.
	for key := range allowedInlineShell {
		if !seen[key] {
			t.Errorf("allowedInlineShell has an entry for %q, but no such step exists — remove the stale exemption", key)
		}
	}
}
