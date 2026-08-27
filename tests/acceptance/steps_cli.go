//go:build acceptance

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/pkg/clifmt"
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
		_, err := lastOutputJSONDoc(worldFrom(c))
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

	// --- Format-aware assertions ------------------------------------------
	//
	// ONE scenario body, every encoding. Off a terminal the CLI derives JSON,
	// an explicit --format wins in both directions, and a Scenario Outline
	// drives the same command once per format — so an assertion that only
	// speaks JSON would force a scenario per format and delete the human
	// renderer's only coverage.
	//
	// These read the encoding off the INVOCATION (see formatAskedFor), then:
	//
	//   json, yaml, toml   decode, resolve the dotted PATH, compare the value
	//                      there. json/yaml/toml decode to the same generic
	//                      shapes, so ONE path addresses all three and one
	//                      expected value serves all three.
	//   text, markdown     the row's own expected column carries that
	//                      renderer's prose, matched as a substring — which is
	//                      exactly the assertion these scenarios always made.
	//
	// So the Examples table varies the expected value BY ROW while the path
	// stays fixed in the step: each row states the fact as its own format
	// spells it, and a row that passes no --format at all asserts the derived
	// default is the structured payload.
	//
	// Substring matching is confined to the RENDERED formats on purpose. A
	// substring of a JSON blob matches across field boundaries — "raw" is
	// satisfied by a key, a different array, or a fragment of a path — which
	// is the vacuous-assertion shape this suite was audited for. Structured
	// rows are always path-exact.

	ctx.Step(`^the output reports "([^"]*)" as "([^"]*)"$`,
		func(c context.Context, path, expected string) error {
			return assertOutputReports(worldFrom(c), path, expected, func(v any) error {
				got, ok := jsonScalar(v)
				if !ok {
					return fmt.Errorf("is a %s, not a scalar — address one element or field", jsonKind(v))
				}
				if got != expected {
					return fmt.Errorf("is %q, want %q", got, expected)
				}
				return nil
			})
		})

	// For values no test can know in advance — a temp-directory path, a
	// version stamp — where the claim is still about the value and not merely
	// that some key exists. The rendered rows carry a plain substring; only
	// the structured rows read the column as a pattern.
	ctx.Step(`^the output reports "([^"]*)" matching "([^"]*)"$`,
		func(c context.Context, path, expected string) error {
			return assertOutputReports(worldFrom(c), path, expected, func(v any) error {
				re, err := regexp.Compile(expected)
				if err != nil {
					return fmt.Errorf("cannot be matched: %q is not a valid regexp: %v", expected, err)
				}
				got, ok := jsonScalar(v)
				if !ok {
					return fmt.Errorf("is a %s, not a scalar — address one element or field", jsonKind(v))
				}
				if !re.MatchString(got) {
					return fmt.Errorf("is %q, which does not match %q", got, expected)
				}
				return nil
			})
		})

	// Membership in an array of scalars — a list of forms, names, tags —
	// where position is an implementation detail and presence is the contract.
	ctx.Step(`^the output reports "([^"]*)" containing "([^"]*)"$`,
		func(c context.Context, path, expected string) error {
			return assertOutputReports(worldFrom(c), path, expected, func(v any) error {
				entries, ok := v.([]any)
				if !ok {
					return fmt.Errorf("is a %s, not an array", jsonKind(v))
				}
				// "every element" and "some element" are both vacuously
				// satisfiable over an empty array, which is the silent-no-op
				// shape: an empty list contains nothing, so say so.
				if len(entries) == 0 {
					return fmt.Errorf("is EMPTY, so it contains nothing and %q is not in it", expected)
				}
				for _, e := range entries {
					if got, ok := jsonScalar(e); ok && got == expected {
						return nil
					}
				}
				return fmt.Errorf("does not contain %q", expected)
			})
		})
}

