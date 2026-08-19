package mcp

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// canonicalRecords renders a minimal well-formed canonical transcript: a
// session record plus one user and one assistant entry, which is the floor
// loadOrDistillSession needs to get past its own "session appears to be empty"
// guard.
func canonicalRecords(harp, sessionID string) []byte {
	const engine = "claude-code"
	lines := []string{
		fmt.Sprintf(`{"v":1,"harp":%q,"session_id":%q,"engine":%q,"seq":0,"ts":"2026-08-06T21:27:20.451494924Z","kind":"session","session":{"model":"claude-opus-5"}}`, harp, sessionID, engine),
		fmt.Sprintf(`{"v":1,"harp":%q,"session_id":%q,"engine":%q,"seq":1,"ts":"2026-08-06T21:27:20.452047329Z","kind":"entry","entry":{"type":"user","content":"where did the essence go"}}`, harp, sessionID, engine),
		fmt.Sprintf(`{"v":1,"harp":%q,"session_id":%q,"engine":%q,"seq":2,"ts":"2026-08-06T21:27:20.452249935Z","kind":"entry","entry":{"type":"assistant","content":"written under one key, read under another"}}`, harp, sessionID, engine),
	}
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return []byte(out)
}

// fixedHistory reports one session, whatever is asked of it: the compactor only
// needs a session to distill, and this test is about what happens to the RESULT.
type fixedHistory struct {
	backends.NilSessionHistory
	session *agent.Session
}

func (h *fixedHistory) GetCurrentSession(string) (*agent.Session, error) { return h.session, nil }

func (h *fixedHistory) GetSession(_, _ string) (*agent.Session, error) { return h.session, nil }

// fixedBackend is the minimum agent.Backend the compactor will accept: identity
// from BaseBackend, a canned history, and a lifecycle that does nothing because
// no engine is ever launched (the LLM call goes through the mock client).
type fixedBackend struct {
	agent.BaseBackend
	history *fixedHistory
}

func (b *fixedBackend) History() agent.SessionHistory                    { return b.history }
func (b *fixedBackend) Setup(context.Context, *agent.SetupRequest) error { return nil }
func (b *fixedBackend) Cleanup(context.Context) error                    { return nil }
func (b *fixedBackend) Execute(context.Context, *agent.ExecuteRequest, io.Writer, io.Writer) (*agent.ExecuteResult, error) {
	return &agent.ExecuteResult{}, nil
}

// fixedCompactor returns a compactorFactory that distills the given session id
// to the given body without an LLM, while otherwise honouring the caller's own
// config (harp, output dir) so the essence lands exactly where production puts
// it and the staleness stamp is computed from the real transcript.
func fixedCompactor(sessionID, body string) func(memory.CompactionConfig) (*memory.Compactor, error) {
	return func(cfg memory.CompactionConfig) (*memory.Compactor, error) {
		be := &fixedBackend{
			BaseBackend: agent.NewBaseBackend("fixed", "1.0.0"),
			history: &fixedHistory{session: &agent.Session{
				ID: sessionID,
				Entries: []agent.SessionEntry{
					{Type: agent.EntryTypeUser, Content: "where did the essence go"},
					{Type: agent.EntryTypeAssistant, Content: "written under one key, read under another"},
				},
			}},
		}
		client := &pb.MockClient{
			RunFunc: func(_ context.Context, _ *pb.RunStart, stdout, _ io.Writer) (int32, error) {
				_, _ = stdout.Write([]byte(body))
				return 0, nil
			},
		}
		return memory.NewCompactor(memory.CompactionConfig{
			BackendOverride: be,
			ClientFactory:   pb.MockClientFactory(client),
			OutputDir:       cfg.OutputDir,
			HarpName:        cfg.HarpName,
		})
	}
}

