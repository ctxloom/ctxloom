package operations

import (
	"fmt"
	"github.com/ctxloom/ctxloom/internal/core/trust"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
)

// RemovedItem identifies a local item to delete during cleanup.
type RemovedItem struct {
	Type remote.ItemType
	Ref  trust.BundleKey
}

// RemoveLocalItemsRequest is the input for RemoveLocalItems.
type RemoveLocalItemsRequest struct {
	Items       []RemovedItem
	Lockfile    *remote.Lockfile
	LockManager remote.LockfileStore
}

// RemoveLocalItemsResult reports the cleanup outcome.
type RemoveLocalItemsResult struct {
	Pruned   []string `json:"pruned"` // lockfile entries pruned (canonical refs)
	Warnings []string `json:"warnings"`
	Saved    bool     `json:"saved"` // whether the lockfile was rewritten
}

// RemoveLocalItems prunes the lockfile entries for items the remote dropped
// and persists the pruned lockfile; the save is gated on entries pruned.
func RemoveLocalItems(req RemoveLocalItemsRequest) (*RemoveLocalItemsResult, error) {
	res := &RemoveLocalItemsResult{}
	for _, item := range req.Items {
		req.Lockfile.RemoveEntry(item.Type, item.Ref)
		res.Pruned = append(res.Pruned, string(item.Ref))
	}
	if len(res.Pruned) > 0 {
		// remote.AllowEmpty: pruning the LAST entry legitimately empties the
		// lockfile, and Save otherwise refuses an empty write over a populated
		// file (that refusal exists to catch callers that computed nothing and
		// wrote it wholesale — see remote.Save). Here every removed entry was
		// named individually, so emptying is the intent, not an accident.
		if err := req.LockManager.Save(req.Lockfile, remote.AllowEmpty()); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("failed to update lockfile: %v", err))
		} else {
			res.Saved = true
		}
	}
	return res, nil
}
