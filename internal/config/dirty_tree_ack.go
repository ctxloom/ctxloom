package config

import (
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/admission"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/filelock"
)

// ===== Dirty-tree-commit human acknowledgement ===============================
//
// operations.commitDirtyTree ("dirty_tree_handler: commit") auto-commits an
// uncommitted tree on the user's behalf so a delegated agent_run child's
// worktree checkout — which only ever sees committed state — can see it. That
// requires a PRIOR HUMAN authorization, and until this file existed, the
// authorization was config.yaml's `dirty_tree_commit_ack` key.
//
// THE HOLE THIS CLOSES. A config key is reachable from the env layer
// (CTXLOOM_CONFIG_DIRTY_TREE_COMMIT_ACK=true) and --config-set, both of which
// an agent can set for itself via any child process it can spawn — VERIFIED
// against a build of this tree (config-layer-scope design doc, "Already wrong
// #1"). commitDirtyTree's own doc said the flag must be read "ONLY from the
// PROJECT config"; the config chain has no such thing as "only" one layer.
//
// THE SHAPE is internal/shared/admission's Store — the SAME mechanism
// paths.HomeCompanionConsentPath uses for "may ctxloom act without asking
// again", except this record is PROJECT-scoped (a fact about ONE checkout's
// branch, not the user across every project), so it lives under
// paths.DirtyTreeCommitAckPath (.ctxloom/state/, gitignored, unrebuildable)
// rather than the home directory. It is a single yes/no decision with no
// finer key than "this checkout", so key and scope collapse to the one
// constant dirtyTreeAckKeyFunc returns — the degenerate case admission.Store's
// own doc describes.

// dirtyTreeAckKey is the store's key type. It carries no fields: there is
// exactly one decision per checkout.
type dirtyTreeAckKey struct{}

// dirtyTreeAckKeyFunc is the store's key function: a fixed string, since the
// store already lives at a per-checkout path (DirtyTreeCommitAckPath) and
// needs no further discrimination.
func dirtyTreeAckKeyFunc(dirtyTreeAckKey) string { return "dirty_tree_commit_ack" }

// dirtyTreeAckReason is this store's internal admission.Reasons vocabulary.
// It is never surfaced to a caller — DirtyTreeCommitAcknowledged answers a
// plain bool — but admission.NewStore requires four distinct, non-zero
// reasons to construct validly.
type dirtyTreeAckReason int

const (
	dirtyTreeAckReasonApproved dirtyTreeAckReason = iota + 1
	dirtyTreeAckReasonDeclined
	dirtyTreeAckReasonUnasked
	dirtyTreeAckReasonFault
)

func dirtyTreeAckReasons() admission.Reasons[dirtyTreeAckReason] {
	return admission.Reasons[dirtyTreeAckReason]{
		Approved: dirtyTreeAckReasonApproved,
		Declined: dirtyTreeAckReasonDeclined,
		Unasked:  dirtyTreeAckReasonUnasked,
		Fault:    dirtyTreeAckReasonFault,
	}
}

// dirtyTreeAckStore opens appPath's dirty-tree-commit acknowledgement record.
// fs defaults to the OS filesystem when nil, matching every other store
// constructor in this package.
//
// WithLockPathFor(filelock.ProjectPathFor): this record lives inside a
// PROJECT .ctxloom tree (paths.DirtyTreeCommitAckPath, under
// .ctxloom/state/), not under the user's home the way companion_consent's
// store does — the same distinction filelock.ProjectPathFor's own doc draws,
// and the reason admission.Store's default (filelock.PathFor, beside-file) is
// wrong here: a beside-the-file lock would land inside .ctxloom/state/ itself
// as an untracked sibling, one more surface for a stray file to turn up on
// where the home-rooted convention has none of that problem to begin with.
func dirtyTreeAckStore(fs afero.Fs, appPath string) *admission.Store[dirtyTreeAckKey, dirtyTreeAckReason] {
	if fs == nil {
		fs = afero.NewOsFs()
	}
	return admission.NewStore(fs, paths.DirtyTreeCommitAckPath(appPath), dirtyTreeAckKeyFunc, dirtyTreeAckReasons(),
		admission.WithLockPathFor[dirtyTreeAckKey](filelock.ProjectPathFor))
}

// DirtyTreeCommitAcknowledged reports whether appPath's checkout has a
// recorded human acknowledgement that ctxloom may auto-commit a dirty tree on
// its behalf (dirty_tree_handler: "commit") — see
// paths.DirtyTreeCommitAckPath. An absent record is NOT acknowledged
// (fail closed, exactly like the config key this replaces defaulting to
// false) and stays QUIET — most checkouts never opt in, and warning on every
// one of them would train a user to ignore this warning.
//
// A record that EXISTS but could not be READ is a different fact: it may
// hold a real acknowledgement (or a real revocation) this call cannot see,
// so it still denies — but unlike the absent case, this is a genuine fault,
// and it used to be swallowed here with no diagnostic at all, the only one
// of this project's admission kinds that neither warned nor escalated
// (violating the "a refusal is loud" rule). It now warns, naming the file,
// the underlying error, and how to re-record the decision.
func DirtyTreeCommitAcknowledged(fs afero.Fs, appPath string) bool {
	rec, found, err := dirtyTreeAckStore(fs, appPath).Lookup(dirtyTreeAckKey{})
	if err != nil {
		clidiag.Warn("ctxloom",
			"could not read the dirty-tree-commit acknowledgement at %s: %v — refusing to auto-commit on your behalf until it is re-recorded (`ctxloom manage commit trust`)",
			paths.DirtyTreeCommitAckPath(appPath), err)
		return false
	}
	if !found {
		return false
	}
	return rec.Approved
}

// SetDirtyTreeCommitAck records appPath's human acknowledgement (ack=true) or
// its revocation (ack=false) that ctxloom may auto-commit a dirty tree on its
// behalf. The scriptable write both `ctxloom init`'s interview
// (operations.InitializeProject) and `ctxloom manage commit` use,
// since the record is no longer hand-editable in the config.yaml file people
// already open.
func SetDirtyTreeCommitAck(fs afero.Fs, appPath string, ack bool) error {
	_, err := dirtyTreeAckStore(fs, appPath).Set(dirtyTreeAckKey{}, ack)
	return err
}
