// Command extract-defaults assembles cmd/ltk/sample.ltk.yaml — the rule set
// shipped with ltk and embedded into the binary — from the fenced ```yaml blocks
// in docs/DEFAULTS.md, which is the source of truth. Run with no args to
// (re)generate the file; run with -check to exit non-zero if it is out of sync
// (used by the lefthook pre-commit hook).
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"regexp"

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

// blockRe captures the body of each ```yaml fenced block.
var blockRe = regexp.MustCompile("(?s)```yaml\n(.*?)```")

// assemble concatenates the bodies of every ```yaml block in md (in order),
// under the generated-file header, and returns the bytes. It errors if md has no
// yaml blocks, if the result is not a valid rule set, or if it carries fewer
// than minRules rules. It is pure (no I/O) so it can be tested directly.
func assemble(md []byte, minRules int) ([]byte, error) {
	matches := blockRe.FindAllSubmatch(md, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("no ```yaml blocks found")
	}
	var b bytes.Buffer
	b.WriteString(header)
	for _, m := range matches {
		b.Write(bytes.TrimRight(m[1], "\n"))
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
