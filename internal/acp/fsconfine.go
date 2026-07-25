package acp

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// This file is the ONE confinement boundary for everything the ACP client
// serves out of the filesystem on behalf of a connected engine
// (fs/read_text_file, fs/write_text_file — session.go's handleFsRead /
// handleFsWrite). Both handlers funnel through confineToWorkspace BEFORE
// they branch on the fs-upstream link, so neither the local-disk path nor
// the editor-chained path can become a second, unconfined way out, and a
// future fs/* handler has exactly one function to call.
//
// WHY (finding S3 / FINDINGS.md T13). The handlers used to hand req.Path
// straight to os.ReadFile / os.WriteFile. That is survivable only while the
// engine is guaranteed to sit on the same host, in the same trust domain,
// as this driver — which is true today ONLY because ChatStart drops the
// runtime axis on the gRPC wire. Restoring that field puts the engine in a
// container while this driver stays on the host, and the first
// `fs/read_text_file {"path": "~/.claude/.credentials.json"}` would defeat
// the isolation the restoration exists to provide. The confinement below
// does not depend on that ordering: it is correct whether or not the engine
// is containerized.
//
// The rules, all of which fail CLOSED:
//
//   - The path must be absolute. The ACP schema types both handlers' `path`
//     as an absolute path, so a relative one is malformed input. It is
//     REFUSED rather than resolved: resolving it against the workspace root
//     would silently rewrite the engine's request into a different file
//     than it named, and resolving it against the process cwd is the actual
//     defect (an unconfined relative write from this package's own tests
//     landed inside the repository).
//   - Symlinks are resolved BEFORE the decision, on both the root and the
//     candidate, so containment is judged on real paths. A lexical check
//     passes a link that lives inside the root and points out of it — the
//     shape that already shipped once here as copyCredentialFile (S5).
//   - A missing FINAL component is tolerated (a write creating a new file);
//     a missing or unreadable ancestor, an unresolvable root, or a symlink
//     loop denies. os.WriteFile would fail on a missing parent anyway, so
//     nothing legitimate is lost by refusing first.
//
// Containment itself is expressed the same way internal/cli/mcp_runner.go's
// resolveCellPath expresses the delegation cell boundary — same separator
// handling, same filesystem-root edge case — deliberately, so this codebase
// has one shape of confinement test rather than two.

// maxSymlinkHops bounds manual link resolution. filepath.EvalSymlinks has
// its own internal bound; this one only covers the dangling-leaf loop below,
// where we follow links ourselves because EvalSymlinks refuses to.
const maxSymlinkHops = 40

// confineToWorkspace resolves an engine-supplied path against the session's
// workspace root and returns the REAL path to operate on, or an error that
// denies the request. Callers must use the returned path for the syscall —
// it is the one that was actually checked.
//
// root is agent.ChatRequest.WorkDir. An empty root means the driver was
// given no explicit workspace, which is exactly the condition under which
// the engine subprocess is spawned with an empty cmd.Dir and therefore
// inherits this process's cwd; filepath.Abs("") yields that same cwd, so
// the boundary still matches the engine's actual working directory rather
// than guessing at one.
func confineToWorkspace(root, path string) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path %q must be absolute (the ACP fs/* methods take absolute paths); ctxloom will not resolve it against a root the engine did not name", path)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		// A root we cannot resolve is a boundary we cannot enforce. Deny.
		return "", fmt.Errorf("resolve workspace root %q: %w", absRoot, err)
	}
	real, err := resolveRealPath(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	if !withinRoot(realRoot, real) {
		return "", fmt.Errorf("path %q is outside this session's workspace %q", path, realRoot)
	}
	return real, nil
}

// withinRoot reports whether real is root itself or lies beneath it. Both
// arguments must already be absolute, cleaned, symlink-resolved paths.
func withinRoot(root, real string) bool {
	if real == root {
		return true
	}
	// The prefix a path under root must start with. Appending the separator
	// unconditionally breaks when root IS the filesystem root ("/"): root is
	// already "/", so root+separator would be "//", a prefix no real path
	// has — rejecting everything. (Same edge case, same handling, as
	// internal/cli/mcp_runner.go's resolveCellPath.)
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(real, prefix)
}

// resolveRealPath is filepath.EvalSymlinks extended to tolerate a path whose
// FINAL component does not exist yet — the ordinary case for a write that
// creates a file. It never tolerates a symlink it has not seen through: a
// DANGLING link (EvalSymlinks reports ENOENT for those too) is followed
// manually, so the caller decides containment on the link's target and not
// on the link's own location.
//
// path must be absolute and already cleaned.
func resolveRealPath(path string) (string, error) {
	for range maxSymlinkHops {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			return resolved, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err // walked to the filesystem root without resolving
		}
		// The parent must exist and resolve on its own. If it does not, the
		// syscall this guards would fail anyway, and we refuse rather than
		// reason about a tree we cannot see.
		realParent, perr := filepath.EvalSymlinks(parent)
		if perr != nil {
			return "", perr
		}
		candidate := filepath.Join(realParent, filepath.Base(path))
		info, lerr := os.Lstat(candidate)
		if lerr != nil {
			if errors.Is(lerr, fs.ErrNotExist) {
				return candidate, nil // genuinely absent leaf under a real parent
			}
			return "", lerr
		}
		if info.Mode()&os.ModeSymlink == 0 {
			// Exists, is not a link, yet EvalSymlinks said ENOENT: treat the
			// disagreement as unresolvable rather than guessing.
			return "", fmt.Errorf("cannot resolve %q", path)
		}
		target, rerr := os.Readlink(candidate)
		if rerr != nil {
			return "", rerr
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(realParent, target)
		}
		path = filepath.Clean(target)
	}
	return "", errors.New("too many levels of symbolic links")
}
