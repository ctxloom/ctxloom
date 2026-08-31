package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/cliemit"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// Execute owns error printing. Without the silence flags cobra prints every
// RunE error twice and dumps the full usage text — including for a wrapped
// LLM's ordinary nonzero exit (run returns ExitError so deferred cleanup can
// run before the process exits).
func TestRootCommand_SilencesCobraErrorNoise(t *testing.T) {
	assert.True(t, rootCmd.SilenceUsage, "usage spam on every error")
	assert.True(t, rootCmd.SilenceErrors, "errors would print twice")
}

// `ctxloom mcp <anything>` must reject unknown subcommands: cobra's legacy
// arg handling would otherwise print the namespace's help and exit 0, which is
// how a mistyped verb reads as a command that ran.
//
// The refusal is the groupNode guard in the RunE, not an Args validator —
// group_node.go documents why Args cannot do this job — so it is driven here
// rather than asserted as a field.
func TestMCPCommand_RejectsUnknownSubcommands(t *testing.T) {
	require.True(t, isGroupNode(mcpCmd), "mcp is a namespace and carries the guard")
	assert.Error(t, mcpCmd.RunE(mcpCmd, []string{"list"}))
}

// cliemit.EmitError is Execute()'s error-printing tail, callable without the
// process-ending os.Exit calls around it. --format text keeps the exact
// "Error: msg\n" line Execute() always printed (a script scraping that line
// today sees no change); the other four formats get clifmt's structured
// {"error": "..."} envelope instead of the same human line regardless of
// --format, which was the one place in the CLI where --format=json still
// leaked plain text onto stderr. The fix moved the rendering itself into
// cliemit, where the package doc already claimed it lived; these assertions
// are unchanged, which is the point.
func TestReportExecuteError_Text_MatchesPreClifmtLine(t *testing.T) {
	var buf bytes.Buffer
	cmd, _ := formatCmd(formatText)
	require.NoError(t, cliemit.EmitError(&buf, cmd, errors.New("boom")))
	assert.Equal(t, "Error: boom\n", buf.String())
}

func TestReportExecuteError_JSON_EmitsStructuredEnvelope(t *testing.T) {
	var buf bytes.Buffer
	cmd, _ := formatCmd(formatJSON)
	require.NoError(t, cliemit.EmitError(&buf, cmd, errors.New("boom")))
	assert.JSONEq(t, `{"error":"boom"}`, buf.String())
}

// PersistentPreRunE is where the CLI's resolved --format flows into
// clidiag's process-wide structured-diagnostics switch (the warnings side
// channel): json/yaml/toml turn it on, text/markdown (and an
// unresolvable value, left for the command's own emit() to report) leave it
// off. cmd is built via formatCmd (format_test.go), the same
// inherited-persistent-flag stand-in this file's other format tests use, since
// mutating rootCmd's real PersistentFlags wouldn't be visible on
// cmd.Flags() without a full cobra parse. Verified end to end via
// clidiag.Fwarn's actual output shape rather than a package-private getter.
func TestPersistentPreRun_SetsClidiagStructuredFromFormat(t *testing.T) {
	t.Cleanup(func() { clidiag.SetStructured(false) })

	cases := []struct {
		format       string
		wantJSONLine bool
	}{
		{"json", true},
		{"yaml", true},
		{"toml", true},
		{"text", false},
		{"markdown", false},
		{"bogus", false},
	}
	for _, c := range cases {
		t.Run(c.format, func(t *testing.T) {
			cmd, _ := formatCmd(c.format)
			// PersistentPreRunE, not PersistentPreRun: the root's pre-run
			// hook is the E variant so it can REFUSE an unsupported
			// --format before RunE runs. The error is irrelevant here —
			// this test is about the clidiag side effect — and cmd is a
			// stand-in whose path carries no format debt, so the refusal
			// never fires for it.
			_ = rootCmd.PersistentPreRunE(cmd, nil)

			var buf bytes.Buffer
			clidiag.Fwarn(&buf, "ctxloom", "probe")
			assert.Equal(t, c.wantJSONLine, json.Valid(buf.Bytes()),
				"--format=%s: Fwarn output %q", c.format, buf.String())
		})
	}
}

