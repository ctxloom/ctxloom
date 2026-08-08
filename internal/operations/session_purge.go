package operations

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
)

// Session purge — j001300 close-out area 2 (docs/design/j001300-closeout-surfaces.design.md
// §4). A harp directory holds three content classes, and purge treats each one
// differently:
//
//	machine  — transcript.jsonl / transcript.acp.jsonl (top level or under
//	           persist/), everything under persist/transcripts/, and whatever
//	           file the index entry's TranscriptPath names (fenced to inside
//	           this harp's own directory). ALWAYS destroyed.
//	derived  — essence.md. Kept by default; destroyed only under --everything.
//	authored — anything else outside ephemeral/. NEVER destroyed. Named in the
//	           report so a kept-but-unmentioned file never goes unfiled.
//
// This is an ALLOWLIST, not a denylist: a file is destroyed only if it matches
// one of the machine-bulk rules above. Everything else is kept and named.
// ephemeral/ is never even walked — a purge runs zero git commands, so the
// scratch worktrees a harp's ephemeral dir may hold are untouched by
// construction, not by a filter applied after the fact.

// PurgeClass names one content class in a harp directory.
type PurgeClass string

const (
	PurgeClassMachine  PurgeClass = "machine"
	PurgeClassDerived  PurgeClass = "derived"
	PurgeClassAuthored PurgeClass = "authored"
)

// PurgeItem is one classified path in a purge plan. Action is "destroy" or
// "keep"; Reason is always populated for "keep" so a kept file is never
// silently kept — every keep line in the report says why.
type PurgeItem struct {
	Path   string     `json:"path"`
	Rel    string     `json:"rel"`
	Class  PurgeClass `json:"class"`
	Bytes  int64      `json:"bytes"`
	Action string     `json:"action"`
	Reason string     `json:"reason,omitempty"`
}

// PurgeSessionRequest is one `session purge` invocation.
type PurgeSessionRequest struct {
	Harp        string
	Everything  bool
	Undistilled bool
	// Apply is the plan/act switch. False walks and classifies only —
	// nothing on disk or in the index changes. This is `session purge`'s
	// default: absence of --yes means report only, never act, regardless of
	// whether the invocation is on a TTY.
	Apply bool
}

// PurgeSessionResult is the plan (and, when Apply, the outcome) of one purge.
type PurgeSessionResult struct {
	Harp    string      `json:"harp"`
	Applied bool        `json:"applied"`
	Destroy []PurgeItem `json:"destroy"`
	Keep    []PurgeItem `json:"keep"`
	// BytesFreed sums Bytes over the items actually removed. Zero on a
	// plan-only run.
	BytesFreed int64 `json:"bytes_freed"`
	// Withheld names, by Rel, every authored file --everything asked to
	// destroy and ctxloom refused. Non-empty here is exactly the condition
	// under which the CLI exits refused (2): the invocation asked for
	// something ctxloom withheld.
	Withheld []string `json:"withheld,omitempty"`
	// PurgedAt is set once MarkPurged has stamped the index entry — the
	// caller's proof that the mark-before-destroy write happened.
	PurgedAt *time.Time `json:"purged_at,omitempty"`
}

var (
	// ErrPurgeLiveSession is returned when the named harp has no EndedAt yet.
	// Purging a session still in progress could destroy the only transcript a
	// live agent is still writing to.
	ErrPurgeLiveSession = errors.New("session is still live")
	// ErrPurgeUndistilled is returned when --everything is asked for against
	// a session with no essence.md and --undistilled was not also given.
	// --everything without an essence would destroy the session's ONLY
	// record; this is the extra deliberate flag that permits that.
	ErrPurgeUndistilled = errors.New("session was never distilled")
	// ErrPurgeNothingToDo is returned when Apply is true and nothing in the
	// plan's Destroy list survived to be freed — an action verb that would
	// change nothing. Nothing is touched and PurgedAt is never written: there
	// is nothing here for it to protect.
	ErrPurgeNothingToDo = errors.New("purge changed nothing: no machine-written bulk matched")
)

