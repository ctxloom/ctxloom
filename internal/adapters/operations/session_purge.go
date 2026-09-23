package operations

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
)

// Session purge. A harp directory's files fall into three purge classes,
// each decided by the paths.HarpMembers row the file lives under
// (classifyPurgeFile), and purge treats each one differently:
//
//	machine  — the canonical transcript row, everything under the transcript
//	           store row, and whatever file the entry's TranscriptPath names
//	           (fenced to inside this harp's own directory). Destroyed by the
//	           transcript population.
//	derived  — the essence row. Destroyed by the artifacts population.
//	authored — every other file under a Persist member, and every top-level
//	           file no row names. NEVER destroyed. Named in the report so a
//	           kept-but-unmentioned file never goes unfiled.
//
// This is an ALLOWLIST, not a denylist: a file is destroyed only if it is in
// the population asked for. The Ephemeral members (the reaper's, by
// Lifetime) are never even walked — a purge runs zero git commands, so the
// scratch worktrees a harp's ephemeral store may hold are untouched by
// construction, not by a filter applied after the fact. The identity rows
// are no population and never an item: the sidecar is what records that a
// purge happened (MarkPurged).

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

// PurgePopulation names one destroyable population inside a harp directory.
// Each one has its own CLI destroyer (`session transcript purge`, `session
// artifacts purge`), and the sweep (`session purge`) asks for both. Authored
// content is deliberately NOT a population: nothing may ask for it, which is
// a stronger guarantee than refusing the request after the fact.
type PurgePopulation string

const (
	// PurgePopulationTranscript is the machine-written bulk: transcript.jsonl
	// and everything under persist/transcripts/.
	PurgePopulationTranscript PurgePopulation = "transcript"
	// PurgePopulationArtifacts is the derived essence — what distillation
	// produced, and what can be produced again only while the transcript
	// still exists.
	PurgePopulationArtifacts PurgePopulation = "artifacts"
)

// PurgeSessionRequest is one purge invocation, over one or both populations.
type PurgeSessionRequest struct {
	Harp string
	// Populations selects what this invocation destroys. Empty destroys
	// nothing and is an error: a destroyer with no population is a command
	// that reports success having done nothing at all.
	Populations []PurgePopulation
	// Undistilled permits destroying the TRANSCRIPT of a session that has no
	// essence. Without an essence the transcript is the only record of what
	// happened, so this is the deliberate second flag that allows it.
	Undistilled bool
	// EvenIfLive permits destroying a session whose liveness lock does not
	// prove its owner dead: held (the owner is running right now), or absent
	// (a session from before the lock existed, or one whose Hold failed), or
	// on a filesystem whose locks cannot be trusted. It is the deliberate
	// second flag for the one mistake purge cannot undo — destroying the
	// transcript a running agent is still writing — and it is the ONLY way
	// past the lock: "cannot determine" is never permission on its own.
	EvenIfLive bool
	// Apply is the plan/act switch. False walks and classifies only —
	// nothing on disk or in the index changes. This is every destroyer's
	// default: absence of --yes means report only, never act, regardless of
	// whether the invocation is on a TTY.
	Apply bool
}

// wants reports whether this request asks for population p.
func (r PurgeSessionRequest) wants(p PurgePopulation) bool {
	return slices.Contains(r.Populations, p)
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
	// Populations echoes what this invocation asked for, so a report read on
	// its own says which destroyer produced it.
	Populations []PurgePopulation `json:"populations"`
	// PurgedAt is set once MarkPurged has stamped the index entry — the
	// caller's proof that the mark-before-destroy write happened.
	PurgedAt *time.Time `json:"purged_at,omitempty"`
}

var (
	// ErrPurgeOwnerNotProvenDead is returned when the harp's liveness lock
	// (internal/shared/sessionlock) does not prove the owning process dead
	// and EvenIfLive was not set. Dead — a lock file that exists and nothing
	// holds — is the ONLY verdict that permits destroying; a held lock, a
	// missing one and an untrusted filesystem all refuse. The index's
	// ended_at plays no part in EITHER direction: a session resumed under its
	// harp is running again with ended_at still set, and a session that died
	// before EndSession has none while its free lock proves it gone. It is a
	// timestamp, not a liveness signal.
	ErrPurgeOwnerNotProvenDead = errors.New("the session lock does not prove its owner dead")
	// ErrPurgeUndistilled is returned when the TRANSCRIPT population is asked
	// for against a session with no essence.md and Undistilled was not also
	// set. Without an essence the transcript is the session's ONLY record;
	// this is the extra deliberate flag that permits destroying it.
	ErrPurgeUndistilled = errors.New("session was never distilled")
	// ErrPurgeNoPopulation is returned when a request names no population. A
	// destroyer that was handed nothing to destroy must say so rather than
	// walk the directory, keep every file, and report success.
	ErrPurgeNoPopulation = errors.New("purge asked for no population")
	// ErrPurgeNothingToDo is returned when Apply is true and nothing in the
	// plan's Destroy list survived to be freed — an action verb that would
	// change nothing. Nothing is touched and PurgedAt is never written: there
	// is nothing here for it to protect.
	ErrPurgeNothingToDo = errors.New("purge changed nothing: no machine-written bulk matched")
)