// A prior finding claimed Execute()'s error path resolves --format from
// rootCmd, "where the --json shorthand does not exist", so a --json
// invocation that fails gets JSON-shaped output on the happy path and plain
// text on the error path. Both halves were measured against the code and
// neither survives for the ctxloom binary:
//
//  1. --format is a PERSISTENT flag on rootCmd, so parsing a subcommand's
//     invocation sets the very pflag.Flag object rootCmd owns. Resolving from
//     the root after a failing subcommand therefore yields the SAME format the
//     command itself would resolve — measured, not reasoned about.
//  2. `ctxloom` registers no --json flag at all, anywhere in its command tree.
//     The shorthand is a cmd/taskloom affordance; nothing here can reach the
//     divergence the row describes.
//
// The second half is what makes the row latent rather than live, so it is the
// half worth pinning: this goes red the moment a --json flag is added under
// rootCmd, which is exactly when Execute()'s root-scoped Resolve would start
// to lie.
func TestExecuteErrorPath_FormatResolvesFromRootAndNoJSONShorthandExists(t *testing.T) {
	var withJSON []string
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Flags().Lookup("json") != nil || c.PersistentFlags().Lookup("json") != nil {
			withJSON = append(withJSON, c.CommandPath())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
	assert.Empty(t, withJSON,
		"Execute() resolves --format from rootCmd, which cannot see a subcommand-local --json; "+
			"adding one re-opens this problem and must move the resolve to the executed command")

	root := &cobra.Command{Use: "probe", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().String("format", formatText, "")
	sub := &cobra.Command{Use: "boom", RunE: func(*cobra.Command, []string) error {
		return errors.New("kaboom")
	}}
	root.AddCommand(sub)
	root.SetArgs([]string{"boom", "--format", "json"})
	require.Error(t, root.Execute())

	fromRoot, err := cliemit.Resolve(root)
	require.NoError(t, err)
	fromSub, err := cliemit.Resolve(sub)
	require.NoError(t, err)
	assert.Equal(t, clifmt.FormatJSON, fromRoot,
		"a persistent --format parsed on a subcommand is visible from the root it belongs to")
	assert.Equal(t, fromSub, fromRoot,
		"root-scoped and command-scoped resolution must not diverge for --format")
}

// TestNoSubcommandDefinesPersistentHooks enforces an invariant found
// asserted only in a comment on rootCmd. Cobra runs the CLOSEST
// PersistentPreRun/PersistentPostRun(E) it finds walking up from the invoked
// command, not every one in the chain — so a single subcommand declaring its
// own would silently switch OFF everything rootCmd's hooks do, for that
// command only, with nothing failing:
//
//	PersistentPreRun    resetFormatGuard, --degraded, --no-companions,
//	                    and the config-override funnel (InstallOverridesFromFlags)
//	PersistentPostRunE  the --format-was-honored guard
//
// Four process-wide behaviours plus the guard that catches a silently ignored
// --format. If a command genuinely needs per-command setup it belongs in that
// command's own PreRun/PreRunE (which cobra runs IN ADDITION to the inherited
// persistent hook), or rootCmd's hook must dispatch to it — not here.
func TestNoSubcommandDefinesPersistentHooks(t *testing.T) {
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, c := range cmd.Commands() {
			if c.PersistentPreRun != nil || c.PersistentPreRunE != nil {
				t.Errorf("%q defines its own PersistentPreRun(E): that REPLACES rootCmd's, "+
					"disabling --degraded, --no-companions, the config-override funnel and the "+
					"--format guard for this command. Use PreRun(E) instead.", c.CommandPath())
			}
			if c.PersistentPostRun != nil || c.PersistentPostRunE != nil {
				t.Errorf("%q defines its own PersistentPostRun(E): that REPLACES rootCmd's, "+
					"disabling the --format-was-honored guard for this command. Use PostRun(E) instead.",
					c.CommandPath())
			}
			walk(c)
		}
	}
	walk(rootCmd)

	// The invariant only holds because cobra resolves ONE persistent hook per
	// invocation. If this is ever flipped on, the "closest wins" reasoning
	// above stops applying and the comment on rootCmd needs rewriting too.
	assert.False(t, cobra.EnableTraverseRunHooks,
		"rootCmd's persistent hooks are documented as the only ones; traverse-run-hooks changes that contract")
}
