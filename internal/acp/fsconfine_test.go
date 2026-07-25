package acp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	api "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// This file is the executable specification for T13 / finding S3: the ACP
// client's fs/read_text_file and fs/write_text_file handlers must serve ONLY
// paths inside the session's workspace root (agent.ChatRequest.WorkDir).
//
// Every test here drives the REAL handler over the REAL jsonrpc connection
// against the REAL OS filesystem (never a fake fs — afero's MemMapFs diverges
// from OsFs precisely on the symlink semantics half of these tests turn on).

// fsFixture is the shared layout: a workspace root the session is confined
// to, and a sibling directory outside it holding the file an escape would
// reach.
type fsFixture struct {
	root    string // the session workspace root (ChatRequest.WorkDir)
	outside string // a sibling directory OUTSIDE the root
	secret  string // an existing file inside outside/
}

func newFsFixture(t *testing.T) fsFixture {
	t.Helper()
	// EvalSymlinks the base so the fixture's own paths are already real —
	// otherwise an escape assertion could pass for the wrong reason on a
	// platform where TMPDIR is itself a symlink.
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	f := fsFixture{
		root:    filepath.Join(base, "workspace"),
		outside: filepath.Join(base, "outside"),
	}
	require.NoError(t, os.MkdirAll(f.root, 0o755))
	require.NoError(t, os.MkdirAll(f.outside, 0o755))
	f.secret = filepath.Join(f.outside, "secret.txt")
	require.NoError(t, os.WriteFile(f.secret, []byte("SECRET: host credential\n"), 0o600))
	return f
}

// startConfinedChat boots a session rooted at f.root with no fs-upstream, so
// the handlers serve local disk (the worktree/container axes).
func startConfinedChat(t *testing.T, f fsFixture) (*chatHarness, func() []agent.ChatEvent) {
	t.Helper()
	h := startChat(t, agent.ChatRequest{WorkDir: f.root})
	events := collect(h.out)
	h.fa.serveHandshake(t)
	return h, events
}

// --- reads: escapes must be denied ---

// TestFsRead_DeniesAbsolutePathOutsideWorkspace is the headline case: the
// engine asks for a well-formed absolute host path that simply is not in the
// workspace (the ~/.claude/.credentials.json shape from S3).
func TestFsRead_DeniesAbsolutePathOutsideWorkspace(t *testing.T) {
	f := newFsFixture(t)
	h, events := startConfinedChat(t, f)

	resp := l0CallClient(h.fa, api.ClientMethodFsReadTextFile, map[string]any{"path": f.secret})
	requireDenied(t, resp, f.secret)

	closeChat(t, h, events)
}

// TestFsRead_DeniesDotDotTraversal: the escape is spelled with ".." from
// inside the root, so it only becomes visible AFTER path cleaning.
func TestFsRead_DeniesDotDotTraversal(t *testing.T) {
	f := newFsFixture(t)
	h, events := startConfinedChat(t, f)

	escape := filepath.Join(f.root, "..", "outside", "secret.txt")
	resp := l0CallClient(h.fa, api.ClientMethodFsReadTextFile, map[string]any{"path": escape})
	requireDenied(t, resp, "SECRET")

	closeChat(t, h, events)
}

// TestFsRead_DeniesSymlinkInsideWorkspacePointingOutside: a lexical check
// passes this one — every component is under the root — and only resolving
// the link catches it. ctxloom has already shipped one symlink-following
// defect (copyCredentialFile, finding S5); this pins the ACP handlers against
// the same shape.
func TestFsRead_DeniesSymlinkInsideWorkspacePointingOutside(t *testing.T) {
	f := newFsFixture(t)
	link := filepath.Join(f.root, "innocent.txt")
	require.NoError(t, os.Symlink(f.secret, link))

	h, events := startConfinedChat(t, f)

	resp := l0CallClient(h.fa, api.ClientMethodFsReadTextFile, map[string]any{"path": link})
	requireDenied(t, resp, "SECRET")

	closeChat(t, h, events)
}

