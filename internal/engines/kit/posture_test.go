package kit

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

func TestResolveDegraded(t *testing.T) {
	floor := map[string]any{"mode": "plan"}
	ok := func(engine.PostureRequest) (map[string]any, error) { return map[string]any{"mode": "x"}, nil }
	bad := func(engine.PostureRequest) (map[string]any, error) { return nil, errors.New("no such mode") }

	got, err := ResolveDegraded(engine.PostureRequest{Degraded: true}, ok, floor)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"mode": "x"}, got, "a posture that resolves is kept, degraded or not")

	_, err = ResolveDegraded(engine.PostureRequest{}, bad, floor)
	require.Error(t, err, "without --degraded the refusal stands")

	var warned string
	got, err = ResolveDegraded(engine.PostureRequest{Degraded: true, Warn: func(f string, a ...any) { warned = fmt.Sprintf(f, a...) }}, bad, floor)
	require.NoError(t, err)
	assert.Equal(t, floor, got)
	assert.Contains(t, warned, "no such mode")
}
