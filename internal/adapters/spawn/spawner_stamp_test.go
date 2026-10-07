package spawn

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// errStampRefused is the failure stampRefusingStore's StampMint returns.
var errStampRefused = errors.New("stamp refused")

// stampRefusingStore is a MemStore whose StampMint always fails.
type stampRefusingStore struct{ *sessions.MemStore }

func (stampRefusingStore) StampMint(string, sessions.MintStamp) error { return errStampRefused }

// A delegated child is stamped as an agent's; a stamp that cannot be recorded
// is warned about, since an unstamped session reads as a human's and a sweep
// never purges it — and a recorded one says nothing.
func TestStampChild_StampsAnAgentAndWarnsOnlyOnFailure(t *testing.T) {
	var findings report.Findings
	s := newSpawner(report.To(&findings), nil, "", nil)

	store := sessions.NewMemStore()
	e, err := store.AssignHarp("/proj", "mock")
	require.NoError(t, err)
	s.stampChild(store, e.HarpName)
	assert.Empty(t, findings, "a recorded stamp is not worth a warning")
	got, err := store.Find(e.HarpName)
	require.NoError(t, err)
	assert.Equal(t, sessions.OriginAgent, got.Origin)

	s.stampChild(stampRefusingStore{sessions.NewMemStore()}, "child-harp")
	require.Len(t, findings, 1)
	assert.Contains(t, findings[0].Text, "session child-harp: cannot record its origin")
	assert.Contains(t, findings[0].Text, errStampRefused.Error())
}
