package mock

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// EnvWakeSocket names the mock's own wake socket: the mock's interactive
// runtime listens on it and takes each line posted there exactly as a typed
// line, and the mock's wake binds to it. The mock is woken natively — never
// through another engine's tool.
const EnvWakeSocket = "CTXLOOM_MOCK_WAKE_SOCKET"

// socketWake is the mock's declared WakeSpec.
type socketWake struct{}

func (socketWake) Bind(_ context.Context, env engine.WakeEnv) (engine.Wake, error) {
	path, _ := env(EnvWakeSocket)
	if path == "" {
		return nil, fmt.Errorf("%w: no %s in this environment", engine.ErrWakeUnbound, EnvWakeSocket)
	}
	return boundSocketWake{path: path}, nil
}

type boundSocketWake struct{ path string }

// Fire posts the wake text as one line and closes.
func (w boundSocketWake) Fire(ctx context.Context, nonce string) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", w.path)
	if err != nil {
		return fmt.Errorf("mock wake: %w", err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintln(conn, engine.WakeText(nonce)); err != nil {
		return fmt.Errorf("mock wake: writing to %s: %w", w.path, err)
	}
	return nil
}

// wakeSocketName is the wake socket an interactive mock session listens on,
// in its session-private root (present.Paths.SessionHome). The name is kept
// short because a unix socket path is capped (108 bytes on linux, 104 on
// darwin) and that root nests under the ctxloom home; a path past the cap
// fails the listen at session start, loudly.
const wakeSocketName = "wake.sock"

// ListenWakes listens on the wake socket at path and hands each line posted
// to it to lines, wrapped as the caller's line type, beside the lines the
// caller reads from its terminal; nothing when path is "". A socket left by
// an earlier incarnation of the session (a resumed session reuses its harp)
// is replaced. stop closes the listener, which removes the socket file, and
// releases every reader.
func ListenWakes[T any](path string, lines chan<- T, wrap func(line string) T) (stop func(), err error) {
	if path == "" {
		return func() {}, nil
	}
	if fi, statErr := os.Lstat(path); statErr == nil && fi.Mode()&fs.ModeSocket != 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("mock: clearing a stale wake socket %s: %w", path, err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("mock: listening for wakes on %s: %w", path, err)
	}
	done := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go readWake(conn, lines, wrap, done)
		}
	}()
	return func() { close(done); _ = ln.Close() }, nil
}

// readWake hands on each line of one posted connection until it ends or the
// session does.
func readWake[T any](conn net.Conn, lines chan<- T, wrap func(string) T, done <-chan struct{}) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		select {
		case lines <- wrap(sc.Text()):
		case <-done:
			return
		}
	}
}
