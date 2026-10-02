//go:build windows

package agentkey

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func uniquePipe(t *testing.T) string {
	return fmt.Sprintf(`\\.\pipe\ctxloom-agentkey-%s-%d-%d`, t.Name(), os.Getpid(), time.Now().UnixNano())
}

// A pipe named by SSH_AUTH_SOCK is dialed and the agent behind it answers.
func TestDialAgentAt_EnvPipeReachesTheAgent(t *testing.T) {
	pipe := uniquePipe(t)
	ln, err := winio.ListenPipe(pipe, nil)
	require.NoError(t, err)
	requireAgentHolds(t, pipe, serveKeyring(t, ln))
}

// An absent pipe is a typed failure naming the pipe that was dialed.
func TestDialAgentAt_AbsentEnvPipeIsTyped(t *testing.T) {
	pipe := uniquePipe(t)
	_, err := dialAgentAt(pipe)()
	var dialErr *AgentDialError
	require.ErrorAs(t, err, &dialErr)
	assert.Equal(t, pipe, dialErr.Endpoint)
}

// A non-pipe SSH_AUTH_SOCK dials the OpenSSH default pipe. The host may or may
// not run the agent service, so success is accepted — but a failure must name
// the default pipe, not the value it was handed.
func TestDialAgentAt_NonPipeDialsTheDefault(t *testing.T) {
	ag, err := dialAgentAt(`C:\Users\nobody\agent.sock`)()
	if err == nil {
		_ = ag.(*closingAgent).Close()
		return
	}
	var dialErr *AgentDialError
	require.True(t, errors.As(err, &dialErr), "dial failure must be typed: %v", err)
	assert.Equal(t, defaultAgentPipe, dialErr.Endpoint)
}
