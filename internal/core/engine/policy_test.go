package engine_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

func TestApprover_ZeroValueIsTheHuman(t *testing.T) {
	var a engine.Approver
	assert.Equal(t, engine.ApproverHuman, a, "ruled default: uncovered requests escalate to the root human")
	assert.Equal(t, "human", engine.ApproverHuman.String())
	assert.Equal(t, "none", engine.ApproverNone.String())
}

func TestParseApprover(t *testing.T) {
	for in, want := range map[string]engine.Approver{"human": engine.ApproverHuman, " None ": engine.ApproverNone} {
		got, ok := engine.ParseApprover(in)
		require.Truef(t, ok, "%q", in)
		assert.Equal(t, want, got)
	}
	for _, in := range []string{"", "agent", "humans", "root"} {
		_, ok := engine.ParseApprover(in)
		assert.Falsef(t, ok, "%q is not an approver", in)
	}
	assert.Equal(t, []string{"human", "none", "reviewer"}, engine.ApproverNames())
}

func TestApprovalTimeoutBounds(t *testing.T) {
	assert.Equal(t, 15*time.Minute, engine.DefaultApprovalTimeout)
	assert.Equal(t, 60*time.Minute, engine.MaxApprovalTimeout)
}

func TestParseApprovalTimeout_BoundsAndSentinel(t *testing.T) {
	d, err := engine.ParseApprovalTimeout(" 20m ")
	require.NoError(t, err)
	assert.Equal(t, 20*time.Minute, d)
	d, err = engine.ParseApprovalTimeout("60m")
	require.NoError(t, err, "the cap itself is allowed")
	assert.Equal(t, engine.MaxApprovalTimeout, d)
	for _, in := range []string{"", "soon", "0s", "-1m", "61m"} {
		_, err := engine.ParseApprovalTimeout(in)
		assert.ErrorIsf(t, err, engine.ErrApprovalTimeout, "%q", in)
	}
}

func TestWorkspaceTrust_ZeroValueIsUntrusted(t *testing.T) {
	var tr engine.WorkspaceTrust
	assert.Equal(t, engine.TrustUntrusted, tr)
}
