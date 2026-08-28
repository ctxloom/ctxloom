// Package ledger records which entries at a location ctxloom owns, so a writer
// can update its own content and leave everything else untouched.
//
// WHY THIS IS ONE SHARED PACKAGE AND NOT A METHOD ON A WRITER. Five
// independently-owned implementations of this idea existed before it — one per
// engine plus the file-set variant — and they drifted: five marker filenames,
// two of which encoded a real invariant and three of which were arbitrary. One
// of them carried a `reprise:accept-drift` waiver naming another as
// "similarly-shaped but independently owned". Every engine needs this, so it
// lives where an engine can reach it without owning it.
//
// WHY A LEDGER RATHER THAN A BACKUP. A writer that cannot tell its own content
// from the user's must rewrite the file wholesale and keep a copy in case it
// was wrong. That is what ctxloom did, and it is how `manage hooks install`
// replaced a settings block containing ctxloom's own hooks from a previous run
// — the backup made the loss recoverable by luck rather than preventing it. A
// ledger removes the need for the copy: content nobody claims is never touched.
//
// THE CO-LOCATION INVARIANT, which is load-bearing. Two surfaces can share one
// directory — kiro writes COMMANDS and SKILLS into the same
// skills dir — and before this package they depended on two separate manifest
// FILENAMES so one surface's cleanup could not delete the other's files. One
// marker with SURFACE-TYPED entries preserves that separation without
// preserving five filenames. Every read and write here is surface-scoped for
// that reason, and an untyped line is adopted by no surface at all.
package ledger

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// Name is the ONE marker filename, for every engine and every surface. It is
// deliberately a single literal: the surface lives in the entries, not in the
// filename, so `internal/gitignore` needs one pattern rather than a glob.
const Name = ".ctxloom-managed"

// fieldSep separates an identifier from its surface. Identifiers can originate
// in remote bundle content, so a name containing it is refused at write rather
// than allowed to forge a surface field.
const fieldSep = "\t"

// Surface distinguishes co-located managed sets, so two writers sharing one
// directory never clean up each other's content. See the package doc.
//
// THE TYPE IS OPEN, DELIBERATELY. Any string is a valid Surface. A plugin, a
// new engine, or a surface nobody has thought of yet declares its own constant
// and starts using it — no registration, no enum to extend here, and no
// release of this package required. Do NOT add validation against a known set:
// closing it would mean a plugin's entries either fail to record or, far worse,
// fall back to some default surface and hand an unrelated writer deletion
// rights over the plugin's files. Reading an unrecognised surface is a no-op
// for everyone but its owner, which is exactly the desired behaviour.
//
// The constants below are the surfaces ctxloom itself writes. They are
// conveniences and a naming example, NOT an exhaustive set.
//
// NAMING, since the identifier is the whole coordination mechanism: a surface
// string is a claim on cleanup rights inside a directory. Two writers choosing
// the same string will delete each other's entries, so anything outside
// ctxloom's own surfaces should namespace itself (e.g. "acme.linter") rather
// than take a bare word that a future built-in might also want.
type Surface string

const (
	SurfaceMCP      Surface = "mcp"
	SurfaceCommands Surface = "commands"
	SurfaceSkills   Surface = "skills"
	SurfaceHooks    Surface = "hooks"
	SurfaceContext  Surface = "context"
	// SurfacePermissions records permission entries ctxloom merged into an
	// engine's config (Claude Code's permissions.deny). Without it those
	// entries are a ONE-WAY leak: the merge appends, and nothing can ever
	// withdraw an entry ctxloom stops declaring, because nothing recorded that
	// ctxloom put it there rather than the user.
	SurfacePermissions Surface = "permissions"
	// SurfaceStatusLine records the statusline command ctxloom installed, so a
	// later run updates or withdraws ITS OWN and never the user's.
	SurfaceStatusLine Surface = "statusline"
)

