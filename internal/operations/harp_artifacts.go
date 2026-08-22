package operations

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// HarpTopLevelArtifacts returns the base names — sorted — of the entries at a
// harp directory's TOP LEVEL that are AGENT-AUTHORED artifacts: design notes,
// audits, write-ups, and above all the *.plan.md documents a session is told
// to write.
//
// WHY THE TOP LEVEL IS THE WRONG PLACE, and why this predicate is shared. A
// harp directory has a declared durability split: persist/ MUST survive
// teardown, ephemeral/ is scratch. A containerized run gets persist/ bind
// mounted and NOTHING ELSE (isolation.Container.sessionStateMounts), so an
// authored file at the top level — in neither class — is written into
// container-ephemeral overlay space and is gone when the container exits. The
// write returns nil, the file is readable for the length of the run, and zero
// bytes remain afterwards.
//
// cli.doctorCheckHarpDurability REPORTS that population and
// MigrateHarpArtifacts MOVES it, and the two must agree exactly: a detector
// that flags a file the mover declines to move is a warning that can never be
// cleared, and a mover that relocates something the detector does not consider
// authored moves ctxloom's own bookkeeping out from under its readers. One
// predicate, used by both, is what makes that impossible.
//
// THE EXCLUSIONS, each for its own reason:
//
//   - Directories. persist/, ephemeral/, segments/ and the rest are already
//     lifetime-classified; this predicate is about the UNclassified middle.
//   - paths.EssenceFileName, paths.CanonicalTranscriptFileName and
//     paths.LegacyCanonicalTranscriptFileName — ctxloom's own session
//     bookkeeping, written by ctxloom at the top level by design.
//   - paths.IndexFileName. The session index lives at the sessions ROOT, not
//     inside a harp, so this exclusion never fires in practice; it is kept so
//     the predicate is safe for any caller that hands it the root by mistake.
//   - paths.EngineTranscriptLinkPrefix-prefixed leaves — one immutable
//     per-vendor-log symlink per binding, ctxloom-owned. Matched by PREFIX
//     because the leaf carries the engine name and session id.
//
// A missing directory yields no names and no error: a harp that has authored
// nothing is not a fault.
func HarpTopLevelArtifacts(harpDir string) ([]string, error) {
	entries, err := os.ReadDir(harpDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read harp dir %q: %w", harpDir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch name {
		case paths.EssenceFileName,
			paths.CanonicalTranscriptFileName,
			paths.LegacyCanonicalTranscriptFileName,
			paths.IndexFileName:
			continue
		}
		if strings.HasPrefix(name, paths.EngineTranscriptLinkPrefix) {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// HarpArtifactMigration tallies one MigrateHarpArtifacts sweep.
//
// Moved and Skipped count only files the sweep actually CONSIDERED — entries
// HarpTopLevelArtifacts named inside a harp the sweep was allowed to touch.
// A live session's harp is not considered at all and contributes to neither,
// because "skipped" would imply the sweep looked at its files and declined
// them one by one.
type HarpArtifactMigration struct {
	// Moved is the number of authored files relocated into persist/.
	Moved int
	// Skipped is the number left where they were: a name persist/ already
	// holds, an entry that is not a regular file, or a rename that failed.
	Skipped int
	// LiveHarps is the number of harp directories passed over entirely
	// because their session is still running.
	LiveHarps int
}

// MigrateHarpArtifacts moves every authored top-level file under
// sessionsRoot/<harp> into that harp's persist/ directory, which is the
// location mcp.sessionInstructions now hands to every session and the only one
// a containerized run can write through to the host.
//
// live is the set of harps whose session is still RUNNING; their directories
// are passed over untouched. That exclusion is not politeness, it is
// correctness: a running agent holds the plan file's OLD path and will write
// to it again, so moving the file mid-session does not relocate the plan, it
// FORKS it — half the design note under persist/, the rest recreated at the
// top level by the next edit, and neither copy complete. Their files are
// migrated by a later sweep, once the session has ended.
//
// NEVER OVERWRITES. A top-level name that persist/ already holds is left
// exactly where it is and counted as skipped, with a warning naming both
// paths. The two files are different documents with the same name — a
// pre-migration copy and whatever the durable location has since accumulated
// — and silently clobbering one with the other would destroy authored work
// while reporting a successful migration.
//
// NEVER FOLLOWS A SYMLINK. A symlink at the top level is left in place: moving
// one a directory deeper silently breaks it if its target is relative, and
// this sweep has no business rewriting link targets. It is counted as skipped
// so the tally, not silence, is what says the directory is not fully migrated.
//
// Best-effort per harp and per file: one failure warns and the sweep
// continues, so a single unreadable directory cannot cost every other harp its
// migration.
func MigrateHarpArtifacts(sessionsRoot string, live map[string]bool) (HarpArtifactMigration, error) {
	var result HarpArtifactMigration
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			// No sessions have ever run. Not a fault, and nothing to migrate.
			return result, nil
		}
		return result, fmt.Errorf("scan %q: %w", sessionsRoot, err)
	}
	for _, e := range entries {
		// Only directories are harps; index.yaml sits beside them at the root.
		// A symlink's DirEntry type comes from lstat, so this is already false
		// for one — stated explicitly because "never descend through a symlink
		// into somewhere outside the sessions root" is the exclusion whose
		// absence would be the serious one.
		if !e.IsDir() || e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		harp := e.Name()
		if live[harp] {
			result.LiveHarps++
			continue
		}
		harpDir := filepath.Join(sessionsRoot, harp)
		names, aerr := HarpTopLevelArtifacts(harpDir)
		if aerr != nil {
			clidiag.Warn("ctxloom", "harp artifact migration: %v", aerr)
			continue
		}
		if len(names) == 0 {
			continue
		}
		migrateOneHarp(harpDir, names, &result)
	}
	return result, nil
}

