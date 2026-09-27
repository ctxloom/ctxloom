package mock

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMock(t *testing.T) {
	mock := newTestBackend()

	assert.Equal(t, "mock", mock.Name())
	assert.Equal(t, "1.0.0", mock.Version())
	assert.NotNil(t, mock.Args)
}

// TestRecordMockInput_CapturesCwdAndConfigHome is the seam this fixes: before,
// The record proves WHERE the engine ran and WHAT isolation env it received
// — the things isolation tests assert on. cwd comes from os.Getwd() (the
// process's REAL cwd), not an echoed-back request field; the home var
// echoed is the KIND'S OWN declared one, and only when set.
func TestRecordMockInput_CapturesCwdAndConfigHome(t *testing.T) {
	dir := t.TempDir()
	recordFile := filepath.Join(dir, "record.txt")

	req := &agent.ExecuteRequest{
		Mode: agent.ModeOneshot,
		Env: map[string]string{
			"FIXTURE_HOME": "/agents/one/.fixture",
			// OTHER_HOME deliberately absent: only set keys should appear.
		},
	}
	b := New(WithHome(engine.HomeSpec{
		Vars: []engine.HomeVar{{Name: "FIXTURE_HOME", Subdir: "fixture"}, {Name: "OTHER_HOME", Subdir: "other"}},
		Auth: engine.Absent[engine.Auth]("fixture authenticates against no vendor"),
	})).(Mock).Backend(nil).(*Backend)
	b.fragments = []*agent.Fragment{{Content: "a"}, {Content: "b"}}

	require.NoError(t, b.recordInput(recordFile, req, "some context", "some prompt"))

	data, err := os.ReadFile(recordFile)
	require.NoError(t, err)
	content := string(data)

	wantCwd, err := os.Getwd()
	require.NoError(t, err)
	assert.Contains(t, content, "cwd="+wantCwd)
	assert.Contains(t, content, "FIXTURE_HOME=/agents/one/.fixture")
	assert.NotContains(t, content, "OTHER_HOME=")
}

// TestRecordMockInput_CapturesDenyToolsAndSkills pins the mock backend's half
// of the flow-level regression guard for deny_tools/skills: without
// b.managed, recordMockInput cannot see req.Managed, and an acceptance scenario
// has nothing to assert against. This proves the LAST hop: what Setup received actually reaches the recorded input a caller can
// observe.
func TestRecordMockInput_CapturesDenyToolsAndSkills(t *testing.T) {
	dir := t.TempDir()
	recordFile := filepath.Join(dir, "record.txt")

	req := &agent.ExecuteRequest{Mode: agent.ModeOneshot}
	managed := &agent.ManagedConfig{
		DenyTools: []string{"Task", "WebFetch"},
		Skills:    []agent.SkillExport{{Name: "release-checklist"}},
	}

	b := newTestBackend()
	b.managed = managed
	require.NoError(t, b.recordInput(recordFile, req, "some context", "some prompt"))

	data, err := os.ReadFile(recordFile)
	require.NoError(t, err)
	content := string(data)

	assert.Contains(t, content, "Task")
	assert.Contains(t, content, "WebFetch")
	assert.Contains(t, content, "release-checklist")
}

// TestRecordMockInput_NilManaged_RecordsEmptySections is the companion
// negative case: a nil Managed (the minimal/distill form) must not panic and
// must record the sections empty rather than omitting them, so a scenario can
// assert absence as confidently as presence.
func TestRecordMockInput_NilManaged_RecordsEmptySections(t *testing.T) {
	dir := t.TempDir()
	recordFile := filepath.Join(dir, "record.txt")

	req := &agent.ExecuteRequest{Mode: agent.ModeOneshot}

	require.NoError(t, newTestBackend().recordInput(recordFile, req, "some context", "some prompt"))

	data, err := os.ReadFile(recordFile)
	require.NoError(t, err)
	content := string(data)

	assert.Contains(t, content, "=== DenyTools ===\n=== Skills ===\n")
}

// recordMockInput's write failure must not be swallowed. Before the
// fix it only warned to stderr and returned nothing, so Execute reported
// success with no record file — a hermetic test asserting against the record
// file would then silently read a STALE file from a previous run instead of
// failing loudly. recordFile is a directory here, so os.WriteFile must fail.
func TestMock_Execute_RecordFileWriteFailurePropagates(t *testing.T) {
	req := &agent.ExecuteRequest{
		Prompt: &agent.Fragment{Content: "hi"},
		Env:    map[string]string{"CTXLOOM_MOCK_RECORD_FILE": t.TempDir()},
	}
	var out strings.Builder
	res, err := newTestBackend().Execute(context.Background(), req, &out, &out)
	require.Error(t, err, "a record-file write failure must not be swallowed as success")
	assert.NotEqual(t, int32(0), res.ExitCode, "a record-file write failure must not report a zero (success) exit code")
}

// newTestBackend is the bare mock kind's backend.
func newTestBackend() *Backend { return New().(Mock).Backend(nil).(*Backend) }
