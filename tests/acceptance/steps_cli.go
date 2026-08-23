//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/cucumber/godog"
)

func registerCLISteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^I run "([^"]*)"$`, func(c context.Context, cmdline string) error {
		return runCLI(c, cmdline, "")
	})

	ctx.Step(`^I run "([^"]*)" with input:$`, func(c context.Context, cmdline string, doc *godog.DocString) error {
		return runCLI(c, cmdline, doc.Content)
	})

	// THE NARRATED-COMMAND STEP. The step TEXT carries the generality in
	// business language; the DocString carries the specific command. Both are
	// parsed Gherkin nodes, which is what makes this work where a `#` comment
	// cannot: comments are dropped by the parser, so the living-docs generator
	// can never read one.
	//
	// The pattern is deliberately anchored on nothing but "not starting with a
	// keyword the other steps own" — any business sentence matches, and ONE Go
	// function backs every variant. That is what keeps a CLI spec's step text
	// indistinguishable in shape from a journey's: the vocabulary is shared,
	// only the granularity differs.
	//
	// Multi-line DocStrings run each non-empty, non-comment line in order, so a
	// scenario can narrate a SEQUENCE under one sentence ("Alice wires ctxloom
	// in and checks it took") without inventing a step per command. The last
	// command's exit code and output are what the following Thens assert on.
	ctx.Step(`^(?:Alice|Bob|Carol|Trent|Dana|Priya|she|he|they) [^"]*:$`, func(c context.Context, doc *godog.DocString) error {
		return runNarratedCommands(c, doc.Content)
	})

	ctx.Step(`^the command succeeds$`, func(c context.Context) error {
		w := worldFrom(c)
		if code := w.env.LastExitCode(); code != 0 {
			return fmt.Errorf("expected exit 0, got %d; output:\n%s", code, w.env.LastOutput())
		}
		return nil
	})

	ctx.Step(`^the command fails$`, func(c context.Context) error {
		w := worldFrom(c)
		if code := w.env.LastExitCode(); code == 0 {
			return fmt.Errorf("expected non-zero exit, got 0; output:\n%s", w.env.LastOutput())
		}
		return nil
	})

	// An EXACT exit code, for commands whose contract distinguishes kinds of
	// failure. "the command fails" only proves non-zero, which collapses
	// distinctions a caller is meant to branch on — most sharply "I found
	// breakage" versus "I could not look", where treating the second as the
	// first reports a clean corpus that was never read.
	ctx.Step(`^the command exits with code (\d+)$`, func(c context.Context, want int) error {
		w := worldFrom(c)
		if got := w.env.LastExitCode(); got != want {
			return fmt.Errorf("expected exit code %d, got %d; output:\n%s", want, got, w.env.LastOutput())
		}
		return nil
	})

	ctx.Step(`^the output contains "([^"]*)"$`, func(c context.Context, want string) error {
		w := worldFrom(c)
		if !strings.Contains(w.env.LastOutput(), want) {
			return fmt.Errorf("output does not contain %q; output:\n%s", want, w.env.LastOutput())
		}
		return nil
	})

	ctx.Step(`^the output does not contain "([^"]*)"$`, func(c context.Context, unwant string) error {
		w := worldFrom(c)
		if strings.Contains(w.env.LastOutput(), unwant) {
			return fmt.Errorf("output unexpectedly contains %q; output:\n%s", unwant, w.env.LastOutput())
		}
		return nil
	})

	ctx.Step(`^the output matches "([^"]*)"$`, func(c context.Context, pattern string) error {
		w := worldFrom(c)
		re, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("invalid regexp %q: %w", pattern, err)
		}
		if !re.MatchString(w.env.LastOutput()) {
			return fmt.Errorf("output does not match /%s/; output:\n%s", pattern, w.env.LastOutput())
		}
		return nil
	})

	registerJSONOutputSteps(ctx)
	registerVersionSteps(ctx)
	registerACPBlockSteps(ctx)
}

