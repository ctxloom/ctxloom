package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	claudereader "github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader/claude"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// The published command, end to end: each check operations.Doctor runs has
// to reach `ctxloom doctor`'s own report (the JSON form, the bytes every
// piped caller reads), and the --deps scope has to keep the project-state
// checks out. operations pins the report's order as a record; these pin that
// the command renders it.

// TestDoctorCmd_ReportCarriesTheLegacyIndexCheck: HOME is pointed at a
// directory this test owns (isolateGitHostState, the same host-state
// isolation every full-command doctor test here uses) so the fixture lands
// where paths.HomeSessionsDir resolves for the command.
func TestDoctorCmd_ReportCarriesTheLegacyIndexCheck(t *testing.T) {
	root, _ := setupProject(t, "claude-code")
	home := t.TempDir()
	isolateGitHostState(t, "", home)
	sessionsRoot, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(sessionsRoot, 0o755))
	stale := filepath.Join(sessionsRoot, paths.IndexFileName)
	require.NoError(t, os.WriteFile(stale, []byte("sessions: []\n"), 0o644))

	out, err := execDoctor(t, root, "--format", "json")
	require.NoError(t, err)
	var report operations.DoctorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))

	var found *operations.DoctorCheck
	for i := range report.Checks {
		if report.Checks[i].Marker == "DOCTOR-CHECK-LEGACY-INDEX-y5" {
			found = &report.Checks[i]
		}
	}
	require.NotNil(t, found, "the legacy-index check is missing from the report")
	assert.Equal(t, operations.DoctorWarn, found.Status)
	assert.Contains(t, found.Detail, stale)
	assert.Contains(t, found.Detail, "delete it; the sidecars are the record")
}

func TestDoctorCmd_ReportsTheMCPInvocationCheck(t *testing.T) {
	root, _ := setupProject(t, "mock")

	out, err := execDoctor(t, root)

	require.NoError(t, err)
	assert.Contains(t, out, "DOCTOR-CHECK-MCP-INVOCATION-g7")
}

// TestDoctorCmd_ShowsLiveCoordinatorSpoolCounters: `ctxloom doctor` — the
// published command, JSON form — carries the spool counters of a LIVE
// coordinator, read over its consumer socket, not from any file.
func TestDoctorCmd_ShowsLiveCoordinatorSpoolCounters(t *testing.T) {
	root, _ := setupProject(t, "claude-code")
	// isolateGitHostState is what runDoctor does internally; done here by
	// hand so the fake coordinator's endpoint.json lands in the HOME the
	// command will actually discover from.
	home := t.TempDir()
	isolateGitHostState(t, "", home)
	f := newFakeConsumerServer()
	f.stats = &agentcoordpb.SpoolStatsResult{Delivered: 42, Failed: 1}
	startFakeCoordinator(t, home, f)

	out, err := execDoctor(t, root, "--format", "json")
	require.NoError(t, err)
	check := doctorCheckNamed(t, out, "DOCTOR-CHECK-SPOOL-COUNTERS-w3")
	assert.Equal(t, operations.DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, "delivered=42")
	assert.Contains(t, check.Detail, "failed=1")
}

// TestDoctorCmd_TranscriptReaderCheckIsWiredIntoTheReport asserts the
// reported range — a real value, true down all three branches of the check
// (selected / no reader / not probed), so it holds on any host regardless of
// whether claude-code is installed here — never a clean exit.
func TestDoctorCmd_TranscriptReaderCheckIsWiredIntoTheReport(t *testing.T) {
	root, _ := setupProject(t, "claude-code")

	out, err := runDoctor(t, root)
	require.NoError(t, err)

	check := doctorCheckNamed(t, out, "DOCTOR-CHECK-TRANSCRIPT-READER-v2")
	assert.Contains(t, check.Detail, "claude-code", "the check must name the configured engine")
	assert.Contains(t, check.Detail, claudereader.VersionedAdapters[0].Range.String(),
		"the check must carry the range ctxloom actually carries a reader for")
}

// TestDoctorCmd_TranscriptReaderCheckIsNotInDepsScope keeps --deps what it is:
// machine-capability probes only, usable before a project exists. This check
// reads the project's configured engines, so it has no place there.
func TestDoctorCmd_TranscriptReaderCheckIsNotInDepsScope(t *testing.T) {
	root, _ := setupProject(t, "claude-code")

	out, err := runDoctor(t, root, "--deps")
	require.NoError(t, err)

	assert.NotContains(t, out, "DOCTOR-CHECK-TRANSCRIPT-READER-v2")
}

func TestDoctorCmd_RendersTheUpstreamSignaturesCheck(t *testing.T) {
	root, _ := setupProject(t, "claude-code")
	out, err := runDoctor(t, root)
	require.NoError(t, err)
	assert.Contains(t, out, "DOCTOR-CHECK-UPSTREAM-SIGNATURES-o5",
		"`ctxloom doctor` must actually render the check; one operations.Doctor omits reports nothing to anyone")
}

// --deps is the pre-setup mode (init's PRIME, the setup skill's phase 1): only
// machine-capability probes. This advisory is about a project's lockfile and a
// publisher's signatures, neither of which exists yet in that mode, so it must
// stay out of it.
func TestDoctorCmd_UpstreamSignaturesCheckIsNotADepsProbe(t *testing.T) {
	root, _ := setupProject(t, "claude-code")
	out, err := runDoctor(t, root, "--deps")
	require.NoError(t, err)
	assert.NotContains(t, out, "DOCTOR-CHECK-UPSTREAM-SIGNATURES-o5")
}
