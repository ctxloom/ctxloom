package agents

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

func writeAgentFile(t *testing.T, dir, name, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0644))
}

func TestParseAgent(t *testing.T) {
	sub, err := ParseAgent([]byte("llm: fast\nprofiles:\n  - p1\n  - p2\n"))
	require.NoError(t, err)
	assert.Equal(t, "fast", sub.LLM)
	assert.Equal(t, []string{"p1", "p2"}, sub.Profiles)
	// Name/Source are not encoded in the body — callers assign them.
	assert.Empty(t, sub.Name)
	assert.Empty(t, sub.Source)
}

// TestLoader_ListReadsDirectory proves an agent loads from a
// .ctxloom/agents/<name>.yaml file, with Name from the filename and Source
// from the path.
func TestLoader_ListReadsDirectory(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "dev.yaml", "llm: claude-code\nprofiles: [go-developer]\n")
	writeAgentFile(t, dir, "finder.yaml", "profiles: [finder]\n")

	list, err := NewLoader([]string{dir}, nil).List()
	require.NoError(t, err)
	require.Len(t, list, 2)
	// Sorted by name.
	assert.Equal(t, "dev", list[0].Name)
	assert.Equal(t, "claude-code", list[0].LLM)
	assert.Equal(t, []string{"go-developer"}, list[0].Profiles)
	assert.Equal(t, filepath.Join(dir, "dev.yaml"), list[0].Source)

	assert.Equal(t, "finder", list[1].Name)
	assert.Empty(t, list[1].LLM, "engine is optional")
}

// TestLoader_FaultTolerantBadFile proves a malformed agent file is skipped
// (warned, not fatal) and the valid ones still load — ctxloom fault tolerance.
func TestLoader_FaultTolerantBadFile(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "good.yaml", "llm: fast\nprofiles: [p1]\n")
	writeAgentFile(t, dir, "bad.yaml", "llm: [this, is, not, a, string\n: : :\n")

	list, err := NewLoader([]string{dir}, nil).List()
	require.NoError(t, err, "a bad file must not fail the whole list")
	require.Len(t, list, 1)
	assert.Equal(t, "good", list[0].Name)
}

func TestLoader_MissingDirectory(t *testing.T) {
	list, err := NewLoader([]string{filepath.Join(t.TempDir(), "nope")}, nil).List()
	require.NoError(t, err)
	assert.Empty(t, list)
}

// TestParseAgent_DrivingRoundTrips proves the driving enum round-trips
// through agent YAML: an empty/absent value, and each named enum value.
func TestParseAgent_DrivingRoundTrips(t *testing.T) {
	sub, err := ParseAgent([]byte("llm: fast\nprofiles: [p1]\n"))
	require.NoError(t, err)
	assert.Equal(t, DrivingMode(""), sub.Driving, "absent driving: leaves the zero value (conversational)")

	sub, err = ParseAgent([]byte("llm: fast\nprofiles: [p1]\ndriving: conversational\n"))
	require.NoError(t, err)
	assert.Equal(t, DrivingConversational, sub.Driving)

	sub, err = ParseAgent([]byte("llm: fast\nprofiles: [p1]\ndriving: oneshot\n"))
	require.NoError(t, err)
	assert.Equal(t, DrivingOneshot, sub.Driving)
}

// TestParseAgent_UnknownDrivingRejected proves an unrecognized driving value
// is REJECTED at parse time (unlike Runtime/Permissions' advisory-only
// unknown-value handling) — a typo here changes execution semantics, so it
// must never silently resolve to the conversational default.
func TestParseAgent_UnknownDrivingRejected(t *testing.T) {
	_, err := ParseAgent([]byte("llm: fast\ndriving: bogus\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus")
	assert.Contains(t, err.Error(), "conversational")
	assert.Contains(t, err.Error(), "oneshot")
}

// TestLoader_FaultTolerantBadDriving proves the Loader's existing
// fault-tolerant degrade (warn + skip, per TestLoader_FaultTolerantBadFile)
// extends to a bad `driving:` value: one agent file with an unknown driving
// string is skipped, the rest of the directory still loads.
func TestLoader_FaultTolerantBadDriving(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "good.yaml", "llm: fast\nprofiles: [p1]\n")
	writeAgentFile(t, dir, "bad.yaml", "llm: fast\ndriving: bogus\n")

	list, err := NewLoader([]string{dir}, nil).List()
	require.NoError(t, err, "a bad driving value must not fail the whole list")
	require.Len(t, list, 1)
	assert.Equal(t, "good", list[0].Name)
}

