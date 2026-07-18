// Package claude implements importer.VendorAdapter for claude-code's own
// interactive-TUI transcript store: ~/.claude/projects/<slug>/<uuid>.jsonl.
// It copies the shape of internal/transcript/importer/codex, the reference
// adapter (docs/transcript-schema.md §8, tough-cloud-writer-a): a small
// envelope/payload type set, a two-pass Convert (session metadata first, then
// a streamed entry pass), and a colocated fixture test runnable in total
// isolation from the rest of the module.
//
// Unlike codex's rollout-*.jsonl, claude's file carries NO outer envelope —
// each line IS the record, discriminated by its own top-level "type" (user |
// assistant | progress | queue-operation | system | ...; docs/
// transcript-schema.md §2b). This adapter only extracts "user" and
// "assistant" lines; every other type is administrative UI/session state
// (hook progress notices, queue bookkeeping, slash-command/title/mode
// bookkeeping, turn-duration telemetry) with no conversational content of
// its own, skipped exactly like an unrecognized response_item variant is
// skipped in codex.
//
// The one bug this package exists to fix is NOT in this file: it lives one
// layer up, in the (not-yet-rebuilt) locate step that turns a cwd into this
// file's path (the deleted reader's cwd→directory-slug re-encoding landing on
// the wrong filename, docs/transcript-schema.md §2b "tall-grab"). Convert
// here takes the transcript path directly, exactly like codex's Convert — it
// has no cwd to get wrong.
package claude

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/ctxloom/ctxloom/internal/transcript"
	"github.com/ctxloom/ctxloom/internal/transcript/importer"
)

// Adapter implements importer.VendorAdapter for claude's
// ~/.claude/projects/<slug>/<uuid>.jsonl store. Stateless: every field
// Convert needs lives in the per-call converter it constructs, so one
// Adapter value is safe to reuse (or share as a package var) across
// concurrent Convert calls for different files.
type Adapter struct{}

var _ importer.VendorAdapter = Adapter{}

// New returns a claude Adapter. A plain Adapter{} literal works identically —
// this exists only so callers outside the package don't need to know the
// type is a zero-size struct they can construct directly.
func New() Adapter { return Adapter{} }

// Convert reads the claude transcript JSONL file at src and appends its
// conversation to rec in the file's own order. See importer.VendorAdapter's
// doc comment for the general contract (malformed lines skipped, not fatal;
// a rec.Record failure or ctx cancellation IS fatal). Deliberately mirrors
// codex.Adapter.Convert's shape (open, readLines, convertLines):
// importer.VendorAdapter's own doc comment requires every vendor package to
// be independently triggerable with no sibling-package dependency
// (adapter.go), so this cannot call codex's copy instead of having its own —
// the duplication is the design, not an oversight.
// reprise:ignore — intentional structural mirror of codex.Adapter.Convert, per the above.
func (Adapter) Convert(ctx context.Context, rec transcript.Recorder, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("claude: open %s: %w", src, err)
	}
	defer func() { _ = f.Close() }()

	lines, err := readLines(f)
	if err != nil {
		return fmt.Errorf("claude: read %s: %w", src, err)
	}
	return convertLines(ctx, rec, lines)
}

// readLines splits r into non-empty, trimmed lines using an UNBOUNDED
// bufio.Reader rather than a capped bufio.Scanner — a claude transcript line
// routinely carries tens of kilobytes of prose (a large tool result, a long
// skill/context injection, an extended-thinking block), and a Scanner's
// default token cap would hard-fail the ENTIRE file on the first such line.
// Identical reasoning and identical implementation to codex's readLines
// (internal/transcript/importer/codex/codex.go) and
// agent.SessionStore.ParseSessionFile — copied rather than shared because
// importer.VendorAdapter's contract is that each vendor package is
// independently triggerable with no sibling-package dependency.
// reprise:ignore — see Convert's pragma above; same intentional-duplication rationale.
func readLines(r io.Reader) ([][]byte, error) {
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
