package claude

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines/kit"
)

// These pin what claude's structured turn and Exec hand the outside world,
// byte for byte, so the driver's scaffolding can live anywhere without the
// process seeing a difference: the stdin bytes, the process the transport is
// asked for (binary, argv, env, dir), the dead-process error's text, and the
// Exec's env map.

// TestTurn_HandsTheTransportExactlyTheExecAndOneNDJSONMessage: the transport
// is opened with the Exec's binary, env and work dir and the turn's argv, and
// stdin receives exactly one NDJSON user message, newline-terminated.
func TestTurn_HandsTheTransportExactlyTheExecAndOneNDJSONMessage(t *testing.T) {
	var stdin bytes.Buffer
	var gotBinary, gotDir string
	var gotArgs []string
	var gotEnv map[string]string
	open := func(_ context.Context, binary string, args []string, env map[string]string, dir string) (*kit.Transport, error) {
		gotBinary, gotArgs, gotEnv, gotDir = binary, args, env, dir
		return &kit.Transport{Stdin: nopWriteCloser{&stdin}, Stdout: strings.NewReader(""), Teardown: func() error { return nil }}, nil
	}
	s := structured("m", "")
	s.Label.Binary = "/opt/claude"
	s.WorkDir = "/work"
	s.Home = []engine.HomeBinding{{Var: ConfigDirEnv, Path: "/h/.claude"}}
	d, ex := driverFor(t, s, open, nil, present.Presentation{Env: map[string]string{"X_SURFACE": "1"}})
	_, err := d.Turn(context.Background(), ex, engine.Turn{Prompt: "say \"hi\"\nplease", Resume: "k1"}, nil)
	require.NoError(t, err)

	assert.Equal(t, `{"type":"user","message":{"role":"user","content":"say \"hi\"\nplease"}}`+"\n", stdin.String())
	assert.Equal(t, "/opt/claude", gotBinary)
	assert.Equal(t, "/work", gotDir)
	assert.Equal(t, ex.Env, gotEnv, "the Exec's env, nothing added per turn")
	assert.Equal(t, strings.Join(ex.Args, " ")+` --input-format stream-json --output-format stream-json --verbose --resume k1 --settings {"permissions":{"defaultMode":"default"}}`, strings.Join(gotArgs, " "))
}

// TestTurn_ProcessDied_ErrorText pins the dead-process error's wording: it
// names the engine, then the death, then the process's own account.
func TestTurn_ProcessDied_ErrorText(t *testing.T) {
	d, ex := driverFor(t, structured("", ""), crashingTransport("", errors.New("exit status 9")), nil)
	_, err := d.Turn(context.Background(), ex, engine.Turn{Prompt: "x"}, nil)
	require.Error(t, err)
	assert.Equal(t, "claude: the turn's process died before it answered: exit status 9", err.Error())
}

// TestTurn_PromptWriteFailure_ClosesAndPropagates: a stdin that refuses the
// message tears the transport down and is the turn's error.
func TestTurn_PromptWriteFailure_ClosesAndPropagates(t *testing.T) {
	closed := 0
	open := func(_ context.Context, _ string, _ []string, _ map[string]string, _ string) (*kit.Transport, error) {
		return &kit.Transport{Stdin: failingWriteCloser{}, Stdout: strings.NewReader(""), Teardown: func() error { closed++; return nil }}, nil
	}
	d, ex := driverFor(t, structured("", ""), open, nil)
	_, err := d.Turn(context.Background(), ex, engine.Turn{Prompt: "x"}, nil)
	require.ErrorIs(t, err, io.ErrClosedPipe)
	assert.Equal(t, 1, closed)
}

type failingWriteCloser struct{}

func (failingWriteCloser) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (failingWriteCloser) Close() error              { return nil }

// TestInstance_ExecEnv_IsHomeVarsThenPresentationsThenTheModeSwitch pins the
// whole Exec env: every home var at its bound path, each presentation's env
// in delivery order (a later one overriding an earlier one and a home var),
// then claude's own mode switch.
func TestInstance_ExecEnv_IsHomeVarsThenPresentationsThenTheModeSwitch(t *testing.T) {
	presented := []present.Presentation{
		{Env: map[string]string{"A": "1", ConfigDirEnv: "/from-surface"}},
		{Env: map[string]string{"A": "2", "B": "3"}},
	}
	home := []engine.HomeBinding{{Var: ConfigDirEnv, Path: "/h/.claude"}, {Var: "XDG_X", Path: "/h/x"}}

	s := structured("", "")
	s.Home = home
	_, ex := driverFor(t, s, nil, nil, presented...)
	assert.Equal(t, map[string]string{ConfigDirEnv: "/from-surface", "XDG_X": "/h/x", "A": "2", "B": "3", disableBackgroundTasksEnv: "1"}, ex.Env)

	kind, err := Build()
	require.NoError(t, err)
	inter := engine.Session{Mode: engine.Interactive, Permission: modePolicy(modeDefault), Home: home}
	inst, err := kind.Instance(inter)
	require.NoError(t, err)
	ex, err = inst.Exec(presented)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{ConfigDirEnv: "/from-surface", "XDG_X": "/h/x", "A": "2", "B": "3", classicScreenEnv: "1"}, ex.Env)
}

// TestInstance_ExecBinary_DefaultsToClaude: the label's binary when it names
// one, else "claude".
func TestInstance_ExecBinary_DefaultsToClaude(t *testing.T) {
	_, ex := driverFor(t, structured("", ""), nil, nil)
	assert.Equal(t, "claude", ex.Binary)
	s := structured("", "")
	s.Label.Binary = "/opt/c"
	_, ex = driverFor(t, s, nil, nil)
	assert.Equal(t, "/opt/c", ex.Binary)
}
