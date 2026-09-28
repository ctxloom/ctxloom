//go:build !windows

// Container isolation has no Windows host support: nothing maps a Windows host path into the Linux container.

package isolation

import (
	"context"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// An unresolvable container auth refuses with what is missing as the message
// and what to run as the fix, so every renderer shows the fix once, as a fix.
func TestPrepareContainerScratch_UnresolvableAuthNamesItsRemedy(t *testing.T) {
	resetStrictness(t)
	testsupport.Isolate(t)
	c := overrideContainer(t, `{"Entrypoint":null,"User":""}`, "user/own:img").
		WithSessionState(SessionState{Harp: "brisk-teal-otter"})
	c.engineSpec.resolveAuth = func(engine.LaunchEnv) (containerAuth, bool) { return containerAuth{}, false }
	c.engineSpec.authHint = "no TOKEN to authenticate"
	c.engineSpec.authRemedy = "store a token with ctxloom auth set-token"

	_, err := c.prepareContainerScratch(context.Background())
	require.Error(t, err)
	fix, ok := clifmt.RemedyOf(err)
	require.True(t, ok)
	assert.Equal(t, c.engineSpec.authRemedy, fix)
	assert.Contains(t, err.Error(), c.engineSpec.authHint)
	assert.False(t, strings.Contains(err.Error(), fix), "the remedy is not spliced into the message: %q", err.Error())
}
