package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestResolveSessionSource_IsFiltered pins the WIRING, not the policy.
//
// It exists because the equivalent claim went untested twice. The content
// policy is only real if the source consumers actually receive is wrapped in
// it; delete the wrap in resolveSessionSource and every reader — `session
// distill`, load_session, recover_session, get_previous_session — silently
// serves unfiltered transcripts, with the policy package still fully green
// because its own tests only ever prove that Apply works when something calls
// it. A mutation removing the wrap survived the entire internal/cli package
// until this test existed.
func TestResolveSessionSource_IsFiltered(t *testing.T) {
	testsupport.Isolate(t)

	cfg := &config.Config{}
	src, _, err := resolveSessionSource(cfg, "claude-code", t.TempDir())
	require.NoError(t, err)
	require.NotNil(t, src)

	_, ok := src.(*pb.FilteredSource)
	assert.True(t, ok,
		"resolveSessionSource must hand back a FilteredSource: it is the single "+
			"construction point, so an unwrapped source here means every consumer "+
			"reads unfiltered content and nothing anywhere reports it (got %T)", src)
}
