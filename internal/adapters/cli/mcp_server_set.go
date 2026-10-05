package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

var (
	// errNotABundleMCPRef: the ref does not select one bundle-scoped MCP
	// server. Every MCP server lives in a bundle, so there is no other store a
	// write could reach.
	errNotABundleMCPRef = errors.New("not a bundle-scoped MCP ref")
	// errMCPSetPairNoEquals: a --env or --header entry has no NAME=value form.
	// The message never echoes the entry: it may carry a secret.
	errMCPSetPairNoEquals = errors.New("an entry is not NAME=value")
	// errMCPSetPairTwice: one name given two different values; a map keeps
	// one, so passing it on would store something the command line does not say.
	errMCPSetPairTwice = errors.New("names one key twice with different values")
)

// parseBundleMCPRef splits a `<bundle>#mcp/<name>` ref, judged by
// bundles.ParseItemAsk, the selector parser every reader shares.
func parseBundleMCPRef(ref string) (bundle, item string, err error) {
	want := "<bundle>#" + trust.FormatSelector(trust.KindMCP, "<name>")
	ask, perr := bundles.ParseItemAsk(ref)
	if perr != nil || !ask.Scoped || ask.Kind != trust.KindMCP {
		return "", "", fmt.Errorf("%w: %q (expected %s); every MCP server lives in a bundle, so there is no other store to address", errNotABundleMCPRef, ref, want)
	}
	if ask.Bundle == "" || ask.Item == "" {
		return "", "", fmt.Errorf("%w: incomplete ref %q (expected %s)", errNotABundleMCPRef, ref, want)
	}
	return ask.Bundle, ask.Item, nil
}

// mcpServerSetFlags holds `mcp server set`'s flag values. Which of them reach
// the patch is decided by pflag's Changed, never by the value: an empty value
// is a clear, not an absence.
type mcpServerSetFlags struct {
	command, url, servedBy, notes, installation string
	args, env, headers, tags                    []string
}

var mcpServerSetOpts mcpServerSetFlags

var mcpServerSetCmd = &cobra.Command{
	Use:   "set <bundle>#mcp/<name>",
	Short: "Set fields of a bundle's MCP server from flags",
	Long: `Set fields of a bundle-scoped MCP server without opening an editor.

Only the flags you pass change the entry; every other field keeps its stored
value. A name the bundle lacks is created. An empty value clears a field:
--url "" clears the url, and --arg "", --env "", --header "" or --tag ""
clears that list or map. A repeatable flag replaces the stored list or map
with exactly the values given; --env and --header take NAME=value.

A server has exactly one target: --command (with --arg), --url, or
--served-by session-endpoint. To move to another target, clear the old one in
the same call (the last example); a set that would leave none or two is
refused and nothing is saved.

Header values land in shell history and in signed bundle content, so name a secret through an environment variable the engine expands, never as a literal.

Examples:
  ctxloom mcp server set tools#mcp/search --url https://mcp.example.com/v1 --header X-Team=core --tag search
  ctxloom mcp server set tools#mcp/search --notes "Team search index"
  ctxloom mcp server set tools#mcp/search --url "" --header "" --command search-mcp --arg=--stdio`,
	Args: cobra.ExactArgs(1),
	RunE: runMCPServerSet,
}

func registerMCPServerSetFlags(cmd *cobra.Command, f *mcpServerSetFlags) {
	fl := cmd.Flags()
	fl.StringVar(&f.command, "command", "", `Command that launches a stdio server ("" clears it)`)
	fl.StringArrayVar(&f.args, "arg", nil, `Command argument (repeatable; replaces the list; "" clears it)`)
	fl.StringArrayVar(&f.env, "env", nil, `Environment variable as NAME=value (repeatable; replaces the map; "" clears it)`)
	fl.StringVar(&f.url, "url", "", `Endpoint of a remote server, http or https ("" clears it)`)
	fl.StringArrayVar(&f.headers, "header", nil, `HTTP header as Name=value (repeatable; replaces the map; "" clears it)`)
	fl.StringArrayVar(&f.tags, "tag", nil, `Tag (repeatable; replaces the list; "" clears it)`)
	fl.StringVar(&f.servedBy, "served-by", "", `"session-endpoint": the running session serves this entry ("" clears it)`)
	fl.StringVar(&f.notes, "notes", "", `Human-readable notes ("" clears them)`)
	fl.StringVar(&f.installation, "installation", "", `Setup instructions shown to the user ("" clears them)`)
}

func runMCPServerSet(cmd *cobra.Command, args []string) error {
	bundleName, name, err := parseBundleMCPRef(args[0])
	if err != nil {
		return fmt.Errorf("mcp server set: %w", err)
	}
	in, err := mcpServerSetOpts.input(cmd.Flags())
	if err != nil {
		return fmt.Errorf("mcp server set: %w", err)
	}
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	res, err := operations.SetBundleMCP(cmd.Context(), cfg, operations.SetBundleMCPRequest{Bundle: bundleName, Name: name, MCP: in})
	if err != nil {
		return err
	}
	verb := "Set"
	if res.Status == operations.SetBundleMCPStatusCreated {
		verb = "Created"
	}
	return emit(cmd, res, func() error {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s MCP server %q in bundle %q\n", verb, name, bundleName)
		return err
	})
}

// input is the patch the typed flags describe: an untyped flag is absent from
// it, so the stored field is kept.
func (f *mcpServerSetFlags) input(fs *pflag.FlagSet) (operations.BundleMCPInput, error) {
	named := func(flag string, v *string) *string {
		if fs.Changed(flag) {
			return v
		}
		return nil
	}
	in := operations.BundleMCPInput{
		Command:      named("command", &f.command),
		URL:          named("url", &f.url),
		ServedBy:     named("served-by", &f.servedBy),
		Notes:        named("notes", &f.notes),
		Installation: named("installation", &f.installation),
		Args:         namedList(fs, "arg", f.args),
		Tags:         namedList(fs, "tag", f.tags),
	}
	var err error
	if in.Env, err = namedPairs(fs, "env", f.env); err != nil {
		return in, err
	}
	in.Headers, err = namedPairs(fs, "header", f.headers)
	return in, err
}

// namedList is a typed repeatable flag's values with empty entries dropped, so
// a lone "" sends an empty list, which clears; nil when the flag was not typed.
func namedList(fs *pflag.FlagSet, flag string, vals []string) *[]string {
	if !fs.Changed(flag) {
		return nil
	}
	return new(slices.DeleteFunc(slices.Clone(vals), func(v string) bool { return v == "" }))
}

// namedPairs is a typed NAME=value flag as a map, split at the first "=" so a
// value may contain one; empty entries are dropped as in namedList.
func namedPairs(fs *pflag.FlagSet, flag string, vals []string) (*map[string]string, error) {
	if !fs.Changed(flag) {
		return nil, nil
	}
	out := map[string]string{}
	for _, v := range vals {
		if v == "" {
			continue
		}
		k, val, ok := strings.Cut(v, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--%s: %w", flag, errMCPSetPairNoEquals)
		}
		if prev, dup := out[k]; dup && prev != val {
			return nil, fmt.Errorf("--%s %s: %w", flag, k, errMCPSetPairTwice)
		}
		out[k] = val
	}
	return &out, nil
}

func init() {
	registerMCPServerSetFlags(mcpServerSetCmd, &mcpServerSetOpts)
	mcpServerCmd.AddCommand(mcpServerSetCmd)
}
