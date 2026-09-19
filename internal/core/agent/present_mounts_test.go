package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/present"
)

// TestContainerizeApply_RecordsTheMountContent guards present.Containerize's
// mount recording from a CALLER, which is the only place the defect it covers
// is reachable: Containerize.ApplyPaths builds its Paths result from a closure
// that appends to a captured mounts slice, and Go leaves unspecified when a
// plain variable operand of a return statement is read relative to the calls
// beside it. Inlining across a package boundary is what let the compiler read
// mounts early, so present's own in-package tests could not observe it — they
// are compiled with the code under test and never inline across the import.
//
// The assertion must also force the Mount into an interface (testify's boxing
// does): the corrupted value was a slice element whose backing array the
// appends never wrote, and a direct == against a stack copy is folded away
// before the bad memory is ever materialized.
func TestContainerizeApply_RecordsTheMountContent(t *testing.T) {
	mapped := present.Containerize{ProjectRoot: "/mnt/proj"}.Apply(present.Paths{
		ProjectRoot: present.Root{Host: "/home/user/project"},
	})

	mounts := mapped.Mounts()
	require.Len(t, mounts, 1)
	assert.Equal(t, present.Mount{HostDir: "/home/user/project", TargetDir: "/mnt/proj"}, mounts[0])
}

// TestContainerizeApplyPaths_RecordsEveryResolvedRoot drives all four roots, so
// a result that captures the mounts slice partway through the appends is caught
// as well as one that captures it before any of them.
func TestContainerizeApplyPaths_RecordsEveryResolvedRoot(t *testing.T) {
	_, mounts := present.Containerize{
		ProjectRoot: "/c/proj",
		EngineHome:  "/c/home",
		CtxloomHome: "/c/ctxloom",
		Scratch:     "/c/scratch",
	}.ApplyPaths(present.Paths{
		ProjectRoot: present.Root{Host: "/h/proj"},
		EngineHome:  present.Root{Host: "/h/home"},
		CtxloomHome: present.Root{Host: "/h/ctxloom"},
		Scratch:     present.Root{Host: "/h/scratch"},
	})

	assert.Equal(t, []present.Mount{
		{HostDir: "/h/proj", TargetDir: "/c/proj"},
		{HostDir: "/h/home", TargetDir: "/c/home"},
		{HostDir: "/h/ctxloom", TargetDir: "/c/ctxloom"},
		{HostDir: "/h/scratch", TargetDir: "/c/scratch"},
	}, mounts)
}
