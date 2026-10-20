package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// stateFixture is three sessions of one project, each stamped ended (the last
// run's end) and each in a different state now, as the lock probe reports it:
// a resumed session running again, one that ended, and one whose lock cannot
// be read. Every title carries the word "lockstep" so a search matches all
// three and only --state narrows.
type stateFixture struct {
	live, ended, unknown string
	endOf                map[string]time.Time
}

func seedStateFixture(t *testing.T) stateFixture {
	t.Helper()
	dir := testsupport.ProjectDir(t)
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)

	f := stateFixture{endOf: map[string]time.Time{}}
	base := time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC)
	for i, slot := range []*string{&f.live, &f.ended, &f.unknown} {
		e, err := mgr.AssignHarp(dir, "claude-code")
		require.NoError(t, err)
		_, err = mgr.RecordOutputDir(e.HarpName, t.TempDir())
		require.NoError(t, err)
		end := base.Add(time.Duration(i) * 7 * time.Hour)
		require.NoError(t, mgr.MarkEnded(e.HarpName, end))
		recordPromptsFor(t, e.HarpName, "Keep the lockstep tests honest")
		*slot = e.HarpName
		f.endOf[e.HarpName] = end
	}

	states := map[string]string{
		f.live:    operations.SessionLive,
		f.ended:   operations.SessionEnded,
		f.unknown: operations.SessionUnknown,
	}
	prev := sessionStateOf
	sessionStateOf = func(harp string) string { return states[harp] }
	t.Cleanup(func() { sessionStateOf = prev })
	return f
}

// runSessionCmd runs the real command tree and returns stdout; flag vars a
// parse leaves set are reset so later tests do not inherit them.
func runSessionCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		sessionListState, sessionQueryState = "", ""
	})
	err := rootCmd.Execute()
	return out.String(), err
}

func jsonRowsByHarp(t *testing.T, out string) map[string]map[string]any {
	t.Helper()
	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &rows), "output: %s", out)
	byHarp := make(map[string]map[string]any, len(rows))
	for _, r := range rows {
		byHarp[r["harp"].(string)] = r
	}
	return byHarp
}

// TestSessionList_JSON_StateFromTheLockAndNoEndWhileLive: every row carries
// the lock's state, and a live session — though its record still carries the
// last run's end stamp — has no end.
func TestSessionList_JSON_StateFromTheLockAndNoEndWhileLive(t *testing.T) {
	f := seedStateFixture(t)
	out, err := runSessionCmd(t, "session", "list", "--format", "json")
	require.NoError(t, err)

	rows := jsonRowsByHarp(t, out)
	require.Len(t, rows, 3)
	assert.Equal(t, "live", rows[f.live]["state"])
	assert.Equal(t, "ended", rows[f.ended]["state"])
	assert.Equal(t, "unknown", rows[f.unknown]["state"])
	assert.NotContains(t, rows[f.live], "end", "a live session's end is blank: its stamp is the LAST run's")
	assert.Contains(t, rows[f.ended], "end")
	assert.Contains(t, rows[f.unknown], "end", "an unknown session shows its end stamp as recorded")
}

// TestSessionList_Text_StateColumnAndBlankEnd: the table has a STATE column,
// and the live row prints no END while the others print theirs.
func TestSessionList_Text_StateColumnAndBlankEnd(t *testing.T) {
	f := seedStateFixture(t)
	out, err := runSessionCmd(t, "session", "list", "--format", "text")
	require.NoError(t, err)

	lines := strings.Split(out, "\n")
	require.NotEmpty(t, lines)
	assert.Contains(t, lines[0], "STATE")
	assert.Contains(t, lines[0], "END")
	lineOf := func(harp string) string {
		for _, l := range lines {
			if strings.Contains(l, harp) {
				return l
			}
		}
		t.Fatalf("no row for %s in:\n%s", harp, out)
		return ""
	}
	endText := func(harp string) string { return sessionTime(f.endOf[harp]).String() }

	assert.Contains(t, lineOf(f.live), "live")
	assert.NotContains(t, lineOf(f.live), endText(f.live), "END is blank while live")
	assert.Contains(t, lineOf(f.ended), "ended")
	assert.Contains(t, lineOf(f.ended), endText(f.ended))
	assert.Contains(t, lineOf(f.unknown), "unknown")
	assert.Contains(t, lineOf(f.unknown), endText(f.unknown))
}

// TestSessionListAndSearch_StateFilter: --state keeps only the rows in that
// state, on both commands.
func TestSessionListAndSearch_StateFilter(t *testing.T) {
	cases := []struct {
		state string
		want  func(stateFixture) string
	}{
		{"live", func(f stateFixture) string { return f.live }},
		{"ended", func(f stateFixture) string { return f.ended }},
		{"unknown", func(f stateFixture) string { return f.unknown }},
	}
	for _, cmd := range [][]string{{"session", "list"}, {"session", "search", "lockstep"}} {
		for _, c := range cases {
			t.Run(cmd[1]+"/"+c.state, func(t *testing.T) {
				f := seedStateFixture(t)
				args := append(append([]string{}, cmd...), "--state", c.state, "--format", "json")
				out, err := runSessionCmd(t, args...)
				require.NoError(t, err)
				rows := jsonRowsByHarp(t, out)
				assert.Len(t, rows, 1, "output: %s", out)
				assert.Contains(t, rows, c.want(f))
			})
		}
	}
}

// TestSessionListAndSearch_StateFilterRefusesAnUnknownWord: a --state that is
// not one of the three is refused naming the three, not read as "match
// nothing".
func TestSessionListAndSearch_StateFilterRefusesAnUnknownWord(t *testing.T) {
	for _, cmd := range [][]string{{"session", "list"}, {"session", "search", "lockstep"}} {
		t.Run(cmd[1], func(t *testing.T) {
			seedStateFixture(t)
			_, err := runSessionCmd(t, append(append([]string{}, cmd...), "--state", "running")...)
			require.Error(t, err)
			for _, w := range []string{"running", "live", "ended", "unknown"} {
				assert.Contains(t, err.Error(), w)
			}
		})
	}
}

// TestSessionMetadataHaystack_NoEndWhileLive: search matches what the row
// shows, so a live session's blank END cannot be matched by the stale stamp.
func TestSessionMetadataHaystack_NoEndWhileLive(t *testing.T) {
	ended := time.Date(2026, 7, 17, 13, 0, 0, 0, time.UTC)
	v := operations.ViewSession(sessions.Entry{HarpName: "swift-amber-falcon", EndedAt: &ended})
	v.State = operations.SessionLive
	assert.NotContains(t, sessionMetadataHaystack(v), strings.ToLower(sessionTime(ended).String()))
	v.State = operations.SessionEnded
	assert.Contains(t, sessionMetadataHaystack(v), strings.ToLower(sessionTime(ended).String()))
}