// registerJSONOutputSteps carries the `--format json` assertions.
//
// A `--format json` scenario that only substring-matches is not testing the
// flag at all: every marker it looks for is present in the HUMAN rendering
// too, so a command that quietly stopped emitting JSON stays green. These
// steps parse the machine stream (stdout alone — see
// testenv.RunRecord.Stdout) and assert DECODED structure, which the text form
// cannot satisfy at any price.
func registerJSONOutputSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the output is valid JSON$`, func(c context.Context) error {
		_, err := lastOutputJSON(worldFrom(c))
		return err
	})

	// "contains an object whose F is V" rather than an index: a check list's
	// position is an implementation detail, its membership is the contract.
	ctx.Step(`^the JSON output array "([^"]*)" contains an object whose "([^"]*)" is "([^"]*)"$`,
		func(c context.Context, arrayKey, field, want string) error {
			w := worldFrom(c)
			entries, err := lastOutputJSONArray(w, arrayKey)
			if err != nil {
				return err
			}
			for _, e := range entries {
				obj, ok := e.(map[string]any)
				if !ok {
					continue
				}
				if fmt.Sprintf("%v", obj[field]) == want {
					return nil
				}
			}
			return fmt.Errorf("JSON array %q has no object whose %q is %q; stdout:\n%s", arrayKey, field, want, w.env.LastStdout())
		})

	// Two fields of the SAME object, which one-field membership cannot express:
	// "a check named X is present" and "a check reporting ok is present" are
	// both satisfied by a report where X warns and something else is ok. Where
	// the claim is about one object's verdict, both fields have to be read off
	// that object.
	ctx.Step(`^the JSON output array "([^"]*)" contains an object whose "([^"]*)" is "([^"]*)" and whose "([^"]*)" is "([^"]*)"$`,
		func(c context.Context, arrayKey, keyField, keyWant, field, want string) error {
			w := worldFrom(c)
			entries, err := lastOutputJSONArray(w, arrayKey)
			if err != nil {
				return err
			}
			for _, e := range entries {
				obj, ok := e.(map[string]any)
				if !ok || fmt.Sprintf("%v", obj[keyField]) != keyWant {
					continue
				}
				if got := fmt.Sprintf("%v", obj[field]); got != want {
					return fmt.Errorf("JSON array %q object with %s=%q has %s=%q, want %q; stdout:\n%s",
						arrayKey, keyField, keyWant, field, got, want, w.env.LastStdout())
				}
				return nil
			}
			return fmt.Errorf("JSON array %q has no object whose %q is %q; stdout:\n%s", arrayKey, keyField, keyWant, w.env.LastStdout())
		})

	ctx.Step(`^every object in the JSON output array "([^"]*)" has a non-empty "([^"]*)"$`,
		func(c context.Context, arrayKey, field string) error {
			w := worldFrom(c)
			entries, err := lastOutputJSONArray(w, arrayKey)
			if err != nil {
				return err
			}
			// The zero-length guard: "every element satisfies P" is
			// vacuously true of an empty array, which is precisely the
			// silent-no-op shape this assertion exists to catch.
			if len(entries) == 0 {
				return fmt.Errorf("JSON array %q is EMPTY — every-element assertions are vacuous over it; stdout:\n%s", arrayKey, w.env.LastStdout())
			}
			for i, e := range entries {
				obj, ok := e.(map[string]any)
				if !ok {
					return fmt.Errorf("JSON array %q entry %d is not an object; stdout:\n%s", arrayKey, i, w.env.LastStdout())
				}
				if s := fmt.Sprintf("%v", obj[field]); s == "" || obj[field] == nil {
					return fmt.Errorf("JSON array %q entry %d has an empty %q; stdout:\n%s", arrayKey, i, field, w.env.LastStdout())
				}
			}
			return nil
		})
}

// lastOutputJSON decodes the last command's STDOUT as a JSON object. Parsing
// the combined stream cannot work — one stderr advisory line concatenated
// onto the payload makes it unparseable — so this reads stdout alone.
func lastOutputJSON(w *World) (map[string]any, error) {
	stdout := w.env.LastStdout()
	if strings.TrimSpace(stdout) == "" {
		return nil, fmt.Errorf("stdout is empty — nothing to parse as JSON; combined output:\n%s", w.env.LastOutput())
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(stdout), &obj); err != nil {
		return nil, fmt.Errorf("stdout is not a JSON object: %v; stdout:\n%s", err, stdout)
	}
	return obj, nil
}

func lastOutputJSONArray(w *World, key string) ([]any, error) {
	obj, err := lastOutputJSON(w)
	if err != nil {
		return nil, err
	}
	raw, ok := obj[key]
	if !ok {
		return nil, fmt.Errorf("JSON output has no %q key; stdout:\n%s", key, w.env.LastStdout())
	}
	entries, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("JSON output key %q is not an array; stdout:\n%s", key, w.env.LastStdout())
	}
	return entries, nil
}

// versionStampRE is the shape a ctxloom version string can legitimately take:
// the ldflags-stamped family stamp (`v<maj.min.patch>[-<sha>-<utc>]`), or the
// literal "dev" an unstamped `go build` leaves in internal/version.Version. Any
// other text — notably a rendering that forgot to print the version at all —
// is refused. A looser pattern is worthless here: `matches "."` accepts one
// arbitrary character, which the literal "MUTATION-not-the-version" satisfies
// as happily as the truth does.
var versionStampRE = regexp.MustCompile(`^(dev|v?[0-9]+\.[0-9]+\.[0-9]+\S*)$`)

