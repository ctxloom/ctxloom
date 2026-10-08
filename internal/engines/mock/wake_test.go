package mock

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/engines/kit"
)

func TestWake_TheMockDeclaresItsOwnSocketWake(t *testing.T) {
	spec, ok := Mock{}.Wake().Get()
	require.True(t, ok)
	assert.Equal(t, kit.SocketWake{EnvVar: EnvWakeSocket}, spec, "the mock's wake is kit's socket wake on its own variable")
}

