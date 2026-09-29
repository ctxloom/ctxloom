package iox

import (
	"errors"
	"fmt"
	"os"
)

// ErrNotAFIFO: OpenFIFOWriter was pointed at something other than a named
// pipe.
var ErrNotAFIFO = errors.New("not a named pipe")

// OpenFIFOWriter opens an existing named pipe for writing, blocking until a
// reader opens the other end. It is the one sanctioned way to write into a
// FIFO: a pipe holds nothing on disk, so the atomic write-then-rename every
// file write goes through does not apply — and it refuses anything that is
// not a FIFO, so it can never become an unsanctioned file write.
func OpenFIFOWriter(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return nil, fmt.Errorf("%s: %w", path, ErrNotAFIFO)
	}
	return os.OpenFile(path, os.O_WRONLY, 0)
}
