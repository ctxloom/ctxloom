package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"

	"github.com/spf13/afero"
)

// Managed-section markers frame ctxloom-owned content inside a file a human may
// also hand-edit (CLAUDE.md, MOCK_CONTEXT.md, …). Content
// BETWEEN the markers is ctxloom-owned and reconciled on every apply; content
// OUTSIDE them is the user's and is preserved byte-for-byte. This is the shared
// merge core every ContextWriter that owns a human-editable file merges
// through, rather than each backend reimplementing the merge — a per-backend
// merge that overwrote the whole file was a data-loss bug.
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
// project dir); desc labels the write for safefs.WriteFileKeepMode's error messages.
//
// Idempotent: applying the same content twice produces byte-identical output —
// the second write reads back its own markers, strips them, and reinserts the
// same section.
//
// The whole read-splice-write(-or-remove) cycle runs under WithFileLock:
// claude.ClaudeCodeHookWriter.WriteContext and mock's context writer call this
// from the same packages whose OTHER settings writes are locked
// (ClaudeCodeHookWriter.writeSettingsFile), so leaving this one unlocked would
// take a lock in one sibling call and not the next. See WithFileLock's own doc for the fail-closed/
// skip-for-non-OS-fs contract this inherits unchanged.
func WriteManagedContext(fs afero.Fs, path, rel, content, desc string) (report ContextReport, err error) {
	err = sessions.WithFileLock(fs, path, func() error {
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
	if err := safefs.WriteFileKeepMode(fs, path, []byte(merged), desc); err != nil {
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

// SurfacePersistsAfterExit is the Delivered handle for a PROJECT SURFACE: its
// reversal is deliberately a no-op, so the surface stays on disk when the run
// ends. Startup reconciles it; `ctxloom clean` and `ctxloom manage uninstall`
// are the commands that remove it.
//
// WHY, and it is not merely that exit-removal was redundant. Exit cleanup NEVER
// RUNS on SIGKILL, on a crash, or on a container stop, so leftover surfaces
// have to be tolerated regardless — which makes startup reconciliation
// load-bearing whatever else is true. Keeping exit-removal as well added no
// safety; it made post-session state depend on HOW THE PROCESS ENDED. Sometimes
// the context file was there afterwards, sometimes not, and nothing said which.
//
// It also collapses a confusion that cost real time: two writers shared one
// path with opposite lifecycles. A run wrote at launch and removed at its
// end (ephemeral); materialize wrote and never removed (persistent). Same file, two
// ownership models, and nothing on the file to say which had produced it.
//
// THIS IS FOR PROJECT SURFACES ONLY. Per-session SCRATCH keeps its teardown and
// must not be given this handle: the session tree is TierLocal so `clean` never
// touches it by design, its ephemeral subdirectory is not its own paths.Layout
// entry so clean cannot see it even in principle, and startup does not
// reconcile it because a new session means a new harp and a new directory.
// Give scratch this handle and it accumulates forever with nothing reaping it.
var SurfacePersistsAfterExit Delivered = DeliveredFunc(func() error { return nil })

// DeliverManagedContext is the shared Delivery.Deliver shape for a
// ContextWriter that owns a human-editable managed-marker file: write content,
// then wrap the reversal (re-writing with empty content, which strips the
// managed section) in a Delivered handle. Every native-file ContextWriter
// context surface shares this exact shape, so it lives here once rather than
// as copy-pasted Deliver methods.
func DeliverManagedContext(w ContextWriter, dir, content string) (Delivered, error) {
	if _, err := w.WriteContext(ContextWriteRequest{ProjectDir: dir, Context: content}); err != nil {
		return nil, err
	}
	return SurfacePersistsAfterExit, nil
}
