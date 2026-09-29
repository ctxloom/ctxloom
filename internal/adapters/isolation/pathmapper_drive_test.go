package isolation

import (
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// driveLetterCases are the Windows host paths driveLetterMapper routes, and
// where each lands in the Linux container. Pure strings: the table runs on
// every OS, which is the point of keeping the mapper free of filepath.
var driveLetterCases = []struct{ host, want string }{
	{`C:\Users\ben\proj`, "/mnt/c/Users/ben/proj"},
	{`c:/x`, "/mnt/c/x"},
	{`D:\`, "/mnt/d"},
	{`C:\a b\c`, "/mnt/c/a b/c"},
	{`C:\Users\Ben`, "/mnt/c/Users/Ben"},
	{`\\?\C:\x`, "/mnt/c/x"},
	{`C:\x\..\y`, "/mnt/c/y"},
	{`C:\..\..\etc`, "/mnt/c/etc"},
	{`C:/mixed\seps/ok`, "/mnt/c/mixed/seps/ok"},
	{`E:\data\ünïcode`, "/mnt/e/data/ünïcode"},
}

func TestDriveLetterMapper(t *testing.T) {
	for _, tc := range driveLetterCases {
		got, err := driveLetterMapper{}.toContainer(tc.host)
		require.NoError(t, err, tc.host)
		assert.Equal(t, tc.want, got, tc.host)
	}
}

// A share or device path has no drive the runtime mounts, so it is refused
// by name rather than guessed at.
func TestDriveLetterMapper_RefusesUNCAndDevicePaths(t *testing.T) {
	for _, host := range []string{
		`\\srv\sh\x`, `\\?\UNC\s\x`, `\\wsl.localhost\Ubuntu\home\u`, `\\wsl$\Ubuntu\x`, `\\.\pipe\x`, `//srv/sh/x`,
	} {
		_, err := driveLetterMapper{}.toContainer(host)
		require.ErrorIs(t, err, errUNCPath, host)
	}
}

func TestDriveLetterMapper_RefusesNonDriveAbsolute(t *testing.T) {
	for _, host := range []string{`C:x`, `\x`, `x`, ``, `1:\x`, `/home/u`} {
		_, err := driveLetterMapper{}.toContainer(host)
		require.ErrorIs(t, err, errNotDriveAbsolute, "%q", host)
	}
}

// The nested mounts (an overlay inside the project, .ctxloom inside a
// checkout) rely on these: an ancestor maps to an ancestor, and distinct
// directories never collide on one target.
func TestDriveLetterMapper_PrefixPreservingInjective(t *testing.T) {
	hosts := []string{`C:\`, `C:\p`, `C:\p\.claude`, `C:\p\q`, `C:\pq`, `D:\p`, `D:\p\.ctxloom`, `C:\P`}
	mapped := map[string]string{}
	for _, h := range hosts {
		m, err := driveLetterMapper{}.toContainer(h)
		require.NoError(t, err)
		for prev, pm := range mapped {
			assert.NotEqual(t, pm, m, "%s and %s collide", prev, h)
		}
		mapped[h] = m
	}
	for _, a := range hosts {
		for _, b := range hosts {
			if a != b && winAncestor(a, b) {
				assert.True(t, strings.HasPrefix(mapped[b], strings.TrimSuffix(mapped[a], "/")+"/"),
					"%s is under %s, so %s must be under %s", b, a, mapped[b], mapped[a])
			}
		}
	}
}

// winAncestor reports whether a is a proper ancestor of b in the
// backslash-separated test fixtures above.
func winAncestor(a, b string) bool {
	return strings.HasPrefix(b, strings.TrimSuffix(a, `\`)+`\`)
}

// Every mapped target is a clean absolute POSIX path: the daemon rejects
// anything else as a --mount target.
func TestDriveLetterMapper_TargetsArePOSIX(t *testing.T) {
	for _, tc := range driveLetterCases {
		got, err := driveLetterMapper{}.toContainer(tc.host)
		require.NoError(t, err)
		assert.NotContains(t, got, `\`)
		assert.True(t, path.IsAbs(got))
		assert.Equal(t, path.Clean(got), got)
	}
}
