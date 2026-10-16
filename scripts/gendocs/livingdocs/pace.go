package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// ErrMalformedCast is the sentinel PublishCast wraps every parse failure in,
// so a caller can distinguish "not a v2 cast at all" from any other error
// with errors.Is rather than matching on message text.
var ErrMalformedCast = errors.New("livingdocs: malformed asciicast")

// Pacing controls how PublishCast re-times a raw capture for a human
// audience. Every field is a duration the paced timeline advances by at one
// specific kind of event; see PublishCast for exactly when each applies.
type Pacing struct {
	LeadIn       time.Duration // blank terminal before the first event; nothing is stamped at t=0, which a player treats as already elapsed
	Narrate      time.Duration // dwell after a narration line before the step's first command
	TypeChar     time.Duration // per character of a "$ ctxloom …" prompt line, so it appears typed
	BeforeOutput time.Duration // pause between the typed prompt and its first output
	DwellMin     time.Duration // minimum pause after a command's output before the next event
	DwellPerLine time.Duration // added per output line, capped by DwellMax
	DwellMax     time.Duration
}

// ReadablePacing is the pacing PublishCast uses for the copy every generated
// page embeds — tuned by watching one rendered scenario, not measured.
var ReadablePacing = Pacing{
	LeadIn:       500 * time.Millisecond,
	Narrate:      1200 * time.Millisecond,
	TypeChar:     35 * time.Millisecond,
	BeforeOutput: 300 * time.Millisecond,
	DwellMin:     800 * time.Millisecond,
	DwellPerLine: 60 * time.Millisecond,
	DwellMax:     4 * time.Second,
}

// castEvent is one parsed [t, code, data] line.
type castEvent struct {
	t    float64
	code string
	data string
}

// parseCast splits raw asciicast v2 bytes into its header (as a generic
// object, so unrecognized fields still round-trip) and its event lines.
func parseCast(raw []byte) (map[string]any, []castEvent, error) {
	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n"))
	if len(lines) == 0 || len(bytes.TrimSpace(lines[0])) == 0 {
		return nil, nil, fmt.Errorf("%w: no header line", ErrMalformedCast)
	}

	header := map[string]any{}
	if err := json.Unmarshal(lines[0], &header); err != nil {
		return nil, nil, fmt.Errorf("%w: header: %v", ErrMalformedCast, err)
	}

	var events []castEvent
	for _, line := range lines[1:] {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var raw [3]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			return nil, nil, fmt.Errorf("%w: event %q: %v", ErrMalformedCast, line, err)
		}
		var e castEvent
		if err := json.Unmarshal(raw[0], &e.t); err != nil {
			return nil, nil, fmt.Errorf("%w: event %q: time: %v", ErrMalformedCast, line, err)
		}
		if err := json.Unmarshal(raw[1], &e.code); err != nil {
			return nil, nil, fmt.Errorf("%w: event %q: code: %v", ErrMalformedCast, line, err)
		}
		if err := json.Unmarshal(raw[2], &e.data); err != nil {
			return nil, nil, fmt.Errorf("%w: event %q: data: %v", ErrMalformedCast, line, err)
		}
		events = append(events, e)
	}
	return header, events, nil
}

// countLines estimates how many terminal lines s renders as, for scaling the
// post-output dwell -- an exact line count is not the point (this is
// presentation timing, not measurement), only that more output earns a
// longer pause.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// isPromptLine reports whether an "o" event is the synthesized "$ ctxloom …"
// command line (testenv.Cast.Prompt), as opposed to a command's real
// output.
func isPromptLine(data string) bool {
	return strings.HasPrefix(data, "$ ")
}

// PublishCast rewrites a raw evidence cast into a presentation copy paced by
// p: a real acceptance run finishes a whole scenario in well under a second
// (median measured 0.10s), too fast for a player to show anything
// readable, and its markers land in the stream with no visible narration
// naming the step they belong to. The evidence copy in the capture directory
// keeps its real, measured timing unchanged -- this function only ever
// touches the copy published for a reader, discarding every real gap between
// events and replacing it with p's fixed dwells; nothing here is evidence of
// how long ctxloom actually took.
//
// Per marker: a dim narration line ("# <label>") is inserted immediately
// before it as its own "o" event, then the clock advances by p.Narrate. Per
// prompt line (an "o" event starting "$ "): it is split into one "o" event
// per character, spaced p.TypeChar apart, so it appears typed, then the
// clock advances by p.BeforeOutput. Every other "o" event is real command
// output: consecutive output events between two markers/prompts are all
// stamped at the SAME time (so multiple writes from one command never each
// pay their own pause) and the clock advances once, by
// p.DwellMin + p.DwellPerLine × (total lines in that run), capped at
// p.DwellMax, right before the next marker, prompt, or the end of the
// stream.
func PublishCast(raw []byte, p Pacing) ([]byte, error) {
	header, events, err := parseCast(raw)
	if err != nil {
		return nil, err
	}
	header["version"] = 2

	var buf bytes.Buffer
	headerBytes, err := json.Marshal(header)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal header: %v", ErrMalformedCast, err)
	}
	buf.Write(headerBytes)
	buf.WriteByte('\n')

	cur := p.LeadIn.Seconds()
	firstEmitted := true
	haveRun := false
	runLines := 0

	writeEvent := func(t float64, code, data string) error {
		// Millisecond precision: the pacing is in whole milliseconds, and
		// summing float seconds otherwise writes 5.4750000000000005.
		line, err := json.Marshal([3]any{math.Round(t*1000) / 1000, code, data})
		if err != nil {
			return fmt.Errorf("%w: marshal event: %v", ErrMalformedCast, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
		firstEmitted = false
		return nil
	}

	flushRun := func() {
		if !haveRun {
			return
		}
		dwell := p.DwellMin + p.DwellPerLine*time.Duration(runLines)
		if dwell > p.DwellMax {
			dwell = p.DwellMax
		}
		cur += dwell.Seconds()
		haveRun = false
		runLines = 0
	}

	for _, e := range events {
		switch {
		case e.code == "m":
			flushRun()
			prefix := "\r\n"
			if firstEmitted {
				prefix = ""
			}
			narration := prefix + "\x1b[2m# " + e.data + "\x1b[0m\r\n"
			if err := writeEvent(cur, "o", narration); err != nil {
				return nil, err
			}
			if err := writeEvent(cur, "m", e.data); err != nil {
				return nil, err
			}
			cur += p.Narrate.Seconds()

		case e.code == "o" && isPromptLine(e.data):
			flushRun()
			for _, r := range e.data {
				if err := writeEvent(cur, "o", string(r)); err != nil {
					return nil, err
				}
				cur += p.TypeChar.Seconds()
			}
			cur += p.BeforeOutput.Seconds()

		case e.code == "o":
			if err := writeEvent(cur, "o", e.data); err != nil {
				return nil, err
			}
			runLines += countLines(e.data)
			haveRun = true

		default:
			return nil, fmt.Errorf("%w: unsupported event code %q", ErrMalformedCast, e.code)
		}
	}
	flushRun()

	return buf.Bytes(), nil
}