func registerVersionSteps(ctx *godog.ScenarioContext) {
	// One step, three surfaces. The version is not knowable to this test
	// process (the CLI binary is stamped by ldflags at build time; the test
	// binary is not), so the assertion is: whatever the binary reports, it is
	// version-SHAPED and every surface that reports it AGREES. A print path
	// that emits anything of its own breaks the agreement.
	ctx.Step(`^every version surface reports the same version-shaped string$`, func(c context.Context) error {
		w := worldFrom(c)

		if err := runCLI(c, "ctxloom version", ""); err != nil {
			return err
		}
		text := strings.TrimSpace(w.env.LastStdout())
		if !versionStampRE.MatchString(text) {
			return fmt.Errorf("`ctxloom version` printed %q, which is not a version string", text)
		}

		if err := runCLI(c, "ctxloom --format json version", ""); err != nil {
			return err
		}
		obj, err := lastOutputJSON(w)
		if err != nil {
			return fmt.Errorf("`ctxloom --format json version`: %w", err)
		}
		if got := fmt.Sprintf("%v", obj["version"]); got != text {
			return fmt.Errorf("json version = %q but `ctxloom version` printed %q", got, text)
		}
		if got := fmt.Sprintf("%v", obj["name"]); got != "ctxloom" {
			return fmt.Errorf("json version name = %q, want %q", got, "ctxloom")
		}

		if err := runCLI(c, "ctxloom --version", ""); err != nil {
			return err
		}
		flag := strings.TrimSpace(w.env.LastStdout())
		if want := "ctxloom version " + text; flag != want {
			return fmt.Errorf("`ctxloom --version` printed %q, want %q", flag, want)
		}
		return nil
	})
}

// zedBlockMarker is the line `acp list` prints immediately before the
// ready-to-paste Zed object; the JSON runs from the next line to the end of
// stdout.
const zedBlockMarker = `merge into "agent_servers"`

func registerACPBlockSteps(ctx *godog.ScenarioContext) {
	// The human paste block is the artifact a user actually copies, and it is
	// rendered by its own function (zedAgentServersBlock) that `--format json`
	// never runs — so asserting on the JSON form proves nothing about it.
	// Asserting on the literal "agent_servers" proves less still: that string
	// is in the surrounding prose, present even when the object is `{}`.
	ctx.Step(`^the agent_servers paste block declares a server "([^"]*)" running "([^"]*)"$`,
		func(c context.Context, name, argv string) error {
			w := worldFrom(c)
			block, err := zedAgentServersJSON(w.env.LastStdout())
			if err != nil {
				return fmt.Errorf("%w; output:\n%s", err, w.env.LastOutput())
			}
			if len(block) == 0 {
				return fmt.Errorf("the agent_servers paste block is an EMPTY object — it advertises no server at all; output:\n%s", w.env.LastOutput())
			}
			entry, ok := block[name]
			if !ok {
				keys := make([]string, 0, len(block))
				for k := range block {
					keys = append(keys, k)
				}
				return fmt.Errorf("the agent_servers paste block has no %q entry (has: %v)", name, keys)
			}
			if strings.TrimSpace(entry.Command) == "" {
				return fmt.Errorf("agent_servers entry %q has an empty command", name)
			}
			if got := strings.Join(entry.Args, " "); got != argv {
				return fmt.Errorf("agent_servers entry %q args = %q, want %q", name, got, argv)
			}
			return nil
		})
}

// zedAgentServerEntry mirrors the value shape `acp list` pastes.
type zedAgentServerEntry struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// zedAgentServersJSON pulls the paste block out of `acp list`'s human output
// and decodes it. Fails loudly when the marker line or the object is missing
// rather than returning an empty map, which would read as "no servers".
func zedAgentServersJSON(out string) (map[string]zedAgentServerEntry, error) {
	i := strings.Index(out, zedBlockMarker)
	if i < 0 {
		return nil, fmt.Errorf("output has no %q line", zedBlockMarker)
	}
	j := strings.Index(out[i:], "{")
	if j < 0 {
		return nil, fmt.Errorf("no JSON object follows the %q line", zedBlockMarker)
	}
	k := strings.LastIndex(out, "}")
	if k < i+j {
		return nil, fmt.Errorf("the paste block after the %q line is unterminated", zedBlockMarker)
	}
	var block map[string]zedAgentServerEntry
	if err := json.Unmarshal([]byte(out[i+j:k+1]), &block); err != nil {
		return nil, fmt.Errorf("the agent_servers paste block is not valid JSON: %w", err)
	}
	return block, nil
}

