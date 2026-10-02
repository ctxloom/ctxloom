package claude

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// Trust is claude's repository-trust verdict: the human's own answer to
// claude's workspace-trust prompt, as claude recorded it.
func (c Claude) Trust() engine.Declared[engine.RepoTrust] {
	return engine.Provide[engine.RepoTrust](claudeRepoTrust{})
}

// claudeRepoTrust reads projects[<dir>].hasTrustDialogAccepted out of the
// human's own ~/.claude.json, over the directories claude itself consults
// for a working directory (trustKeys). ctxloom never writes that file, so
// the only way a repository becomes trusted is the human accepting claude's
// prompt in their own claude.
type claudeRepoTrust struct{}

var _ engine.RepoTrust = claudeRepoTrust{}

// Verdict is TrustTrusted when any directory claude would consult for
// q.WorkDir carries an accepted answer. No host home, no host file, or no
// answer is TrustUntrusted; a host file that does not parse is an error.
func (claudeRepoTrust) Verdict(fs afero.Fs, q engine.TrustQuery) (engine.WorkspaceTrust, error) {
	if q.HostHome == "" || q.WorkDir == "" {
		return engine.TrustUntrusted, nil
	}
	fs = agent.GetFS(fs)
	cfg, err := loadJSONObject(fs, filepath.Join(q.HostHome, hostConfigRelPath))
	if err != nil {
		return engine.TrustUntrusted, fmt.Errorf("claude repo trust: read the host %s: %w", hostConfigRelPath, err)
	}
	projects, _ := cfg["projects"].(map[string]any)
	workDir, err := filepath.Abs(q.WorkDir)
	if err != nil {
		return engine.TrustUntrusted, fmt.Errorf("claude repo trust: %w", err)
	}
	for _, dir := range trustKeys(fs, filepath.Clean(workDir)) {
		entry, _ := projects[dir].(map[string]any)
		if accepted, _ := entry[trustAcceptedKey].(bool); accepted {
			return engine.TrustTrusted, nil
		}
	}
	return engine.TrustUntrusted, nil
}

// trustAcceptedKey is claude's per-project workspace-trust flag.
const trustAcceptedKey = "hasTrustDialogAccepted"

// trustKeys are the project keys claude consults for workDir (read from the
// 2.1.286 bundle): the canonical repository root — the main checkout a
// worktree's common dir belongs to — and every directory from workDir up to
// its repository's top level, inclusive. Outside a repository the walk runs
// to the filesystem root. The walk stops at the top level on purpose: a
// trusted ~/src does not trust a repository cloned beneath it.
func trustKeys(fs afero.Fs, workDir string) []string {
	top, dotGit, inRepo := repoTop(fs, workDir)
	var keys []string
	if inRepo {
		if root := canonicalRoot(fs, top, dotGit); root != "" {
			keys = append(keys, root)
		}
	}
	for dir := workDir; ; dir = filepath.Dir(dir) {
		keys = append(keys, dir)
		if (inRepo && dir == top) || filepath.Dir(dir) == dir {
			return keys
		}
	}
}

// repoTop is the nearest directory at or above dir holding a .git entry, and
// that entry's path.
func repoTop(fs afero.Fs, dir string) (top, dotGit string, ok bool) {
	for d := dir; ; d = filepath.Dir(d) {
		p := filepath.Join(d, ".git")
		if _, err := fs.Stat(p); err == nil {
			return d, p, true
		}
		if filepath.Dir(d) == d {
			return "", "", false
		}
	}
}

// canonicalRoot is the main checkout of the repository whose top level is
// top: top itself when .git is a directory; for a worktree (.git a
// "gitdir:" file) the parent of the common dir its admin dir names. "" when
// that cannot be read (a bare common dir, a submodule's private gitdir).
func canonicalRoot(fs afero.Fs, top, dotGit string) string {
	info, err := fs.Stat(dotGit)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return top
	}
	data, err := afero.ReadFile(fs, dotGit)
	if err != nil {
		return ""
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return ""
	}
	gitdir = absUnder(top, strings.TrimSpace(gitdir))
	common, err := afero.ReadFile(fs, filepath.Join(gitdir, "commondir"))
	if err != nil {
		return ""
	}
	dir := absUnder(gitdir, strings.TrimSpace(string(common)))
	if filepath.Base(dir) != ".git" {
		return ""
	}
	return filepath.Dir(dir)
}

// absUnder is p made absolute against base when it is relative.
func absUnder(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}

// errUntrustedSettingsPresented refuses an untrusted session whose
// presentations name a settings file on --settings: a flag source is one
// --setting-sources does not filter, so the named file — the unsafe-file
// settings form names the repository's own .claude/settings.json — would
// load the repository's hooks into the session the verdict keeps them out
// of.
var errUntrustedSettingsPresented = errors.New("claude: an untrusted repository's session takes no presented --settings (the file would load whatever the repository committed into it) — deliver settings to the session home instead")

// ErrUntrustedProjectMCP refuses an untrusted session whose presentations
// include the project's own .mcp.json (the unsafe-file MCP approach, which
// names no flag): --strict-mcp-config makes claude ignore that file, so the
// session would launch without ctxloom's servers, and loading it would load
// whatever servers the repository committed beside them.
var ErrUntrustedProjectMCP = errors.New("claude: an untrusted repository's session loads no project .mcp.json (--strict-mcp-config), so the unsafe-file MCP approach would launch it without ctxloom's servers — use the default MCP approach, which names its file on --mcp-config")

// repoSourceArgs keeps an untrusted repository's own surfaces out of a
// launch: settings from the user source alone (the session home, where
// ctxloom's hooks live) and MCP servers from --mcp-config alone. Anything
// but an explicit Trusted verdict is untrusted.
func repoSourceArgs(trust engine.WorkspaceTrust) []string {
	if trust == engine.TrustTrusted {
		return nil
	}
	return []string{flagSettingSources, "user", flagStrictMCPConfig}
}
