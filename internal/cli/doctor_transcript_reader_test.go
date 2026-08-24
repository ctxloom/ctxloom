package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	claudereader "github.com/ctxloom/ctxloom/internal/transcript/vendorreader/claude"
	codexreader "github.com/ctxloom/ctxloom/internal/transcript/vendorreader/codex"
)

// --- DOCTOR-CHECK-TRANSCRIPT-READER-v2 --------------------------------------
//
// Every assertion below reads the REPORTED VALUES — the detected version, the
// reader selected for it, the range that reader declares, the validated
// version it cites — never that a command exited cleanly. A doctor check that
// runs and reports nothing true is precisely the failure this package's
// characteristic bug takes: a clean exit over an empty finding.
//
// The probe is injected in every unit test here, so none of them depends on
// claude-code, codex or kiro actually being installed on the machine running
// the suite, and each can name the exact version it wants selected.

// fixedVersionProbe answers every engine with one version, whatever it is
// asked about — enough for the single-engine projects setupProject scaffolds.
func fixedVersionProbe(version string) engineVersionProbe {
	return func(context.Context, string) (string, error) { return version, nil }
}

// failingVersionProbe stands in for an engine that is not installed, or whose
// --version output could not be parsed: internal/engineversion's typed
// refusals, which never carry a fallback value.
func failingVersionProbe(err error) engineVersionProbe {
	return func(context.Context, string) (string, error) { return "", err }
}

// TestDoctorCheckTranscriptReaders_RightState_DetectedVersionSelectsCarriedReader
// asserts the whole reported triple for a version a carried reader claims:
// the version detected, the reader selected, and the range ctxloom carries.
// The expected range and validated version are READ FROM the reader package's
// own VersionedAdapters declaration rather than retyped here, so this test
// cannot pass while reporting a range no reader actually declares.
func TestDoctorCheckTranscriptReaders_RightState_DetectedVersionSelectsCarriedReader(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	require.Len(t, claudereader.VersionedAdapters, 1, "this test reads the single declared claude reader")
	declared := claudereader.VersionedAdapters[0]

	check := doctorCheckTranscriptReaders(context.Background(), cfg, fixedVersionProbe("2.1.225"))

	assert.Equal(t, doctorInfo, check.Status)
	assert.Contains(t, check.Detail, "claude-code", "must name the engine")
	assert.Contains(t, check.Detail, "2.1.225", "must report the DETECTED version, not a placeholder")
	assert.Contains(t, check.Detail, "claude.Adapter", "must name the reader actually selected")
	assert.Contains(t, check.Detail, declared.Versions.String(), "must report the range that reader declares")
	assert.Contains(t, check.Detail, declared.ValidatedVersion, "must cite the version that range was validated at")
	assert.NotContains(t, check.Detail, "REFUSE")
}

// TestDoctorCheckTranscriptReaders_RightState_RangesAreThisEngineOwn proves the
// ranges reported are read per-engine rather than one hard-coded list: codex's
// declared range is deliberately narrow (0.144.x) where claude's spans a major
// line, so a check that printed a single fixed list would fail here.
func TestDoctorCheckTranscriptReaders_RightState_RangesAreThisEngineOwn(t *testing.T) {
	_, cfg := setupProject(t, "codex")
	require.Len(t, codexreader.VersionedAdapters, 1, "this test reads the single declared codex reader")
	declared := codexreader.VersionedAdapters[0]

	check := doctorCheckTranscriptReaders(context.Background(), cfg, fixedVersionProbe("0.144.4"))

	assert.Equal(t, doctorInfo, check.Status)
	assert.Contains(t, check.Detail, "codex 0.144.4")
	assert.Contains(t, check.Detail, declared.Versions.String())
	assert.Contains(t, check.Detail, declared.ValidatedVersion)
	assert.NotContains(t, check.Detail, claudereader.VersionedAdapters[0].Versions.String(),
		"must not report another engine's range")
}

// TestDoctorCheckTranscriptReaders_WrongState_DetectedVersionCarriesNoReader is
// the state this check exists for: an installed engine whose version no
// carried reader claims, so every transcript it writes will refuse to convert.
// A warn, because it is actionable — and the line must carry BOTH halves of
// the diagnosis (the version that has no reader, and what ctxloom does carry).
func TestDoctorCheckTranscriptReaders_WrongState_DetectedVersionCarriesNoReader(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	declared := claudereader.VersionedAdapters[0]

	check := doctorCheckTranscriptReaders(context.Background(), cfg, fixedVersionProbe("9.9.9"))

	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, "claude-code 9.9.9", "must name the version that has no reader")
	assert.Contains(t, check.Detail, declared.Versions.String(), "must show what ctxloom DOES carry — that gap is the diagnosis")
	assert.Contains(t, check.Detail, "REFUSE", "must say the transcript refuses rather than being read by an unvalidated reader")
}

