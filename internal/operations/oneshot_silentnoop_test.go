package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// TestRunOneshot_EmptyStdoutIsLoud pins that the launch tail every headless
// run shares (a delegated oneshot turn, `acp run`, mirrored by run --one-shot)
// used to accept exit 0 with nothing on stdout as a successful run, publishing
// Output:"" into RunOneshotResult.Output / a child's assistant entry with no
// error anywhere. A oneshot exists ONLY to capture output, so zero bytes
// is a failed run, not a legitimately-empty one.
func TestRunOneshot_EmptyStdoutIsLoud(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := oneshotTestConfig(t)

	cases := []struct{ name, out string }{
		{"no bytes at all", ""},
		{"whitespace only", "  \n\t\n "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubClient{out: tc.out}
			factory := func(string, string, int) (pb.Client, error) { return stub, nil }

			res, err := RunOneshot(context.Background(), cfg, RunOneshotRequest{
				Profile: "rev", Task: "review this diff", Pipeline: opPipe(cfg, loader), Factory: factory,
			})
			require.Error(t, err, "exit 0 with empty stdout must fail loudly, not report a successful empty run")
			assert.Nil(t, res)
			assert.Contains(t, err.Error(), "no output")
		})
	}
}

// TestRunResolvedAgent_EmptyContextForNamedProfilesIsLoud pins that the
// `if req.Context != ""` guard let a member whose named profiles assemble to
// nothing launch context-free — the agent still runs and produces plausible
// output. Naming profiles and delivering none of them is never what the caller
// asked for.
func TestRunResolvedAgent_EmptyContextForNamedProfilesIsLoud(t *testing.T) {
	stub := &stubClient{out: "plausible but unspecialised"}
	factory := func(string, string, int) (pb.Client, error) { return stub, nil }

	_, err := runResolvedAgent(context.Background(), resolvedRunRequest{
		Context:  "",
		Task:     "review this diff",
		Label:    "claude-fast",
		Backend:  "claude-code",
		Profiles: []string{"rev"},
		AgentID:  "rev",
		Factory:  factory,
	})
	require.Error(t, err, "a member naming profiles that assemble to nothing must not run context-free")
	assert.Contains(t, err.Error(), "empty context")
	assert.Nil(t, stub.gotReq, "the engine must not be launched at all")
}

// A member that names NO profile is legitimately context-free (the bare
// `ctxloom run --one-shot` with no profile, a defaults-only agent): it must keep
// running. This is the discriminator that keeps the fix above from becoming a
// blanket refusal.
func TestRunResolvedAgent_NoProfilesRunsContextFree(t *testing.T) {
	stub := &stubClient{out: "fine"}
	factory := func(string, string, int) (pb.Client, error) { return stub, nil }

	res, err := runResolvedAgent(context.Background(), resolvedRunRequest{
		Task:        "just answer",
		Label:       "claude-fast",
		Backend:     "claude-code",
		Permissions: "bypass", // headless-safe: this test is about the context-free path, not permission resolution
		Factory:     factory,
	})
	require.NoError(t, err)
	assert.Equal(t, "fine", res.Output)
	require.NotNil(t, stub.gotReq)
	assert.Empty(t, stub.gotReq.Fragments)
}
