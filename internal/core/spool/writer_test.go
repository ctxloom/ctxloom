package spool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestWriter_PublishesReadableMessage(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	w, err := NewWriter(m, testHarp, DirIn, "coord")
	require.NoError(t, err)

	msg := &Message{Kind: "message", FromHarp: "coord", To: testHarp, Body: "do the thing\n"}
	ref, err := w.Write(msg)
	require.NoError(t, err)
	require.Equal(t, testHarp, ref.Harp)
	require.Equal(t, DirIn, ref.Dir)
	require.NoError(t, ref.Validate())

	// The writer stamps the caller's copy so it agrees with disk.
	require.NotEmpty(t, msg.ID)
	require.False(t, msg.Created.IsZero())
	require.Equal(t, CurrentVersion, msg.V)

	path, err := m.Resolve(ref)
	require.NoError(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, raw, "empty-source guard: a zero-byte file would pass a naive parse-and-compare")

	got, err := Read(m, ref)
	require.NoError(t, err)
	require.Equal(t, "do the thing\n", got.Body)
	require.Equal(t, "message", got.Kind)
	require.Equal(t, msg.ID, got.ID, "the id on disk must be the filename stem the writer chose")
	require.Equal(t, strings.TrimSuffix(ref.Name, MessageFileExt), got.ID)
}

// TestWriter_FilenameIsIdentityAndOrder: filename sort order must equal write
// order, including within one nanosecond, because a sweep gets arrival order
// from readdir+sort and never from file contents.
func TestWriter_FilenameIsIdentityAndOrder(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	w, err := NewWriter(m, testHarp, DirOut, testHarp)
	require.NoError(t, err)

	const count = 200
	written := make([]string, 0, count)
	for i := range count {
		ref, err := w.Write(&Message{Kind: "message", Body: fmt.Sprintf("body %d\n", i)})
		require.NoError(t, err)
		written = append(written, ref.Name)
	}
	require.Len(t, written, count)

	unique := map[string]bool{}
	for _, n := range written {
		require.False(t, unique[n], "duplicate filename %q: a same-nanosecond collision is a lost message", n)
		unique[n] = true
	}

	sorted := append([]string(nil), written...)
	sort.Strings(sorted)
	require.Equal(t, written, sorted, "lexicographic filename order must equal write order")

	// And the parsed seq must be strictly increasing.
	var prev uint64
	for i, n := range written {
		parsed, err := ParseName(n)
		require.NoError(t, err)
		require.Equal(t, testHarp, parsed.Writer)
		if i > 0 {
			require.Greater(t, parsed.Seq, prev, "seq must be strictly increasing")
		}
		prev = parsed.Seq
	}
}

// TestWriter_ReseedsSequenceAcrossRestart: a fresh process must not reissue a
// name a still-present file already holds, including one already consumed.
func TestWriter_ReseedsSequenceAcrossRestart(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()

	first, err := NewWriter(m, testHarp, DirIn, "coord")
	require.NoError(t, err)
	var last Ref
	for range 3 {
		last, err = first.Write(&Message{Kind: "message", Body: "x\n"})
		require.NoError(t, err)
	}
	// Consume one so the seed has to look past the live directory.
	_, err = Consume(m, last)
	require.NoError(t, err)

	restarted, err := NewWriter(m, testHarp, DirIn, "coord")
	require.NoError(t, err)
	next, err := restarted.Write(&Message{Kind: "message", Body: "after restart\n"})
	require.NoError(t, err)

	lastSeq, err := ParseName(last.Name)
	require.NoError(t, err)
	nextSeq, err := ParseName(next.Name)
	require.NoError(t, err)
	require.Greater(t, nextSeq.Seq, lastSeq.Seq,
		"a restarted writer must resume past the highest seq on disk (%s vs %s)", next.Name, last.Name)
}

