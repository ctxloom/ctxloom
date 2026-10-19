package buildpins

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const lefthookPath = "../../../lefthook.yml"

// shellSyntaxMarkers are the spellings that make a `run:` more than one simple
// command. Each one hands the line to the shell's grammar, and so to whichever
// shell /bin/sh happens to be.
var shellSyntaxMarkers = []string{"\n", ";", "|", "&", "<", ">", "$(", "`", "[[", "((", "{", "}"}

// TestLefthookRunBlocksAreSimpleCommands: lefthook hands every `run:` to
// /bin/sh, and /bin/sh is a different shell on every machine a commit is made
// on. Debian trixie's dash accepts `set -o pipefail`; the agent image's dash
// (its ubuntu base) rejects it, and a hook that dies in its prologue refuses
// EVERY commit before any check runs. A `dash -n` parse cannot catch that —
// an unknown `set -o` option is a runtime error, and whether it is one depends
// on the dash version — so the rule is structural instead: a run block is one
// simple command, which every POSIX sh executes identically. A body that
// needs shell syntax lives in a script whose shebang names its interpreter.
func TestLefthookRunBlocksAreSimpleCommands(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(readFile(t, lefthookPath)), &doc); err != nil {
		t.Fatalf("parse %s: %v", lefthookPath, err)
	}
	runs := map[string]string{}
	collectRunBlocks(&doc, "", runs)
	if len(runs) == 0 {
		t.Fatalf("%s: found no run blocks — the walk is not reaching them", lefthookPath)
	}
	for where, run := range runs {
		body := strings.TrimSpace(run)
		for _, marker := range shellSyntaxMarkers {
			if strings.Contains(body, marker) {
				t.Errorf("%s: %s uses shell syntax %q; move the body into a script under scripts/ "+
					"and invoke it with its interpreter (e.g. `bash scripts/<name>`):\n%s",
					lefthookPath, where, marker, body)
				break
			}
		}
	}
}

// collectRunBlocks records every scalar `run:` value in the document, keyed by
// the dotted path of mapping keys leading to it, so hooks, commands and jobs
// are all covered however lefthook.yml nests them.
func collectRunBlocks(n *yaml.Node, path string, out map[string]string) {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			collectRunBlocks(c, path, out)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			child := key.Value
			if path != "" {
				child = path + "." + key.Value
			}
			if key.Value == "run" && val.Kind == yaml.ScalarNode {
				out[child] = val.Value
				continue
			}
			collectRunBlocks(val, child, out)
		}
	}
}

// preCommitHook is the part of lefthook.yml's pre-commit hook the ordering
// depends on.
type preCommitHook struct {
	Piped    bool `yaml:"piped"`
	Parallel bool `yaml:"parallel"`
	Commands map[string]struct {
		Priority   int    `yaml:"priority"`
		Run        string `yaml:"run"`
		StageFixed bool   `yaml:"stage_fixed"`
	} `yaml:"commands"`
}

// TestLefthookPreCommitOrder: pre-commit runs its steps one at a time and
// stops at the first failure, formatters first, then the checks, then
// mutation last. Every later step judges the bytes about to be committed, so a
// formatter that ran after one would commit bytes it never saw; mutation is the
// slowest step by far, so it runs only once everything cheaper has passed.
//
// lefthook orders `commands` by priority, and by NAME when none is set — never
// by their order in the file — with a zero priority sorting last and the order
// of equal priorities unspecified. So every step must carry its own priority,
// and the order is read from those alone.
//
// A step is a formatter when it runs scripts/fmt's staged mode and the
// mutation step when it runs scripts/hook-mutation-staged; anything else is a
// check, so a new check lands between them or fails here.
func TestLefthookPreCommitOrder(t *testing.T) {
	var doc struct {
		PreCommit preCommitHook `yaml:"pre-commit"`
	}
	if err := yaml.Unmarshal([]byte(readFile(t, lefthookPath)), &doc); err != nil {
		t.Fatalf("parse %s: %v", lefthookPath, err)
	}
	hook := doc.PreCommit
	if !hook.Piped || hook.Parallel {
		t.Errorf("%s: pre-commit must be `piped: true` (sequential, stopping at the first failure), not parallel", lefthookPath)
	}

	const fmtPrefix = "bash scripts/fmt staged "
	const mutationRun = "bash scripts/hook-mutation-staged"
	formatters := map[string]int{}
	checks := map[string]int{}
	mutation := map[string]int{}
	seen := map[int]string{}
	for name, cmd := range hook.Commands {
		if cmd.Priority <= 0 {
			t.Errorf("%s: pre-commit %s has no priority; lefthook would order it by name, or last", lefthookPath, name)
		}
		if other, dup := seen[cmd.Priority]; dup {
			t.Errorf("%s: pre-commit %s and %s share priority %d, and lefthook leaves their order unspecified", lefthookPath, name, other, cmd.Priority)
		}
		seen[cmd.Priority] = name
		// stage_fixed re-adds every matching staged file whole, staging a
		// partially staged file's unstaged hunks with it; scripts/fmt
		// re-stages only what it formatted.
		if cmd.StageFixed {
			t.Errorf("%s: pre-commit %s sets stage_fixed", lefthookPath, name)
		}
		run := strings.TrimSpace(cmd.Run)
		switch {
		case strings.HasPrefix(run, fmtPrefix):
			formatters[strings.TrimPrefix(run, fmtPrefix)] = cmd.Priority
		case run == mutationRun:
			mutation[name] = cmd.Priority
		default:
			checks[name] = cmd.Priority
		}
	}

	for _, kind := range []string{"go", "proto", "shell", "just"} {
		if _, ok := formatters[kind]; !ok {
			t.Errorf("%s: pre-commit runs no %q formatter (%s%s)", lefthookPath, kind, fmtPrefix, kind)
		}
	}
	for _, name := range []string{"reprise", "archlint", "lint-pins", "gitleaks"} {
		if _, ok := checks[name]; !ok {
			t.Errorf("%s: pre-commit lost its %s check", lefthookPath, name)
		}
	}
	if len(mutation) != 1 {
		t.Fatalf("%s: pre-commit must run %q exactly once, found %d", lefthookPath, mutationRun, len(mutation))
	}

	for kind, fp := range formatters {
		for check, cp := range checks {
			if fp >= cp {
				t.Errorf("%s: the %s formatter (priority %d) does not run before the %s check (priority %d)", lefthookPath, kind, fp, check, cp)
			}
		}
	}
	for name, mp := range mutation {
		for p, other := range seen {
			if p > mp {
				t.Errorf("%s: %s (priority %d) runs after mutation step %s (priority %d); mutation runs last", lefthookPath, other, p, name, mp)
			}
		}
	}
}
