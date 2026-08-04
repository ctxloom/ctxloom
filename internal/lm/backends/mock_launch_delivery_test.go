package backends

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file pins the LAUNCH half of the mock engine's context route:
// mock_surfaces_test.go proves the surface writes bytes when something calls
// it, and this file proves the live turn's Setup is one of the things that
// calls it. Those are different claims, and the second was false until Mock
// stopped overriding Setup with a stash-and-return-nil bypass — the route
// existed and nothing on the run path reached it, which is exactly the shape
// of a vacuous green (docs/design/engine-delivery-seam.design.md).
//
// Every assertion below is on delivered BYTES or their deliberate absence.
// "Setup returned nil" is precisely what the broken version also did.

// launchSetupRequest is the SetupRequest a live turn hands a backend, in the
// shape grpc.runTurnSetup builds it: the resolved workspace, the run's
// fragments, the host-assembled managed payload, and the resolved cell. It is a
// helper rather than a literal per test so a field that matters to delivery
// (CellKind especially) cannot be silently omitted by one test and set by
// another.
func launchSetupRequest(workDir string, fragments []*agent.Fragment, managed *agent.ManagedConfig) *agent.SetupRequest {
	return &agent.SetupRequest{
		WorkDir:   workDir,
		Fragments: fragments,
		Managed:   managed,
		CellKind:  agent.CellKindShared,
	}
}

// hostManagedConfig is the minimal non-nil payload backends.AssembleManagedConfig
// ships on every non-skip-setup run. Its CONTENTS are irrelevant to the context
// surface; its non-nil-ness is not (see setupViaCells' documented "nothing
// managed → no surfaces to deliver" short-circuit, pinned below).
func hostManagedConfig() *agent.ManagedConfig {
	return &agent.ManagedConfig{Hooks: &wire.HooksConfig{}, MCP: &wire.MCPConfig{}}
}

// TestMock_Setup_DeliversContextBytesOnTheLaunchPath is the headline: the SAME
// Setup a live `ctxloom run --backend mock` invokes must leave the composed
// context in MOCK_CONTEXT.md, inside the managed markers. Before Mock embedded
// agent.LaunchBackend, this Setup stashed its payload, returned nil, and wrote
// zero bytes — the file assertion below is the only thing that separates those
// two worlds.
func TestMock_Setup_DeliversContextBytesOnTheLaunchPath(t *testing.T) {
	dir := t.TempDir()
	m := NewMock()

	require.NoError(t, m.Setup(context.Background(),
		launchSetupRequest(dir, []*agent.Fragment{{Content: "LAUNCH-MARKER-4c81"}}, hostManagedConfig())))

	got, err := os.ReadFile(filepath.Join(dir, mockContextFilename))
	require.NoError(t, err, "the launch path must have written %s", mockContextFilename)
	body := string(got)
	assert.Contains(t, body, "LAUNCH-MARKER-4c81",
		"the delivered file must carry the run's actual composed bytes")
	assert.Contains(t, body, agent.ManagedContextBegin, "content must sit inside the managed markers")
	assert.Contains(t, body, agent.ManagedContextEnd)
}

// TestMock_Cleanup_ReversesTheLaunchDelivery: the turn's teardown
// (grpc.runTurn calls Cleanup immediately after Execute) must strip what Setup
// wrote, exactly as it does for antigravity's AGENTS.md — mock rides the shared
// LIFO reversal rather than leaving debris in the user's project. This is also
// why a CLI-level assertion AFTER a run finds nothing: the file lives for the
// duration of the turn.
func TestMock_Cleanup_ReversesTheLaunchDelivery(t *testing.T) {
	dir := t.TempDir()
	m := NewMock()
	require.NoError(t, m.Setup(context.Background(),
		launchSetupRequest(dir, []*agent.Fragment{{Content: "TEARDOWN-MARKER-77b0"}}, hostManagedConfig())))
	require.FileExists(t, filepath.Join(dir, mockContextFilename), "precondition: Setup delivered")

	require.NoError(t, m.Cleanup(context.Background()))

	_, err := os.Stat(filepath.Join(dir, mockContextFilename))
	assert.True(t, os.IsNotExist(err),
		"Cleanup must reverse the delivery (nothing user-authored remained), got stat err %v", err)
}

