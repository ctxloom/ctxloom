package testenv

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// castLines splits a recording into its header and its events.
func castLines(t *testing.T, raw string) (header castHeader, events [][3]any) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("header %q is not JSON: %v", lines[0], err)
	}
	for _, l := range lines[1:] {
		var ev [3]any
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatalf("event %q is not a [t, code, data] triple: %v", l, err)
		}
		events = append(events, ev)
	}
	return header, events
}

// The header is asciicast v2: version 2, the size and the title given, a real
// start time, and no event before the first write.
func TestNewCast_WritesAV2Header(t *testing.T) {
	var buf bytes.Buffer
	NewCast(&buf, 100, 30, "my scenario")

	header, events := castLines(t, buf.String())
	if header.Version != 2 || header.Width != 100 || header.Height != 30 || header.Title != "my scenario" {
		t.Fatalf("header = %+v", header)
	}
	if header.Timestamp <= 0 {
		t.Fatalf("Timestamp = %d, want a unix time", header.Timestamp)
	}
	if len(events) != 0 {
		t.Fatalf("%d events before any write", len(events))
	}
}

// Write keeps the bytes exactly (a pty's own "\r\n"); WritePipe supplies the
// carriage return a pipe's bare "\n" lacks; Marker is an "m" event.
func TestCast_EventShapes(t *testing.T) {
	var buf bytes.Buffer
	c := NewCast(&buf, 80, 24, "t")
	_, _ = c.Write([]byte("pty\r\nline"))
	_, _ = c.WritePipe([]byte("one\ntwo\n"))
	_ = c.Marker("Given a step")

	_, events := castLines(t, buf.String())
	want := [][2]string{{"o", "pty\r\nline"}, {"o", "one\r\ntwo\r\n"}, {"m", "Given a step"}}
	if len(events) != len(want) {
		t.Fatalf("got %d events, want %d", len(events), len(want))
	}
	for i, w := range want {
		if events[i][1] != w[0] || events[i][2] != w[1] {
			t.Errorf("event %d = (%v, %q), want (%s, %q)", i, events[i][1], events[i][2], w[0], w[1])
		}
	}
}

// Prompt echoes a command line as a reader would type it: argv[0] by its base
// name, and a word that needs quoting quoted.
func TestCast_PromptEchoesTheCommandLine(t *testing.T) {
	var buf bytes.Buffer
	c := NewCast(&buf, 80, 24, "t")
	_ = c.Prompt([]string{"/tmp/build/ctxloom", "search", "two words", "it's"})

	_, events := castLines(t, buf.String())
	if got, want := events[0][2], `$ ctxloom search 'two words' 'it'\''s'`+"\r\n"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

// Timestamps never go backwards, and concurrent writers (a run's stdout and
// stderr copiers) each land as one whole event — a torn line would fail to
// parse in castLines.
func TestCast_ConcurrentWritesStayWholeAndOrdered(t *testing.T) {
	var buf bytes.Buffer
	c := NewCast(&buf, 80, 24, "t")
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				_, _ = c.Write([]byte("x"))
			}
		}()
	}
	wg.Wait()

	_, events := castLines(t, buf.String())
	if len(events) != 160 {
		t.Fatalf("got %d events, want 160", len(events))
	}
	prev := 0.0
	for i, ev := range events {
		ts := ev[0].(float64)
		if ts < prev {
			t.Fatalf("event %d at %v precedes the one before it at %v", i, ts, prev)
		}
		prev = ts
	}
}

// RecordInto makes Exec an extra destination, never a substitute: the run's
// command line and its output reach the recording, and the history still
// records exactly what it would without one. RecordInto(nil) stops it.
func TestRunHistory_RecordInto_TeesARunIntoTheCast(t *testing.T) {
	var h RunHistory
	var buf bytes.Buffer
	h.RecordInto(NewCast(&buf, 80, 24, "t"))

	if err := h.Exec(sandboxedCommand(t, "sh", "-c", "printf 'out\\n'; printf 'err\\n' >&2")); err != nil {
		t.Fatal(err)
	}
	h.RecordInto(nil)
	if err := h.Exec(sandboxedCommand(t, "sh", "-c", "echo after")); err != nil {
		t.Fatal(err)
	}

	if h.NthLastOutput(1) != "out\nerr\n" || h.LastStdout() != "after\n" {
		t.Fatalf("history changed by the cast: %q, %q", h.NthLastOutput(1), h.LastStdout())
	}
	_, events := castLines(t, buf.String())
	var data []string
	for _, ev := range events {
		data = append(data, ev[2].(string))
	}
	got := strings.Join(data, "")
	if !strings.HasPrefix(got, "$ sh -c ") {
		t.Fatalf("recording does not open with the command line: %q", got)
	}
	for _, want := range []string{"out\r\n", "err\r\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("recording lacks %q: %q", want, got)
		}
	}
	if strings.Contains(got, "after") {
		t.Errorf("a run after RecordInto(nil) was recorded: %q", got)
	}
}
