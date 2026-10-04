package operations

import (
	"context"
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The shipped engines keep no transcript store of their own: ctxloom's
// canonical capture is the only transcript. These tests pin what every reader
// does for a harp that has NO canonical capture — bound only by an engine
// session id, or only by a located vendor transcript path — for each shipped
// engine: resume and distill refuse, scrollback degrades to live-only, and a
// source resolves sessions only from canonical capture.
var shippedEngines = []string{"claude-code", "mock"}

// boundWithoutCapture mints a harp for eng bound to an engine session id (and
// optionally a vendor transcript path) with no canonical transcript.
func boundWithoutCapture(t *testing.T, eng, transcriptPath string) *sessions.Entry {
	t.Helper()
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	minted, err := mgr.AssignHarp("/proj", eng)
	require.NoError(t, err)
	require.NoError(t, mgr.BindSession(minted.HarpName, "engine-native-id", transcriptPath))
	entry, err := mgr.Find(minted.HarpName)
	require.NoError(t, err)
	require.NotNil(t, entry)
	require.Empty(t, entry.CanonicalTranscriptPath, "fixture wants no canonical capture")
	return entry
}

func TestRecordedSessionEntries_WithoutCanonicalCaptureRefuses(t *testing.T) {
	for _, eng := range shippedEngines {
		t.Run(eng, func(t *testing.T) {
			testsupport.Isolate(t)
			entry := boundWithoutCapture(t, eng, "")
			entries, err := RecordedSessionEntries(context.Background(), engines.Registry(), entry.HarpName)
			require.Error(t, err)
			assert.Nil(t, entries)
		})
	}
}

func TestRecordedSessionEntries_UnreadableCanonicalCaptureRefuses(t *testing.T) {
	for _, eng := range shippedEngines {
		t.Run(eng, func(t *testing.T) {
			testsupport.Isolate(t)
			mgr, err := sessions.Open(nil)
			require.NoError(t, err)
			minted, err := mgr.AssignHarp("/proj", eng)
			require.NoError(t, err)
			rec, err := transcript.NewRecorder(afero.NewOsFs(), minted.HarpName, eng)
			require.NoError(t, err)
			require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeUser, Content: "q"}}))
			require.NoError(t, rec.Close())
			require.NoError(t, mgr.BindSession(minted.HarpName, "engine-native-id", ""))
			entry, err := mgr.Find(minted.HarpName)
			require.NoError(t, err)
			require.NotEmpty(t, entry.CanonicalTranscriptPath)
			require.NoError(t, os.WriteFile(entry.CanonicalTranscriptPath, []byte("{not json\n"), 0o600))

			entries, err := RecordedSessionEntries(context.Background(), engines.Registry(), entry.HarpName)
			require.Error(t, err)
			assert.Nil(t, entries)
		})
	}
}

func TestDistillable_VendorPathWithoutCanonicalCaptureRefuses(t *testing.T) {
	testsupport.Isolate(t)
	require.Error(t, distillable(&sessions.Entry{
		HarpName:       "vexed-scary-gab",
		TranscriptPath: "/nonexistent/vendor/transcript.jsonl",
	}))
}

func TestFeedScrollback_WithoutCanonicalCaptureIsLiveOnly(t *testing.T) {
	for _, eng := range shippedEngines {
		for name, entry := range map[string]*sessions.Entry{
			"session id":  {HarpName: "vexed-scary-gab", SessionID: "engine-native-id", ProjectDir: "/proj"},
			"vendor path": {HarpName: "vexed-scary-gab", TranscriptPath: "/nonexistent/vendor/transcript.jsonl"},
		} {
			t.Run(eng+"/"+name, func(t *testing.T) {
				testsupport.Isolate(t)
				assert.Nil(t, feedScrollback(entry))
			})
		}
	}
}

func TestWatchStoreFeed_WithoutCanonicalCaptureRefuses(t *testing.T) {
	for name, entry := range map[string]*sessions.Entry{
		"session id":  {HarpName: "vexed-scary-gab", SessionID: "engine-native-id", ProjectDir: "/proj"},
		"vendor path": {HarpName: "vexed-scary-gab", TranscriptPath: "/nonexistent/vendor/transcript.jsonl"},
	} {
		t.Run(name, func(t *testing.T) {
			testsupport.Isolate(t)
			feed, err := watchStoreFeed(context.Background(), entry)
			require.Error(t, err)
			assert.Nil(t, feed)
		})
	}
}

func TestSessionSources_ServeOnlyCanonicalCapture(t *testing.T) {
	for _, eng := range shippedEngines {
		t.Run(eng, func(t *testing.T) {
			home := testsupport.Isolate(t)
			ctx := context.Background()

			resolved, _, err := ResolveSessionSource(engines.Registry(), &config.Config{}, eng, home)
			require.NoError(t, err)
			distill, err := distillSource(home)
			require.NoError(t, err)

			for name, src := range map[string]interface {
				GetSession(context.Context, string) (*agent.Session, error)
				CurrentSession(context.Context) (*agent.Session, error)
			}{"resolved": resolved, "distill": distill} {
				_, err := src.GetSession(ctx, "engine-native-id")
				assert.Error(t, err, "%s: an uncaptured session is not found", name)
				cur, err := src.CurrentSession(ctx)
				assert.NoError(t, err, name)
				assert.Nil(t, cur, "%s: a project with no capture has no current session", name)
			}
			metas, err := resolved.ListSessions(ctx)
			require.NoError(t, err)
			assert.Empty(t, metas)
		})
	}
}
