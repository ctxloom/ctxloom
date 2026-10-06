//go:build windows

package spool

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport/fileperm"
)

// On Windows owner-only is a DACL, not a mode, and it is the established
// sessions root's (paths.EnsureHomeRoots, at process start): the spool's
// writers only create beneath it, so the root, the layout, a published
// message, a delivered record and an armed wake all inherit the root's
// owner-only ACE.
func TestSpool_WhatIsBeneathTheEstablishedRootIsOwnerOnly_ADACL(t *testing.T) {
	hostHome(t)
	require.NoError(t, paths.EnsureHomeRoots(safefs.New().Private))
	m := NewHomeMapper()
	root, err := Root(m, testHarp)
	require.NoError(t, err)

	w, err := NewWriter(afero.NewOsFs(), m, testHarp, DirIn, "coord")
	require.NoError(t, err)
	ref, err := w.Write(&Message{Kind: "message", FromHarp: "coord", To: testHarp, Body: "x\n"})
	require.NoError(t, err)
	nonce, err := ArmWake(afero.NewOsFs(), m, testHarp)
	require.NoError(t, err)

	delivered, err := w.Write(&Message{Kind: "message", FromHarp: "coord", To: testHarp, Body: "y\n"})
	require.NoError(t, err)
	require.NoError(t, Deliver(afero.NewOsFs(), m, delivered, "m-perm", time.Now()))

	msg, err := m.Resolve(ref)
	require.NoError(t, err)
	fileperm.OwnerOnly(t, root)
	fileperm.OwnerOnly(t, filepath.Join(root, filepath.FromSlash(string(DirOutConsumed))))
	fileperm.OwnerOnly(t, filepath.Join(root, filepath.FromSlash(deliveredDirName), "m-perm"))
	fileperm.OwnerOnly(t, msg)
	fileperm.OwnerOnly(t, filepath.Join(root, filepath.FromSlash(wakeDirName), nonce))
}