// assertOutputReports is the one format branch every format-aware step shares:
// a rendered format is matched as prose, a structured one is decoded and
// addressed by path.
func assertOutputReports(w *World, path, expected string, check func(any) error) error {
	format := formatAskedFor(w)
	if !format.Structured() {
		if !strings.Contains(w.env.LastOutput(), expected) {
			return fmt.Errorf("the %s rendering does not report %q; output:\n%s", format, expected, w.env.LastOutput())
		}
		return nil
	}
	doc, err := lastOutputStructured(w, format)
	if err != nil {
		return err
	}
	v, err := jsonAtPath(doc, path)
	if err != nil {
		return fmt.Errorf("%v; %s stdout:\n%s", err, format, w.env.LastStdout())
	}
	if err := check(v); err != nil {
		return fmt.Errorf("the %s payload at %q %v; stdout:\n%s", format, path, err, w.env.LastStdout())
	}
	return nil
}

// formatAskedFor reports the encoding the last command was asked for, read off
// its own command line: an explicit --format/-o value, --json, or — when
// nothing was asked for — the format the CLI DERIVES for a stdout that is not
// a terminal.
//
// That default is load-bearing rather than a convenience. This harness
// captures stdout through a pipe and is never a terminal, so a row that passes
// no --format at all is exercising the derived default; resolving it to the
// structured payload here is what makes such a row a test of that rule instead
// of a test of whatever the command happened to emit.
//
// STDOUT ONLY. An assertion about the ERROR stream must ask formatExplicit
// instead — see its doc for why the two streams diverge.
func formatAskedFor(w *World) clifmt.Format {
	if f, ok := formatOnCommandLine(w); ok {
		return f
	}
	return derivedNonTerminalFormat
}

// formatExplicit reports whether the last command's own command line SET a
// format, as opposed to having one derived for it.
//
// The distinction governs the ERROR stream: cliemit.EmitError renders a
// structured error only when the format was EXPLICITLY asked for, because a
// derived format is not a request, and stdout's consumer says nothing about
// who reads stderr — a different fd with a different reader. So a no-flag row
// gets the human "Error: ..." line even though its STDOUT would have been
// JSON, and an error assertion that branches on formatAskedFor alone tries to
// decode prose as an envelope.
func formatExplicit(w *World) bool {
	_, ok := formatOnCommandLine(w)
	return ok
}

// formatOnCommandLine scans the last command's own argv for a format request.
// Both callers above read the same flags, so they read them in one place: two
// copies of this scan would drift the moment a spelling is added.
func formatOnCommandLine(w *World) (clifmt.Format, bool) {
	fields := w.env.LastArgs()
	for i, f := range fields {
		switch {
		case f == "--json":
			return clifmt.FormatJSON, true
		case f == "--format" || f == "-o":
			if i+1 < len(fields) {
				if parsed, err := clifmt.ParseFormat(fields[i+1]); err == nil {
					return parsed, true
				}
			}
		case strings.HasPrefix(f, "--format="):
			if parsed, err := clifmt.ParseFormat(strings.TrimPrefix(f, "--format=")); err == nil {
				return parsed, true
			}
		}
	}
	return clifmt.Format(""), false
}

// derivedNonTerminalFormat is what cliemit.Resolve settles on when stdout is
// not a terminal and no format was asked for. Naming it once keeps every
// no-flag Examples row asserting the same rule.
const derivedNonTerminalFormat = clifmt.FormatJSON

// lastOutputStructured decodes the last command's STDOUT in the structured
// encoding it was asked for.
//
// The switch is on the FORMAT, not on JSON alone, because that is the seam the
// other structured encodings extend through: every structured encoder decodes
// to the same generic Go shapes, so a new case is a decoder call and nothing
// else — no second path grammar and no second expected value. Only the
// encodings this suite actually drives are wired, so no untested decoder is
// carried on the promise of a scenario that does not exist.
func lastOutputStructured(w *World, format clifmt.Format) (any, error) {
	stdout := w.env.LastStdout()
	if strings.TrimSpace(stdout) == "" {
		return nil, fmt.Errorf("stdout is empty — nothing to decode as %s; combined output:\n%s", format, w.env.LastOutput())
	}
	var doc any
	switch format {
	case clifmt.FormatJSON:
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			return nil, fmt.Errorf("stdout is not valid %s: %v; stdout:\n%s", format, err, stdout)
		}
	default:
		return nil, fmt.Errorf("no scenario drives %s yet, so this suite decodes no %s payload; stdout:\n%s", format, format, stdout)
	}
	return doc, nil
}