// TestFsRead_DeniesSymlinkedDirectoryEscape: the link is a DIRECTORY inside
// the root, so the escaping component is not the leaf.
func TestFsRead_DeniesSymlinkedDirectoryEscape(t *testing.T) {
	f := newFsFixture(t)
	require.NoError(t, os.Symlink(f.outside, filepath.Join(f.root, "elsewhere")))

	h, events := startConfinedChat(t, f)

	resp := l0CallClient(h.fa, api.ClientMethodFsReadTextFile, map[string]any{
		"path": filepath.Join(f.root, "elsewhere", "secret.txt"),
	})
	requireDenied(t, resp, "SECRET")

	closeChat(t, h, events)
}

// TestFsRead_DeniesRelativePath pins the deliberate decision on relative
// paths: the ACP schema types ReadTextFileRequest.path as an ABSOLUTE path,
// so a relative one is malformed input and is refused rather than silently
// resolved against some root the engine never named.
//
// The probe is a file that exists relative to the DRIVER PROCESS's cwd and
// not in the workspace — which is exactly what an unconfined handler serves,
// so this test is red for the right mechanism rather than for an incidental
// ENOENT.
func TestFsRead_DeniesRelativePath(t *testing.T) {
	f := newFsFixture(t)
	h, events := startConfinedChat(t, f)

	// "session.go" is this package's own source: present in the test binary's
	// cwd, absent from the workspace root.
	resp := l0CallClient(h.fa, api.ClientMethodFsReadTextFile, map[string]any{"path": "session.go"})
	requireDenied(t, resp, "package acp")

	closeChat(t, h, events)
}

// TestFsRead_DeniesEmptyPath: fail closed on absent input rather than
// resolving to the root directory.
func TestFsRead_DeniesEmptyPath(t *testing.T) {
	f := newFsFixture(t)
	h, events := startConfinedChat(t, f)

	resp := l0CallClient(h.fa, api.ClientMethodFsReadTextFile, map[string]any{"path": ""})
	require.NotNil(t, resp.Error, "an empty path must be refused, not resolved")

	closeChat(t, h, events)
}

// --- reads: legitimate use must keep working ---

// TestFsRead_AllowsPathInsideWorkspace is the anti-regression half. A
// confinement fix that breaks ordinary reads is worse than the hole.
func TestFsRead_AllowsPathInsideWorkspace(t *testing.T) {
	f := newFsFixture(t)
	require.NoError(t, os.MkdirAll(filepath.Join(f.root, "pkg", "sub"), 0o755))
	inside := filepath.Join(f.root, "pkg", "sub", "file.go")
	require.NoError(t, os.WriteFile(inside, []byte("package sub\n"), 0o644))

	h, events := startConfinedChat(t, f)

	resp := l0CallClient(h.fa, api.ClientMethodFsReadTextFile, map[string]any{"path": inside})
	require.Nil(t, resp.Error, "a path genuinely inside the workspace must still be served")
	var got api.ReadTextFileResponse
	require.NoError(t, json.Unmarshal(resp.Result, &got))
	assert.Equal(t, "package sub\n", got.Content)

	closeChat(t, h, events)
}

// TestFsRead_AllowsSymlinkInsideWorkspace: resolving links must not turn an
// INTERNAL link into a denial — only escapes are denied.
func TestFsRead_AllowsSymlinkInsideWorkspace(t *testing.T) {
	f := newFsFixture(t)
	target := filepath.Join(f.root, "real.txt")
	require.NoError(t, os.WriteFile(target, []byte("internal\n"), 0o644))
	link := filepath.Join(f.root, "alias.txt")
	require.NoError(t, os.Symlink(target, link))

	h, events := startConfinedChat(t, f)

	resp := l0CallClient(h.fa, api.ClientMethodFsReadTextFile, map[string]any{"path": link})
	require.Nil(t, resp.Error, "a link that stays inside the workspace must still resolve")
	var got api.ReadTextFileResponse
	require.NoError(t, json.Unmarshal(resp.Result, &got))
	assert.Equal(t, "internal\n", got.Content)

	closeChat(t, h, events)
}

