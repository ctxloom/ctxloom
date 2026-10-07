package bundles

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/yamlform"
)

// TestEnvelopeSaveIsWriteBackForm: a bundle tree's envelope is saved in the
// encoding an upgrade write-back would give it.
func TestEnvelopeSaveIsWriteBackForm(t *testing.T) {
	raw, err := TreeEnvelope(&Bundle{Version: "1.2.0", Tags: []string{"go", "review"}, Description: "d"})
	require.NoError(t, err)
	yamlform.RequireWriteBackForm(t, raw)
}
