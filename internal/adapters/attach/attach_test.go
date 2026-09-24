package attach

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStart_TheRunCLIIsOnAPtyAndKillTearsDownByName: the container's
// attach is the runtime CLI (`docker run -i -t …`) started on a pty this
// process holds — its stdio IS the container's tty, so what the runner
// prints reaches the master and what is typed reaches the runner. Kill
// removes the container by name FIRST (the CLI's death alone would leave
// the daemon's container running) and then ends the CLI.
func TestStart_TheRunCLIIsOnAPtyAndKillTearsDownByName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var order []string
	remove := func(<-chan struct{}) { order = append(order, "remove") }
	// A stand-in for the runtime CLI: it proves it is on a tty, echoes one
	// line, then parks like an attached container does.
	s, err := Start(ctx, exec.Command("sh", "-c", "[ -t 0 ] && echo ATTACHED; read line; echo got:$line; sleep 30"), "ctr-1", remove)
	require.NoError(t, err)
	out := &lockedBuffer{}
	drained := make(chan struct{})
	go func() { defer close(drained); _, _ = io.Copy(out, s.Master()) }()
	_, err = io.WriteString(s.Master(), "hello\n")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "got:hello") }, 5*time.Second, 10*time.Millisecond)
	assert.Contains(t, out.String(), "ATTACHED")

	s.Kill()
	order = append(order, "killed")
	<-drained
	assert.Equal(t, []string{"remove", "killed"}, order, "the container is removed by name before the CLI is ended")
	assert.Equal(t, "ctr-1", s.Name())
}

// lockedBuffer is a goroutine-safe io.Writer: the pty copier and the
// assertion read it from different goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestStart_WaitReapsTheCLIWithoutTouchingTheContainer: a CLI that exits
// on its own (the container ended) is reaped by Wait; the teardown by name
// is Kill's alone.
func TestStart_WaitReapsTheCLIWithoutTouchingTheContainer(t *testing.T) {
	removed := false
	s, err := Start(context.Background(), exec.Command("sh", "-c", "exit 3"), "ctr-2", func(<-chan struct{}) { removed = true })
	require.NoError(t, err)
	code, err := s.Wait()
	require.NoError(t, err)
	assert.Equal(t, 3, code)
	assert.False(t, removed, "Wait reaps; it does not remove — the container ended on its own")
}