// TestFsRead_AllowsSymlinkedWorkspaceRootSpelling: the WorkDir the session
// was handed may itself be reached through a symlink (a common shape for
// worktrees and for /tmp on some platforms) while the engine names the real
// path — or vice versa. Both spellings denote the same root and must be
// accepted; a naive lexical prefix test rejects one of them.
func TestFsRead_AllowsSymlinkedWorkspaceRootSpelling(t *testing.T) {
	f := newFsFixture(t)
	inside := filepath.Join(f.root, "file.txt")
	require.NoError(t, os.WriteFile(inside, []byte("real root\n"), 0o644))

	aliasRoot := filepath.Join(filepath.Dir(f.root), "workspace-alias")
	require.NoError(t, os.Symlink(f.root, aliasRoot))

	// Session rooted at the ALIAS; the engine names the REAL path.
	h := startChat(t, agent.ChatRequest{WorkDir: aliasRoot})
	events := collect(h.out)
	h.fa.serveHandshake(t)

	resp := l0CallClient(h.fa, api.ClientMethodFsReadTextFile, map[string]any{"path": inside})
	require.Nil(t, resp.Error, "the real spelling of a symlinked workspace root must be accepted")
	var got api.ReadTextFileResponse
	require.NoError(t, json.Unmarshal(resp.Result, &got))
	assert.Equal(t, "real root\n", got.Content)

	closeChat(t, h, events)
}

// --- writes ---

// TestFsWrite_DeniesAbsolutePathOutsideWorkspace: the write half of the
// headline case. The denial is proven by the file's bytes, not just by the
// error — a handler that errors AFTER writing would still be a breach.
func TestFsWrite_DeniesAbsolutePathOutsideWorkspace(t *testing.T) {
	f := newFsFixture(t)
	h, events := startConfinedChat(t, f)

	resp := l0CallClient(h.fa, api.ClientMethodFsWriteTextFile, map[string]any{
		"path": f.secret, "content": "OWNED",
	})
	require.NotNil(t, resp.Error, "a write outside the workspace must be refused")
	body, err := os.ReadFile(f.secret)
	require.NoError(t, err)
	assert.Equal(t, "SECRET: host credential\n", string(body), "the outside file must be untouched")

	closeChat(t, h, events)
}

// TestFsWrite_DeniesDotDotTraversal: the write escapes via ".." to a file
// that does not exist yet, so the check must handle a MISSING destination
// without falling open.
func TestFsWrite_DeniesDotDotTraversal(t *testing.T) {
	f := newFsFixture(t)
	h, events := startConfinedChat(t, f)

	dest := filepath.Join(f.root, "..", "outside", "planted.txt")
	resp := l0CallClient(h.fa, api.ClientMethodFsWriteTextFile, map[string]any{
		"path": dest, "content": "OWNED",
	})
	require.NotNil(t, resp.Error, "a traversing write must be refused")
	_, statErr := os.Stat(filepath.Join(f.outside, "planted.txt"))
	assert.True(t, os.IsNotExist(statErr), "nothing may be created outside the workspace")

	closeChat(t, h, events)
}

