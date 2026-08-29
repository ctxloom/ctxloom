//go:build parked_engines

// PARKED (parked_engines): tests the codex-only doctor_codex_home.go, out of
// the default build with internal/codex. grep -rn parked_engines finds every
// parked site.
package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/codex"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// codexHomeFixture stands up an isolated HOME whose ~/.codex is real, and
// returns the project dir plus that home. Both halves matter: the check reports
// the host home whether or not an instance exists, and a leaked real $HOME
// would make the "present"/"not created yet" assertions depend on the machine
// running the suite.
func codexHomeFixture(t *testing.T) (projectDir, hostHome string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// codex.GlobalHome honours $CODEX_HOME first; empty it so the fixture's
	// HOME is what resolves, the same way it would for a user with no override.
	t.Setenv(codex.CodexHomeEnv, "")
	hostHome = filepath.Join(home, codex.ConfigDirName)
	require.NoError(t, os.MkdirAll(hostHome, 0o755))
	return t.TempDir(), hostHome
}

// writeCodexInstance creates a per-session codex home under projectDir for harp
// and stamps its mtime, standing in for a session that has run. It writes a
// config.toml because an instance with an empty .codex is not what one looks
// like on disk.
func writeCodexInstance(t *testing.T, projectDir, harp string, modTime time.Time) string {
	t.Helper()
	dir := filepath.Join(paths.StatePath(filepath.Join(projectDir, paths.AppDirName)),
		harp, paths.SessionHomeDirName, codex.ConfigDirName)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, codex.ConfigFileName), []byte("model = \"o3\"\n"), 0o644))
	require.NoError(t, os.Chtimes(dir, modTime, modTime))
	return dir
}

// ageShape is the SHAPE an age must render in — a coarse unit and the word
// "ago", never a Go duration string. Asserted as a shape rather than a value
// because the exact number is a function of when the test runs, and pinning it
// would make the test flaky in exchange for proving nothing extra.
var ageShape = regexp.MustCompile(`\d+[mhd] ago`)

// TestDoctorCheckCodexHome_ReportsBothHomes is D6's core claim: with an
// instance on disk, the report names the real host home AND that instance, and
// the instance is LABELLED — harp, age, and the fact that the next session
// rebuilds it.
//
// The label is the load-bearing part and the reason reporting half (b) is safe
// at all. An unlabelled instance path reads as live configuration, and the
// reader's next move is to edit a directory that is about to be deleted.
func TestDoctorCheckCodexHome_ReportsBothHomes(t *testing.T) {
	projectDir, hostHome := codexHomeFixture(t)
	instance := writeCodexInstance(t, projectDir, "ugly-icy-squid", time.Now().Add(-3*time.Hour))

	check := doctorCheckCodexHome(projectDir)

	assert.Equal(t, doctorCodexHomeMarker, check.Marker)
	assert.Equal(t, doctorInfo, check.Status, "nothing here is broken; a warn would teach a codex user to skip the line")

	assert.Contains(t, check.Detail, hostHome, "half (a): the real host home an unbound run uses")
	assert.Contains(t, check.Detail, instance, "half (b): the most recent per-session instance")

	assert.Contains(t, check.Detail, "ugly-icy-squid", "the instance must be labelled with the harp it belonged to")
	assert.Regexp(t, ageShape, check.Detail, "the instance must be labelled with its age")
	assert.Contains(t, check.Detail, "rebuilt fresh next session",
		"an unlabelled instance path reads as live configuration and invites editing a doomed directory")

	assert.Contains(t, check.Detail, codex.LaunchOnlySettingsReason,
		"the report must say WHY there is no project copy, or it reads as a broken install")
}

// TestDoctorCheckCodexHome_NoInstanceReportsHostHomeAndANote is the other,
// far more common state: no session has run in this project, so there is no
// instance. The host home is still reported, plus a note saying when one would
// appear — silence there is what sends a user hunting for a config file that
// was never going to be written.
func TestDoctorCheckCodexHome_NoInstanceReportsHostHomeAndANote(t *testing.T) {
	projectDir, hostHome := codexHomeFixture(t)

	check := doctorCheckCodexHome(projectDir)

	assert.Equal(t, doctorInfo, check.Status)
	assert.Contains(t, check.Detail, hostHome)
	assert.Contains(t, check.Detail, "no per-session instance")
	assert.Contains(t, check.Detail, "config_home: project",
		"the note must say what makes an instance appear")
	assert.NotRegexp(t, ageShape, check.Detail,
		"there is no instance, so there is no age to report — a stray one would be about nothing")
}