// TestParseAgent_NoProfilesRejected proves an empty, comments-only,
// or mistyped-key agent file is REJECTED rather than silently producing a
// blank binding that composes nothing (agents.go's own doc: a binding of an
// engine to "one or more composed profiles").
func TestParseAgent_NoProfilesRejected(t *testing.T) {
	cases := map[string]string{
		"zero-byte":       "",
		"comments-only":   "# just a comment\n",
		"engine, no list": "llm: fast\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseAgent([]byte(body))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "no profiles")
		})
	}
}

// TestParseAgent_UnknownKeyRejected proves a typo'd top-level key
// is caught at parse time (KnownFields) instead of being silently dropped.
func TestParseAgent_UnknownKeyRejected(t *testing.T) {
	_, err := ParseAgent([]byte("llm: fast\nprofils: [p1]\n"))
	require.Error(t, err)
}

// TestLoader_FaultTolerantNoProfiles proves the Loader's
// warn-and-skip fault tolerance extends to a profile-less agent file: it is
// skipped rather than emitted as a blank binding, and the rest of the
// directory still loads.
func TestLoader_FaultTolerantNoProfiles(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "good.yaml", "llm: fast\nprofiles: [p1]\n")
	writeAgentFile(t, dir, "empty.yaml", "")

	list, err := NewLoader([]string{dir}, nil).List()
	require.NoError(t, err, "a profile-less agent file must not fail the whole list")
	require.Len(t, list, 1)
	assert.Equal(t, "good", list[0].Name)
}

func TestParseDrivingMode(t *testing.T) {
	cases := []struct {
		in     string
		want   DrivingMode
		wantOk bool
	}{
		{"", DrivingConversational, true},
		{"conversational", DrivingConversational, true},
		{"oneshot", DrivingOneshot, true},
		{"bogus", "", false},
		{"Conversational", "", false}, // NOT lenient on case, unlike ParsePermissionMode
	}
	for _, tc := range cases {
		got, ok := parseDrivingMode(tc.in)
		assert.Equal(t, tc.wantOk, ok, "in=%q", tc.in)
		if tc.wantOk {
			assert.Equal(t, tc.want, got, "in=%q", tc.in)
		}
	}
}

// TestGetAgentDirs returns only directories that exist on disk.
func TestGetAgentDirs(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, ".ctxloom")
	require.NoError(t, os.MkdirAll(filepath.Join(app, "agents"), 0755))

	dirs := GetAgentDirs(nil, []string{app})
	require.Len(t, dirs, 1)
	assert.Equal(t, filepath.Join(app, "agents"), dirs[0])

	// No agents dir → no entry.
	assert.Empty(t, GetAgentDirs(nil, []string{filepath.Join(root, "absent")}))
}

// TestLoader_CorruptFileDoesNotShadowLaterValidAgent pins that the
// first-directory-wins rule is about a NAME that RESOLVED, not a name that was
// merely encountered. A file that fails to parse is skipped with a warning, so
// it must not also claim the name — otherwise a corrupt agent in an
// earlier-searched directory makes a perfectly valid same-named agent in a
// later directory disappear, and the user is told only "skipping agent …",
// never that a real definition was suppressed.
func TestLoader_CorruptFileDoesNotShadowLaterValidAgent(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	writeAgentFile(t, first, "reviewer.yaml", "profiles: [\n")
	writeAgentFile(t, second, "reviewer.yaml", "llm: fast\nprofiles:\n  - review\n")

	l := NewLoader([]string{first, second}, nil)
	got, err := l.List()
	require.NoError(t, err)

	require.Len(t, got, 1, "the valid definition in the second directory must survive")
	assert.Equal(t, "reviewer", got[0].Name)
	assert.Equal(t, "fast", got[0].LLM)
	assert.Equal(t, []string{"review"}, got[0].Profiles)
}

// TestLoader_ValidFileStillWinsOverLaterDuplicate pins the other half of the
// same rule, so the fix cannot be "stop deduplicating": a SUCCESSFUL
// load in the first directory still shadows a later same-named agent.
func TestLoader_ValidFileStillWinsOverLaterDuplicate(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	writeAgentFile(t, first, "reviewer.yaml", "llm: first\nprofiles:\n  - a\n")
	writeAgentFile(t, second, "reviewer.yaml", "llm: second\nprofiles:\n  - b\n")

	l := NewLoader([]string{first, second}, nil)
	got, err := l.List()
	require.NoError(t, err)

	require.Len(t, got, 1)
	assert.Equal(t, "first", got[0].LLM)
}

// statErrorFs makes Stat on one exact path fail with an error that is NOT
// os.ErrNotExist — the "the directory is there but I cannot look at it" case
// (permissions, a dead mount, an unreadable overlay). afero's MemMapFs and the
// OS filesystem both refuse to produce it on demand, so it is injected here.
type statErrorFs struct {
	afero.Fs
	failPath string
	err      error
}

func (f statErrorFs) Stat(name string) (os.FileInfo, error) {
	if name == f.failPath {
		return nil, f.err
	}
	return f.Fs.Stat(name)
}

