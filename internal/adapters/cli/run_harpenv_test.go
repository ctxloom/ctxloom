package cli

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// The controller carries its own session harp in its environment, whatever it
// inherited: a process it starts, and the container self-lookup, name the
// session from it (isolation.findSelf).
func TestBindLaunch_ExportsTheSessionHarp(t *testing.T) {
	t.Setenv(sessions.EnvHarp, "inherited-other-harp")
	st := &runState{}
	st.bindLaunch(launch.Launch{Identity: sessions.Identity{Harp: "brisk-amber-fox"}}, operations.Opened{})
	assert.Equal(t, "brisk-amber-fox", os.Getenv(sessions.EnvHarp))
}