// TestDoctorCheckCodexHome_PicksTheMostRecentInstance pins RECENCY, which is
// the whole value of reporting only one: the newest instance is the one whose
// contents answer "what did ctxloom generate for me last time". Reporting an
// older one shows a state ctxloom has since stopped producing, which is worse
// than reporting none.
//
// The harps are chosen so name order and mtime order DISAGREE: "aged-old-mule"
// sorts first and is oldest, so an implementation that took the first entry, or
// the last, rather than the newest passes only by accident.
func TestDoctorCheckCodexHome_PicksTheMostRecentInstance(t *testing.T) {
	projectDir, _ := codexHomeFixture(t)
	old := writeCodexInstance(t, projectDir, "aged-old-mule", time.Now().Add(-72*time.Hour))
	recent := writeCodexInstance(t, projectDir, "brisk-new-otter", time.Now().Add(-10*time.Minute))
	oldest := writeCodexInstance(t, projectDir, "zzzz-stale-yak", time.Now().Add(-240*time.Hour))

	check := doctorCheckCodexHome(projectDir)

	assert.Contains(t, check.Detail, recent, "the NEWEST instance is the one worth reporting")
	assert.Contains(t, check.Detail, "brisk-new-otter")
	assert.NotContains(t, check.Detail, old, "an older instance shows state ctxloom no longer produces")
	assert.NotContains(t, check.Detail, oldest)
}

// TestDoctorCheckCodexHome_CreatesNothing is the read-only contract. doctor
// diagnoses; a diagnostic that materialises the thing it was asked about
// answers a question it just changed the answer to — and here it would mean
// creating a project-side directory that is supposed to appear only at launch.
func TestDoctorCheckCodexHome_CreatesNothing(t *testing.T) {
	projectDir, _ := codexHomeFixture(t)
	appDir := filepath.Join(projectDir, paths.AppDirName)

	_ = doctorCheckCodexHome(projectDir)

	_, err := os.Stat(appDir)
	assert.True(t, os.IsNotExist(err), "doctor must not create %s (or anything under it)", appDir)
}

// TestDoctorCheckCodexHome_ReportsAnAbsentHostHomeAsAbsent keeps the host-home
// half honest. "present" is a claim about the filesystem, and a check that
// printed the path unconditionally would report a home that has never existed
// exactly like one full of configuration.
func TestDoctorCheckCodexHome_ReportsAnAbsentHostHomeAsAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(codex.CodexHomeEnv, "")

	check := doctorCheckCodexHome(t.TempDir())

	assert.Contains(t, check.Detail, "not created yet")
	assert.NotContains(t, check.Detail, "(present)")
}

// TestDoctorCheckCodexHome_IgnoresOtherEnginesInstances pins the leaf. One
// session's state/<harp>/home hosts claude's and kiro's instances beside
// codex's; reporting a claude home as codex's would be worse than reporting
// nothing, because it is confidently wrong.
func TestDoctorCheckCodexHome_IgnoresOtherEnginesInstances(t *testing.T) {
	projectDir, _ := codexHomeFixture(t)
	claudeInstance := filepath.Join(paths.StatePath(filepath.Join(projectDir, paths.AppDirName)),
		"ugly-icy-squid", paths.SessionHomeDirName, "claude")
	require.NoError(t, os.MkdirAll(claudeInstance, 0o755))

	check := doctorCheckCodexHome(projectDir)

	assert.Contains(t, check.Detail, "no per-session instance",
		"a claude instance is not a codex one, however recently it was written")
	assert.NotContains(t, check.Detail, claudeInstance)
}

// TestDoctorAgeString_RendersCoarsestTrueUnit pins the rendering itself. A Go
// duration ("2h13m47.9s") is a measurement; the question a reader is asking is
// "is this stale?", and the coarsest true unit answers it.
func TestDoctorAgeString_RendersCoarsestTrueUnit(t *testing.T) {
	assert.Equal(t, "just now", doctorAgeString(20*time.Second))
	assert.Equal(t, "13m ago", doctorAgeString(13*time.Minute))
	assert.Equal(t, "5h ago", doctorAgeString(5*time.Hour))
	assert.Equal(t, "3d ago", doctorAgeString(74*time.Hour))
	assert.Contains(t, doctorAgeString(-time.Hour), "clock skew",
		"a negative age is a fact about the clock, not an age")
}
