package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/cliversion"
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

// TestVersion_TextEmitsANonEmptyVersionPayload pins the invariant U105-F02
// alleged was violated: `version` must never answer with a bare newline and
// exit 0. The row aimed that claim at cliversion.Render's ""/"text" branch,
// which no longer exists (deleted as test-only under U105-F01) — but the
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
	assert.Equal(t, Version, strings.TrimSpace(got))
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