// Valid reports whether s can be recorded. It checks the ONLY two things that
// would corrupt the file rather than merely being unfamiliar: a surface must be
// non-empty, and it must not contain the field or line separator. It is
// emphatically NOT a membership test against the constants above — see the
// Surface doc.
func (s Surface) Valid() bool {
	t := strings.TrimSpace(string(s))
	return t != "" && !strings.ContainsAny(t, fieldSep+"\n")
}

// Ledger is the persisted set of identifiers ctxloom owns at one location:
// entry names inside a shared config file (MCP server names), or relative
// paths inside a shared directory (command and skill files).
type Ledger struct {
	// FS is the writer's filesystem, already default-resolved.
	FS afero.Fs
	// Dir is the location whose contents this ledger describes. The marker is
	// written inside it.
	Dir string
	// Warn reports two things, neither of which fails the operation: a
	// malformed entry on read (a corrupt line is a reason to skip that line
	// loudly, not to refuse the whole ledger and strand every entry it
	// correctly records), and a net RETRACTION on write (see Write). Optional
	// — a nil Warn silences both.
	Warn func(format string, args ...any)
}

// Path is the marker file this ledger reads and writes.
func (l Ledger) Path() string {
	return filepath.Join(l.Dir, Name)
}

// Read returns the identifiers this surface owns here, in stable order. A
// missing marker is the legitimate "nothing managed yet" case and returns
// (nil, nil); any OTHER read error is returned rather than flattened into it,
// because a writer that mistakes an unreadable ledger for an empty one
// concludes it manages nothing and orphans every entry it wrote last time.
func (l Ledger) Read(s Surface) ([]string, error) {
	all, err := l.readAll()
	if err != nil {
		return nil, err
	}
	return all[s], nil
}

// Write replaces this surface's entries, leaving every other surface's
// untouched, and rewrites the marker atomically so a crash cannot leave a torn
// ledger that silently orphans managed content.
//
// THE CALLER MUST HOLD A LOCK ACROSS ITS WHOLE READ-MODIFY-WRITE. This is a
// read-modify-write of the WHOLE marker (readAll, replace one surface,
// rewrite every surface) and it does not serialize itself. Atomicity here
// prevents a TORN marker; it does nothing about writer B reading the marker
// before writer A's rename lands and then rewriting it without A's surface.
// MEASURED: 20 concurrent unserialized Writes to 20 different surfaces lose
// 11 to 15 of them, and lose ZERO once serialized — see
// TestLedger_IsNotSelfSerializing_TheCallerMustLock.
//
// The lock is deliberately NOT here. Every production caller already wraps
// its whole load-modify-save-and-ledger-write cycle in agent.WithFileLock,
// keyed on the settings file it is editing (claude, codex, opencode,
// agent.MCPFileConfig), and tests/arch/lock_discipline_test.go excludes this
// package from its scan for exactly that reason: this is the primitive, and
// "their own callers are what must hold the lock". A second lock in here
// would be a second idiom over the same file with no way to make the two
// agree.
//
// The cost of that placement, stated so nobody has to rediscover it: the
// caller's lock is keyed on the SETTINGS FILE, not on this marker. Two
// callers holding two different settings-file locks while writing
// CO-LOCATED surfaces into one marker do not exclude each other — which is
// the same directory-sharing case the co-location invariant in this package's
// doc exists to protect. A lost update there leaves the surviving marker
// claiming fewer entries than ctxloom actually wrote, so the next cleanup
// treats the lost surface's files as content nobody claims and orphans them.
//
// The marker is removed only when EVERY surface is empty: removing it because
// one surface emptied would strand a co-located surface's entries.
func (l Ledger) Write(s Surface, names []string) error {
	// The surface is caller-supplied and the type is open, so a plugin's value
	// is untrusted input exactly like a name is: one carrying the separator
	// could forge a second field and claim another surface's entries.
	if !s.Valid() {
		return fmt.Errorf("ledger: refusing to write surface %q: a surface must be non-empty and free of field and line separators", s)
	}
	for _, n := range names {
		if strings.ContainsAny(n, fieldSep+"\n") {
			return fmt.Errorf("ledger: refusing to record %q for surface %q: it contains a field or line separator and could forge another surface's entry", n, s)
		}
	}
	all, err := l.readAll()
	if err != nil {
		return err
	}
	// Losing entries without regaining them is a RETRACTION: what ctxloom
	// wrote to this surface last round is gone from the user's disk now. It is
	// legitimate — reconciling to the declared state is what this ledger is
	// for — but it is destructive and would otherwise be silent, so it says so.
	//
	// Only the NET loss warns. The writers remove-then-readd on every run, so
	// warning on any removal would fire on every launch and carry no signal.
	if dropped := retracted(all[s], names); len(dropped) > 0 {
		l.warn("ctxloom retracted %d %s entr%s no longer configured: %s",
			len(dropped), s, plural(len(dropped)), strings.Join(dropped, ", "))
	}
	if len(names) == 0 {
		delete(all, s)
	} else {
		all[s] = dedupeSorted(names)
	}
	if len(all) == 0 {
		exists, eerr := afero.Exists(l.FS, l.Path())
		if eerr != nil {
			return eerr
		}
		if exists {
			return l.FS.Remove(l.Path())
		}
		return nil
	}
	return iox.WriteFileAtomicFs(l.FS, l.Path(), render(all), 0o644)
}

