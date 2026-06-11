package backends

import (
	"context"
	"testing"

	"github.com/ctxloom/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMock(t *testing.T) {
	mock := NewMock()

	assert.Equal(t, "mock", mock.Name())
	assert.Equal(t, "1.0.0", mock.Version())
	assert.NotNil(t, mock.Args)
	assert.NotNil(t, mock.Env)
}

func TestMock_Setup(t *testing.T) {
	mock := NewMock()

	fragments := []*agent.Fragment{
		{Content: "test fragment"},
	}

	req := &agent.SetupRequest{
		WorkDir:   "/test/dir",
		Fragments: fragments,
	}

	err := mock.Setup(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "/test/dir", mock.WorkDir())
	assert.Len(t, mock.fragments, 1)
}
