package backends

import (
	"context"
	"testing"

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

func TestMock_Lifecycle(t *testing.T) {
	mock := NewMock()
	assert.Nil(t, mock.Lifecycle())
}

func TestMock_Skills(t *testing.T) {
	mock := NewMock()
	assert.Nil(t, mock.Skills())
}

func TestMock_Context(t *testing.T) {
	mock := NewMock()
	assert.Nil(t, mock.Context())
}

func TestMock_MCP(t *testing.T) {
	mock := NewMock()
	assert.Nil(t, mock.MCP())
}

func TestMock_Setup(t *testing.T) {
	mock := NewMock()

	fragments := []*Fragment{
		{Content: "test fragment"},
	}

	req := &SetupRequest{
		WorkDir:   "/test/dir",
		Fragments: fragments,
	}

	err := mock.Setup(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "/test/dir", mock.WorkDir())
	assert.Len(t, mock.fragments, 1)
}
