package operations

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// denyOpenFs wraps an afero.Fs and fails Open for any path in deny — a fake
// (not a mock) that simulates "permission denied"/"I/O error" reads without
// touching real OS file permissions, mirroring
// internal/signing/countersign's own denyFs test fixture. Used here to drive
// EffectiveTrust's records-construction preamble down its fail-closed path
// deterministically, with no chmod/root-skip flakiness.
type denyOpenFs struct {
	afero.Fs
	deny map[string]error
}

func (f denyOpenFs) Open(name string) (afero.File, error) {
	if err, ok := f.deny[name]; ok {
		return nil, err
	}
	return f.Fs.Open(name)
}

// TestEffectiveTrust_AbsentApprovalsStore_NormalPending is the CONTROL case
// for the fail-closed preamble: neither approvals directory has EVER been
// created (a fresh project, HOME pointed at an empty temp dir — exactly
// TestEffectiveTrust_DefaultRecords_NothingApprovedOrRejected's setup). This
// must resolve as an ordinary pending decision — the normal "nothing
// reviewed yet" outcome — and must NOT record a strictness finding. A fresh
// checkout with no approvals recorded yet is not a fault.
func TestEffectiveTrust_AbsentApprovalsStore_NormalPending(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewMemMapFs()

	mark := strictness.Checkpoint()
	res, err := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref:     trust.Ref{RepoURL: trustRepo, Bundle: "b", Kind: trust.KindFragment, Name: "f"},
		Payload: pbytes("x"),
		Form:    rawForm,
		FS:      fs,
	})
	require.NoError(t, err)
	assert.Equal(t, trust.Deny, res.Decision)
	assert.Equal(t, trust.SourcePending, res.Source)
	assert.Empty(t, strictness.Since(mark), "an absent approvals store must never record a strictness finding")
}

// TestEffectiveTrust_UnreadableApprovalsStore_DenyAllAndStrictFatal is the
// DECIDED security fix under test: when the project approvals store EXISTS
// but cannot be read (permission denied listing it), EffectiveTrust must
// deny the item — even one that would otherwise be ALLOWED at an earlier
// step (here, step 2's local-content exemption) — and record a ClassTrust
// strictness finding, mirroring the pre-S6 ledger's own store-open check.
// Proving the override on an IsLocal item is the point: if the preamble
// merely fell through to steps 2-6 on error, this item would wrongly be
// allowed at step 2 before ever consulting the (broken) approvals store.
func TestEffectiveTrust_UnreadableApprovalsStore_DenyAllAndStrictFatal(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())

	projectDir := filepath.Join(t.TempDir(), ".ctxloom")
	approvalsDir := filepath.Join(projectDir, "approvals")
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(approvalsDir, 0o755))
	wrapped := denyOpenFs{Fs: fs, deny: map[string]error{approvalsDir: errors.New("permission denied")}}

	cfg := &config.Config{AppPaths: []string{projectDir}}

	mark := strictness.Checkpoint()
	res, err := EffectiveTrust(cfg, EffectiveTrustRequest{
		Ref:     trust.Ref{Bundle: "b", Kind: trust.KindFragment, Name: "f", IsLocal: true},
		Payload: pbytes("x"),
		Form:    rawForm,
		FS:      wrapped,
	})
	require.NoError(t, err)
	assert.Equal(t, trust.Deny, res.Decision, "an unreadable approvals store must deny even an otherwise-local-allowed item")
	assert.NotEqual(t, trust.SourceLocal, res.Source, "the local exemption must never be reached once the store proves unreadable")

	found := strictness.Since(mark)
	require.Len(t, found, 1)
	assert.Equal(t, strictness.ClassTrust, found[0].Class)
	assert.Contains(t, found[0].Message, "approvals store")
}
