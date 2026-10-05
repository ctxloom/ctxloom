package mock

import (
	"context"
	"io/fs"
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// eventSink is an in-memory transcript.Recorder: the conversion under test
// writes nothing anywhere.
type eventSink struct{ events []agent.ChatEvent }

func (s *eventSink) Record(ev agent.ChatEvent) error {
	s.events = append(s.events, ev)
	return nil
}

func (*eventSink) Close() error { return nil }

// TestConvert_ReadsTheSourceThroughTheGivenFs: src exists only in the
// injected fs, so a conversion that reached for the disk would fail to open it.
func TestConvert_ReadsTheSourceThroughTheGivenFs(t *testing.T) {
	const src = "/mock-memfs-only/basic.jsonl"
	_, statErr := os.Stat(src)
	require.ErrorIs(t, statErr, fs.ErrNotExist, "the source must be absent from disk")

	raw, err := os.ReadFile(fixturePath(t, "basic.jsonl"))
	require.NoError(t, err)
	mem := afero.NewMemMapFs()
	testsupport.WriteFile(t, mem, src, raw, 0o644)

	sink := &eventSink{}
	require.NoError(t, Adapter{}.Convert(context.Background(), mem, sink, src))
	require.NotEmpty(t, sink.events)
}
