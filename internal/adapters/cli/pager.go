package cli

import (
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// defaultPagerCmd is used whenever $PAGER is unset or blank.
const defaultPagerCmd = "less -R"

// resolvePagerCommand returns the argv for the pager to exec: $PAGER, split
// on whitespace so a value like "less -R" carries its flags through, or the
// "less -R" default when $PAGER is unset or blank (matching how a shell
// treats an empty variable).
func resolvePagerCommand() []string {
	raw := os.Getenv("PAGER")
	if strings.TrimSpace(raw) == "" {
		raw = defaultPagerCmd
	}
	return strings.Fields(raw)
}

// startPager launches the command named by argv with its own stdout wired to
// dst, and returns a writer for the caller to stream paged content into plus
// a cleanup func that closes that pipe and waits for the process to exit.
// Split out from pagerWriter so the piping mechanics are testable against a
// stand-in command (e.g. "cat") writing to a plain buffer, independent of any
// real terminal or the TTY decision (that lives in shouldPage/pagerWriter).
// An empty argv or a command that fails to start degrades to dst unchanged
// with a no-op cleanup and a non-nil error, so callers can fall back to
// plain output rather than losing it.
func startPager(argv []string, dst io.Writer) (io.Writer, func() error, error) {
	if len(argv) == 0 {
		return dst, func() error { return nil }, nil
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = dst
	cmd.Stderr = os.Stderr
	pipe, err := cmd.StdinPipe()
	if err != nil {
		return dst, func() error { return nil }, err
	}
	if err := cmd.Start(); err != nil {
		return dst, func() error { return nil }, err
	}
	return pipe, func() error {
		closeErr := pipe.Close()
		waitErr := cmd.Wait()
		if closeErr != nil {
			return closeErr
		}
		return waitErr
	}, nil
}

// shouldPage reports whether out is the process's real, unredirected
// os.Stdout AND that stdout is attached to a terminal — the only situation
// where inserting a pager is safe. Any other destination (a test's captured
// buffer via cmd.SetOut, or a real `> file` / `| jq` pipe, which replaces
// os.Stdout at the fd level and so fails the TTY check) must never be paged,
// which is what keeps structured formats and non-interactive invocations
// pipeable.
func shouldPage(out io.Writer) bool {
	return out == io.Writer(os.Stdout) && stdoutIsTerminal()
}

// pagerWriter is the seam `session list --full` / `session query --full`
// call for text/markdown output: when shouldPage(out) holds, it starts the
// resolved pager (see resolvePagerCommand) writing to out and returns a
// writer piping into it; otherwise (or if the pager fails to start — e.g.
// $PAGER names a missing binary) it hands back out unchanged. Structured
// formats (json/yaml/toml) never call this at all — see emitSessionRows.
func pagerWriter(out io.Writer) (io.Writer, func() error) {
	if !shouldPage(out) {
		return out, func() error { return nil }
	}
	return startPagerOrFallback(resolvePagerCommand(), out)
}

// startPagerOrFallback starts argv as a pager writing to dst, degrading to dst
// unpaged when it cannot start. Content is never lost either way; the pager is
// a presentation convenience, not a delivery path.
//
// The failure is announced rather than swallowed. Unpaged output is not
// self-evidently wrong — it looks like a short result — so a $PAGER naming a
// binary that is missing or not executable stays broken indefinitely unless
// something says so. The diagnostic goes to the clidiag sink, never to dst,
// so it cannot contaminate the content being paged.
func startPagerOrFallback(argv []string, dst io.Writer) (io.Writer, func() error) {
	w, cleanup, err := startPager(argv, dst)
	if err != nil {
		clidiag.Warn("ctxloom", "could not start the pager %q (%v); showing unpaged output — check $PAGER", strings.Join(argv, " "), err)
		return dst, func() error { return nil }
	}
	return w, cleanup
}
