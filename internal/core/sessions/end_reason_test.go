package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A session that ends before its engine launches records why in its sidecar:
// ended_at alone said only when. Both stores keep the reason, and the Manager
// writes it to the file a human reads.
func TestManager_RecordEndReason_PersistsToTheSidecar(t *testing.T) {
	m := newManager(t)
	e, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	want := EndReason{Exit: 3, Class: EndClassFatalStartup, Message: "ctxloom: aborting startup", Findings: []string{"  - [bundle] bad\n    fix: repair it"}}

	require.NoError(t, m.RecordEndReason(e.HarpName, want))

	found, err := m.Find(e.HarpName)
	require.NoError(t, err)
	require.NotNil(t, found.EndReason)
	assert.Equal(t, want, *found.EndReason)
	raw, err := os.ReadFile(filepath.Join(m.root, e.HarpName, "session.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "end_reason:")
	assert.Contains(t, string(raw), "class: fatal-startup")
	assert.Contains(t, string(raw), "exit: 3")

	assert.Error(t, m.RecordEndReason("no-such-harp", want))
}

func TestMemStore_RecordEndReason_MatchesTheManager(t *testing.T) {
	s := NewMemStore()
	e, err := s.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	want := EndReason{Exit: 2, Class: EndClassRefused, Message: "no credential"}

	require.NoError(t, s.RecordEndReason(e.HarpName, want))

	found, err := s.Find(e.HarpName)
	require.NoError(t, err)
	require.NotNil(t, found.EndReason)
	assert.Equal(t, want, *found.EndReason)
	assert.Error(t, s.RecordEndReason("no-such-harp", want))
}

func TestManager_NoEndReasonIsAbsentFromTheSidecar(t *testing.T) {
	m := newManager(t)
	e, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(m.root, e.HarpName, "session.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "end_reason")
}
