package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/cliversion"
	"github.com/ctxloom/ctxloom/internal/version"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// runVersionCmd executes `ctxloom version` with the given --format and
// returns everything it wrote. --format is an explicit argument rather than
// an omitted flag because rootCmd's --format is a process-wide
// PersistentFlags() value pflag does not reset between Execute() calls in
// one test binary (same hazard format_test.go documents).
func runVersionCmd(t *testing.T, format string) string {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"version", "--format", format})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	require.NoError(t, rootCmd.Execute())
	return out.String()
}

// TestVersion_TextEmitsANonEmptyVersionPayload pins the invariant a prior
// finding alleged was violated: `version` must never answer with a bare
// newline and exit 0. The row aimed that claim at cliversion.Render's
// ""/"text" branch, which no longer exists (deleted as test-only) — but the
// surviving text branch is this one, and it is the one a user actually
// reaches, so the invariant is pinned here rather than left unowned.
//
// Version's default is a real stamp ("dev"), overwritten at build time by
// ldflags; nothing in the reachable code path can make it empty. This test
// goes red the moment that stops being true.
func TestVersion_TextEmitsANonEmptyVersionPayload(t *testing.T) {
	got := runVersionCmd(t, "text")
	assert.NotEmpty(t, strings.TrimSpace(got),
		"`ctxloom version` must not print a bare newline and exit 0")
	assert.Equal(t, version.Version, strings.TrimSpace(got))
}

// TestVersion_UnknownFormatIsRejectedByTheSharedVocabulary pins its subject:
// the set of legal --format values for `version` is owned in ONE
// place (clifmt.ParseFormat, reached through emit/cliemit.Resolve) and not
// re-enumerated by the version command itself. The row described a bare
// string switch over ""/"text"/"json" inside cliversion.Render duplicating
// that vocabulary; Render is gone, and this asserts what replaced
// it — an unknown format is refused, with a message naming the FULL five-format
// set, which a command-local text/json switch could not produce.
func TestVersion_UnknownFormatIsRejectedByTheSharedVocabulary(t *testing.T) {
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"version", "--format", "definitely-not-a-format"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})

	err := rootCmd.Execute()
	require.Error(t, err, "an unknown --format must not be silently rendered as text")
	assert.Contains(t, err.Error(), "definitely-not-a-format")
	assert.Contains(t, err.Error(), string(clifmt.FormatYAML),
		"the error must come from the shared five-format vocabulary, not a command-local text/json pair")
}

// TestVersion_JSONCarriesTheCliversionContract pins the machine-readable half:
// the payload is the cross-binary {"name","version"} shape that
// config.companionVersion parses out of every companion, with both fields
// populated.
func TestVersion_JSONCarriesTheCliversionContract(t *testing.T) {
	got := runVersionCmd(t, "json")

	var info cliversion.Info
	require.NoError(t, json.Unmarshal([]byte(got), &info), "raw output: %q", got)
	assert.Equal(t, "ctxloom", info.Name)
	assert.NotEmpty(t, info.Version, "a zero-payload version field is the defect this pins")
}
