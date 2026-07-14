package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
)

// Managed-section markers frame ctxloom-owned content inside a file a human may
// also hand-edit (CLAUDE.md, .agents/AGENTS.md, codex's AGENTS.md, …). Content
// BETWEEN the markers is ctxloom-owned and reconciled on every apply; content
// OUTSIDE them is the user's and is preserved byte-for-byte. This is the shared
// merge core every ContextWriter that owns a human-editable file merges
// through, rather than each backend reimplementing the merge (originally
// written for antigravity's .agents/AGENTS.md; ported here so claude's
// CLAUDE.md and codex's AGENTS.md share one implementation instead of three —
// see taskloom lanky-plop, the P0 data-loss bug this closes for claude).
const (
	ManagedContextBegin = "<!-- ctxloom:context:begin (managed — do not edit between markers) -->"
	ManagedContextEnd   = "<!-- ctxloom:context:end -->"
)

// WriteManagedContext merges content into the ctxloom-managed marker section of
// the file at path, preserving any content outside the markers BYTE-FOR-BYTE.
// Empty content strips the managed section; if nothing user-authored remains,
// the file itself is removed (never left as an empty husk) — and if the file
// didn't exist to begin with, nothing is created. rel is the caller-relative
// path reported in the returned ContextReport (typically relative to the
// project dir); desc labels the write for AtomicWriteFile's error messages.
//
// Idempotent: applying the same content twice produces byte-identical output —
// the second write reads back its own markers, strips them, and reinserts the
// same section.
func WriteManagedContext(fs afero.Fs, path, rel, content, desc string) (ContextReport, error) {
	existing, err := afero.ReadFile(fs, path)
	if err != nil && !os.IsNotExist(err) {
		return ContextReport{}, fmt.Errorf("failed to read %s: %w", path, err)
	}

	userContent := StripManagedSection(string(existing))

	var section string
	if content != "" {
		section = ManagedContextBegin + "\n" + content + "\n" + ManagedContextEnd + "\n"
	}

	merged := userContent
	if section != "" {
		if merged != "" && !strings.HasSuffix(merged, "\n") {
			merged += "\n"
		}
		merged += section
	}

	if strings.TrimSpace(merged) == "" {
		// Nothing left: remove the file if it exists, never create it.
		if exists, _ := afero.Exists(fs, path); exists {
			if err := fs.Remove(path); err != nil {
				return ContextReport{}, err
			}
		}
		return ContextReport{Removed: []string{rel}}, nil
	}

	if err := fs.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return ContextReport{}, fmt.Errorf("failed to create %s directory: %w", filepath.Dir(path), err)
	}
	if err := AtomicWriteFile(fs, path, []byte(merged), desc); err != nil {
		return ContextReport{}, err
	}
	return ContextReport{Wrote: []string{rel}}, nil
}

// StripManagedSection returns content with the ctxloom-managed marker section
// removed. Content outside the markers is untouched; an unterminated begin
// marker drops through to the end of the file (the section is ours to own).
func StripManagedSection(content string) string {
	begin := strings.Index(content, ManagedContextBegin)
	if begin < 0 {
		return content
	}
	rest := content[begin+len(ManagedContextBegin):]
	end := strings.Index(rest, ManagedContextEnd)
	if end < 0 {
		return strings.TrimRight(content[:begin], "\n") + ifNonEmptySuffix(content[:begin], "\n")
	}
	after := strings.TrimLeft(rest[end+len(ManagedContextEnd):], "\n")
	before := content[:begin]
	if before == "" {
		return after
	}
	return before + after
}

// ifNonEmptySuffix returns suffix when s is non-empty, else "".
func ifNonEmptySuffix(s, suffix string) string {
	if s == "" {
		return ""
	}
	return suffix
}

// DeliveredFunc adapts a cleanup closure to Delivered, for a Delivery whose
// reversal is a single function call.
type DeliveredFunc func() error

// Cleanup runs the wrapped cleanup closure.
func (f DeliveredFunc) Cleanup() error { return f() }

// DeliverManagedContext is the shared Delivery.Deliver shape for a
// ContextWriter that owns a human-editable managed-marker file: write content,
// then wrap the reversal (re-writing with empty content, which strips the
// managed section) in a Delivered handle. Every native-file ContextWriter
// context surface — antigravity's .agents/AGENTS.md, claude's CLAUDE.md, and
// codex's AGENTS.md — shares this exact shape, so it lives here once rather
// than as three (and counting) copy-pasted Deliver methods.
func DeliverManagedContext(w ContextWriter, dir, content string) (Delivered, error) {
	if _, err := w.WriteContext(ContextWriteRequest{ProjectDir: dir, Context: content}); err != nil {
		return nil, err
	}
	return DeliveredFunc(func() error {
		_, err := w.WriteContext(ContextWriteRequest{ProjectDir: dir, Context: ""})
		return err
	}), nil
}