// TestFsWrite_DeniesSymlinkInsideWorkspacePointingOutside: a DANGLING
// symlink inside the root whose target does not exist yet. os.WriteFile
// follows it and creates the target outside the root — the exact
// copyCredentialFile (S5) primitive, here in the ACP handler.
func TestFsWrite_DeniesSymlinkInsideWorkspacePointingOutside(t *testing.T) {
	f := newFsFixture(t)
	victim := filepath.Join(f.outside, "victim.txt")
	link := filepath.Join(f.root, "notes.txt")
	require.NoError(t, os.Symlink(victim, link)) // dangling: victim does not exist

	h, events := startConfinedChat(t, f)

	resp := l0CallClient(h.fa, api.ClientMethodFsWriteTextFile, map[string]any{
		"path": link, "content": "OWNED",
	})
	require.NotNil(t, resp.Error, "a write through a link that leaves the workspace must be refused")
	_, statErr := os.Stat(victim)
	assert.True(t, os.IsNotExist(statErr), "the write must not have followed the link out of the workspace")

	closeChat(t, h, events)
}

// TestFsWrite_DeniesRelativePath mirrors the read half's spec decision. The
// unconfined handler resolves a relative write against the DRIVER PROCESS's
// cwd — which for this package is the source tree itself, so the defect
// literally writes into the repository (observed while this test was red).
func TestFsWrite_DeniesRelativePath(t *testing.T) {
	f := newFsFixture(t)
	// Clean up unconditionally: a red run creates this in the package dir.
	t.Cleanup(func() { _ = os.Remove("acp-confinement-probe.txt") })

	h, events := startConfinedChat(t, f)

	resp := l0CallClient(h.fa, api.ClientMethodFsWriteTextFile, map[string]any{
		"path": "acp-confinement-probe.txt", "content": "x",
	})
	require.NotNil(t, resp.Error, "a relative write path must be refused")
	_, statErr := os.Stat("acp-confinement-probe.txt")
	assert.True(t, os.IsNotExist(statErr), "a relative write must not land in the driver process's cwd")

	closeChat(t, h, events)
}

// TestFsWrite_AllowsPathInsideWorkspace: the anti-regression half — creating
// a NEW file under the root (the common case) must still work.
func TestFsWrite_AllowsPathInsideWorkspace(t *testing.T) {
	f := newFsFixture(t)
	require.NoError(t, os.MkdirAll(filepath.Join(f.root, "pkg"), 0o755))
	dest := filepath.Join(f.root, "pkg", "new.go")

	h, events := startConfinedChat(t, f)

	resp := l0CallClient(h.fa, api.ClientMethodFsWriteTextFile, map[string]any{
		"path": dest, "content": "package pkg\n",
	})
	require.Nil(t, resp.Error, "a write inside the workspace must still work")
	body, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "package pkg\n", string(body))

	closeChat(t, h, events)
}

// --- the fs-upstream (editor-chained) branch ---

// TestFsUpstream_ConfinesBeforeChainingToEditor: the confinement decision
// must happen BEFORE the branch, so the editor-chained path cannot become a
// second, unconfined way out. Proven by the editor never seeing the request.
func TestFsUpstream_ConfinesBeforeChainingToEditor(t *testing.T) {
	f := newFsFixture(t)
	up := startFakeFsUpstream(t, "EDITOR: content")

	h := startChat(t, agent.ChatRequest{
		WorkDir: f.root,
		Env:     map[string]string{fsUpstreamEnvVar: up.addr()},
	})
	events := collect(h.out)
	h.fa.serveHandshake(t)

	resp := l0CallClient(h.fa, api.ClientMethodFsWriteTextFile, map[string]any{
		"path": f.secret, "content": "OWNED",
	})
	require.NotNil(t, resp.Error, "the editor-chained branch must be confined too")
	select {
	case w := <-up.writes:
		t.Fatalf("an out-of-workspace path reached the editor: %q", w.Path)
	case <-time.After(200 * time.Millisecond):
	}

	closeChat(t, h, events)
}

// requireDenied asserts the handler refused, and that the refusal did not
// leak the content it was protecting.
func requireDenied(t *testing.T, resp rpcMessage, mustNotContain string) {
	t.Helper()
	require.NotNil(t, resp.Error, "expected a denial, got result %s", string(resp.Result))
	assert.NotContains(t, string(resp.Result), mustNotContain, "the denial must not carry the protected content")
}