// PurgeSession classifies a harp's directory and, when req.Apply, destroys
// exactly the populations asked for. It never descends into an Ephemeral
// member and never runs git.
//
// Ordering is load-bearing: the index entry is marked purged BEFORE any file
// is unlinked (see sessions.Manager.MarkPurged's doc). If PurgeSession dies
// between the two, the index already reads "purged" — the next `session list`
// keeps the row instead of silently reconciling it away over its now-missing
// transcript.
//
// "Is this session distilled?" is sessions.Distilled — the disk, never
// entry.Summary.
func PurgeSession(harp string, req PurgeSessionRequest) (*PurgeSessionResult, error) {
	if len(req.Populations) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrPurgeNoPopulation, harp)
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

	res := &PurgeSessionResult{Harp: harp, Populations: req.Populations}

	// THE LOCK ONLY EVER REFUSES, and it is held across the destruction: a
	// session resuming under this harp meanwhile waits in sessionlock.Hold
	// instead of racing the unlink of the transcript it is about to append
	// to. Dead alone passes; Alive, Indeterminate and any verdict that does
	// not exist yet refuse — unless the caller's EvenIfLive says, explicitly,
	// that it accepts the consequence. The release is deferred rather than
	// called early so nothing below can be reached under a fresh probe: this
	// process now holds the lock, and probing again from under its own hold
	// would read it as a live owner.
	probe, release := sessionlock.Acquire(harp)
	defer release()
	if !probe.Verdict.MayReclaim() && !req.EvenIfLive {
		return res, fmt.Errorf("%w: %s", ErrPurgeOwnerNotProvenDead, probe.Reason)
	}

	hasEssence := sessions.Distilled(harpDir)
	wantTranscript := req.wants(PurgePopulationTranscript)
	wantArtifacts := req.wants(PurgePopulationArtifacts)

	items, err := classifyHarpDir(harpDir, entry)
	if err != nil {
		return nil, err
	}

	// The undistilled guard protects a real file, so it asks whether there IS
	// one. Firing on the request alone would refuse forever for a session
	// whose transcript is already deliberately gone — the caller would have
	// done exactly what the refusal asked and still be told no.
	if wantTranscript && !hasEssence && !req.Undistilled && hasClass(items, PurgeClassMachine) {
		return res, fmt.Errorf("%w: %q — its transcript is the only record of this session; pass --undistilled to destroy it anyway", ErrPurgeUndistilled, harp)
	}

	for _, it := range items {
		switch it.Class {
		case PurgeClassMachine:
			if wantTranscript {
				it.Action = "destroy"
				res.Destroy = append(res.Destroy, it)
			} else {
				it.Action = "keep"
				it.Reason = "transcript: not this destroyer's population (see `ctxloom session transcript purge`)"
				res.Keep = append(res.Keep, it)
			}
		case PurgeClassDerived:
			if wantArtifacts {
				it.Action = "destroy"
				res.Destroy = append(res.Destroy, it)
			} else {
				it.Action = "keep"
				it.Reason = "derived essence: not this destroyer's population (see `ctxloom session artifacts purge`)"
				res.Keep = append(res.Keep, it)
			}
		case PurgeClassAuthored:
			it.Action = "keep"
			it.Reason = "authored: never destroyed by purge"
			res.Keep = append(res.Keep, it)
		}
	}

	if !req.Apply {
		return res, nil
	}

	if len(res.Destroy) == 0 {
		return res, ErrPurgeNothingToDo
	}

	// MARK BEFORE DESTROY. See the func doc and sessions.Manager.MarkPurged.
	// Only a TRANSCRIPT purge marks: PurgedAt is what keeps a row alive past
	// its own missing transcript, so it protects exactly that case. An
	// artifacts-only purge leaves the transcript in place and needs nothing.
	if wantTranscript {
		now := time.Now().UTC()
		if err := mgr.MarkPurged(harp, now); err != nil {
			return res, fmt.Errorf("mark %s purged: %w", harp, err)
		}
		res.PurgedAt = &now
	}

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

// hasClass reports whether any classified item belongs to class c.
func hasClass(items []PurgeItem, c PurgeClass) bool {
	return slices.ContainsFunc(items, func(it PurgeItem) bool { return it.Class == c })
}

// classifyHarpDir walks harp's directory, classifying every regular file
// under it EXCEPT those under an Ephemeral member, which the walk itself
// skips (never filtered afterward — see the package doc), and the identity
// rows. Returned items are sorted by Rel for a deterministic report.
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
		member, isMember := paths.ClassifyMember(filepath.ToSlash(rel))
		if d.IsDir() {
			if isMember && member.Lifetime == paths.Ephemeral {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // symlinks/devices/etc: not one of the enumerated classes, left alone
		}
		if isMember && member.Tier == paths.MemberIdentity {
			return nil
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

// classifyPurgeFile decides one file's PurgeClass from the paths.HarpMembers
// row it lives under (paths.ClassifyMember). isTranscriptMatch is true when
// this file is the one entry.TranscriptPath names (already fenced to inside
// the harp dir by the caller) — a real session may bind a transcript
// filename no row names, and that file is still machine-written bulk.
func classifyPurgeFile(rel string, isTranscriptMatch bool) PurgeClass {
	if isTranscriptMatch {
		return PurgeClassMachine
	}
	member, ok := paths.ClassifyMember(filepath.ToSlash(rel))
	if !ok {
		return PurgeClassAuthored
	}
	switch member.Name {
	case paths.CanonicalTranscriptFileName, paths.TranscriptStoreDirName:
		return PurgeClassMachine
	case paths.EssenceFileName:
		return PurgeClassDerived
	default:
		return PurgeClassAuthored
	}
}
