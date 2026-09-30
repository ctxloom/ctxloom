//go:build !windows

package claude

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// shTurn is a REAL per-turn process through the real transport: sh runs
// script in place of claude, under grace. The script reads the user message
// first, so the driver's write never meets a closed pipe.
func shTurn(script string, grace time.Duration) chatTransportFunc {
	return func(ctx context.Context, _ string, _ []string, _ map[string]string, _ string) (*chatTransport, error) {
		return spawnChatTransportGrace(ctx, "sh", []string{"-c", "head -n1 >/dev/null; " + script}, nil, "", grace)
	}
}

const shResult = `printf '%s\n' '{"type":"result","subtype":"success","result":"done","stop_reason":"end_turn","num_turns":1}'`

// TestTurn_ResultThenExit3_CarriesTheCodeAndStillAnswers: the engine's own
// exit status rides the turn as INFORMATION. A turn that answered and then
// exited 3 is still an answered turn — no error — and says it exited 3.
func TestTurn_ResultThenExit3_CarriesTheCodeAndStillAnswers(t *testing.T) {
	d, ex := driverFor(t, structured("", ""), shTurn(shResult+"; exit 3", time.Second), nil)
	res, err := d.Turn(context.Background(), ex, engine.Turn{Prompt: "x"}, nil)
	require.NoError(t, err, "a clean result stays a completed turn whatever the exit after it")
	require.NotNil(t, res.ExitCode, "the engine exited on its own: its status is reported")
	assert.Equal(t, 3, *res.ExitCode)
}

// TestTurn_CrashWithoutResult_FailsAndCarriesTheCode: no result and a failed
// exit is a turn that died (errTurnProcessDied), and the status it died with
// rides along.
func TestTurn_CrashWithoutResult_FailsAndCarriesTheCode(t *testing.T) {
	d, ex := driverFor(t, structured("", ""), shTurn("exit 3", time.Second), nil)
	res, err := d.Turn(context.Background(), ex, engine.Turn{Prompt: "x"}, nil)
	require.ErrorIs(t, err, errTurnProcessDied)
	require.NotNil(t, res.ExitCode)
	assert.Equal(t, 3, *res.ExitCode)
}

// TestTurn_WeEndedIt_ReportsNoCode: a process the turn ended itself has no
// status of its own to report — the SIGKILL or SIGINT was ctxloom's, and a
// 137 or 130 would read as the engine's failure. Both ways the turn ends a
// process: a reap that outran its grace (stdout closed, process lingering),
// and an interrupt.
func TestTurn_WeEndedIt_ReportsNoCode(t *testing.T) {
	t.Run("reap past its grace", func(t *testing.T) {
		d, ex := driverFor(t, structured("", ""), shTurn(shResult+"; exec >&- 2>&-; exec sleep 30", 100*time.Millisecond), nil)
		res, err := d.Turn(context.Background(), ex, engine.Turn{Prompt: "x"}, nil)
		require.NoError(t, err)
		assert.Nil(t, res.ExitCode, "we killed it: the status is ours, not the engine's")
	})
	t.Run("interrupt", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		d, ex := driverFor(t, structured("", ""), shTurn(`printf '%s\n' '{"type":"system","subtype":"init","session_id":"s"}'; exec sleep 30`, time.Second), nil)
		out := make(chan engine.Event, 16)
		go func() { <-out; cancel() }()
		res, err := d.Turn(ctx, ex, engine.Turn{Prompt: "x"}, out)
		require.ErrorIs(t, err, context.Canceled)
		assert.Nil(t, res.ExitCode, "an interrupted process ended because we asked it to")
	})
}
