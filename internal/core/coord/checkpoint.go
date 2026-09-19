package coord

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// D4 — CHECKPOINT compaction (pre-made semantics, Wave D playbook): a
// SCOPE_CHECKPOINT Summary (agent_report, covers_through_seq) is the
// natural compaction point — the reporting agent has just synthesized
// everything up to a plane-1 seq into resumable text, so the coordinator
// takes the opportunity to snapshot the items fold's state (counts/chars/
// maxSeq — items.go) to a checked file alongside the journal it summarizes.
// A later coordinator restart loads the snapshot and replays only the
// TAIL of items.jsonl (openStoreFromOffset, journal.go) instead of
// re-parsing the whole file. Pre-made: the snapshot file lands now; journal
// TRUNCATION stays deferred to Wave E's retention decision — this is purely
// an additive replay shortcut, never a data-loss risk (a missing, stale, or
// corrupt snapshot just falls back to a full replay from byte 0).

// itemsSnapshotFileName is the checked-in-state (not checked-in-git) file
// name, alongside items.jsonl in the coordinator's state dir.
const itemsSnapshotFileName = "items-snapshot.json"

// itemsSnapshotPath is the snapshot file's path for a coordinator state dir.
func itemsSnapshotPath(stateDir string) string {
	return filepath.Join(stateDir, itemsSnapshotFileName)
}

// writeItemsSnapshot takes the items fold's current state (under its own
// journal's View window, so it is consistent with the recorded offset) and
// persists it atomically (temp file + rename, so a crash mid-write never
// leaves a torn snapshot the loader would need to detect). Best-effort: a
// failure warns — the journal remains the source of truth regardless.
func (c *Coordinator) writeItemsSnapshot() {
	// Offset and fold state MUST be read atomically (OffsetView),
	// not as Offset() followed by a separate View() — a concurrent Exec
	// landing in that gap would tag a snapshot of NEWER state with an OLDER
	// offset, and a later tail replay from that offset would re-apply facts
	// the snapshot already counted.
	var snap itemsSnapshot
	_, err := c.items.OffsetView(func(offset int64) { snap = c.itemsF.snapshot(offset) })
	if err != nil {
		clidiag.Warn("ctxloom", "coordinator: checkpoint snapshot: read items journal offset: %v", err)
		return
	}

	raw, err := json.Marshal(snap)
	if err != nil {
		clidiag.Warn("ctxloom", "coordinator: checkpoint snapshot: marshal: %v", err)
		return
	}
	// Route through iox: a fixed temp name (path + ".tmp") gives two
	// coordinators checkpointing the same stateDir one shared temp path, and
	// without an fsync a power loss can persist the rename ahead of the data.
	// iox.WriteFileAtomic owns that invariant.
	if err := iox.WriteFileAtomic(itemsSnapshotPath(c.stateDir), raw, 0o600); err != nil {
		clidiag.Warn("ctxloom", "coordinator: checkpoint snapshot: write: %v", err)
	}
}

// loadItemsSnapshot reads a prior checkpoint snapshot for stateDir. A
// missing file is the normal (no checkpoint yet) case, reported via ok=false
// with NO warning; any other read failure (EISDIR, EACCES, EIO) and a
// present-but-corrupt file both warn and also report ok=false — either way
// the caller falls back to a full replay from 0, safe by construction.
//
// Only ErrNotExist is silent: it is the one case that says nothing is wrong.
// Every other error means a checkpoint that EXISTS is being ignored, and
// because compaction is a performance contract rather than a functional one,
// that failure has no symptom of its own — the coordinator just replays the
// whole journal on every boot, permanently and quietly.
func loadItemsSnapshot(stateDir string) (snap itemsSnapshot, ok bool) {
	path := itemsSnapshotPath(stateDir)
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			clidiag.Warn("ctxloom", "coordinator: checkpoint snapshot %s unreadable, falling back to a full replay: %v", path, err)
		}
		return itemsSnapshot{}, false
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		clidiag.Warn("ctxloom", "coordinator: checkpoint snapshot unreadable, falling back to a full replay: %v", err)
		return itemsSnapshot{}, false
	}
	return snap, true
}

// maybeCheckpointOnSummary triggers the snapshot write when s is a
// SCOPE_CHECKPOINT report — called from recordSummary AFTER the summary
// fact itself is durably journaled (the checkpoint's own record must exist
// before the snapshot that references it as the compaction point).
func (c *Coordinator) maybeCheckpointOnSummary(s *agentcoordpb.Summary) {
	if s.GetScope() != agentcoordpb.Summary_SCOPE_CHECKPOINT {
		return
	}
	c.writeItemsSnapshot()
}
