// Package owneronly is the one seam that makes a path private to the user
// ctxloom runs as, and checks that it still is. What "owner-only" means is
// the platform's business, not the caller's: on unix it is the mode bits
// (DirMode, FileMode, no group or other bit); on Windows a mode is not access
// control at all — os.Chmod only toggles the read-only attribute — so it is
// the DACL, with SYSTEM and the Administrators group tolerated beside the
// owner (ruled 2026-09-25 on task unwitting-reproach: both can take any file
// on the machine regardless, and refusing them would refuse the ACLs Windows
// itself writes by default).
package owneronly

import (
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
)

// The modes an owner-only directory and file are created with. On unix they
// ARE the protection; on Windows they are what os honours of them (the
// read-only attribute) and the protection is the directory's DACL, which a
// file created inside it inherits.
const (
	DirMode  fs.FileMode = 0o700
	FileMode fs.FileMode = 0o600
)

// ExposedError is a path open to someone beyond its owner (and, on Windows,
// the tolerated machine principals). Why says how, in the platform's terms: a
// mode on unix, the principals granted access on Windows.
type ExposedError struct {
	Path string
	Why  string
}

func (e *ExposedError) Error() string { return e.Path + " " + e.Why }

// EnsureDir creates dir if it is missing and makes it owner-only. The
// restriction is re-applied on every call, so a directory loosened after the
// fact is tightened by the next one. On Windows the new DACL is inheritable,
// so what is created inside dir afterwards is owner-only too.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return err
	}
	return restrict(dir)
}

// Check holds each path to owner-only, in order, and returns the first that
// is not as an *ExposedError. A path that cannot be stat'ed is returned as
// the stat error (so a missing one is fs.ErrNotExist).
func Check(paths ...string) error {
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		why, err := violation(p, info)
		if err != nil {
			return fmt.Errorf("check who may read %s: %w", p, err)
		}
		if why != "" {
			return &ExposedError{Path: p, Why: why}
		}
	}
	return nil
}

// Describe is the platform's account of who can read p, for a human: its
// mode on unix; on Windows, where a mode says nothing, the ACL verdict
// ("owner-only", or "exposed: " and to whom).
func Describe(p string, info fs.FileInfo) (string, error) {
	return describe(p, info)
}

// exposure names the grantees of an access list beyond the owner and the
// tolerated principals, "" when there are none. It is the platform-neutral
// half of the Windows check (violation there reads the ACL and hands the SIDs
// here as strings), kept out of the build-tagged file so it is tested
// everywhere.
func exposure(owner string, tolerated, grantees []string) string {
	var extra []string
	for _, g := range grantees {
		if g == owner || slices.Contains(tolerated, g) || slices.Contains(extra, g) {
			continue
		}
		extra = append(extra, g)
	}
	if len(extra) == 0 {
		return ""
	}
	return "grants access to " + strings.Join(extra, ", ")
}
