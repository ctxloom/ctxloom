package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// A COMMAND'S OWN OUTPUT MUST GO THROUGH ITS COMMAND, NOT THE PACKAGE'S
// STDOUT.
//
// `remote`, `profile` and `remote browse` reported their results with
// package-level fmt.Printf/fmt.Println straight to os.Stdout. That writes the
// right bytes in production and is untestable everywhere else: cmd.SetOut is
// ignored, so nothing can capture what the command said, and the writer a
// frontend or a wrapping command installs is bypassed.
//
// `remote default --clear` stands for the class: it needs no network and no
// remote fixture, so it exercises the writer decision and nothing else.
func TestRemoteDefaultClear_WritesThroughTheCommandsWriter(t *testing.T) {
	testsupport.ProjectDir(t)

	// Driven through the command's own RunE rather than rootCmd.Execute():
	// Execute() lazily materialises cobra's built-in `help` command onto the
	// root, and the --format coverage walk then reports it as an unregistered
	// command. Find() is a pure traversal, so this exercises the real command
	// object without mutating global cobra state for whatever runs next.
	cmd, _, err := rootCmd.Find([]string{"remote", "default"})
	require.NoError(t, err)
	require.NotNil(t, cmd.RunE)

	var out bytes.Buffer
	cmd.SetOut(&out)
	remoteDefaultClear = true
	t.Cleanup(func() {
		cmd.SetOut(nil)
		remoteDefaultClear = false
	})

	require.NoError(t, cmd.RunE(cmd, nil))
	assert.Contains(t, out.String(), "Cleared default remote.",
		"the command's own result line must reach cmd.OutOrStdout(), not os.Stdout directly")
}

// printProfileCreated is the `profile create` result. Pinned directly because
// it is the one write in that path with no cobra.Command in scope — it took
// none, so it could only ever have gone to os.Stdout.
func TestPrintProfileCreated_WritesToTheGivenWriter(t *testing.T) {
	t.Cleanup(func() {
		profileCreateParents = nil
		profileCreateBundles = nil
	})
	profileCreateParents = []string{"base"}
	profileCreateBundles = nil

	var out bytes.Buffer
	printProfileCreated(&out, "developer", "/tmp/developer.yaml")

	assert.Contains(t, out.String(), "developer", "the created profile must be named on the caller's writer")
	assert.Contains(t, out.String(), "/tmp/developer.yaml", "and so must where it was saved")
	assert.Contains(t, out.String(), "base", "and the parents it was created with")
}

// `remote browse --recursive` DEFAULTS TO TRUE, AND THE HELP MUST SAY SO.
//
// The help text read "List items in subdirectories", which describes an
// opt-in — so `-r` looks like the way to turn recursion on when it is already
// on, and nothing said that `--recursive=false` is the only lever that
// changes anything.
//
// The default is deliberately true and is NOT changed here: bundles live in
// subdirectories of a remote (bundles/<name>), so a non-recursive read of a
// bundle repository would surface almost nothing. Flipping it would change
// what `ctxloom remote show <remote>` returns, which is a contract call.
//
// This pins the two together: if someone flips the default, the text
// asserting it no longer matches and this fails, rather than the help
// quietly going stale again.
func TestRemoteShowRecursive_HelpMatchesTheActualDefault(t *testing.T) {
	f := remoteShowCmd.Flags().Lookup("recursive")
	require.NotNil(t, f, "the --recursive flag must exist")

	assert.Equal(t, "true", f.DefValue,
		"recursion is on by default; bundles live in subdirectories, so a non-recursive read would surface almost nothing")
	assert.Contains(t, f.Usage, "--recursive=false",
		"the help must name the only form that actually changes behaviour; cobra renders the \"(default true)\" part itself, so the usage text must not repeat it")
}
