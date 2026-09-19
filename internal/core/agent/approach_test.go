package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// unknown to this build.
func TestCellKindString_UnknownValueIsNotRenderedAsShared(t *testing.T) {
	assert.Equal(t, "shared", CellKindShared.String())
	assert.Equal(t, "directory-isolated", CellKindDirectoryIsolated.String())
	assert.Equal(t, "process-isolated", CellKindProcessIsolated.String())

	got := CellKind(99).String()
	assert.NotEqual(t, "shared", got, "an unrecognised cell must not report the run as un-isolated")
	assert.Contains(t, got, "unknown")
	assert.Contains(t, got, "99")
}