// TestLoadOrDistillSession_DistillsOnceThenServesTheCache pins the end-to-end
// property recover_session's cost depends on: an essence written by one call
// must be FOUND by the next call addressing the same session the same way.
//
// The write key and the read key are chosen in different places — Compactor
// keys the essence off the session it resolved, the caller looks it up by the id
// it passed — and when those drifted apart, every /recover re-distilled an
// essence already on disk (one measured incident spent 143,878 input tokens
// doing it) while reporting nothing wrong. Neither half's own unit test sees
// that: it appears only when a real distillation is followed by a real lookup.
//
// The session is addressed throughout by a vendor UUID that looks nothing like
// its harp, because a harp-keyed id makes the two keys coincide by accident and
// the test would then pass no matter which one either side used.
func TestLoadOrDistillSession_DistillsOnceThenServesTheCache(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open("")
	require.NoError(t, err)

	projectDir := t.TempDir()
	entry, err := mgr.AssignHarp(projectDir, "claude-code")
	require.NoError(t, err)
	harp := entry.HarpName

	const vendorSessionID = "12b623a9-b883-4ded-a058-73aba1d1c53c"
	require.NotEqual(t, harp, vendorSessionID, "the ids must differ or this test proves nothing")
	require.NoError(t, mgr.BindSession(harp, vendorSessionID, ""))

	canonPath, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(canonPath), 0o755))
	require.NoError(t, os.WriteFile(canonPath, canonicalRecords(harp, vendorSessionID), 0o644))

	const body = "Distilled: the write key and the read key must agree."
	appDir := filepath.Join(projectDir, ".ctxloom")
	s := &ctxServer{
		cfg:              config.NewFixture(config.Fixture{AppDir: appDir}),
		compactorFactory: fixedCompactor(vendorSessionID, body),
	}

	_, first, err := s.loadOrDistillSession(context.Background(), vendorSessionID, "claude-code", "", policyLive)
	require.NoError(t, err, "recovery must never block the agent")
	require.NotNil(t, first)
	require.True(t, first.Loaded, "the first call must distill and load: %s", first.Message)
	assert.False(t, first.WasCached, "nothing was on disk yet, so this must be a fresh distillation")
	assert.Contains(t, first.Content, body)

	// The assertion that matters: the same request again must find what the
	// first one wrote. A miss here is invisible in production — it just quietly
	// pays for the same distillation a second time.
	_, second, err := s.loadOrDistillSession(context.Background(), vendorSessionID, "claude-code", "", policyLive)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.True(t, second.Loaded, "the second call must load: %s", second.Message)
	assert.True(t, second.WasCached,
		"the essence written by the first call must be found by the second; a miss silently re-distills")
	assert.Contains(t, second.Content, body, "and it must be the SAME essence, not a re-derived one")
}

// TestDistillSessionOnce_ReadsBackUnderTheKeyCompactWrote closes the gap that
// let the read-back key go untested: distillSessionOnce built its compactor
// inline, so nothing could drive it without a live LLM, and a mutation swapping
// the read key survived the whole package.
//
// The failure it pins is this project's characteristic shape — the distillation
// succeeds, the essence is on disk, and the caller is told "couldn't read it
// back" because it looked under a key nobody wrote. Compact resolves its own
// session and keys the essence off that; the caller must read back with the key
// Compact REPORTS, never the one it happened to pass in.
func TestDistillSessionOnce_ReadsBackUnderTheKeyCompactWrote(t *testing.T) {
	testsupport.Isolate(t)

	projectDir := t.TempDir()
	appDir := filepath.Join(projectDir, ".ctxloom")

	// A rotation's essence is filed under its harp's segments dir, which is
	// also where the read-back looks.
	const harp = "shut-hoary-yahoo"
	sessionsDir, err := paths.ResolveHarpSegmentsDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	// The compactor resolves this session to a key of its OWN choosing, which
	// differs from the id the caller passes below.
	const callerID = "12b623a9-b883-4ded-a058-73aba1d1c53c"
	const resolvedID = "shut-hoary-yahoo"
	const body = "Distilled: read back under the key that was written."

	s := &ctxServer{
		cfg:              config.NewFixture(config.Fixture{AppDir: appDir}),
		compactorFactory: fixedCompactor(resolvedID, body),
	}

	out, err := s.distillSessionOnce(context.Background(), callerID, "claude-code", "", projectDir, sessionsDir, harp)
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.True(t, out.Loaded,
		"the essence was written under %q; reading back under the caller's %q reports a successful distillation as unreadable", resolvedID, callerID)
	assert.Contains(t, out.Content, body, "the distilled body must come back, not an empty result")
	assert.False(t, out.WasCached, "this asserts the freshly-distilled path, not a cache hit")
}
