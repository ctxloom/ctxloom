package memory

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// progressFixture builds a compactor over a mock session + mock LLM, with the
// caller's progress sink wired in.
func progressFixture(t *testing.T, progress io.Writer) *Compactor {
	t.Helper()
	testsupport.Isolate(t)
	mockHistory := &mockSessionHistory{
		currentSession: &agent.Session{
			ID: "progress-session",
			Entries: []agent.SessionEntry{
				{Type: agent.EntryTypeUser, Content: "ask"},
				{Type: agent.EntryTypeAssistant, Content: "answer"},
			},
		},
	}
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			_, _ = stdout.Write([]byte("Distilled."))
			return 0, nil
		},
	}
	c, err := NewCompactor(CompactionConfig{
		BackendOverride: &mockBackend{history: mockHistory},
		ClientFactory:   pb.MockClientFactory(mockClient),
		OutputDir:       t.TempDir(),
		HarpName:        "compactor-under-test",
		Progress:        progress,
	})
	require.NoError(t, err)
	return c
}

// TestCompact_ProgressGoesToTheInjectedSink: distillation progress is reported
// to the sink the CALLER supplied, so a caller that owns a terminal can render
// it and one that does not can discard it.
func TestCompact_ProgressGoesToTheInjectedSink(t *testing.T) {
	var mu sync.Mutex
	var sink strings.Builder
	c := progressFixture(t, &lockedWriter{mu: &mu, w: &sink})

	_, err := c.Compact(context.Background())
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, sink.String(), "distilling session",
		"distillation progress must reach the caller's sink")
}

// TestCompact_WritesNoProgressToStderr is the TUI-corruption regression. The
// host-relay handlers run IN-PROCESS inside the session-owning process (e.g.
// `ctxloom run`), whose os.Stderr is the real terminal that the harness is
// painting its interface onto. Library progress written straight to that fd
// lands at whatever cursor position happens to be current and shreds the
// display. Progress belongs to the caller's sink; the compactor writes none of
// it to the process's stderr.
func TestCompact_WritesNoProgressToStderr(t *testing.T) {
	captured := filepath.Join(t.TempDir(), "stderr.txt")
	f, err := os.Create(captured)
	require.NoError(t, err)
	defer f.Close()

	orig := os.Stderr
	os.Stderr = f
	t.Cleanup(func() { os.Stderr = orig })

	var sink strings.Builder
	c := progressFixture(t, &sink)
	_, err = c.Compact(context.Background())
	require.NoError(t, err)

	os.Stderr = orig
	require.NoError(t, f.Sync())
	got, err := os.ReadFile(captured)
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(got)),
		"a clean distillation must write nothing to the process's stderr — that fd may be a live TUI")
}

// TestCompact_NilProgressIsSilentNotFatal: the sink is optional; callers with
// nowhere safe to render progress simply omit it.
func TestCompact_NilProgressIsSilentNotFatal(t *testing.T) {
	c := progressFixture(t, nil)
	_, err := c.Compact(context.Background())
	require.NoError(t, err)
}

// lockedWriter serializes concurrent chunk progress writes for the assertion.
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