// TestWriter_NeverPublishesAPartialFile: the staging file lives in tmp/, so a
// reader of in/ can never observe a half-written message, and a crash leaves
// debris only where no sweep looks.
func TestWriter_NeverPublishesAPartialFile(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	w, err := NewWriter(m, testHarp, DirIn, "coord")
	require.NoError(t, err)
	ref, err := w.Write(&Message{Kind: "message", Body: "complete\n"})
	require.NoError(t, err)

	root, err := Root(m, testHarp)
	require.NoError(t, err)

	tmpEntries, err := os.ReadDir(filepath.Join(root, "tmp"))
	require.NoError(t, err)
	require.Empty(t, tmpEntries, "the staging dir must be empty after a successful publish")

	inEntries, err := os.ReadDir(filepath.Join(root, "in"))
	require.NoError(t, err)
	files := 0
	for _, e := range inEntries {
		if e.IsDir() {
			continue
		}
		files++
		require.Equal(t, ref.Name, e.Name(), "in/ must hold exactly the published name")
	}
	require.Equal(t, 1, files)

	res, err := Sweep(m, testHarp, DirIn)
	require.NoError(t, err)
	require.NoError(t, res.ProblemErr())
	require.Len(t, res.Entries, 1)
}

func TestNewWriter_RefusesNonWritableDirections(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	for _, d := range []Dir{DirInConsumed, DirOutConsumed, DirInWithdrawn, Dir("nope"), Dir("")} {
		_, err := NewWriter(m, testHarp, d, "coord")
		require.Error(t, err, "%q must not be directly writable", d)
	}
}

func TestNewWriter_RefusesBadWriterID(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	for _, id := range []string{"", "has.dot", "has/slash", "has\\back", "has:colon", "ctl\x01"} {
		_, err := NewWriter(m, testHarp, DirIn, id)
		require.Error(t, err, "writer id %q must be refused (it would make the filename ambiguous)", id)
	}
}

func TestWriter_RefusesKindlessMessage(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	w, err := NewWriter(m, testHarp, DirIn, "coord")
	require.NoError(t, err)

	_, err = w.Write(&Message{Body: "no kind\n"})
	require.Error(t, err, "a message with no kind cannot be routed and must be refused at the writer")

	res, err := Sweep(m, testHarp, DirIn)
	require.NoError(t, err)
	require.Empty(t, res.Entries, "a refused write must leave nothing behind")
	require.NoError(t, res.ProblemErr())
}

func TestParseName_RoundTripAndRejections(t *testing.T) {
	n := Name{Nanos: 1754919000123456789, Seq: 42, Writer: "coord"}
	require.Equal(t, "01754919000123456789.00000042.coord.md", n.String())
	require.Equal(t, "01754919000123456789.00000042.coord", n.Stem())

	back, err := ParseName(n.String())
	require.NoError(t, err)
	require.Equal(t, n, back)

	for _, bad := range []string{
		"", "..", "notamessage", "1.2.coord.txt", "x.2.coord.md", "1.x.coord.md",
		"1.2.md", "1.2..md", ".1.2.coord.md", "1.2.co/ord.md",
	} {
		_, err := ParseName(bad)
		require.Error(t, err, "ParseName must refuse %q", bad)
	}
}

// invalidArgumentText is the rendered message syscall.EINVAL happens to
// produce on this platform. errorIsInvalid must NOT match on this text — it
// exists here only to construct a message-alike error that is deliberately
// NOT the typed errno, proving the check does not fall back to string
// comparison.
var invalidArgumentText = syscall.EINVAL.Error()

// TestErrorIsInvalid_TypedErrnoOnly pins errorIsInvalid to the typed errno
// syscall.EINVAL rather than any rendering of its message. The case that
// matters most is errLooksLikeEINVALButIsnt: an error whose .Error() is the
// exact same text os.PathError.Err.Error() would produce for a real EINVAL,
// but whose underlying value is not syscall.EINVAL. A string-match
// implementation (strings.Contains(err.Error(), "invalid argument")) passes
// that case; the typed check must reject it.
func TestErrorIsInvalid_TypedErrnoOnly(t *testing.T) {
	errLooksLikeEINVALButIsnt := &os.PathError{
		Op:   "sync",
		Path: "/some/dir",
		Err:  errors.New(invalidArgumentText),
	}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "PathError wrapping the real syscall.EINVAL",
			err:  &os.PathError{Op: "sync", Path: "/some/dir", Err: syscall.EINVAL},
			want: true,
		},
		{
			name: "bare syscall.EINVAL with no PathError wrapper",
			err:  syscall.EINVAL,
			want: true,
		},
		{
			name: "PathError wrapping a different errno (ENOENT)",
			err:  &os.PathError{Op: "sync", Path: "/some/dir", Err: syscall.ENOENT},
			want: false,
		},
		{
			name: "PathError whose message merely reads like EINVAL's",
			err:  errLooksLikeEINVALButIsnt,
			want: false,
		},
		{
			name: "a plain error with no PathError wrapper at all",
			err:  errors.New(invalidArgumentText),
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, errorIsInvalid(tc.err), "errorIsInvalid(%v)", tc.err)
		})
	}
}