// TestMock_Setup_NilManaged_DeliversNothing pins the ONE condition under which
// a live mock run still materializes nothing: setupViaCells' documented
// "nothing managed → no surfaces to deliver" short-circuit. mock declares
// RawContext false, so with a nil Managed there is no pre-step either and Setup
// legitimately writes zero bytes.
//
// This is NOT hypothetical bookkeeping: the skip-setup callers
// (bundle_distill.go, operations/oneshot.go's shared-cwd member path) send no
// managed payload at all. It is pinned as a TEST rather than left implicit so
// that "mock delivered nothing" is a stated, reviewed contract with a named
// cause, instead of a silent no-op somebody rediscovers.
func TestMock_Setup_NilManaged_DeliversNothing(t *testing.T) {
	dir := t.TempDir()
	m := NewMock()

	require.NoError(t, m.Setup(context.Background(),
		launchSetupRequest(dir, []*agent.Fragment{{Content: "NEVER-DELIVERED-1a2b"}}, nil)))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries,
		"with a nil Managed payload the cells seam short-circuits before any surface: nothing may be written")
}

// TestMock_SetupThenExecute_DeliveryIsAdditiveToTheEcho is the regression guard
// the whole change hangs on. A large number of hermetic acceptance scenarios
// read Execute's echo and its CTXLOOM_MOCK_RECORD_FILE; delivering a surface
// must ADD a file, never subtract from either of those. Asserting the delivered
// bytes AND the echo AND the record in one turn is what proves they coexist —
// checking them in separate tests would not catch a Setup that delivered by
// consuming the fragments it was meant to stash.
func TestMock_SetupThenExecute_DeliveryIsAdditiveToTheEcho(t *testing.T) {
	dir := t.TempDir()
	recordFile := filepath.Join(dir, "record.txt")
	m := NewMock()

	require.NoError(t, m.Setup(context.Background(),
		launchSetupRequest(dir, []*agent.Fragment{{Content: "ADDITIVE-MARKER-5d19"}}, &agent.ManagedConfig{
			Hooks:     &wire.HooksConfig{},
			DenyTools: []string{"Bash"},
			Skills:    []agent.SkillExport{{Name: "reviewer"}},
		})))

	var out strings.Builder
	res, err := m.Execute(context.Background(), &agent.ExecuteRequest{
		Mode:   agent.ModeOneshot,
		Prompt: &agent.Fragment{Content: "do the thing"},
		Env:    map[string]string{"CTXLOOM_MOCK_RECORD_FILE": recordFile},
	}, &out, &out)
	require.NoError(t, err)
	require.Equal(t, int32(0), res.ExitCode)

	// 1. The surface still holds the delivered bytes.
	delivered, err := os.ReadFile(filepath.Join(dir, mockContextFilename))
	require.NoError(t, err)
	assert.Contains(t, string(delivered), "ADDITIVE-MARKER-5d19")

	// 2. The echo is untouched: mode, fragment count, verbatim context, prompt.
	echo := out.String()
	assert.Contains(t, echo, "[mock] mode=1")
	assert.Contains(t, echo, "[mock] fragments=1", "Setup must still stash the fragments the echo counts")
	assert.Contains(t, echo, "[mock] context=ADDITIVE-MARKER-5d19", "the verbatim context echo survives delivery")
	assert.Contains(t, echo, "[mock] prompt=do the thing")

	// 3. The record file is untouched: the managed payload Setup stashed is
	// still what recordMockInput reports (the deny_tools/skills wire guard).
	record, err := os.ReadFile(recordFile)
	require.NoError(t, err)
	assert.Contains(t, string(record), "fragments=1")
	assert.Contains(t, string(record), "workdir=")
	assert.Contains(t, string(record), "=== DenyTools ===\nBash\n")
	assert.Contains(t, string(record), "=== Skills ===\nreviewer\n")
	assert.Contains(t, string(record), "=== Context ===\nADDITIVE-MARKER-5d19")
	assert.Contains(t, string(record), "=== Prompt ===\ndo the thing")
}

// TestMock_Setup_PreservesUserContentOutsideTheMarkers: mock's context file is a
// human-editable managed-marker file like CLAUDE.md, so a launch delivery into a
// project that already has one must merge, not clobber. Proven on the LAUNCH
// path (not just the surface's own unit test) because that is where a real user
// meets it.
func TestMock_Setup_PreservesUserContentOutsideTheMarkers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, mockContextFilename)
	require.NoError(t, os.WriteFile(path, []byte("# hand written\nUSER-PROSE-6e44\n"), 0o644))

	m := NewMock()
	require.NoError(t, m.Setup(context.Background(),
		launchSetupRequest(dir, []*agent.Fragment{{Content: "MERGED-MARKER-8b03"}}, hostManagedConfig())))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(got), "USER-PROSE-6e44", "hand-written content outside the markers must survive")
	assert.Contains(t, string(got), "MERGED-MARKER-8b03", "the managed section must still be delivered")

	// And the reversal keeps the user's half.
	require.NoError(t, m.Cleanup(context.Background()))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(after), "USER-PROSE-6e44")
	assert.NotContains(t, string(after), "MERGED-MARKER-8b03")
}
