package mock

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"

	"github.com/ctxloom/ctxloom/internal/engines/kit"
)

// EnvWakeSocket names the mock's own wake socket: the mock's interactive
// runtime listens on it and takes each line posted there exactly as a typed
// line, and the mock's wake binds to it. The mock is woken natively — never
// through another engine's tool.
const EnvWakeSocket = "CTXLOOM_MOCK_WAKE_SOCKET"

// wake is the mock's declared WakeSpec: its own socket.
var wake = kit.SocketWake{EnvVar: EnvWakeSocket}

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
