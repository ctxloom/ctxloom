package claude

import (
	"encoding/json"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader"
	claudecli "github.com/ctxloom/ctxloom/internal/engines/claude"
)

var _ vendorreader.RecordSpanner = Adapter{}

// timestampLine is the one field RecordSpan reads from a claude transcript
// line. Every line of a claude transcript carries a top-level "timestamp" —
// administrative lines included — the same field line.Timestamp decodes for
// conversion.
type timestampLine struct {
	Timestamp string `json:"timestamp"`
}

// RecordSpan reads every line of the claude transcript at src and returns
// the min/max "timestamp" across every line that carries a parseable one
// (vendorreader.RecordSpanner). n==0 means nothing usable was found (an empty
// file, every line missing the field, or every value unparseable) — a real
// outcome the caller treats as "this file's span cannot be determined", not
// an error. err is only an I/O failure opening or reading the file.
func (Adapter) RecordSpan(fsys afero.Fs, src string) (start, end time.Time, n int, err error) {
	lines, err := vendorreader.OpenAndReadJSONLLines(fsys, claudecli.EngineName, src)
	if err != nil {
		return time.Time{}, time.Time{}, 0, err
	}
	for _, raw := range lines {
		var l timestampLine
		if json.Unmarshal(raw, &l) != nil || l.Timestamp == "" {
			continue
		}
		ts, perr := time.Parse(time.RFC3339, l.Timestamp)
		if perr != nil {
			continue
		}
		if n == 0 || ts.Before(start) {
			start = ts
		}
		if n == 0 || ts.After(end) {
			end = ts
		}
		n++
	}
	return start, end, n, nil
}
