package main

import (
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// The global --format flag (json/yaml/toml/text/markdown, default text) routes
// every command's output through the shared cliemit filter — see cmd/ctxloom
// for the same pattern. Commands call cliemit.Emit/Resolve directly.
//
// --json is nothing but shorthand for --format json (cliemit.Resolve honors it
// wherever it is set), so it is declared at the same scope as the flag it
// abbreviates: a shorthand available on only some subcommands is a shorthand
// the user cannot rely on.
func init() {
	rootCmd.PersistentFlags().String("format", string(clifmt.FormatText),
		"Output format: json, yaml, toml, text, or markdown")
	rootCmd.PersistentFlags().Bool("json", false, "shorthand for --format json (for jq)")
}
