package main

// The global --format flag (json/yaml/toml/text/markdown; formatFlagUsage
// states its default) routes every command's output through the shared cobrafmt filter — see cmd/ctxloom
// for the same pattern. Commands call cobrafmt.Emit/Resolve directly.
//
// --json is nothing but shorthand for --format json (cobrafmt.Resolve honors it
// wherever it is set), so it is declared at the same scope as the flag it
// abbreviates: a shorthand available on only some subcommands is a shorthand
// the user cannot rely on.
// formatFlagUsage is --format's help. Its default is derived, not fixed —
// cobrafmt.Resolve answers text on a terminal and json off one — so the flag is
// registered with an empty default and the usage says what an unset flag does.
const formatFlagUsage = "Output format: json, yaml, toml, text, or markdown (default: text on a terminal, json when output is piped or redirected)"

func init() {
	rootCmd.PersistentFlags().String("format", "", formatFlagUsage)
	rootCmd.PersistentFlags().Bool("json", false, "shorthand for --format json (for jq)")
}
