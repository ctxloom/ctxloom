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
