package sessions

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/harp"
	"github.com/ctxloom/ctxloom/internal/shared/lockwait"
	"github.com/ctxloom/ctxloom/internal/shared/textutil"
)

// IsSessionDir is THE predicate for "this entry under the sessions root is a
// session": a real directory (never a symlink, in either direction — a
// sweep that followed one would act on whatever it points at), whose name
// is a usable harp, and which carries the sidecar. Every walker over the
// root — the listing here, the reaper, doctor — must answer the question
// through this one function, or they will disagree about what exists.
//
// A directory WITHOUT the sidecar is not a session. That is what
// `session remove` (purge, then Forget) leaves behind on purpose, and what an
// engine-created directory nobody ever recorded looks like; neither is
// invented into a listing.
func IsSessionDir(root string, e fs.DirEntry) bool {
	if e.Type()&fs.ModeSymlink != 0 || !e.IsDir() {
		return false
	}
	if harp.Validate(e.Name()) != nil {
		return false
	}
	info, err := os.Lstat(filepath.Join(root, e.Name(), paths.SessionSidecarFileName))
	return err == nil && info.Mode().IsRegular()
}

// MigrationReport says what one MigrateIndex pass did, by harp.
type MigrationReport struct {
	// Migrated names the harps whose sidecar this pass wrote from an index
	// row.
	Migrated []string
	// AlreadyPresent names the rows that already had a sidecar when this
	// pass reached them — an earlier, interrupted pass wrote it, or the
	// session was minted after the index stopped being written. Their
	// sidecars are left exactly as found.
	AlreadyPresent []string
}

// legacyIndex is the shape of the retired global index file, read here and
// nowhere else.
type legacyIndex struct {
	Sessions []legacyRow `yaml:"sessions"`
}

// legacyRow is one index.yaml row: an Entry plus the harp name the index had
// to carry because it lived outside the directory.
type legacyRow struct {
	HarpName string `yaml:"harp_name"`
	Entry    `yaml:",inline"`
}

// MigrateIndex turns a not-yet-consumed index.yaml at root into per-session
// sidecars, once. It is safe to call on every launch:
//
//   - No index.yaml, or an index.yaml beside a MigratedIndexFileName marker:
//     nothing to do. The marker is what makes a LATER index.yaml — an older
//     binary wrote one — stale rather than a second source; it is ignored,
//     never re-imported.
//   - Otherwise, under the root's index lock: every row whose harp is a
//     usable name gets its directory (minted if absent — a row is never
//     lost for want of a place to live) and its sidecar, written ONLY if
//     none exists — an interrupted earlier pass, or a session written since,
//     is never overwritten. Then index.yaml is renamed to the marker name,
//     which both records completion and keeps the consumed file as a manual
//     fallback.
//
// Rows are the migration's whole scope: a directory the index never
// recorded is not turned into a session, and nothing outside the root is
// looked at.
func MigrateIndex(root string) (*MigrationReport, error) {
	report := &MigrationReport{}
	indexPath := filepath.Join(root, paths.IndexFileName)
	if _, err := os.Stat(indexPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return report, nil
		}
		return nil, fmt.Errorf("inspect %s: %w", indexPath, err)
	}
	markerPath := filepath.Join(root, paths.MigratedIndexFileName)
	if _, err := os.Stat(markerPath); err == nil {
		return report, nil // already consumed; whatever index.yaml is now, it is stale
	}

	lockPath := paths.PathFor(indexPath)
	fl := flock.New(lockPath, flock.SetPermissions(lockFileMode))
	stop := lockwait.Watch(lockPath)
	lockErr := fl.Lock()
	stop()
	if lockErr != nil {
		return nil, fmt.Errorf("lock %s: %w", indexPath, lockErr)
	}
	defer func() { _ = fl.Unlock() }()

	// Re-check under the lock: a concurrent launch may have finished the
	// migration while this one waited.
	if _, err := os.Stat(markerPath); err == nil {
		return report, nil
	}
	data, err := os.ReadFile(indexPath)
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", indexPath, err)
	}

	var idx legacyIndex
	if len(strings.TrimSpace(string(data))) > 0 {
		// Older on-disk encodings (legacy timestamp formats) are normalized
		// in memory before parsing, exactly as the index loader used to.
		data, _ = indexUpgrades.Run(data)
		if err := yaml.Unmarshal(data, &idx); err != nil {
			return nil, fmt.Errorf("parse %s: %w", indexPath, err)
		}
	}

	m := &Manager{root: root}
	for _, row := range idx.Sessions {
		if err := harp.Validate(row.HarpName); err != nil {
			clidiag.Warn("ctxloom", "session index migration: row skipped: %v", err)
			continue
		}
		sidecar := filepath.Join(root, row.HarpName, paths.SessionSidecarFileName)
		if _, err := os.Lstat(sidecar); err == nil {
			report.AlreadyPresent = append(report.AlreadyPresent, row.HarpName)
			continue
		}
		e := row.Entry
		if err := m.writeSidecar(row.HarpName, &e); err != nil {
			return nil, fmt.Errorf("session index migration: %s: %w", row.HarpName, err)
		}
		report.Migrated = append(report.Migrated, row.HarpName)
	}
	if err := os.Rename(indexPath, markerPath); err != nil {
		return nil, fmt.Errorf("session index migration: retire %s: %w", indexPath, err)
	}
	return report, nil
}