func runCLI(c context.Context, cmdline, stdin string) error {
	w := worldFrom(c)
	// $PROJECT_DIR is the scenario's own project path. Some commands REQUIRE an
	// already-resolved absolute path and deliberately refuse to expand "~" or
	// guess a location (`util config-write --file` says so in its help), so a
	// feature cannot spell those invocations with a relative path. Expanding
	// here keeps the real command visible in the feature text instead of hiding
	// it behind a step that builds the path privately.
	//
	// Not "{{...}}" (the fragment templater's syntax) and not "<...>" (Gherkin's
	// Scenario Outline placeholder) — both would be claimed by something else.
	cmdline = strings.ReplaceAll(cmdline, "$PROJECT_DIR", w.env.ProjectDir)
	args, err := ctxloomArgs(cmdline)
	if err != nil {
		return err
	}
	if stdin == "" {
		_ = w.env.Run(args...)
	} else {
		_ = w.env.RunWithStdin(stdin, args...)
	}
	return nil // exit status is asserted by a dedicated step
}

// runNarratedCommands runs each command line in a narrated step's DocString,
// in order. Blank lines and #-comments are skipped so a block can be annotated
// without inventing a second step.
//
// It DOES NOT assert exit status — that stays the following Then's job, exactly
// as it is for the plain `I run` step, so a scenario can still narrate a
// command it expects to FAIL. The last command's exit code and output are what
// those Thens see.
//
// A block that contains no runnable line at all is an ERROR rather than a
// silent pass: an empty DocString under a business sentence is the shape where
// a scenario reads as exercising something and exercises nothing, which is the
// vacuous-assertion failure this suite has been audited for.
func runNarratedCommands(c context.Context, block string) error {
	ran := 0
	for _, line := range strings.Split(block, "\n") {
		cmdline := strings.TrimSpace(line)
		if cmdline == "" || strings.HasPrefix(cmdline, "#") {
			continue
		}
		if err := runCLI(c, cmdline, ""); err != nil {
			return err
		}
		ran++
	}
	if ran == 0 {
		return fmt.Errorf("narrated step ran no commands: its block held only blanks/comments:\n%s", block)
	}
	return nil
}

// ctxloomArgs strips the leading "ctxloom " from a feature command line and
// splits the remainder into argv via shellSplit, so a feature file can
// express a quoted argument, an argument containing spaces, or a flag whose
// value is a sentence — exactly the argv shapes where the live
// variadic-flag defect class lives. Before this, bare
// strings.Fields whitespace-splitting made those shapes structurally
// inexpressible.
func ctxloomArgs(cmdline string) ([]string, error) {
	fields, err := shellSplit(cmdline)
	if err != nil {
		return nil, fmt.Errorf("parse command line %q: %w", cmdline, err)
	}
	if len(fields) == 0 || fields[0] != "ctxloom" {
		return nil, fmt.Errorf("command must start with \"ctxloom\": %q", cmdline)
	}
	return fields[1:], nil
}

// shellSplit tokenizes cmdline the way a POSIX shell would for the subset
// this suite needs: unquoted whitespace-separated words, single-quoted
// literals (no escapes inside, matching sh), and double-quoted strings
// (backslash escapes \" and \\ only).
func shellSplit(cmdline string) ([]string, error) {
	var fields []string
	var cur strings.Builder
	haveCur := false
	runes := []rune(cmdline)
	for i := 0; i < len(runes); {
		c := runes[i]
		switch c {
		case ' ', '\t':
			if haveCur {
				fields = append(fields, cur.String())
				cur.Reset()
				haveCur = false
			}
			i++
		case '\'':
			haveCur = true
			i++
			start := i
			for i < len(runes) && runes[i] != '\'' {
				i++
			}
			if i >= len(runes) {
				return nil, fmt.Errorf("unterminated single quote")
			}
			cur.WriteString(string(runes[start:i]))
			i++
		case '"':
			haveCur = true
			i++
			for i < len(runes) && runes[i] != '"' {
				if runes[i] == '\\' && i+1 < len(runes) && (runes[i+1] == '"' || runes[i+1] == '\\') {
					cur.WriteRune(runes[i+1])
					i += 2
					continue
				}
				cur.WriteRune(runes[i])
				i++
			}
			if i >= len(runes) {
				return nil, fmt.Errorf("unterminated double quote")
			}
			i++
		default:
			haveCur = true
			cur.WriteRune(c)
			i++
		}
	}
	if haveCur {
		fields = append(fields, cur.String())
	}
	return fields, nil
}
