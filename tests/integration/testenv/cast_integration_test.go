//go:build integration || acceptance

// Execs the real ctxloom binary, so it carries mcpsession_test.go's tag; see
// that file for why an untagged run would measure a stale binary.

package testenv

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newCastEnv(t *testing.T) *TestEnvironment {
	t.Helper()
	env, err := NewTestEnvironment()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, env.Cleanup()) })
	require.NoError(t, env.Setup())
	return env
}

// RunWithStdin tees like Run does: its own buffers, so it needs its own tee.
func TestRunWithStdin_RecordsIntoTheCast(t *testing.T) {
	env := newCastEnv(t)
	var buf bytes.Buffer
	env.RecordInto(NewCast(&buf, CastCols, CastRows, "stdin"))

	require.NoError(t, env.RunWithStdin("", "version", "--format", "text"))
	env.RecordInto(nil)

	_, events := castLines(t, buf.String())
	require.NotEmpty(t, events)
	assert.Equal(t, "$ ctxloom version --format text\r\n", events[0][2])
	assert.Contains(t, buf.String(), strings.TrimSpace(env.LastStdout()), "the run's output reaches the recording")
}

// A pty's bytes reach the recording through Write, unmodified: routed through
// WritePipe they would read "\r\r\n".
func TestRunPTY_RecordsRawPtyBytes(t *testing.T) {
	env := newCastEnv(t)
	var buf bytes.Buffer
	env.RecordInto(NewCast(&buf, CastCols, CastRows, "pty"))

	sess, err := env.RunPTY(CastCols, CastRows, nil, "version")
	require.NoError(t, err)
	exited, waitErr := sess.Wait(10 * time.Second)
	sess.Close()
	require.True(t, exited, "the pty session never exited")
	require.NoError(t, waitErr)
	env.RecordInto(nil)

	_, events := castLines(t, buf.String())
	require.GreaterOrEqual(t, len(events), 2, "a prompt and the pty's output")
	assert.Equal(t, "$ ctxloom version\r\n", events[0][2])
	assert.NotContains(t, buf.String(), `\r\r\n`)
}
