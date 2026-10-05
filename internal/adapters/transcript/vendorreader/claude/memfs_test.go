package claude

import (
	"context"
	"io/fs"
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader"
	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// eventSink is an in-memory transcript.Recorder: the conversion under test
// writes nothing anywhere.
type eventSink struct{ events []agent.ChatEvent }

func (s *eventSink) Record(ev agent.ChatEvent) error {
	s.events = append(s.events, ev)
	return nil
}

func (*eventSink) Close() error { return nil }

// TestConvertFrom_ReadsTheSourceThroughTheGivenFs: src exists only in the
// injected fs, so a conversion that reached for the disk would fail to open
// it — for a full Convert and for a resumable ConvertFrom alike.
func TestConvertFrom_ReadsTheSourceThroughTheGivenFs(t *testing.T) {
	const src = "/claude-memfs-only/session.jsonl"
	_, statErr := os.Stat(src)
	require.ErrorIs(t, statErr, fs.ErrNotExist, "the source must be absent from disk")

	raw, err := os.ReadFile(fixturePath(t, "transcript-fixture.jsonl"))
	require.NoError(t, err)
	mem := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(mem, src, raw, 0o644))

	full := &eventSink{}
	require.NoError(t, Adapter{}.Convert(context.Background(), mem, full, src))
	require.NotEmpty(t, full.events)

	var checkpoints int
	resumed := &eventSink{}
	require.NoError(t, Adapter{}.ConvertFrom(context.Background(), mem, resumed, src, vendorreader.Checkpoint{},
		func(vendorreader.Checkpoint) error { checkpoints++; return nil }))
	require.Equal(t, full.events, resumed.events)
	require.Equal(t, 1, checkpoints)
}
