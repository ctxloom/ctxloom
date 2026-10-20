package cli

import (
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The status bar counts the session's own undelivered mail: what waits in
// in/ or in/claimed/, less what the delivered record holds.
func TestHudUnread_CountsTheSessionsUndeliveredMail(t *testing.T) {
	testsupport.Isolate(t)
	fs, m := afero.NewOsFs(), spool.NewHomeMapper()
	assert.Zero(t, hudUnread(fs, mailDrainOwner), "a spool never created has no mail")
	assert.Zero(t, hudUnread(fs, ""), "no harp: no inbox to name")

	seedOwnerMail(t, "kid", "message", "one")
	seedOwnerMail(t, "kid", "message", "two")
	assert.Equal(t, 2, hudUnread(fs, mailDrainOwner))
	assert.Zero(t, hudUnread(fs, "someone-else"), "only the session's own inbox")

	res, err := spool.Claim(fs, m, mailDrainOwner)
	require.NoError(t, err)
	require.Len(t, res.Entries, 2)
	assert.Equal(t, 2, hudUnread(fs, mailDrainOwner), "claimed is not yet delivered")
	require.NoError(t, spool.Deliver(fs, m, res.Entries[0].Ref, res.Entries[0].Identity(), time.Now()))
	assert.Equal(t, 1, hudUnread(fs, mailDrainOwner), "delivered mail is no longer counted")
}

func TestFormatHud_Mail(t *testing.T) {
	testsupport.Isolate(t)
	t.Setenv("CTXLOOM_SESSION_HARP", "swift-amber-falcon")
	assert.Contains(t, formatHud(agentSessionJSON{}, ctxloomHudInfo{Unread: 3}), colorYellow+"✉ 3"+colorReset)
	assert.NotContains(t, formatHud(agentSessionJSON{}, ctxloomHudInfo{}), "✉", "no mail: no section")
}
