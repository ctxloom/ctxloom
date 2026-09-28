//go:build !windows

// Container isolation has no Windows host support: nothing maps a Windows host path into the Linux container.

package isolation

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// An engine that declared no container story refuses with what is missing as
// the message and what to do as the fix, so every renderer shows the fix
// once, as a fix.
func TestPrepareContainerScratch_UndeclaredContainerNamesItsRemedy(t *testing.T) {
	resetStrictness(t)
	testsupport.Isolate(t)
	c := overrideContainer(t, `{"Entrypoint":null,"User":""}`, "user/own:img").
		WithSessionState(SessionState{Harp: "brisk-teal-otter"})
	c.engineSpec.declared = false

	_, err := c.prepareContainerScratch(context.Background())
	require.Error(t, err)
	fix, ok := clifmt.RemedyOf(err)
	require.True(t, ok)
	assert.Equal(t, noContainerRemedy, fix)
	assert.Contains(t, err.Error(), noContainerHint)
	assert.False(t, strings.Contains(err.Error(), fix), "the remedy is not spliced into the message: %q", err.Error())
}