// PurgeSession classifies a harp's directory and, when req.Apply, destroys
// exactly the machine-written bulk (plus the derived essence under
// --everything). It never descends into ephemeral/ and never runs git.
//
// Ordering is load-bearing: the index entry is marked purged BEFORE any file
// is unlinked (see sessions.Manager.MarkPurged's doc). If PurgeSession dies
// between the two, the index already reads "purged" — the next `session list`
// keeps the row instead of silently reconciling it away over its now-missing
// transcript.
//
// "Is this session distilled?" is answered by whether essence.md exists on
// disk under the harp directory — NOT by entry.Summary. The session index
// always carries a Summary once anything has synced a picker line for it
// (harp rename, a resume pass, this journey's own fixture), so Summary != ""
// is true long before a real essence has ever been written; using it here
// would let --everything sail through the one session it exists to protect.
func PurgeSession(harp string, req PurgeSessionRequest) (*PurgeSessionResult, error) {
	if req.Undistilled && !req.Everything {
		return nil, fmt.Errorf("--undistilled has no effect without --everything")
	}

	mgr, err := openSessions()
	if err != nil {
		return nil, err
	}
	entry, err := mgr.Find(harp)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, fmt.Errorf("harp not in index: %q", harp)
	}

	harpDir, err := paths.HarpDir(harp)
	if err != nil {
		return nil, err
	}

	res := &PurgeSessionResult{Harp: harp}

	if entry.EndedAt == nil {
		return res, fmt.Errorf("%w: %q has no ended_at yet", ErrPurgeLiveSession, harp)
	}

	hasEssence := false
	if _, statErr := os.Stat(filepath.Join(harpDir, paths.EssenceFileName)); statErr == nil {
		hasEssence = true
	}
	if req.Everything && !hasEssence && !req.Undistilled {
		return res, fmt.Errorf("%w: %q — its transcript is the only record of this session; pass --undistilled to destroy it anyway", ErrPurgeUndistilled, harp)
	}

	items, err := classifyHarpDir(harpDir, entry)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		switch it.Class {
		case PurgeClassMachine:
			it.Action = "destroy"
			res.Destroy = append(res.Destroy, it)
		case PurgeClassDerived:
			if req.Everything {
				it.Action = "destroy"
				res.Destroy = append(res.Destroy, it)
			} else {
				it.Action = "keep"
				it.Reason = "derived essence: kept by default (pass --everything to destroy it too)"
				res.Keep = append(res.Keep, it)
			}
		case PurgeClassAuthored:
			it.Action = "keep"
			it.Reason = "authored: never destroyed by purge"
			res.Keep = append(res.Keep, it)
			if req.Everything {
				res.Withheld = append(res.Withheld, it.Rel)
			}
		}
	}

	if !req.Apply {
		return res, nil
	}

	if len(res.Destroy) == 0 {
		return res, ErrPurgeNothingToDo
	}

	// MARK BEFORE DESTROY. See the func doc and sessions.Manager.MarkPurged.
	now := time.Now().UTC()
	if err := mgr.MarkPurged(harp, now); err != nil {
		return res, fmt.Errorf("mark %s purged: %w", harp, err)
	}
	res.PurgedAt = &now

	var freed int64
	var destroyed []PurgeItem
	for _, it := range res.Destroy {
		if rmErr := os.Remove(it.Path); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			res.Destroy = destroyed
			res.BytesFreed = freed
			res.Applied = true
			return res, fmt.Errorf("destroy %s: %w (%d byte(s) already freed before the failure)", it.Rel, rmErr, freed)
		}
		freed += it.Bytes
		destroyed = append(destroyed, it)
	}
	res.Destroy = destroyed
	res.BytesFreed = freed
	res.Applied = true
	return res, nil
}

// classifyHarpDir walks harp's directory, classifying every regular file
// under it EXCEPT ephemeral/, which is skipped by the walk itself (never
// filtered afterward — see the package doc). Returned items are sorted by Rel
// for a deterministic report.
func classifyHarpDir(harpDir string, entry *sessions.Entry) ([]PurgeItem, error) {
	var transcriptAbs string
	if entry.TranscriptPath != "" {
		if rel, relErr := filepath.Rel(harpDir, entry.TranscriptPath); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			transcriptAbs = filepath.Clean(entry.TranscriptPath)
		}
	}

	var items []PurgeItem
	walkErr := filepath.WalkDir(harpDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if path == harpDir {
			return nil
		}
		rel, relErr := filepath.Rel(harpDir, path)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			if rel == paths.EphemeralDirName {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // symlinks/devices/etc: not one of the enumerated classes, left alone
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		isTranscriptMatch := transcriptAbs != "" && filepath.Clean(path) == transcriptAbs
		items = append(items, PurgeItem{
			Path:  path,
			Rel:   filepath.ToSlash(rel),
			Class: classifyPurgeFile(rel, isTranscriptMatch),
			Bytes: info.Size(),
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Rel < items[j].Rel })
	return items, nil
}

// classifyPurgeFile decides one file's PurgeClass from its path relative to
// the harp directory. isTranscriptMatch is true when this file is the one
// entry.TranscriptPath names (already fenced to inside the harp dir by the
// caller) — a real session may bind a transcript filename this rule set does
// not otherwise recognize, and that file is still machine-written bulk.
func classifyPurgeFile(rel string, isTranscriptMatch bool) PurgeClass {
	if isTranscriptMatch {
		return PurgeClassMachine
	}
	relSlash := filepath.ToSlash(rel)
	switch {
	case relSlash == paths.CanonicalTranscriptFileName, relSlash == "transcript.acp.jsonl":
		return PurgeClassMachine
	case relSlash == paths.PersistDirName+"/"+paths.CanonicalTranscriptFileName,
		relSlash == paths.PersistDirName+"/transcript.acp.jsonl":
		return PurgeClassMachine
	case strings.HasPrefix(relSlash, paths.PersistDirName+"/"+paths.TranscriptStoreDirName+"/"):
		return PurgeClassMachine
	case relSlash == paths.EssenceFileName:
		return PurgeClassDerived
	default:
		return PurgeClassAuthored
	}
}
