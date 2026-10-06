package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// testCmd returns a bare cobra command whose stdout is captured. With no
// --format flag registered, emit() takes the text branch (see format.go). A
// Background context is set so cmd.Context() is non-nil, matching what cobra's
// Execute installs in production (a bare command's Context() is otherwise nil).
func testCmd() (*cobra.Command, *bytes.Buffer) {
	// One command is one invocation: the root composes the process afresh
	// per Execute, so a command driven directly gets the same.
	resetApp()
	c := &cobra.Command{}
	c.SetContext(context.Background())
	var buf bytes.Buffer
	c.SetOut(&buf)
	return c, &buf
}

// seedLocalFragment writes a local bundle with one fragment to the temp project
// so the read-path loader can resolve it.
func seedLocalFragment(t *testing.T, cfg *config.Config, bundle, name, body string) {
	t.Helper()
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{
		Name: bundle,
		Fragments: map[string]operations.BundleFragmentInput{
			name: {Content: body, NoDistill: true},
		},
	})
	require.NoError(t, err)
}
