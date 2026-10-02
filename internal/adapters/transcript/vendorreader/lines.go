package vendorreader

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
)

// Line is one non-empty, trimmed line of a JSONL source and where it sits in
// that source. Start and End bracket the RAW line — leading whitespace and the
// terminating newline included — so End is where the next read resumes.
// Terminated is false only for a last line that has not reached its newline:
// in a vendor file being written live that line may still be mid-write, which
// is why a resumable conversion never checkpoints past one.
type Line struct {
	Bytes      []byte
	Start, End int64
	Terminated bool
}

// ReadJSONLLines splits r into non-empty, trimmed lines using an UNBOUNDED
// bufio.Reader rather than a capped bufio.Scanner, reporting each line's
// position as an offset from base (where r starts in its source). Every
// JSONL-per-session vendor store this package's adapters read (claude's
// <uuid>.jsonl) routinely carries a single line running to tens of kilobytes (a base_instructions
// blob, a large tool result/diff, an extended-thinking block, a
// PLANNER_RESPONSE's "thinking" field) — a Scanner's default token cap would
// hard-fail the ENTIRE file on the first such line, which is exactly the
// degrade-to-partial contract vendorreader.VendorAdapter's doc comment promises
// and must not violate. Mirrors agent.SessionStore.ParseSessionFile's
// identical reasoning (internal/core/agent/sessionstore.go).
//
// This is the ONE copy of the primitive. Every adapter means the same thing
// by it — "split on newlines, trim, drop empties" — so a second copy has no
// behavioral reason to exist, and a structurally different rewrite (an
// os.ReadFile+bytes.Split variant, say) that only dodges the duplicate-
// detection gate is still a second copy. A resumed read (Checkpoint) uses it
// too, which is what keeps a resumed conversion splitting lines exactly as a
// full one does.
func ReadJSONLLines(r io.Reader, base int64) ([]Line, error) {
	reader := bufio.NewReaderSize(r, 64*1024)
	var lines []Line
	off := base
	for {
		raw, err := reader.ReadBytes('\n')
		start := off
		off += int64(len(raw))
		if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 {
			lines = append(lines, Line{Bytes: trimmed, Start: start, End: off, Terminated: err == nil})
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return lines, nil
}

// LineBytes is lines' content alone, for the dispatch shell
// (ConvertJSONLLines) and the scans that take raw lines.
func LineBytes(lines []Line) [][]byte {
	out := make([][]byte, len(lines))
	for i, l := range lines {
		out[i] = l.Bytes
	}
	return out
}

// OpenAndReadJSONLLines opens the JSONL file at path and reads every line
// via ReadJSONLLines, wrapping either failure with vendor's own error
// prefix ("claude: open ...", "claude: read ...") — the "open, read, hand the
// lines to convertLines" shell every JSONL-per-session engine's Convert
// repeats verbatim once its actual line-reading delegates to
// ReadJSONLLines.
func OpenAndReadJSONLLines(vendor, path string) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: open %s: %w", vendor, path, err)
	}
	defer func() { _ = f.Close() }()

	lines, err := ReadJSONLLines(f, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: read %s: %w", vendor, path, err)
	}
	return LineBytes(lines), nil
}
