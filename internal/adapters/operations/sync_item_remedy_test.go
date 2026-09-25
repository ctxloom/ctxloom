package operations

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

var _ clifmt.Remedier = SyncItem{}

// SyncItem.Remedy reads the fix its CAUSE names, so the raise site decides
// the wording; only a cause that names none gets the network/auth line.
func TestSyncItem_RemedyUsesItsCause(t *testing.T) {
	const fix = "a fix the raise site named"
	named := SyncItem{Status: "failed", cause: fmt.Errorf("pull: %w", report.Error{Fix: fix, Err: errors.New("boom")})}
	assert.Equal(t, fix, named.Remedy())

	plain := SyncItem{Status: "failed", cause: errors.New("connection refused")}
	assert.Equal(t, remedySyncFailed, plain.Remedy())

	assert.Empty(t, SyncItem{Status: "skipped"}.Remedy(), "only a failed item has a fix")
}

// An invalid reference is a spelling problem, not a network one: its fix
// must say so rather than send the user to check their connection (6c).
func TestSyncItem_InvalidReferenceNamesItsOwnFix(t *testing.T) {
	item := syncItem(context.Background(), &syncMockPuller{}, "not a reference at all ::", remote.ItemTypeBundle, treeBase, true, nil, nil)
	assert.Equal(t, "failed", item.Status)
	assert.Equal(t, remedyInvalidReference, item.Remedy())
	assert.Contains(t, item.Error, "invalid reference")
}
