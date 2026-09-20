package operations

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
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
//   - Anything that is not a REGULAR FILE. Directories first: persist/,
//     ephemeral/, segments/ and the rest are already lifetime-classified, and
//     this predicate is about the UNclassified middle. But also sockets, FIFOs
//     and symlinks, and that half is not pedantry — ctxloom's own retired
//     agent-bus.sock sits at the harp top level on real machines, and a
//     socket is not a design note anybody can lose. It also keeps the check
//     and the mover in step at the only place they could disagree: the mover
//     cannot relocate a non-regular entry (a socket is meaningless once
//     moved, and a relative symlink breaks), so flagging one would be a
//     warning with no action behind it — the exact shape that teaches a user
//     to stop reading the report.
//   - paths.SessionSidecarFileName, paths.EssenceFileName and
//     paths.CanonicalTranscriptFileName — ctxloom's own session
//     bookkeeping, written by ctxloom at the top level by design.
//   - paths.IndexFileName and paths.MigratedIndexFileName. The retired
//     session index lived at the sessions ROOT, not inside a harp, so this
//     exclusion never fires in practice; it is kept so the predicate is safe
//     for any caller that hands it the root by mistake.
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
		// Type() comes from lstat, so a symlink is non-regular here whatever
		// it points at — the containment question a symlink raises is not one
		// this predicate should be answering.
		if !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		switch name {
		case paths.SessionSidecarFileName,
			paths.EssenceFileName,
			paths.CanonicalTranscriptFileName,
			paths.IndexFileName,
			paths.MigratedIndexFileName,
			// The reaper's exemption marker and the captured next step are
			// ctxloom's own top-level members: the reaper Lstat's the marker at
			// the top only, and memory.ReadNextStep reads the step there, so
			// sweeping either into persist/ would silently disable both.
			paths.SessionKeepMarkerFileName,
			paths.NextStepFileName:
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
// A harp the lock refused is not considered at all and contributes to
// neither, because "skipped" would imply the sweep looked at its files and
// declined them one by one.
type HarpArtifactMigration struct {
	// Moved is the number of authored files relocated into persist/.
	Moved int
	// Skipped is the number left where they were: a name persist/ already
	// holds, an entry that is not a regular file, or a rename that failed.
	Skipped int
	// RefusedHarps is the number of harp directories passed over entirely
	// because the liveness lock did not prove their session ended: it is
	// running, or nothing can prove it is not.
	RefusedHarps int
}

// MigrateHarpArtifacts moves every authored top-level file under
// sessionsRoot/<harp> into that harp's persist/ directory, which is the
// location mcp.sessionInstructions now hands to every session and the only one
// a containerized run can write through to the host.
//
// A harp is touched ONLY when its liveness lock (internal/shared/sessionlock)
// proves the session dead: the lock file exists and nothing holds it. That
// exclusion is not politeness, it is correctness: a running agent holds the
// plan file's OLD path and will write to it again, so moving the file
// mid-session does not relocate the plan, it FORKS it — half the design note
// under persist/, the rest recreated at the top level by the next edit, and
// neither copy complete. Their files are migrated by a later sweep, once the
// session has ended under the lock.
//
// THE LOCK ONLY EVER REFUSES, and the session index's EndedAt plays no part:
// a session RESUMED under its harp is running with EndedAt still set, so the
// timestamp reads a live session as ended — the destructive direction. A held
// lock refuses; so does Indeterminate (no lock file, an untrusted filesystem,
// an error), and that includes a rename, because "no lock file" is not only a
// harp from before the lock existed: a session whose Hold failed keeps running
// without one by design (AssignSessionHarp), and its plan is being written
// right now. The accepted cost is that a lock-less harp is never migrated
// until its session is run and ended under the lock; that harp stays on
// cli.doctorCheckHarpDurability's report, which is the recoverable direction.
//
// Inspect, not Acquire: this sweep deletes nothing, so it has no business
// holding the lock across its renames; the verdict is all it needs. A session
// that resumes between the probe and the rename is writing under persist/
// already, and a name it has taken there is left alone by the rule below.
//
// NEVER OVERWRITES. A top-level name that persist/ already holds is left
// exactly where it is and counted as skipped, with a warning naming both
// paths. The two files are different documents with the same name — a
// pre-migration copy and whatever the durable location has since accumulated
// — and silently clobbering one with the other would destroy authored work
// while reporting a successful migration.
//
// ONLY REGULAR FILES. A symlink, socket or FIFO at the top level is left in
// place and is not even a candidate (HarpTopLevelArtifacts excludes it):
// moving a symlink a directory deeper silently breaks it if its target is
// relative, a moved socket is meaningless, and neither is authored work at
// risk of being lost.
//
// Best-effort per harp and per file: one failure warns and the sweep
// continues, so a single unreadable directory cannot cost every other harp its
// migration.
func MigrateHarpArtifacts(sessionsRoot string) (HarpArtifactMigration, error) {
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
		harp := e.Name()
		if !isHarpDirCandidate(e, harp) {
			continue
		}
		if !sessionlock.Inspect(harp).Verdict.MayReclaim() {
			result.RefusedHarps++
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
			// HarpTopLevelArtifacts already excludes these, so reaching here
			// means the entry changed type between the listing and now. Kept
			// as a backstop rather than trusted away: this branch is the last
			// thing standing between a rename and a socket or a symlink.
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

// SweepHarpArtifacts is the startup entry point for MigrateHarpArtifacts,
// wired into both entry points (`ctxloom run` and `ctxloom mcp`) because the
// sweep must run however the session was started.
//
// It exists because repointing mcp.sessionInstructions at persist/ only fixes
// the sessions that start AFTER it; every harp already on disk keeps its
// authored files in the undurable middle, and cli.doctorCheckHarpDurability
// keeps reporting them, until something moves them. This is that something.
//
// Best-effort and silent on the all-clear path, mirroring the sibling sweeps'
// reporting shape: it reports only when it actually moved something, and a
// failure warns rather than blocking startup. Liveness comes from each harp's
// lock inside MigrateHarpArtifacts; the session index is not consulted.
func SweepHarpArtifacts(w io.Writer) {
	root, err := paths.HomeSessionsDir()
	if err != nil {
		clidiag.Warn("ctxloom", "harp artifact migration: %v", err)
		return
	}
	result, err := MigrateHarpArtifacts(root)
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
