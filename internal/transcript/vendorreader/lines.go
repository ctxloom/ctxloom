package vendorreader

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
)

// readJSONLLines splits r into non-empty, trimmed lines using an UNBOUNDED
// bufio.Reader rather than a capped bufio.Scanner. Every JSONL-per-session
// vendor store this package's adapters read (codex's rollout-*.jsonl,
// claude's <uuid>.jsonl, antigravity's transcript_full.jsonl) routinely
// carries a single line running to tens of kilobytes (a base_instructions
// blob, a large tool result/diff, an extended-thinking block, a
// PLANNER_RESPONSE's "thinking" field) — a Scanner's default token cap would
// hard-fail the ENTIRE file on the first such line, which is exactly the
// degrade-to-partial contract vendorreader.VendorAdapter's doc comment promises
// and must not violate. Mirrors agent.SessionStore.ParseSessionFile's
// identical reasoning (internal/shared/agent/sessionstore.go).
//
// This one implementation replaces what were, before this package existed,
// THREE independently-written copies of the same primitive across
// codex/claude/antigravity — two byte-identical (codex, claude) carrying
// reprise:ignore pragmas to excuse the duplication, and antigravity's own
// os.ReadFile+bytes.Split version, structurally different for no reason
// beyond dodging the same duplicate-detection gate. There was never a real
// behavioral reason for three copies: every one of them means "split on
// newlines, trim, drop empties," so there is now exactly one.
func readJSONLLines(r io.Reader) ([][]byte, error) {
	reader := bufio.NewReaderSize(r, 64*1024)
	var lines [][]byte
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
				lines = append(lines, trimmed)
			}
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

// OpenAndReadJSONLLines opens the JSONL file at path and reads every line
// via readJSONLLines, wrapping either failure with vendor's own error
// prefix ("codex: open ...", "codex: read ...") — the "open, read, hand the
// lines to convertLines" shell every JSONL-per-session engine's Convert
// repeats verbatim once its actual line-reading delegates to
// readJSONLLines.
func OpenAndReadJSONLLines(vendor, path string) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: open %s: %w", vendor, path, err)
	}
	defer func() { _ = f.Close() }()

	lines, err := readJSONLLines(f)
	if err != nil {
		return nil, fmt.Errorf("%s: read %s: %w", vendor, path, err)
	}
	return lines, nil
}
