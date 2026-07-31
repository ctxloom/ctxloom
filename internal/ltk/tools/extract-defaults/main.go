// Command extract-defaults assembles cmd/ltk/sample.ltk.yaml — the rule set
// shipped with ltk and embedded into the binary — from the fenced ```yaml blocks
// in docs/ltk/DEFAULTS.md, which is the source of truth. Run with no args to
// (re)generate the file; run with -check to exit non-zero if it is out of sync.
// Nothing currently invokes -check: there is no lefthook entry and no CI step
// for it (see docs/architecture/companions/ltk.md, "Invariants").
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ctxloom/ctxloom/internal/ltk/rules"
)

const (
	source    = "docs/ltk/DEFAULTS.md"
	generated = "cmd/ltk/sample.ltk.yaml"
	// header is prepended to the generated file. It is written verbatim into a
	// user's project on `ltk manage install`, so it must be user-facing and free
	// of this project's own tooling (no just/lefthook/versionator references).
	header = "# llm-tool-killer default rules — edit freely and commit alongside your project.\n" +
		"# Rule model: https://github.com/ctxloom/ctxloom/blob/main/docs/ltk/RULES.md\n"
)

// yamlBlocks returns the body of every fenced block in md, and refuses any
// fence that does not open with exactly "```yaml".
//
// Every fenced block in the source document is a rule block — it is the source
// of truth for the shipped guard, not a mixed tutorial — so a fence spelled
// ```yml, ```YAML, ```yaml title="…" or left bare is an authoring slip, and
// SKIPPING it drops a rule out of the binary ltk installs into a user's
// project. Nothing downstream would catch that: the rule-count floor only
// fires on a large loss, and -check cannot fire at all, because it compares
// the generated file against a re-extraction of the same document by this same
// reader — both sides lose the same block and agree perfectly on a rule set
// that is missing one. An unterminated block is the same loss by another
// route.
func yamlBlocks(md []byte) ([][]byte, error) {
	var (
		blocks  [][]byte
		body    []string
		open    bool
		openLn  int
		lineNum int
	)
	for _, line := range strings.Split(string(md), "\n") {
		lineNum++
		if !strings.HasPrefix(line, "```") {
			if open {
				body = append(body, line)
			}
			continue
		}
		if open {
			blocks = append(blocks, []byte(strings.Join(body, "\n")))
			body, open = nil, false
			continue
		}
		if info := strings.TrimPrefix(line, "```"); info != "yaml" {
			return nil, fmt.Errorf(
				"%s:%d: fenced block opens with ```%s — every fenced block in this document is a rule block and must open with ```yaml exactly, or its rules are silently dropped from the shipped defaults",
				source, lineNum, info)
		}
		open, openLn = true, lineNum
	}
	if open {
		return nil, fmt.Errorf("%s:%d: ```yaml block is never closed; its rules would be dropped from the shipped defaults", source, openLn)
	}
	return blocks, nil
}

// assemble concatenates the bodies of every ```yaml block in md (in order),
// under the generated-file header, and returns the bytes. It errors if md has no
// yaml blocks, if the result is not a valid rule set, or if it carries fewer
// than minRules rules. It is pure (no I/O) so it can be tested directly.
func assemble(md []byte, minRules int) ([]byte, error) {
	matches, err := yamlBlocks(md)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no ```yaml blocks found")
	}
	var b bytes.Buffer
	b.WriteString(header)
	for _, m := range matches {
		b.Write(bytes.TrimRight(m, "\n"))
		b.WriteByte('\n')
	}
	out := b.Bytes()
	cfg, err := rules.Parse(out)
	if err != nil {
		return nil, fmt.Errorf("assembled defaults do not parse: %w", err)
	}
	// U077-F01: the block-count check above validates the FENCES, not the
	// rules. A document whose fences carry only prose, comments or a bare
	// `rules:` assembles to a rule-free config, and rules.Parse accepts that —
	// its decoder tolerates io.EOF and normalizeAndValidate has no minimum. The
	// result is embedded into the ltk binary and written into a user's project
	// on `manage install`, so shipping it means ltk installs a guard that
	// permits everything, silently, out of a generator that reported success.
	//
	// The floor is not just >0: the doc ships 17 blocks and 16 rules, and a
	// silent drop from 16 to 2 is as invisible as a drop to 0.
	if len(cfg.Rules) == 0 {
		return nil, fmt.Errorf("assembled defaults contain no rules (%d yaml blocks parsed) — refusing to ship a permit-everything default", len(matches))
	}
	if len(cfg.Rules) < minRules {
		return nil, fmt.Errorf("assembled defaults contain only %d rules, below the floor of %d — %s has probably lost blocks", len(cfg.Rules), minRules, source)
	}
	return out, nil
}

// minDefaultRules is the floor the shipped default rule set must clear. The doc
// currently assembles to 16; this is deliberately well below that so ordinary
// edits do not trip it, and well above zero so a gutted doc does.
const minDefaultRules = 8

func main() {
	check := flag.Bool("check", false, "verify the generated file is in sync; non-zero exit on drift")
	flag.Parse()

	md, err := os.ReadFile(source)
	must(err)

	out, err := assemble(md, minDefaultRules)
	must(err)

	if *check {
		have, err := os.ReadFile(generated)
		if err != nil || !bytes.Equal(have, out) {
			fail("%s is out of sync with %s — run `just defaults`", generated, source)
		}
		fmt.Printf("%s is in sync with %s\n", generated, source)
		return
	}
	must(os.WriteFile(generated, out, 0o644))
	fmt.Printf("wrote %s\n", generated)
}

func must(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "extract-defaults: "+format+"\n", a...)
	os.Exit(1)
}
