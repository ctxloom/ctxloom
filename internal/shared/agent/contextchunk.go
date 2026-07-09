package agent

import "strings"

// ContextChunkMaxChars bounds each SessionStart context chunk.
//
// Claude Code caps a single hook's additionalContext at ~10,000 chars: output
// above that is persisted to a file and only a ~2KB preview reaches the model,
// so a large assembled profile injected as one block never actually loads. We
// keep a safety margin well under the cap and split large context across
// multiple ordered SessionStart hooks (see NewContextInjectionChunkHook and
// AwaitTurn), each emitting one sub-cap chunk.
const ContextChunkMaxChars = 7500

// contextSectionSep is the separator WriteContextFile uses to join fragments.
// We split on the same boundary so a chunk never cuts through the middle of a
// section (which would scatter a markdown construct across two separately
// framed hook blocks).
const contextSectionSep = "\n\n---\n\n"

// ChunkContext splits assembled context into ordered pieces, each at most
// ContextChunkMaxChars, preferring whole-section boundaries. A single section
// larger than the limit is line-split as a fallback (never mid-line). Content
// that already fits returns a single chunk; empty content returns no chunks.
//
// Deterministic by construction: the hook assembler calls this to decide how
// many chunk hooks to emit, and the hook itself calls it to select chunk k.
// Because the context file is content-addressed (immutable), both sides see
// identical input and therefore agree on the split — no write-time/run-time
// drift.
func ChunkContext(content string) []string {
	if content == "" {
		return nil
	}
	if len(content) <= ContextChunkMaxChars {
		return []string{content}
	}

	sections := strings.Split(content, contextSectionSep)
	var chunks []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
	}

	for _, sec := range sections {
		// A section that alone exceeds the limit can't be packed; flush the
		// buffer first (to preserve order) then line-split it on its own.
		if len(sec) > ContextChunkMaxChars {
			flush()
			chunks = append(chunks, splitOversizedSection(sec)...)
			continue
		}

		// Would appending this section (plus the rejoin separator) overflow the
		// current chunk? If so, start a fresh chunk.
		addLen := len(sec)
		if cur.Len() > 0 {
			addLen += len(contextSectionSep)
		}
		if cur.Len() > 0 && cur.Len()+addLen > ContextChunkMaxChars {
			flush()
		}
		if cur.Len() > 0 {
			cur.WriteString(contextSectionSep)
		}
		cur.WriteString(sec)
	}
	flush()
	return chunks
}

// splitOversizedSection line-splits a single section that exceeds the chunk
// limit, never breaking a line. A lone line longer than the limit is emitted
// whole (we never cut mid-line) — the harness will persist that one over-cap
// chunk, which is strictly better than corrupting the section's markdown.
func splitOversizedSection(sec string) []string {
	lines := strings.Split(sec, "\n")
	var chunks []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
	}
	for _, ln := range lines {
		// A single line longer than the cap is emitted whole (we never cut
		// mid-line). That one chunk will exceed ContextChunkMaxChars and the
		// harness will persist it instead of injecting it inline — warn so the
		// truncation is diagnosable rather than silent.
		if len(ln) > ContextChunkMaxChars {
			Warn("context chunk: single line of %d chars exceeds the %d-char cap; it will be emitted whole and the harness may truncate it", len(ln), ContextChunkMaxChars)
		}
		addLen := len(ln)
		if cur.Len() > 0 {
			addLen++ // rejoin newline
		}
		if cur.Len() > 0 && cur.Len()+addLen > ContextChunkMaxChars {
			flush()
		}
		if cur.Len() > 0 {
			cur.WriteString("\n")
		}
		cur.WriteString(ln)
	}
	flush()
	return chunks
}
