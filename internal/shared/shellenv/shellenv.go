// Package shellenv resolves the user's LOGIN SHELL PATH so ctxloom (and the
// engine binaries it spawns) can find binaries installed via login-shell rc
// files (nvm, rbenv, homebrew, ~/go/bin, ~/.cargo/bin) even when ctxloom
// itself was launched with a minimal environment. A detached GUI launch (a
// Dock/Finder icon, a desktop launcher, an editor extension host) inherits a
// bare PATH that omits those additions — sometimes with $SHELL itself unset —
// so a bare-name spawn ("claude", "codex", "kiro-cli", ...) fails with ENOENT
// even though the binary is on the user's ordinary interactive PATH. This is
// the exact class of bug ctxloom-vscode already fixed for its OWN spawns
// (src/shell-env.ts, commit 9dbaa40, mirroring VS Code's own
// getUnixShellEnvironment). This package closes the same gap one process
// down, inside ctxloom's own engine launcher, so `ctxloom run` is not
// dependent on its caller having already done this resolution.
package shellenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/stderrtail"
	"github.com/ctxloom/ctxloom/internal/shared/textutil"
)

// resolveTimeout bounds the login-shell probe so a slow or hanging rc file
// never stalls an engine launch — mirrors ctxloom-vscode's
// RESOLVE_TIMEOUT_MS.
const resolveTimeout = 10 * time.Second

// probeStderrBudget bounds how much of the shell's stderr is retained and
// then reported. Enough to carry the rc-file line that names the failure;
// small enough that the error stays readable wherever it surfaces, since a
// startup file can print a great deal on the way to failing.
const probeStderrBudget = 512

// execCommandContext is the seam tests override to avoid actually spawning a
// shell — production points it at exec.CommandContext (the same pattern
// internal/cli/run.go's execCommand var uses for shellOutDistill).
var execCommandContext = exec.CommandContext

// cache holds the login shell's resolved PATH, computed at most once per
// process. A nil err with empty path is a valid "resolved to nothing"
// outcome; the zero cache (before the first Resolve call needing it) is
// distinguished by done being false.
type cache struct {
	mu   sync.Mutex
	done bool
	path string
	err  error
}

var shellPathCache cache

// resetCacheForTest clears the memoized login-shell PATH so tests can rerun
// resolution under a fresh execCommandContext fake. Unexported: only
// shellenv's own tests (same package) call it.
func resetCacheForTest() {
	shellPathCache = cache{}
}

// Resolve returns the absolute path to spawn for the named binary. A name
// that already contains a path separator (relative or absolute) is returned
// unchanged — exec.Command applies the same rule, so this never overrides an
// operator-configured explicit path. Otherwise it first tries the ordinary
// exec.LookPath against the process's own inherited PATH (the fast, common
// case — costs nothing extra when it succeeds); on failure it falls back to
// the user's login+interactive shell's PATH (resolved once, cached) and
// retries there. If neither resolves the name, the ORIGINAL exec.LookPath
// error is returned — this function only ever WIDENS what resolves, never
// narrows or reshapes the failure a caller would otherwise see.
func Resolve(name string) (string, error) {
	if strings.ContainsRune(name, os.PathSeparator) {
		return name, nil
	}
	// Kept, not re-derived. exec.LookPath stats every directory in PATH, and
	// on the failure path it was previously called a second time purely to
	// reconstruct the error the first call had already produced and dropped —
	// paying the whole PATH walk again for a value that was in hand. Holding
	// it also makes the promise above literal: the error a caller sees is the
	// one that actually happened, not a fresh one from a re-run that could
	// disagree with it.
	p, lookErr := exec.LookPath(name)
	if lookErr == nil {
		return p, nil
	}
	pathEnv, err := loginShellPath()
	if err != nil || pathEnv == "" {
		return "", lookErr
	}
	if p, err := lookPathIn(name, pathEnv); err == nil {
		return p, nil
	}
	return "", lookErr
}

// lookPathIn searches name across the directories in pathEnv (a
// PATH-formatted string, filepath.ListSeparator-joined), the same rule
// exec.LookPath applies to os.Getenv("PATH") — but against an EXPLICIT PATH
// string instead, since the process's own environment is exactly what
// failed to resolve name in the first place.
func lookPathIn(name, pathEnv string) (string, error) {
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s: not found in login-shell PATH", name)
}

// isExecutableFile reports whether path exists, is a regular file, and is
// executable BY THIS PROCESS — exec.LookPath's own criterion for a PATH hit,
// applied here against a directory drawn from the login-shell probe rather
// than os.Getenv("PATH").
//
// "By this process" is the load-bearing half. lookPathIn returns the first
// accepting directory, so accepting a file the current user cannot exec lets
// it SHADOW the real binary further down the PATH, and the launch then fails
// with EACCES on a path ctxloom itself chose — a worse outcome than the ENOENT
// this package exists to prevent, because it looks like a successful find.
func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return currentUserCanExecute(path)
}