// errForcedDirSync is the failure injected into the post-publish directory
// fsync; the log record must carry THIS error, not merely some error.
var errForcedDirSync = errors.New("forced directory fsync failure")

// observeWarnings routes the global logger to an observer for the test.
func observeWarnings(t *testing.T) *observer.ObservedLogs {
	t.Helper()
	core, logs := observer.New(zapcore.WarnLevel)
	t.Cleanup(zap.ReplaceGlobals(zap.New(core)))
	return logs
}

// TestWriter_PostPublishDirSyncFailureWarnsAndSucceeds: once the rename has
// published the file a reader can deliver it, so a failed directory fsync
// must not turn into a Write error — the sender would hear "failed" about a
// delivered message and a retry would duplicate it. It is a warning, and the
// caller gets the ref.
func TestWriter_PostPublishDirSyncFailureWarnsAndSucceeds(t *testing.T) {
	hostHome(t)
	logs := observeWarnings(t)
	m := NewHomeMapper()
	w, err := NewWriter(m, testHarp, DirIn, "coord")
	require.NoError(t, err)
	var synced []string
	w.syncDir = func(path string) error {
		synced = append(synced, path)
		return errForcedDirSync
	}

	ref, err := w.Write(&Message{Kind: "message", Body: "published\n"})
	require.NoError(t, err, "a failure after the publish must not be reported as a failed write")
	require.NotEmpty(t, ref.Name, "the caller must get the published ref (its withdraw handle)")
	require.Len(t, synced, 1, "the forced fsync must actually have run")

	path, err := m.Resolve(ref)
	require.NoError(t, err)
	require.FileExists(t, path, "the message is published")
	require.Equal(t, filepath.Dir(path), synced[0], "the fsync targets the published file's directory")

	entries := logs.FilterMessage(logDirSyncFailed).All()
	require.Len(t, entries, 1, "the lost durability must leave a warning; records: %v", logs.All())
	require.Equal(t, zapcore.WarnLevel, entries[0].Level)
	fields := entries[0].ContextMap()
	require.Equal(t, ref.String(), fields["ref"], "the warning names the published ref")
	var loggedErr error
	for _, f := range entries[0].Context {
		if f.Key == "error" {
			loggedErr, _ = f.Interface.(error)
		}
	}
	require.ErrorIs(t, loggedErr, errForcedDirSync, "the warning carries the fsync error")
}

// TestWriter_PrePublishFailureStillErrorsAndPublishesNothing: before the
// rename nothing is visible, so a failure there is still a Write error and
// leaves no file under the final name — and no post-publish warning.
func TestWriter_PrePublishFailureStillErrorsAndPublishesNothing(t *testing.T) {
	hostHome(t)
	logs := observeWarnings(t)
	m := NewHomeMapper()
	w, err := NewWriter(m, testHarp, DirIn, "coord")
	require.NoError(t, err)
	synced := 0
	w.syncDir = func(string) error { synced++; return nil }

	root, err := Root(m, testHarp)
	require.NoError(t, err)
	// Removing the staging dir forces the staging-file create to fail.
	require.NoError(t, os.RemoveAll(filepath.Join(root, tmpDirName)))

	ref, err := w.Write(&Message{Kind: "message", Body: "never\n"})
	require.Error(t, err, "a failure before the publish is a failed write")
	require.Equal(t, Ref{}, ref)
	require.Zero(t, synced, "nothing was published, so nothing is fsynced")
	require.Zero(t, logs.FilterMessage(logDirSyncFailed).Len())

	res, err := Sweep(m, testHarp, DirIn)
	require.NoError(t, err)
	require.Empty(t, res.Entries, "nothing may be published")
	require.Empty(t, res.Problems)
}