// essenceFrontmatter is the slice of essence.md's frontmatter a listing
// needs. The file is written by internal/memory (distilledMeta); only the
// summary is read back here.
type essenceFrontmatter struct {
	Summary string `yaml:"summary"`
}

// fillFromEssence derives Summary and Detail from the harp's essence.md:
// the frontmatter's summary line and the leading bullets of the body's Open
// Items section. A missing or malformed essence leaves both empty — a
// never-distilled session simply has no summary.
func fillFromEssence(e *Entry) {
	if e == nil || e.HarpName == "" {
		return
	}
	p, err := paths.HarpEssencePath(e.HarpName)
	if err != nil {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return
	}
	var fm essenceFrontmatter
	if err := yaml.Unmarshal([]byte(rest[:end+1]), &fm); err != nil {
		return
	}
	e.Summary = FirstLineSummary(fm.Summary)
	e.Detail = PickerDetail(rest[end+len("\n---\n"):])
}

// maxPickerDetailLines caps the Open Items shown under a session row. With the
// subject line that's up to 5 lines per session, enough to tell two sessions
// apart without the listing growing unwieldy.
const maxPickerDetailLines = 4

// PickerDetail extracts the leading bullets of the body's "### Open Items"
// section as the entry's detail lines — the "what's left to do". Each
// returned line is normalized to a single line capped at 80 bytes; the "- "
// bullet marker is preserved for readability. Returns nil when the body has
// no Open Items section.
func PickerDetail(body string) []string {
	lines := strings.Split(body, "\n")
	inOpen := false
	var detail []string
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			// A heading ends the Open Items section once we're inside it; before
			// that, look for the Open Items heading specifically.
			if inOpen {
				break
			}
			if strings.Contains(strings.ToLower(t), "open items") {
				inOpen = true
			}
			continue
		}
		if !inOpen || t == "" {
			continue
		}
		if !strings.HasPrefix(t, "-") && !strings.HasPrefix(t, "*") {
			continue
		}
		detail = append(detail, FirstLineSummary(t))
		if len(detail) >= maxPickerDetailLines {
			break
		}
	}
	return detail
}

// FirstLineSummary trims s to its first non-empty line and caps it at 80
// bytes, matching the one-line summary spec.
func FirstLineSummary(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if len(s) > 80 {
		s = textutil.TruncateBytes(s, 80)
	}
	return s
}
