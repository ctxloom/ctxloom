package cli

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolvePagerCommand pins the $PAGER precedence `--full` output relies
// on: an explicit PAGER wins (split on whitespace, so "less -R" style
// multi-word values carry their flags through), an unset or blank PAGER
// falls back to the "less -R" default.
func TestResolvePagerCommand(t *testing.T) {
	t.Run("respects PAGER", func(t *testing.T) {
		t.Setenv("PAGER", "more -f")
		assert.Equal(t, []string{"more", "-f"}, resolvePagerCommand())
	})

	t.Run("unset PAGER falls back to less -R", func(t *testing.T) {
		t.Setenv("PAGER", "")
		assert.Equal(t, []string{"less", "-R"}, resolvePagerCommand())
	})

	t.Run("blank PAGER falls back to less -R", func(t *testing.T) {
		t.Setenv("PAGER", "   ")
		assert.Equal(t, []string{"less", "-R"}, resolvePagerCommand())
	})
}

// TestStartPager_PipesThroughRealCommand exercises the actual piping
// mechanics with `cat` standing in for a pager: what's written to the
// returned writer must arrive on dst once cleanup (which closes the pipe and
// waits for the process) runs. dst is a plain buffer here, not os.Stdout —
// startPager itself has no TTY opinion; that decision lives in shouldPage.
func TestStartPager_PipesThroughRealCommand(t *testing.T) {
	if _, err := os.Stat("/bin/cat"); err != nil {
		t.Skip("/bin/cat not available")
	}
	var dst bytes.Buffer
	w, cleanup, err := startPager([]string{"/bin/cat"}, &dst)
	require.NoError(t, err)

	_, err = w.Write([]byte("hello from the pager\n"))
	require.NoError(t, err)

	require.NoError(t, cleanup())
	assert.Equal(t, "hello from the pager\n", dst.String())
}

// TestStartPager_EmptyArgvPassesThrough covers the defensive empty-argv
// case (resolvePagerCommand should never actually produce this, but a
// hand-rolled empty PAGER split defensively degrades to the plain writer
// rather than exec'ing nothing).
func TestStartPager_EmptyArgvPassesThrough(t *testing.T) {
	var dst bytes.Buffer
	w, cleanup, err := startPager(nil, &dst)
	require.NoError(t, err)
	assert.Same(t, io.Writer(&dst), w)
	require.NoError(t, cleanup())
}

// TestStartPager_UnknownCommandErrors covers a $PAGER pointing at a binary
// that doesn't exist: startPager must surface the error rather than hang or
// panic, so the caller (pagerWriter) can fall back to the plain writer.
func TestStartPager_UnknownCommandErrors(t *testing.T) {
	var dst bytes.Buffer
	_, _, err := startPager([]string{"this-binary-does-not-exist-anywhere"}, &dst)
	assert.Error(t, err)
}

// TestShouldPage pins the TTY guard: paging only ever happens when the
// destination writer IS the process's real os.Stdout. Every other
// destination (a cobra test buffer, a redirected file, anything captured
// via cmd.SetOut) must never page — this is what keeps structured formats
// and any non-interactive invocation pipeable.
func TestShouldPage(t *testing.T) {
	var buf bytes.Buffer
	assert.False(t, shouldPage(&buf), "a non-stdout writer must never be paged, regardless of the terminal check")
}

// TestPagerWriter_NonStdoutNeverPages exercises the top-level seam
// `session list --full` / `session query --full` call: when the command's
// output writer isn't the real stdout (as in every test, and every `>
// file` / `| jq` invocation), pagerWriter must hand back the same writer
// untouched and a harmless no-op cleanup.
func TestPagerWriter_NonStdoutNeverPages(t *testing.T) {
	var buf bytes.Buffer
	w, cleanup := pagerWriter(&buf)
	assert.Same(t, io.Writer(&buf), w)
	assert.NoError(t, cleanup())
}
