// Package spooltest is the file-spool test kit shared by the coordinator's
// and the runner's suites: a HOME of the test's own, and the reads and
// writes a test does straight against a harp's spool without either side's
// process in between.
package spooltest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// TeeHome points HOME at a directory of the test's own, so a spool written
// by one test is never swept by another and no test leaves files in the
// user's home. Idempotent per test: a second call returns the HOME the first
// minted rather than switching the test to a directory it has already been
// told about. The marker is a key t.Setenv restores at the test's end, so
// it can never name another test; it is kept outside the CTXLOOM_* namespace
// because testsupport.Isolate clears that whole namespace.
func TeeHome(t *testing.T) string {
	t.Helper()
	const marker = "COORD_TEST_HOME_OWNER"
	if os.Getenv(marker) == t.Name() {
		return os.Getenv("HOME")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(marker, t.Name())
	return home
}

// Entries sweeps a spool directory, treating "the directory does not exist"
// as "no messages" rather than as an error.
func Entries(t *testing.T, harp string, dir spool.Dir) []spool.Entry {
	t.Helper()
	path, err := spool.DirPath(spool.NewHomeMapper(), harp, dir)
	require.NoError(t, err)
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		return nil
	}
	res, err := spool.Sweep(spool.NewHomeMapper(), harp, dir)
	require.NoError(t, err)
	require.NoError(t, res.ProblemErr())
	return res.Entries
}

// DirsUnder lists every directory named "spool" anywhere below root — the
// evidence for "the disabled tee touched nothing".
func DirsUnder(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// A vanished temp entry is not evidence either way; a real
			// failure is, so it is returned rather than swallowed.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() && d.Name() == spool.SpoolDirName {
			found = append(found, path)
		}
		return nil
	})
	require.NoError(t, err, "walking %s", root)
	return found
}

// WriteMail puts one message file straight into harp's in/ spool, the way
// the coordinator's courier would, without a coordinator: for a test whose
// premise is "mail is waiting for this harp" and nothing more. spoolKind is
// the frontmatter spelling; writerID names the writer the file is stamped
// with.
func WriteMail(t *testing.T, harp, from, spoolKind, body, writerID string) {
	t.Helper()
	w, err := spool.NewWriter(spool.NewHomeMapper(), harp, spool.DirIn, writerID)
	require.NoError(t, err)
	_, err = w.Write(&spool.Message{Kind: spoolKind, FromHarp: from, To: harp, Body: body})
	require.NoError(t, err)
}
