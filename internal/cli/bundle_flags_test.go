package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every flag on every `bundle` subcommand, pinned by name, shorthand, default
// and usage. Flags are a user-visible contract AND the input to the generated
// reference docs, so relocating where they are REGISTERED must not move a
// character of what they ARE.
func TestBundleSubcommandFlags(t *testing.T) {
	type flagSpec struct {
		name      string
		shorthand string
		defValue  string
		usage     string
	}

	for _, tc := range []struct {
		cmd   *cobra.Command
		flags []flagSpec
	}{
		{bundleCreateCmd, []flagSpec{
			{"description", "d", "", "Bundle description"},
		}},
		{bundleRemoveCmd, []flagSpec{
			{"yes", "y", "false", "Apply the removal this invocation would report (default: report only)"},
		}},
		{bundlePushCmd, []flagSpec{
			{"pr", "", "false", "Create a pull request instead of pushing directly"},
			{"message", "m", "", "Commit message"},
			// The wording is load-bearing, which is why it is pinned here: a
			// signature belongs to the BUNDLE, so --sign describes signing
			// FIRST and --no-sign describes publishing BARE. The old text
			// ("don't sign, even if sign.default is true") described signing as
			// a property of the push, which is the model this replaced.
			{"sign", "", "false", "sign the bundle first, then publish that signature (same as `ctxloom bundle sign <name>` before pushing)"},
			{"no-sign", "", "false", "publish unsigned: do not carry an existing signature, and do not sign even if sign.default is true"},
		}},
		{bundleImportCmd, []flagSpec{
			{"force", "f", "false", "Overwrite existing bundle"},
		}},
		{bundleExportCmd, []flagSpec{
			{"output", "o", "", "Output file path"},
		}},
		{bundleMoveCmd, []flagSpec{
			{"to", "", "", "Destination: a configured remote name, or a local directory / ctxloom project checkout (a remote name wins)"},
			{"force", "f", "false", "Overwrite an existing bundle at a local destination"},
			{"message", "m", "", "Commit message (remote destination only)"},
		}},
		{bundleViewCmd, []flagSpec{
			{"distilled", "d", "false", "Show distilled version if available"},
		}},
		{bundleShowCmd, []flagSpec{
			{"interactive", "i", "false", "Review per-item effective trust and trust/blacklist individual hooks (interactive terminal only)"},
		}},
		{bundleEditCmd, []flagSpec{
			{"description", "d", "", "New description"},
			{"version", "", "", "New version"},
			{"add-tag", "", "[]", "Tag(s) to add"},
			{"remove-tag", "", "[]", "Tag(s) to remove"},
			{"add-fragment", "", "[]", "Fragment(s) to add"},
			{"remove-fragment", "", "[]", "Fragment(s) to remove"},
			{"add-prompt", "", "[]", "Prompt(s) to add"},
			{"remove-prompt", "", "[]", "Prompt(s) to remove"},
			{"add-mcp", "", "[]", "MCP server(s) to add"},
			{"remove-mcp", "", "[]", "MCP server(s) to remove"},
		}},
		{bundleDistillCmd, []flagSpec{
			{"force", "f", "false", "Re-distill even if unchanged"},
			{"dry-run", "n", "false", "Preview what would be distilled"},
			{"llm", "l", "", "config label to use (e.g. claude-code, claude-fast, antigravity); overrides the configured default"},
		}},
	} {
		t.Run(tc.cmd.Name(), func(t *testing.T) {
			local := tc.cmd.Flags()
			var registered []string
			local.VisitAll(func(f *pflag.Flag) {
				if f.Name != "help" { // cobra injects this one
					registered = append(registered, f.Name)
				}
			})

			var want []string
			for _, spec := range tc.flags {
				want = append(want, spec.name)
				f := local.Lookup(spec.name)
				require.NotNil(t, f, "%s must register --%s", tc.cmd.Name(), spec.name)
				assert.Equal(t, spec.shorthand, f.Shorthand, "--%s shorthand", spec.name)
				assert.Equal(t, spec.defValue, f.DefValue, "--%s default", spec.name)
				assert.Equal(t, spec.usage, f.Usage, "--%s usage", spec.name)
			}
			assert.ElementsMatch(t, want, registered, "no flag may be added or dropped by relocating the registration")
		})
	}

	// `bundle move` cannot run without a destination, and that requirement is
	// an annotation on the flag — easy to lose when the registration moves.
	to := bundleMoveCmd.Flags().Lookup("to")
	require.NotNil(t, to)
	assert.Contains(t, to.Annotations, cobra.BashCompOneRequiredFlag, "--to must stay MarkFlagRequired")
}
