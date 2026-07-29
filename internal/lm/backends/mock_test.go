package backends

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMock(t *testing.T) {
	mock := NewMock()

	assert.Equal(t, "mock", mock.Name())
	assert.Equal(t, "1.0.0", mock.Version())
	assert.NotNil(t, mock.Args)
	assert.NotNil(t, mock.Env)
}

func TestMock_Setup(t *testing.T) {
	mock := NewMock()

	fragments := []*agent.Fragment{
		{Content: "test fragment"},
	}

	req := &agent.SetupRequest{
		WorkDir:   "/test/dir",
		Fragments: fragments,
	}

	err := mock.Setup(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "/test/dir", mock.WorkDir())
	assert.Len(t, mock.fragments, 1)
}

// TestRecordMockInput_CapturesCwdAndConfigHome is the seam this fixes: before,
// recordMockInput recorded only mode/fragment-count/context/prompt, so no
// hermetic test could prove WHERE the engine ran or WHAT isolation env it
// received — the very things isolation tests need to assert on. cwd comes
// from os.Getwd() (the process's REAL cwd, set by launcher.go's cmd.Dir at
// spawn time), not an echoed-back request field, so this genuinely proves
// the boundary rather than a request round-trip.
func TestRecordMockInput_CapturesCwdAndConfigHome(t *testing.T) {
	dir := t.TempDir()
	recordFile := filepath.Join(dir, "record.txt")

	req := &agent.ExecuteRequest{
		Mode: agent.ModeOneshot,
		Env: map[string]string{
			"CLAUDE_CONFIG_DIR": "/agents/one/.claude",
			// CODEX_HOME / KIRO_HOME deliberately absent: only set keys should appear.
		},
	}

	require.NoError(t, recordMockInput(recordFile, req, nil, "some context", "some prompt", 2))

	data, err := os.ReadFile(recordFile)
	require.NoError(t, err)
	content := string(data)

	wantCwd, err := os.Getwd()
	require.NoError(t, err)
	assert.Contains(t, content, "cwd="+wantCwd)
	assert.Contains(t, content, "CLAUDE_CONFIG_DIR=/agents/one/.claude")
	assert.NotContains(t, content, "CODEX_HOME=")
	assert.NotContains(t, content, "KIRO_HOME=")
}

// TestRecordMockInput_CapturesDenyToolsAndSkills pins the mock backend's half
// of the flow-level regression guard for T2 (deny_tools/skills were silently
// dropped crossing internal/lm/grpc's proto wire — see
// TestArch_ProtoConverters_MirrorEveryStructField). Before this test's fix,
// recordMockInput had no way to see req.Managed at all — b.managed did not
// exist on Mock, so an acceptance scenario had nothing to assert against even
// once the wire itself carried the fields correctly. This proves the LAST
// hop: what Setup received actually reaches the recorded input a caller can
// observe.
func TestRecordMockInput_CapturesDenyToolsAndSkills(t *testing.T) {
	dir := t.TempDir()
	recordFile := filepath.Join(dir, "record.txt")

	req := &agent.ExecuteRequest{Mode: agent.ModeOneshot}
	managed := &agent.ManagedConfig{
		DenyTools: []string{"Task", "WebFetch"},
		Skills:    []agent.SkillExport{{Name: "release-checklist"}},
	}

	require.NoError(t, recordMockInput(recordFile, req, managed, "some context", "some prompt", 0))

	data, err := os.ReadFile(recordFile)
	require.NoError(t, err)
	content := string(data)

	assert.Contains(t, content, "Task")
	assert.Contains(t, content, "WebFetch")
	assert.Contains(t, content, "release-checklist")
}

// TestRecordMockInput_NilManaged_RecordsEmptySections is the companion
// negative case: a nil Managed (skip_setup/distill paths) must not panic and
// must record the sections empty rather than omitting them, so a scenario can
// assert absence as confidently as presence.
func TestRecordMockInput_NilManaged_RecordsEmptySections(t *testing.T) {
	dir := t.TempDir()
	recordFile := filepath.Join(dir, "record.txt")

	req := &agent.ExecuteRequest{Mode: agent.ModeOneshot}

	require.NoError(t, recordMockInput(recordFile, req, nil, "some context", "some prompt", 0))

	data, err := os.ReadFile(recordFile)
	require.NoError(t, err)
	content := string(data)

	assert.Contains(t, content, "=== DenyTools ===\n=== Skills ===\n")
}

// U057-F22: recordMockInput's write failure must not be swallowed. Before the
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
	res, err := NewMock().Execute(context.Background(), req, &out, &out)
	require.Error(t, err, "a record-file write failure must not be swallowed as success")
	assert.NotEqual(t, int32(0), res.ExitCode, "a record-file write failure must not report a zero (success) exit code")
}
