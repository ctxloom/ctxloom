package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/attach"
)

func envFrom(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

// TestResolveAttachEndpoint_TakesTheHostFromTheCoordinatorURL: the trio
// carries an MCP URL, and gRPC dials host:port — handing grpc.NewClient the
// full URL produces a target that resolves to nothing.
func TestResolveAttachEndpoint_TakesTheHostFromTheCoordinatorURL(t *testing.T) {
	host, cred, err := resolveAttachEndpoint(envFrom(map[string]string{
		coord.EnvCoordURL:  "http://127.0.0.1:34567/mcp",
		coord.EnvCoordCred: "tok-9f2c",
	}))
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:34567", host, "the dial target is host:port, not the MCP URL")
	assert.Equal(t, "tok-9f2c", cred)
}

// TestResolveAttachEndpoint_RefusesWithoutACredential: attaching is a
// CoordinatorService call, which a read-only consumer credential can never
// make. Proceeding credential-less would dial and fail deep inside the
// stream, where the message is about gRPC rather than about what the operator
// must do.
func TestResolveAttachEndpoint_RefusesWithoutACredential(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"nothing set", nil},
		{"url only", map[string]string{coord.EnvCoordURL: "http://127.0.0.1:1/mcp"}},
		{"cred only", map[string]string{coord.EnvCoordCred: "tok"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := resolveAttachEndpoint(envFrom(tc.env))
			require.Error(t, err)
			assert.ErrorContains(t, err, coord.EnvCoordCred,
				"the refusal must name the variable the operator has to set")
		})
	}
}

// TestResolveAttachEndpoint_RefusesAURLWithNoHost: a URL that parses but
// carries no host would otherwise dial the empty target.
func TestResolveAttachEndpoint_RefusesAURLWithNoHost(t *testing.T) {
	_, _, err := resolveAttachEndpoint(envFrom(map[string]string{
		coord.EnvCoordURL:  "/mcp",
		coord.EnvCoordCred: "tok",
	}))
	require.Error(t, err)
	assert.ErrorContains(t, err, "no host")
}

// TestAttachOutcome_DetachIsSuccess: leaving is what Ctrl-] is FOR. Reporting
// it as a failure would make every normal exit look like an error.
func TestAttachOutcome_DetachIsSuccess(t *testing.T) {
	assert.NoError(t, attachOutcome(attach.ErrDetached))
	assert.NoError(t, attachOutcome(nil))
	assert.NoError(t, attachOutcome(context.Canceled))
}

// TestAttachOutcome_ClosedPaneExitsWithThePanesCode is the assertion that
// bites: a dead run must not exit 0.
func TestAttachOutcome_ClosedPaneExitsWithThePanesCode(t *testing.T) {
	err := attachOutcome(&attach.PaneClosedError{ExitCode: 42, Message: "boom"})
	var exit *ExitError
	require.ErrorAs(t, err, &exit, "a nonzero pane exit must become a process exit code")
	assert.Equal(t, 42, exit.Code)
}

// TestAttachOutcome_CleanPaneExitIsNotAnError: a pane that ended 0 ended
// well; inventing a nonzero status for it is as wrong as swallowing 42.
func TestAttachOutcome_CleanPaneExitIsNotAnError(t *testing.T) {
	assert.NoError(t, attachOutcome(&attach.PaneClosedError{ExitCode: 0}))
}

// TestAttachOutcome_OtherFailuresSurvive: a transport error must reach the
// operator, not be flattened into a clean exit.
func TestAttachOutcome_OtherFailuresSurvive(t *testing.T) {
	boom := errors.New("connection refused")
	assert.ErrorIs(t, attachOutcome(boom), boom)
}

// TestAttachCmd_IsWiredWithAHarpAndReadOnly pins the surface a human types.
func TestAttachCmd_IsWiredWithAHarpAndReadOnly(t *testing.T) {
	var found bool
	for _, c := range rootCmd.Commands() {
		if c.Name() == "attach" {
			found = true
			require.NotNil(t, c.Flags().Lookup("read-only"), "--read-only must exist")
			require.Error(t, c.Args(c, nil), "attach with no harp must be refused")
			require.NoError(t, c.Args(c, []string{"swift-amber-falcon"}))
			require.Error(t, c.Args(c, []string{"a", "b"}), "attach takes exactly one harp")
		}
	}
	assert.True(t, found, "`ctxloom attach` must be registered on the root command")
}
