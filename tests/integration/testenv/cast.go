package testenv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CastCols and CastRows size the terminal a scenario recording declares. One
// size for a whole recording: a scenario that mixes pipe runs with a pty run
// records them into one stream, which cannot change size mid-way.
const (
	CastCols = 100
	CastRows = 24
)

// castHeader is line 1 of an asciicast v2 file
// (https://docs.asciinema.org/manual/asciicast/v2/): the terminal size, the
// recording's start time, and its title. v2, which every asciicast reader
// accepts.
type castHeader struct {
	Version   int    `json:"version"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Timestamp int64  `json:"timestamp"`
	Title     string `json:"title"`
}

// Cast writes an asciicast v2 recording: the header line, then one
// [seconds, code, data] event line per Write, WritePipe or Marker call,
// timestamped against the moment the header was written, so the events are
// in time order by construction.
//
// Safe for concurrent use: a run's stdout and stderr are copied into the same
// Cast by two goroutines at once.
type Cast struct {
	mu    sync.Mutex
	dest  io.Writer
	start time.Time
	err   error // first write error, sticky; every later call is a no-op
}

// NewCast writes the v2 header (cols x rows, title, now as t=0) to w and
// returns the Cast recording into it.
func NewCast(w io.Writer, cols, rows int, title string) *Cast {
	c := &Cast{dest: w, start: time.Now()}
	c.writeLine(castHeader{Version: 2, Width: cols, Height: rows, Timestamp: c.start.Unix(), Title: title})
	return c
}

// writeLine marshals v as one line, recording the first error. Callers hold
// c.mu, or own c exclusively (NewCast).
func (c *Cast) writeLine(v any) {
	if c.err != nil {
		return
	}
	line, err := json.Marshal(v)
	if err != nil {
		c.err = err
		return
	}
	_, c.err = fmt.Fprintf(c.dest, "%s\n", line)
}

func (c *Cast) event(code, data string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeLine([3]any{time.Since(c.start).Seconds(), code, data})
	return c.err
}

// Write records p as one "o" (output) event, byte for byte: bytes that
// already carry a terminal's own line endings, as a pty's do. Output read
// from a pipe goes through WritePipe instead.
func (c *Cast) Write(p []byte) (int, error) {
	if err := c.event("o", string(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// WritePipe records p as one "o" event with every "\n" written as "\r\n".
// A pipe carries bare newlines, and a player renders event data as raw
// terminal bytes, where "\n" alone moves down without returning to column 0:
// piped output left as-is staircases across the screen. A pty's bytes already
// carry "\r\n" and must use Write, or they come out "\r\r\n" — which is why
// the two are separate methods rather than a guess at which kind arrived.
func (c *Cast) WritePipe(p []byte) (int, error) {
	if err := c.event("o", string(bytes.ReplaceAll(p, []byte("\n"), []byte("\r\n")))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Marker records label as an "m" (marker) event: a named point in the
// recording, which a player offers as a place to jump to. The acceptance
// suite marks each Gherkin step.
func (c *Cast) Marker(label string) error {
	return c.event("m", label)
}

// Prompt records the command line a run is about to execute, as a shell
// would echo it ("$ ctxloom profile list"), so the recording shows which
// command produced the output after it. argv[0] is shown by its base name.
func (c *Cast) Prompt(argv []string) error {
	words := make([]string, 0, len(argv))
	for i, a := range argv {
		if i == 0 {
			a = filepath.Base(a)
		}
		words = append(words, shellWord(a))
	}
	_, err := c.Write([]byte("$ " + strings.Join(words, " ") + "\r\n"))
	return err
}

// shellWord quotes a for display the way a reader would type it: bare when
// it needs no quoting, single-quoted otherwise.
func shellWord(a string) string {
	if a != "" && !strings.ContainsAny(a, " \t\n'\"\\$`*?[]{}()<>|&;#~!") {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

// Err reports the first error Cast met writing its recording, if any.
func (c *Cast) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// pipeWriter adapts WritePipe to io.Writer, so a piped child's stdout and
// stderr can be teed into the Cast with io.MultiWriter without making
// WritePipe the Cast's own io.Writer (where a pty caller could pick it by
// mistake).
type pipeWriter struct{ cast *Cast }

func (w pipeWriter) Write(p []byte) (int, error) { return w.cast.WritePipe(p) }