// TestDoctorCheckTranscriptReaders_WrongState_UnparseableVersionAlsoRefuses
// covers the other refusal SelectVersionedAdapter distinguishes: a detected
// value that is not a version at all. It must land in the same warn, not be
// mistaken for a clean selection.
func TestDoctorCheckTranscriptReaders_WrongState_UnparseableVersionAlsoRefuses(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")

	check := doctorCheckTranscriptReaders(context.Background(), cfg, fixedVersionProbe("not-a-version"))

	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, "not-a-version")
	assert.Contains(t, check.Detail, "NO reader carried")
}

// TestDoctorCheckTranscriptReaders_RightState_UnprobedVersionIsInfoNotWarn
// keeps an uninstalled engine from warning twice: that is
// DOCTOR-CHECK-DEPS-a1's finding. The carried ranges are still reported,
// because they are true regardless of what is installed.
func TestDoctorCheckTranscriptReaders_RightState_UnprobedVersionIsInfoNotWarn(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	declared := claudereader.VersionedAdapters[0]

	check := doctorCheckTranscriptReaders(context.Background(), cfg,
		failingVersionProbe(errors.New("claude: binary not on PATH")))

	assert.Equal(t, doctorInfo, check.Status)
	assert.Contains(t, check.Detail, "version not detected")
	assert.Contains(t, check.Detail, "claude: binary not on PATH", "must carry the probe's own reason")
	assert.Contains(t, check.Detail, declared.Versions.String(), "carried ranges are true whether or not the engine is installed")
}

// TestDoctorCheckTranscriptReaders_RightState_EngineWithNoVendorReader proves
// an engine with no vendor-native transcript store is SILENT rather than
// reported as a gap — opencode reads its own store, and inventing a missing
// reader for it would be a false finding.
func TestDoctorCheckTranscriptReaders_RightState_EngineWithNoVendorReader(t *testing.T) {
	_, cfg := setupProject(t, "opencode")

	check := doctorCheckTranscriptReaders(context.Background(), cfg, fixedVersionProbe("1.18.4"))

	assert.Equal(t, doctorInfo, check.Status)
	assert.Contains(t, check.Detail, "no configured engine reads a vendor-native transcript store")
	assert.NotContains(t, check.Detail, "1.18.4", "nothing was selected, so no version may be reported")
}

// TestDoctorCheckTranscriptReaders_RightState_NoConfigIsNotACrash covers the
// config-failed-to-load path every other check here handles: no configured
// engines means nothing to report, not a panic.
func TestDoctorCheckTranscriptReaders_RightState_NoConfigIsNotACrash(t *testing.T) {
	check := doctorCheckTranscriptReaders(context.Background(), nil, fixedVersionProbe("2.1.225"))

	assert.Equal(t, doctorInfo, check.Status)
	assert.Contains(t, check.Detail, "no configured engine reads a vendor-native transcript store")
}

// TestDoctorCmd_TranscriptReaderCheckIsWiredIntoTheReport is the wiring half:
// the check has to appear in the command's OWN report, not merely be callable.
// It asserts the reported range — a real value, true down all three branches of
// the check (selected / no reader / not probed), so it holds on any host
// regardless of whether claude-code is installed here — never a clean exit.
func TestDoctorCmd_TranscriptReaderCheckIsWiredIntoTheReport(t *testing.T) {
	root, _ := setupProject(t, "claude-code")

	out, err := runDoctor(t, root)
	require.NoError(t, err)

	line := lineContaining(t, out, doctorTranscriptReaderMarker)
	assert.Contains(t, line, "claude-code", "the report line must name the configured engine")
	assert.Contains(t, line, claudereader.VersionedAdapters[0].Versions.String(),
		"the report line must carry the range ctxloom actually carries a reader for")
}

// TestDoctorCmd_TranscriptReaderCheckIsNotInDepsScope keeps --deps what it is:
// machine-capability probes only, usable before a project exists. This check
// reads the project's configured engines, so it has no place there.
func TestDoctorCmd_TranscriptReaderCheckIsNotInDepsScope(t *testing.T) {
	root, _ := setupProject(t, "claude-code")

	out, err := runDoctor(t, root, "--deps")
	require.NoError(t, err)

	assert.NotContains(t, out, doctorTranscriptReaderMarker)
}