// migrateOneHarp moves one harp's named top-level files into its persist/
// directory, updating the running tally. Split out so MigrateHarpArtifacts
// reads as the harp-selection policy it is.
func migrateOneHarp(harpDir string, names []string, result *HarpArtifactMigration) {
	persistDir := filepath.Join(harpDir, paths.PersistDirName)
	if err := os.MkdirAll(persistDir, 0o755); err != nil {
		clidiag.Warn("ctxloom", "harp artifact migration: cannot create %s: %v", persistDir, err)
		result.Skipped += len(names)
		return
	}
	for _, name := range names {
		src := filepath.Join(harpDir, name)
		dst := filepath.Join(persistDir, name)
		info, err := os.Lstat(src)
		if err != nil {
			// Vanished between listing and moving — a reaped session, a
			// concurrent sweep. Nothing is there to lose.
			result.Skipped++
			continue
		}
		if !info.Mode().IsRegular() {
			clidiag.Warn("ctxloom", "harp artifact migration: %s is not a regular file, leaving it at the harp top level", src)
			result.Skipped++
			continue
		}
		if _, err := os.Lstat(dst); err == nil {
			clidiag.Warn("ctxloom", "harp artifact migration: %s already exists, so %s stays at the harp top level — the two are different documents with the same name; merge them by hand", dst, src)
			result.Skipped++
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			clidiag.Warn("ctxloom", "harp artifact migration: cannot move %s to %s: %v", src, dst, err)
			result.Skipped++
			continue
		}
		result.Moved++
	}
}

// SweepHarpArtifacts is the startup entry point for MigrateHarpArtifacts, the
// sibling of SweepOrphanedSessionHomes and wired into the same two entry
// points (`ctxloom run` and `ctxloom mcp`) for the same reason: the sweep must
// run however the session was started.
//
// It exists because repointing mcp.sessionInstructions at persist/ only fixes
// the sessions that start AFTER it; every harp already on disk keeps its
// authored files in the undurable middle, and cli.doctorCheckHarpDurability
// keeps reporting them, until something moves them. This is that something.
//
// Best-effort and silent on the all-clear path, mirroring the sibling sweeps'
// reporting shape: it reports only when it actually moved something, and a
// failure warns rather than blocking startup. NO INDEX, NO SWEEP — without the
// liveness signal every running session would look migratable, which is the
// one case that forks a live plan file in half.
func SweepHarpArtifacts(w io.Writer) {
	root, err := paths.HomeSessionsDir()
	if err != nil {
		clidiag.Warn("ctxloom", "harp artifact migration: %v", err)
		return
	}
	live, _, err := sessionLiveness()
	if err != nil {
		clidiag.Warn("ctxloom", "harp artifact migration: %v", err)
		return
	}
	result, err := MigrateHarpArtifacts(root, live)
	if err != nil {
		clidiag.Warn("ctxloom", "harp artifact migration: %v", err)
		return
	}
	if result.Moved == 0 {
		return
	}
	// Best-effort reporting on a fault-tolerant startup path; a failed write
	// is intentionally dropped (captured-but-unchecked via iox.ErrWriter),
	// matching every other startup reporter.
	ew := iox.NewErrWriter(w)
	ew.Printf("ctxloom: moved %d authored session file(s) under persist/ so a containerized run cannot lose them\n", result.Moved)
	if result.Skipped > 0 {
		ew.Printf("ctxloom: %d session file(s) stayed at their harp's top level — see the warnings above\n", result.Skipped)
	}
}
