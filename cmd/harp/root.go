package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/internal/shared/harp"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// progName is the binary/command name, used in the cobra root and in
// error-line prefixes, matching the ltk/taskloom family convention.
const progName = "harp"

// generateOpts holds the flag-bound values consumed by the root command's
// RunE. Its fields mirror harp.Options 1:1 by design: this binary preserves
// the exact flag surface the removed `ctxloom harp` subcommand exposed
// (-c/-s/--max-len/-n/-g), just wrapping a standalone command tree instead
// of a ctxloom subcommand.
type generateOpts struct {
	components int
	separator  string
	maxLen     int
	count      int
	group      string
}

// newRootCmd assembles harp's command tree. Kept a factory (matching the
// ltk/taskloom family convention) so tests exercise exactly the tree the
// binary runs, and a future doc generator could walk it the same way.
func newRootCmd() *cobra.Command {
	var opts generateOpts

	root := &cobra.Command{
		Use:   progName,
		Short: "Generate harp IDs (human-appropriate random phraselets)",
		Long: `Generate human-readable, memorable identifiers from adjective-noun
combinations, e.g. "swift-amber-falcon".

ctxloom uses harp IDs internally for sessions and tasks. This standalone
binary exposes the same generator for ad-hoc and scripted use.

Examples:
  harp                          # one name, default 3 components
  harp -c 2                     # adj-noun
  harp -n 5                     # five names, one per line
  harp -g long                  # draw from the long-word group
  harp -s _ --max-len 5         # short_hawk style
  harp -n 3 --format json       # ["a-b-c", "d-e-f", "g-h-i"]`,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          opts.run,
	}

	root.Flags().IntVarP(&opts.components, "components", "c", 3,
		fmt.Sprintf("number of words (%d-%d)", harp.MinComponents, harp.MaxComponents))
	root.Flags().StringVarP(&opts.separator, "separator", "s", "-", "delimiter between words")
	root.Flags().IntVar(&opts.maxLen, "max-len", 0, "max characters per word (0 = no cap)")
	root.Flags().IntVarP(&opts.count, "number", "n", 1, "how many names to generate")
	root.Flags().StringVarP(&opts.group, "group", "g", harp.DefaultGroup, groupsHelp())
	root.PersistentFlags().String("format", string(clifmt.FormatText),
		"output format: json, yaml, toml, text, or markdown")

	root.AddCommand(newVersionCmd())
	return root
}

// run is the root command's RunE. A METHOD rather than a package-level
// function because the command literal is built inside newRootCmd() and its
// body reads that call's own `opts` — a bound method value on the addressable
// local preserves that exactly, where a package-level func would need package
// state and would break the factory's re-entrancy.
func (o *generateOpts) run(cmd *cobra.Command, _ []string) error {
	return runGenerate(cmd, *o)
}

// groupsHelp renders the available word-list groups for the --group flag's
// usage text.
func groupsHelp() string {
	g := harp.Groups()
	slices.Sort(g)
	return fmt.Sprintf("word-list group (%s)", strings.Join(g, ", "))
}

// runGenerate produces opts.count names and renders them through clifmt.
// The result is handed to clifmt as a bare []string (not wrapped in a
// result struct): clifmt renders a top-level slice of scalars as a single
// JSON array (json), a native YAML/TOML list, one line per name (text), or
// a bullet list (markdown) — exactly the "array of names" / "newline-
// separated" shapes this binary needs, with no per-command rendering code
// of its own.
func runGenerate(cmd *cobra.Command, opts generateOpts) error {
	// count<=0 used to silently render an empty payload at exit 0
	// (text's default renderer writes nothing for an empty slice, so the
	// default invocation path failed silently). A generator asked to
	// generate something that produces nothing has failed, not succeeded.
	if opts.count < 1 {
		return fmt.Errorf("--number must be at least 1, got %d", opts.count)
	}
	// The flag help advertises a component range and a group set; the library
	// deliberately CLAMPS out-of-range options rather than rejecting them (its
	// documented zero-value contract), so enforcement of what this CLI promises
	// has to live here. Otherwise a typo becomes a plausible wrong answer at
	// exit 0 — `-c 1` renders three words, `-g typo` renders the default group.
	if opts.components < harp.MinComponents || opts.components > harp.MaxComponents {
		return fmt.Errorf("--components must be between %d and %d, got %d",
			harp.MinComponents, harp.MaxComponents, opts.components)
	}
	if groups := harp.Groups(); !slices.Contains(groups, opts.group) {
		slices.Sort(groups)
		return fmt.Errorf("--group %q is not a word-list group (have: %s)", opts.group, strings.Join(groups, ", "))
	}
	// Same principle, third limb. An empty Separator is the one remaining
	// value normalize REWRITES rather than uses ("" becomes "-"), so `-s ""`
	// asked for no separator and got the default one back at exit 0. This
	// layer cannot honour the request instead: harp.Options has no way to say
	// "explicitly empty" — the zero value already means "use the default" —
	// so refusing is the only answer that is not a different one.
	if opts.separator == "" {
		return fmt.Errorf("--separator must not be empty (the generator would substitute %q)", "-")
	}

	format, err := resolveFormat(cmd)
	if err != nil {
		return err
	}

	genOpts := harp.Options{
		Components:       opts.components,
		MaxElementLength: opts.maxLen,
		Separator:        opts.separator,
		Group:            opts.group,
	}
	names := make([]string, 0, opts.count)
	for range opts.count {
		names = append(names, harp.GenerateNameWithOptions(genOpts))
	}

	return clifmt.Render(cmd.OutOrStdout(), names, format)
}

// resolveFormat reads the --format flag (a persistent flag, so it resolves
// through cobra's inherited-flag machinery on subcommands too) via the
// family-wide resolver.
//
// A private re-implementation (GetString + clifmt.ParseFormat) drifts: it
// rejects an empty --format as unsupported, so `harp --format ""` fails where
// every other binary in the family renders text. One reader, one behavior; the
// parity test in format_parity_test.go pins it.
func resolveFormat(cmd *cobra.Command) (clifmt.Format, error) {
	return cliemit.Resolve(cmd)
}
