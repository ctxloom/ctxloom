// Package codex implements importer.VendorAdapter for codex CLI's own
// transcript store: ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl. It is the
// REFERENCE adapter (docs/transcript-schema.md §8, tough-cloud-writer-a) —
// every other engine's importer (kiro/claude/antigravity) copies this
// package's shape: a small envelope/payload type set, a two-pass Convert
// (session metadata first, then a streamed entry pass), and a colocated
// fixture test runnable in total isolation from the rest of the module.
//
// The one bug this package exists to fix (docs/transcript-schema.md §2b/§8,
// "the confirmed break (lived-zone)"): codex's rollout file is an ENVELOPE,
// {timestamp, type, payload}, with every real field nested under payload —
// the deleted reader (internal/codex/capabilities.go, removed in tough-cloud
// S5) declared a FLAT struct with type/role/content/tool_name at the top
// level, so every real field silently decoded to its zero value and the
// reader returned a zero-entry session for every real rollout file it ever
// read. See rollout.go's rolloutLine doc comment for the fix.
package codex

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

// Adapter implements importer.VendorAdapter for codex's rollout-*.jsonl store.
// Stateless: every field Convert needs lives in the per-call converter it
// constructs, so one Adapter value is safe to reuse (or share as a package
// var) across concurrent Convert calls for different files.
type Adapter struct{}

var _ importer.VendorAdapter = Adapter{}

// New returns a codex Adapter. A plain Adapter{} literal works identically —
// this exists only so callers outside the package don't need to know the
// type is a zero-size struct they can construct directly.
func New() Adapter { return Adapter{} }

// Convert reads the codex rollout-*.jsonl file at src and appends its
// conversation to rec in the file's own order. See importer.VendorAdapter's
// doc comment for the general contract (malformed lines skipped, not fatal;
// a rec.Record failure or ctx cancellation IS fatal).
func (Adapter) Convert(ctx context.Context, rec transcript.Recorder, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("codex: open %s: %w", src, err)
	}
	defer func() { _ = f.Close() }()

	lines, err := readLines(f)
	if err != nil {
		return fmt.Errorf("codex: read %s: %w", src, err)
	}
	return convertLines(ctx, rec, lines)
}

// readLines splits r into non-empty, trimmed lines using an UNBOUNDED
// bufio.Reader rather than a capped bufio.Scanner — a rollout file routinely
// carries a single JSONL line tens of kilobytes long (codex's own
// base_instructions blob alone runs past 20KB on this box, and a large tool
// result or diff can run much larger). A Scanner's default token cap would
// hard-fail the ENTIRE file on the first such line, which is exactly the
// degrade-to-partial contract this package must not violate. Mirrors
// agent.SessionStore.ParseSessionFile's identical reasoning
// (internal/shared/agent/sessionstore.go).
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