// loginShellPath returns the login shell's PATH, probed at most once per
// process and cached thereafter (including a cached failure — a hung or
// missing shell doesn't retry on every subsequent engine launch this run).
func loginShellPath() (string, error) {
	shellPathCache.mu.Lock()
	defer shellPathCache.mu.Unlock()
	if !shellPathCache.done {
		shellPathCache.path, shellPathCache.err = probeLoginShellPath()
		shellPathCache.done = true
	}
	return shellPathCache.path, shellPathCache.err
}

// pathSentinelBegin/pathSentinelEnd fence the probed PATH value against
// anything else the shell's startup files print to stdout (U117-F01): a
// login+interactive invocation SOURCES the user's real rc files, and an rc
// file printing a banner, an update nag, or a fastfetch/neofetch splash on
// every interactive start is common — un-fenced, that text was taken
// verbatim as PATH. Sufficiently unlikely to appear in real shell output that
// no quoting/escaping is needed around them.
const (
	pathSentinelBegin = "__ctxloom_shellenv_path_begin__"
	pathSentinelEnd   = "__ctxloom_shellenv_path_end__"
)

// probeLoginShellPath runs the user's login+interactive shell once to print
// its resolved PATH. POSIX-only (mirrors ctxloom-vscode's shell-env.ts,
// which skips win32 outright — Windows has no login-shell rc-file PATH
// convention to recover here). $SHELL empty (the "GUI launch didn't even set
// $SHELL" case) falls back to /bin/bash, same as the TS resolver.
func probeLoginShellPath() (string, error) {
	if runtime.GOOS == "windows" {
		return "", errors.New("login-shell PATH resolution is POSIX-only")
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()

	probe := fmt.Sprintf("echo %s; echo \"$PATH\"; echo %s", pathSentinelBegin, pathSentinelEnd)
	cmd := execCommandContext(ctx, shell, loginShellArgs(shell, probe)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	// The probe sources the user's REAL rc files, so a failure has almost
	// always already been diagnosed by the shell itself — a syntax error with
	// a file and line, an nvm/rbenv init that aborted, a missing command. An
	// exit status alone cannot name which startup file broke a feature whose
	// whole point is to be invisible when it works. Captured, not tee'd: this
	// runs on every engine launch, so rc-file noise must not reach the
	// operator's terminal on the paths that succeed.
	stderr := stderrtail.New(probeStderrBudget)
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if tail := stderr.Tail(); tail != "" {
			return "", fmt.Errorf("login shell PATH probe (%s) failed: %w: %s", shell, err, textutil.TruncateBytes(tail, probeStderrBudget))
		}
		return "", fmt.Errorf("login shell PATH probe (%s) failed: %w (the shell wrote nothing to stderr)", shell, err)
	}
	return extractFencedPath(out.String())
}

// extractFencedPath pulls the PATH value from between the two sentinel
// lines, discarding anything printed before or after them by the shell's own
// startup files. Returns an error (never a banner-polluted guess) when
// either sentinel is missing — e.g. an rc file that redirects or swallows
// stdout for a non-interactive-looking probe.
func extractFencedPath(raw string) (string, error) {
	begin := strings.Index(raw, pathSentinelBegin)
	if begin == -1 {
		return "", fmt.Errorf("login shell PATH probe: begin marker not found in shell output (a startup file may have swallowed or redirected stdout): %q", textutil.TruncateBytes(raw, 200))
	}
	rest := raw[begin+len(pathSentinelBegin):]
	end := strings.Index(rest, pathSentinelEnd)
	if end == -1 {
		return "", fmt.Errorf("login shell PATH probe: end marker not found in shell output: %q", textutil.TruncateBytes(raw, 200))
	}
	return strings.TrimSpace(rest[:end]), nil
}

// loginShellArgs builds the shell invocation that sources the user's startup
// files before printing PATH. Login+interactive (-l -i) for bash/zsh/sh so
// both .zprofile/.profile (login) and .zshrc/.bashrc (interactive) contribute
// — the additions this whole package exists to recover typically live in one
// or the other. fish/tcsh don't accept -l here, so interactive only —
// mirrors ctxloom-vscode's shellArgs (src/shell-env.ts).
//
// The classification is trimmed and case-folded because $SHELL is user-supplied
// environment: a case-insensitive filesystem (APFS by default) resolves
// /bin/Fish to fish, and a mis-set export leaves surrounding whitespace. Either
// shape misclassified makes the probe FAIL — fish rejects -l — and the PATH
// recovery this package exists for then silently does nothing.
func loginShellArgs(shell, cmd string) []string {
	switch strings.ToLower(filepath.Base(strings.TrimSpace(shell))) {
	case "fish", "tcsh":
		return []string{"-i", "-c", cmd}
	default:
		return []string{"-l", "-i", "-c", cmd}
	}
}
