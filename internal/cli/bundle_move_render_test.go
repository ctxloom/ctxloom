package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// printMoveResult branches on DestKind: a REMOTE destination annotates the
// destination with the remote's name, a local path does not. The branch is the
// behaviour a shared constant must preserve, so it is pinned here — green
// before and after routing the comparison through operations.MoveDestRemote.
func TestPrintMoveResult_BranchesOnDestKind(t *testing.T) {
	t.Run("remote destination names the remote", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, printMoveResult(&buf, &operations.MoveBundleResult{
			Source:    ".ctxloom/content/bundles/go-tools.yaml",
			Dest:      "bundles/go-tools.yaml",
			DestKind:  operations.MoveDestRemote,
			Remote:    "ctxloom-default",
			CommitSHA: "0123456789abcdef",
			SigDest:   "bundles/go-tools.yaml.sig",
		}))
		got := buf.String()
		assert.Contains(t, got, "Moved .ctxloom/content/bundles/go-tools.yaml -> bundles/go-tools.yaml (ctxloom-default)")
		assert.Contains(t, got, "Commit: ")
		assert.Contains(t, got, "Signature: bundles/go-tools.yaml.sig")
	})

	t.Run("local path destination does not", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, printMoveResult(&buf, &operations.MoveBundleResult{
			Source:   ".ctxloom/content/bundles/go-tools.yaml",
			Dest:     "../other/.ctxloom/content/bundles/go-tools.yaml",
			DestKind: operations.MoveDestPath,
			Remote:   "",
		}))
		got := buf.String()
		assert.Contains(t, got, "Moved .ctxloom/content/bundles/go-tools.yaml -> ../other/.ctxloom/content/bundles/go-tools.yaml\n")
		assert.NotContains(t, got, "(")
		assert.NotContains(t, got, "Commit:")
		assert.NotContains(t, got, "Signature:")
	})

	// The exported constants ARE the values the operations core writes into
	// DestKind — that is what lets the frontend stop spelling "remote".
	assert.Equal(t, "remote", operations.MoveDestRemote)
	assert.Equal(t, "path", operations.MoveDestPath)
}