// jsonScalar renders a decoded JSON value for comparison, and reports whether
// it IS one. Containers and null are refused rather than stringified: a
// comparison against "[map[...]]" or "<nil>" is one nobody wrote on purpose
// and one that a missing field can satisfy.
func jsonScalar(v any) (string, bool) {
	switch v.(type) {
	case map[string]any, []any, nil:
		return "", false
	}
	return fmt.Sprintf("%v", v), true
}

func jsonKind(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case nil:
		return "null"
	}
	return "scalar"
}

// jsonAtPath walks a dotted path from a decoded document. "$" (or the empty
// path) is the document itself; a numeric segment indexes an array; anything
// else is an object key.
func jsonAtPath(doc any, path string) (any, error) {
	if path == "" || path == "$" {
		return doc, nil
	}
	cur := doc
	walked := ""
	for i, seg := range jsonPathSegments(path) {
		// A leading "$" names the document itself, so it addresses nothing
		// further and is skipped rather than looked up as a key.
		if i == 0 && seg == "$" {
			walked = "$"
			continue
		}
		walked = jsonWalkedPath(walked, seg)

		next, err := jsonStepInto(cur, seg, walked)
		if err != nil {
			return nil, err
		}
		cur = next
	}
	return cur, nil
}

// jsonWalkedPath extends the breadcrumb of what has been walked so far. It
// exists so a failure names the path the reader wrote ("entries[0].Source")
// rather than the segment it died on, which alone says nothing about where.
func jsonWalkedPath(walked, seg string) string {
	switch {
	case walked == "":
		return seg
	case strings.HasPrefix(seg, "["):
		return walked + seg
	default:
		return walked + "." + seg
	}
}

// jsonStepInto resolves one path segment against the current node. A segment
// either SELECTS out of an array by predicate or DESCENDS into a container;
// walked is carried only to name the position in an error.
func jsonStepInto(cur any, seg, walked string) (any, error) {
	// A "[field=value]" segment SELECTS the array element that carries
	// that value, so an assertion can name the object it means instead of
	// its index. A list's position is an implementation detail; which
	// bundle was signed, or which store an entry came from, is the claim.
	if field, want, ok := jsonPathPredicate(seg); ok {
		return jsonSelectAt(cur, seg, walked, field, want)
	}
	return jsonDescend(cur, seg, walked)
}

// jsonSelectAt applies a "[field=value]" predicate to an array node.
func jsonSelectAt(cur any, seg, walked, field, want string) (any, error) {
	entries, isArray := cur.([]any)
	if !isArray {
		return nil, fmt.Errorf("JSON output at %q is a %s, so %q selects nothing", walked, jsonKind(cur), seg)
	}
	match, err := jsonSelect(entries, field, want)
	if err != nil {
		return nil, fmt.Errorf("at %q: %w", walked, err)
	}
	return match, nil
}

// jsonDescend reads seg as an object key or an array index, depending on what
// the current node actually is.
func jsonDescend(cur any, seg, walked string) (any, error) {
	switch node := cur.(type) {
	case map[string]any:
		v, ok := node[seg]
		if !ok {
			return nil, fmt.Errorf("JSON output has nothing at %q (no %q key)", walked, seg)
		}
		return v, nil
	case []any:
		i, err := strconv.Atoi(seg)
		if err != nil {
			return nil, fmt.Errorf("JSON output at %q is an array, so %q must be an index", walked, seg)
		}
		if i < 0 || i >= len(node) {
			return nil, fmt.Errorf("JSON output at %q indexes element %d of a %d-element array", walked, i, len(node))
		}
		return node[i], nil
	default:
		return nil, fmt.Errorf("JSON output at %q is a %s, so %q addresses nothing beneath it", walked, jsonKind(cur), seg)
	}
}