// readAll parses the marker into its per-surface sets. Surfaces with no
// entries are absent from the map, so len(all)==0 means "nothing is managed
// here" — the condition Write keys the marker's removal off.
func (l Ledger) readAll() (map[Surface][]string, error) {
	out := map[Surface][]string{}
	data, err := afero.ReadFile(l.FS, l.Path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return nil, fmt.Errorf("ledger: read %s: %w", l.Path(), err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		name, surface, ok := strings.Cut(line, fieldSep)
		// An untyped line belongs to NO surface. Adopting it into whichever
		// surface read first would hand that surface deletion rights over
		// another's files — the data-loss shape this package exists to remove.
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(surface) == "" {
			l.warn("ledger: skipping malformed entry %q in %s: expected \"<name>\\t<surface>\"", line, l.Path())
			continue
		}
		s := Surface(strings.TrimSpace(surface))
		out[s] = append(out[s], strings.TrimSpace(name))
	}
	return out, nil
}

func (l Ledger) warn(format string, args ...any) {
	if l.Warn != nil {
		l.Warn(format, args...)
	}
}

// render serialises every surface in a stable order, so the same managed set
// produces identical bytes on every run and a materialize does not churn the
// file (or a user's git diff).
func render(all map[Surface][]string) []byte {
	surfaces := make([]string, 0, len(all))
	for s := range all {
		surfaces = append(surfaces, string(s))
	}
	sort.Strings(surfaces)

	var b strings.Builder
	for _, s := range surfaces {
		for _, name := range all[Surface(s)] {
			b.WriteString(name)
			b.WriteString(fieldSep)
			b.WriteString(s)
			b.WriteString("\n")
		}
	}
	return []byte(b.String())
}

// retracted returns the entries prev holds that next does not, in stable
// order. It is the ledger's whole notion of "ctxloom stopped managing this":
// a set difference over the two sets Write already has in hand, so detecting a
// retraction costs no extra read.
func retracted(prev, next []string) []string {
	if len(prev) == 0 {
		return nil
	}
	keep := make(map[string]bool, len(next))
	for _, n := range next {
		keep[n] = true
	}
	var out []string
	for _, p := range prev {
		if !keep[p] {
			out = append(out, p)
		}
	}
	return dedupeSorted(out)
}

// plural is the suffix that makes the retraction warning read correctly for a
// count of one, which is the common case.
func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

func dedupeSorted(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
