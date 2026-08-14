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
// closing a P0 data-loss bug for claude).
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
//
// The whole read-splice-write(-or-remove) cycle runs under WithFileLock: this
// is a genuine D7 gap the fs-consolidation plan's closing verification found
// (N2) — claude.ClaudeCodeHookWriter.WriteContext, codex, and
// backends.mock_surfaces all call this on the SAME managed files (CLAUDE.md /
// AGENTS.md) C6 already locks their OTHER settings writes for
// (claude.writeSettingsFile, codex's config writers, ...), so leaving this one
// unlocked left a lock taken in one sibling call and not the next, inside the
// very same packages. See WithFileLock's own doc for the fail-closed/
// skip-for-non-OS-fs contract this inherits unchanged.
func WriteManagedContext(fs afero.Fs, path, rel, content, desc string) (report ContextReport, err error) {
	err = WithFileLock(fs, path, func() error {
		var lockedErr error
		report, lockedErr = writeManagedContextLocked(fs, path, rel, content, desc)
		return lockedErr
	})
	return report, err
}

// writeManagedContextLocked is WriteManagedContext's body, run under its
// caller's lock. Split out rather than inlined into the WithFileLock closure
// so the merge logic below reads exactly as it did before this fix — every
// return in it is a return FROM THE CLOSURE, not from WriteManagedContext
// itself, which local `return`s inside a closure sometimes hide.
func writeManagedContextLocked(fs afero.Fs, path, rel, content, desc string) (ContextReport, error) {
	existing, err := afero.ReadFile(fs, path)
	if err != nil && !os.IsNotExist(err) {
		return ContextReport{}, fmt.Errorf("failed to read %s: %w", path, err)
	}

	// Reinsert the new section at the SAME position the old one occupied
	// (between before and after) instead of always appending at the
	// end — the prior implementation stripped to before+after and then always
	// appended, so any user content that came AFTER the end marker was
	// hoisted ABOVE the re-appended section on every rewrite, violating the
	// "preserved byte-for-byte" doc comment's implied ordering.
	before, after, _ := splitManagedSection(string(existing))

	var section string
	if content != "" {
		section = ManagedContextBegin + "\n" + content + "\n" + ManagedContextEnd + "\n"
	}

	merged := before
	if section != "" {
		if merged != "" && !strings.HasSuffix(merged, "\n") {
			merged += "\n"
		}
		merged += section
	}
	merged += after

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
	before, after, _ := splitManagedSection(content)
	if before == "" {
		return after
	}
	return before + after
}

// splitManagedSection splits content around the ctxloom-managed marker
// section, returning the prefix (before the begin marker) and suffix (after
// the end marker) SEPARATELY — unlike StripManagedSection, which
// concatenates them — so WriteManagedContext can reinsert a new section at
// the same position rather than always at the end. found reports
// whether a properly terminated section was present; when it wasn't (no
// begin marker, or an unterminated one — the section is ours to own and
// drops through to EOF), before is the whole non-owned prefix and after is
// empty, matching StripManagedSection's existing unterminated-begin handling.
func splitManagedSection(content string) (before, after string, found bool) {
	begin := strings.Index(content, ManagedContextBegin)
	if begin < 0 {
		return content, "", false
	}
	rest := content[begin+len(ManagedContextBegin):]
	end := strings.Index(rest, ManagedContextEnd)
	if end < 0 {
		// Check the TRIMMED prefix for emptiness, not the untrimmed
		// one — an all-blank-lines prefix (e.g. "\n\n" before the marker) must
		// vanish entirely, not survive as a stray "\n". The prior helper
		// (ifNonEmptySuffix) tested content[:begin] untrimmed, so it appended
		// "\n" even when the trimmed result was "".
		if prefix := strings.TrimRight(content[:begin], "\n"); prefix != "" {
			return prefix + "\n", "", false
		}
		return "", "", false
	}
	after = strings.TrimLeft(rest[end+len(ManagedContextEnd):], "\n")
	before = content[:begin]
	return before, after, true
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
// context surface — claude's CLAUDE.md and codex's AGENTS.md — shares this
// exact shape, so it lives here once rather
// than as two (and counting) copy-pasted Deliver methods.
func DeliverManagedContext(w ContextWriter, dir, content string) (Delivered, error) {
	if _, err := w.WriteContext(ContextWriteRequest{ProjectDir: dir, Context: content}); err != nil {
		return nil, err
	}
	return DeliveredFunc(func() error {
		_, err := w.WriteContext(ContextWriteRequest{ProjectDir: dir, Context: ""})
		return err
	}), nil
}

// DeliverAll runs every delivery against dir in order, collecting the handles
// from any that actually deliver (a nil handle — "nothing written" — is
// skipped, not an error) into one combined Delivered whose Cleanup reverses
// all of them. Returns a nil handle if none delivered. This is how a backend
// composes TWO OR MORE independent delivery routes for the SAME cross-backend
// SurfaceKind into the single Delivery SurfaceFor must return for it (codex's
// context: a fragments-keyed hook cache file for the run/launch path, and a
// context-string-keyed native AGENTS.md write for the static materialize/init
// path — see internal/codex/surfaces.go's contextRoutes).
//
// Stops at the first error without rolling back any prior successful
// sub-delivery in this call — consistent with how a caller one level up
// (materialize's DeliverUnder) already tolerates partial delivery across
// surfaces rather than transactionally reversing them.
func DeliverAll(dir string, deliveries ...Delivery) (Delivered, error) {
	var handles []Delivered
	for _, d := range deliveries {
		h, err := d.Deliver(dir)
		if err != nil {
			return nil, err
		}
		if h != nil {
			handles = append(handles, h)
		}
	}
	if len(handles) == 0 {
		return nil, nil
	}
	return DeliveredFunc(func() error {
		var firstErr error
		for _, h := range handles {
			if err := h.Cleanup(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}), nil
}

// ComposedDelivery composes multiple Deliveries that share ONE cross-backend
// SurfaceKind into a single KindedDelivery, via DeliverAll. A backend uses
// this when it has more than one independent thing to write for the same
// kind (codex's context: a fragments-keyed hook cache file for the run/launch
// path, plus a context-string-keyed native AGENTS.md write for the static
// materialize/init path) — SurfaceFor names EXACTLY one Delivery per kind
// (cells.go's SurfaceSelection.Build calls it once per selected kind), so the
// parts must be composed into one value rather than registered separately
// (the map type cannot express one key, two values).
type ComposedDelivery struct {
	Parts       []Delivery
	Info        string
	SurfaceKind SurfaceKind
}

// Deliver runs every part via DeliverAll.
func (c ComposedDelivery) Deliver(dir string) (Delivered, error) {
	return DeliverAll(dir, c.Parts...)
}

// UnsafeInfo returns the composed surface's identity for the DeliverShared
// fallback's warning.
func (c ComposedDelivery) UnsafeInfo() string { return c.Info }

// Kind reports the composed surface's cross-backend kind.
func (c ComposedDelivery) Kind() SurfaceKind { return c.SurfaceKind }

// Compile-time contract: ComposedDelivery is a KindedDelivery.
var _ KindedDelivery = ComposedDelivery{}
