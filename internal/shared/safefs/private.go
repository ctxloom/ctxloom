package safefs

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/spf13/afero"
)

// privateOn is Private over one filesystem, with the platform's (or a test
// double's) judgement of exposure and its way of restricting.
type privateOn struct {
	fs        afero.Fs
	violation func(path string, info fs.FileInfo) (string, error)
	restrict  func(dir string) error
}

func (p privateOn) Ensure(dir string) error {
	_, err := p.fs.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// A dir this call creates holds nothing, so restricting it walks
		// nothing: it is made owner-only outright rather than left with
		// whatever it inherited.
		if err := p.fs.MkdirAll(dir, PrivateDirMode); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		err := p.Check(dir)
		var exposed *ExposedError
		if !errors.As(err, &exposed) {
			return err
		}
	}
	if err := p.restrict(dir); err != nil {
		return fmt.Errorf("restrict %s to its owner: %w", dir, err)
	}
	return nil
}

func (p privateOn) Check(paths ...string) error {
	for _, path := range paths {
		info, err := p.fs.Stat(path)
		if err != nil {
			return err
		}
		why, err := p.violation(path, info)
		if err != nil {
			return fmt.Errorf("check who may read %s: %w", path, err)
		}
		if why != "" {
			return &ExposedError{Path: path, Why: why}
		}
	}
	return nil
}

// modeViolation says why info is open beyond its owner by its mode bits, or
// "" when no group or other permission bit is set.
func modeViolation(_ string, info fs.FileInfo) (string, error) {
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Sprintf("has mode %04o", perm), nil
	}
	return "", nil
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