// lastOutputJSONDoc decodes the last command's STDOUT as a JSON document of
// whatever shape it has — a bare top-level array is as valid a payload as an
// object, and several commands emit one. Parsing the combined stream cannot
// work — one stderr advisory line concatenated onto the payload makes it
// unparseable — so this reads stdout alone.
func lastOutputJSONDoc(w *World) (any, error) {
	stdout := w.env.LastStdout()
	if strings.TrimSpace(stdout) == "" {
		return nil, fmt.Errorf("stdout is empty — nothing to parse as JSON; combined output:\n%s", w.env.LastOutput())
	}
	var doc any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		return nil, fmt.Errorf("stdout is not valid JSON: %v; stdout:\n%s", err, stdout)
	}
	return doc, nil
}

// jsonPathSegments splits a dotted path into segments, breaking a
// "[field=value]" predicate out as its own segment whether or not a dot
// precedes it — so "signed[bundle=x].signed_by" reads the way a jq user would
// write it — and keeping the predicate whole even though the field inside it
// may itself be dotted.
func jsonPathSegments(path string) []string {
	var segs []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			segs = append(segs, cur.String())
			cur.Reset()
		}
	}
	depth := 0
	for _, r := range path {
		switch r {
		case '[':
			if depth == 0 {
				flush()
			}
			depth++
			cur.WriteRune(r)
		case ']':
			if depth > 0 {
				depth--
			}
			cur.WriteRune(r)
			if depth == 0 {
				flush()
			}
		case '.':
			if depth == 0 {
				flush()
				continue
			}
			cur.WriteRune(r)
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return segs
}

func jsonPathPredicate(seg string) (field, want string, ok bool) {
	if !strings.HasPrefix(seg, "[") || !strings.HasSuffix(seg, "]") {
		return "", "", false
	}
	inner := seg[1 : len(seg)-1]
	field, want, ok = strings.Cut(inner, "=")
	return field, want, ok
}

// jsonSelect returns the one array element whose field holds want. AMBIGUITY
// IS AN ERROR: if two elements match, the assertion that follows would read a
// value off whichever came first, and "the entry for X" would silently become
// "some entry for X".
func jsonSelect(entries []any, field, want string) (any, error) {
	var found []any
	for _, e := range entries {
		v, err := jsonAtPath(e, field)
		if err != nil {
			continue
		}
		if got, ok := jsonScalar(v); ok && got == want {
			found = append(found, e)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no element of the %d-element array has %s=%q", len(entries), field, want)
	case 1:
		return found[0], nil
	default:
		return nil, fmt.Errorf("%d elements have %s=%q, so this selects no single one", len(found), field, want)
	}
}

// lastOutputJSON decodes the last command's STDOUT as a JSON OBJECT, for the
// callers whose claim is about the top-level object's own keys.
func lastOutputJSON(w *World) (map[string]any, error) {
	doc, err := lastOutputJSONDoc(w)
	if err != nil {
		return nil, err
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("stdout is not a JSON object but a %s; stdout:\n%s", jsonKind(doc), w.env.LastStdout())
	}
	return obj, nil
}

// lastOutputJSONAt resolves a dotted path against the last command's payload.
func lastOutputJSONAt(w *World, path string) (any, error) {
	doc, err := lastOutputJSONDoc(w)
	if err != nil {
		return nil, err
	}
	v, err := jsonAtPath(doc, path)
	if err != nil {
		return nil, fmt.Errorf("%v; stdout:\n%s", err, w.env.LastStdout())
	}
	return v, nil
}

// lastOutputJSONArray resolves an array by PATH, so the membership steps reach
// a nested array and a bare top-level one ("$") as readily as a flat key.
func lastOutputJSONArray(w *World, path string) ([]any, error) {
	v, err := lastOutputJSONAt(w, path)
	if err != nil {
		return nil, err
	}
	entries, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("JSON output at %q is a %s, not an array; stdout:\n%s", path, jsonKind(v), w.env.LastStdout())
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