// TestLoader_UnstattableDirectoryWarns pins the loader half: an agents
// directory that cannot be statted is skipped, and that skip must be VISIBLE.
// Swallowing the stat error makes "you have no agents configured" and "your
// agents directory is unreadable" the same observable outcome — an empty list,
// exit 0, in silence.
func TestLoader_UnstattableDirectoryWarns(t *testing.T) {
	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	defer restore()

	fs := statErrorFs{
		Fs:       afero.NewMemMapFs(),
		failPath: "/broken/agents",
		err:      fmt.Errorf("permission denied"),
	}
	l := NewLoader([]string{"/broken/agents"}, fs)

	got, err := l.List()
	require.NoError(t, err, "one unreadable directory must not sink the whole list")
	assert.Empty(t, got)

	assert.Contains(t, sink.String(), "/broken/agents")
	assert.Contains(t, sink.String(), "permission denied")
}

// TestGetAgentDirs_UnstattableDirectoryWarns pins the discovery half.
// GetAgentDirs drops the directory on a stat error with `err == nil &&`, which
// is the same silence one layer earlier: discovery reports "no agent
// directories" and the loader is never even asked.
func TestGetAgentDirs_UnstattableDirectoryWarns(t *testing.T) {
	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	defer restore()

	agentsDir := paths.AgentsPath("/app")
	fs := statErrorFs{
		Fs:       afero.NewMemMapFs(),
		failPath: agentsDir,
		err:      fmt.Errorf("permission denied"),
	}

	assert.Empty(t, GetAgentDirs(fs, []string{"/app"}))
	assert.Contains(t, sink.String(), agentsDir)
	assert.Contains(t, sink.String(), "permission denied")
}

// TestGetAgentDirs_AbsentDirectoryIsSilent guards the other side of the
// fix: a directory that simply does not exist is the ordinary case
// for every project without agents, and must stay quiet. Only a stat FAILURE
// is worth a warning.
func TestGetAgentDirs_AbsentDirectoryIsSilent(t *testing.T) {
	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	defer restore()

	assert.Empty(t, GetAgentDirs(afero.NewMemMapFs(), []string{"/app"}))
	assert.Empty(t, sink.String())
}

// TestAgent_FromConfig pins that Source is a control-flow discriminator,
// not a diagnostic string. Exactly the SourceConfig sentinel means "config.yaml
// owns this"; every other value is a file path the config-key writer must not
// claim to own. A file path that merely happens to contain "config" is still a
// file.
func TestAgent_FromConfig(t *testing.T) {
	assert.True(t, Agent{Source: SourceConfig}.FromConfig())
	assert.False(t, Agent{Source: "/proj/.ctxloom/agents/reviewer.yaml"}.FromConfig())
	assert.False(t, Agent{Source: "/proj/.ctxloom/agents/config.yaml"}.FromConfig())
	assert.False(t, Agent{}.FromConfig(), "an unsourced binding is not config-owned")
}

// TestParseAgent_ValidationBoundary pins the answer to which of an
// agent's user-settable fields THIS package validates, and which it
// deliberately leaves to a later phase. The split is a decision, not an
// oversight, and it is invisible in the code because it is spread across four
// packages — so it is recorded here.
//
// Owned here, at parse time, because a wrong value changes what runs:
//   - profiles: at least one (a binding that composes nothing is a typo)
//   - driving:  a closed enum (it decides whether a child's engine process
//     survives a turn boundary)
//
// Deliberately NOT rejected here, because an unknown value degrades to a
// documented default rather than changing semantics; internal/operations warns
// at WRITE time instead:
//   - runtime, permissions
//
// Escalation is validated later still, by the coordinator that builds the
// ladder. If a consolidating Agent.Validate() ever lands, this test is what
// stops the two parse-time rejections from being quietly lost in the move.
func TestParseAgent_ValidationBoundary(t *testing.T) {
	rejected := map[string]string{
		"no profiles":     "llm: fast\n",
		"unknown driving": "profiles: [p]\ndriving: warm\n",
	}
	for name, body := range rejected {
		t.Run("rejects "+name, func(t *testing.T) {
			_, err := ParseAgent([]byte(body))
			assert.Error(t, err)
		})
	}

	accepted := map[string]string{
		"unknown runtime":     "profiles: [p]\nruntime: submarine\n",
		"unknown permissions": "profiles: [p]\npermissions: whatever\n",
		"unvalidated escalation rung": "profiles: [p]\nescalation:\n" +
			"  - action: auto_acept\n    kinds: [NOT_A_KIND]\n",
	}
	for name, body := range accepted {
		t.Run("accepts "+name, func(t *testing.T) {
			a, err := ParseAgent([]byte(body))
			require.NoError(t, err)
			require.NotNil(t, a)
		})
	}
}
