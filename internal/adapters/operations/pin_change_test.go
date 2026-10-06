package operations

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// kitV1 is a bundle carrying one of each executable surface: an MCP server, a
// hook, and a skill whose script is committed executable.
const kitV1 = `version: 1.0.0
description: kit
mcp:
  srv:
    command: node
    args: [server.js]
    env:
      TOKEN: one
hooks:
  session_start:
    - type: command
      command: ./hello.sh
fragments:
  frag:
    content: hello
`

// kitV2 changes every executable surface kitV1 carries, adds a network MCP
// server, and drops the fragment.
const kitV2 = `version: 1.1.0
description: kit
mcp:
  srv:
    command: node
    args: [server.js, --v2]
    env:
      TOKEN: two
  hosted:
    url: https://mcp.example.test/sse
    headers:
      Authorization: Bearer x
hooks:
  session_start:
    - type: command
      command: ./goodbye.sh
`

// disclosureRepo is a file:// repository serving bundle "kit", and a project
// that has it registered.
type disclosureRepo struct {
	repoDir, kitRef string
	cfg             *config.Config
}

func newDisclosureRepo(t *testing.T) *disclosureRepo {
	t.Helper()
	testsupport.Isolate(t)
	repoDir := filepath.Join(t.TempDir(), "source")
	_, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	registerTestRemote(t, appDir, "file://"+repoDir)
	return &disclosureRepo{repoDir: repoDir, kitRef: "file://" + repoDir + "@bundles/kit", cfg: testConfigWithSCMPath(appDir)}
}

// commit replaces the kit tree with doc and a skill whose script prints say,
// and returns the commit.
func (d *disclosureRepo) commit(t *testing.T, doc, say string) string {
	t.Helper()
	authored := authoredV2(filepath.Join(d.repoDir, paths.AppDirName))
	require.NoError(t, os.RemoveAll(filepath.Join(authored, "kit")))
	require.NoError(t, os.MkdirAll(authored, 0o755))
	bundletree.WriteOS(t, authored, "kit", doc, bundletree.WithSkill("runner", map[string]bundletree.File{
		"SKILL.md":       {Body: "---\nname: runner\ndescription: Runs things.\n---\n\nbody\n"},
		"scripts/run.sh": {Body: "#!/bin/sh\necho " + say + "\n", Executable: true},
	}))
	repo, err := git.PlainOpen(d.repoDir)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, wt.AddWithOptions(&git.AddOptions{All: true}))
	sha, err := wt.Commit("kit", &git.CommitOptions{Author: &object.Signature{Name: "test", Email: "test@test.com", When: time.Now()}})
	require.NoError(t, err)
	return sha.String()
}

func (d *disclosureRepo) pin(t *testing.T, sha string) PinnedRef {
	t.Helper()
	return PinnedRef{Identity: lockKeyOf(t, d.kitRef), Hash: sha, URL: "file://" + d.repoDir, Type: remote.ItemTypeBundle}
}

func itemChange(t *testing.T, pc PinChange, kind, name string) ItemChange {
	t.Helper()
	for _, it := range pc.Items {
		if it.Kind == kind && it.Name == name {
			return it
		}
	}
	require.Failf(t, "no such item change", "%s %s in %+v", kind, name, pc.Items)
	return ItemChange{}
}

func fileChange(t *testing.T, pc PinChange, path string) FileChange {
	t.Helper()
	for _, f := range pc.Files {
		if f.Path == path {
			return f
		}
	}
	require.Failf(t, "no such file change", "%s in %+v", path, pc.Files)
	return FileChange{}
}

// A FIRST pin discloses everything the bundle brings in, every executable
// surface included: that is the whole of what the user is adopting.
func TestDiffPin_FirstPinListsEverythingItBringsIn(t *testing.T) {
	d := newDisclosureRepo(t)
	c1 := d.commit(t, kitV1, "one")

	pc, err := diffPin(context.Background(), d.cfg, d.pin(t, c1), remote.LockEntry{}, false)
	require.NoError(t, err)

	assert.Equal(t, "", pc.FromSHA, "a first pin has nothing to come from")
	assert.Equal(t, c1, pc.ToSHA)
	srv := itemChange(t, pc, "mcp", "srv")
	assert.Equal(t, ChangeAdded, srv.Change)
	require.NotNil(t, srv.Exec)
	assert.Nil(t, srv.Exec.Before)
	assert.Equal(t, &ExecSpec{Command: "node", Args: []string{"server.js"}, Env: map[string]string{"TOKEN": "one"}}, srv.Exec.After)
	hook := itemChange(t, pc, "hook", "session_start/0")
	assert.Equal(t, ChangeAdded, hook.Change)
	require.NotNil(t, hook.Exec)
	assert.Equal(t, "./hello.sh", hook.Exec.After.Command)
	assert.Equal(t, ChangeAdded, itemChange(t, pc, "fragment", "frag").Change)
	assert.Equal(t, ChangeAdded, itemChange(t, pc, "skill", "runner").Change)
	assert.Nil(t, itemChange(t, pc, "fragment", "frag").Exec, "only hooks and MCP servers carry an exec spec")

	script := fileChange(t, pc, "skills/runner/scripts/run.sh")
	assert.Equal(t, ChangeAdded, script.Change)
	assert.Contains(t, script.Diff, "+echo one", "a script's content is shown in full")
	manifest := fileChange(t, pc, "bundle.yaml")
	assert.Empty(t, manifest.Diff, "only scripts carry a diff")
}

// An advance shows, for hooks and MCP servers, what they run before and after,
// and for scripts, the unified diff.
func TestDiffPin_AdvanceShowsExecDeltasAndScriptDiffs(t *testing.T) {
	d := newDisclosureRepo(t)
	c1 := d.commit(t, kitV1, "one")
	c2 := d.commit(t, kitV2, "two")

	pc, err := diffPin(context.Background(), d.cfg, d.pin(t, c2), remote.LockEntry{SHA: c1, Version: "v1.0.0"}, true)
	require.NoError(t, err)

	assert.Equal(t, c1, pc.FromSHA)
	assert.Equal(t, c2, pc.ToSHA)
	assert.Equal(t, "v1.0.0", pc.FromVersion)
	srv := itemChange(t, pc, "mcp", "srv")
	assert.Equal(t, ChangeModified, srv.Change)
	assert.Equal(t, &ExecDelta{
		Before: &ExecSpec{Command: "node", Args: []string{"server.js"}, Env: map[string]string{"TOKEN": "one"}},
		After:  &ExecSpec{Command: "node", Args: []string{"server.js", "--v2"}, Env: map[string]string{"TOKEN": "two"}},
	}, srv.Exec)
	hosted := itemChange(t, pc, "mcp", "hosted")
	assert.Equal(t, ChangeAdded, hosted.Change)
	assert.Equal(t, &ExecSpec{URL: "https://mcp.example.test/sse", Headers: map[string]string{"Authorization": "Bearer x"}}, hosted.Exec.After)
	hook := itemChange(t, pc, "hook", "session_start/0")
	assert.Equal(t, ChangeModified, hook.Change)
	assert.Equal(t, "./hello.sh", hook.Exec.Before.Command)
	assert.Equal(t, "./goodbye.sh", hook.Exec.After.Command)
	assert.Equal(t, ChangeRemoved, itemChange(t, pc, "fragment", "frag").Change)
	for _, it := range pc.Items {
		assert.NotEqual(t, "runner", it.Name, "an unchanged skill is not listed")
	}

	script := fileChange(t, pc, "skills/runner/scripts/run.sh")
	assert.Equal(t, ChangeModified, script.Change)
	assert.Contains(t, script.Diff, "-echo one")
	assert.Contains(t, script.Diff, "+echo two")
}

// An advance that changed nothing the bundle carries lists nothing.
func TestDiffPin_UnchangedContentListsNothing(t *testing.T) {
	d := newDisclosureRepo(t)
	c1 := d.commit(t, kitV1, "one")
	c2 := addFileToLocalRepo(t, d.repoDir, "README.md", "unrelated\n")

	pc, err := diffPin(context.Background(), d.cfg, d.pin(t, c2), remote.LockEntry{SHA: c1}, true)
	require.NoError(t, err)
	assert.Empty(t, pc.Items)
	assert.Empty(t, pc.Files)
}

// A script is a file that may be run: executable (declared or committed), or
// under a scripts/ directory. Only scripts carry a diff, and flipping only the
// exec bit is a change.
func TestDiffTreeFiles_DiffsExactlyTheScripts(t *testing.T) {
	from := map[string]remote.TreeFile{
		"bin/flip": {Data: []byte("same\n")},
	}
	to := map[string]remote.TreeFile{
		"bin/tool":       {Data: []byte("run\n"), DeclaredExecutable: true},
		"bin/committed":  {Data: []byte("run\n"), CommittedExecutable: true},
		"scripts/helper": {Data: []byte("help\n")},
		"notes.md":       {Data: []byte("words\n")},
		"bin/flip":       {Data: []byte("same\n"), DeclaredExecutable: true},
	}

	got := map[string]FileChange{}
	for _, fc := range diffTreeFiles(from, to) {
		got[fc.Path] = fc
	}
	assert.Contains(t, got["bin/tool"].Diff, "+run")
	assert.Contains(t, got["bin/committed"].Diff, "+run")
	assert.Contains(t, got["scripts/helper"].Diff, "+help")
	assert.Equal(t, ChangeAdded, got["notes.md"].Change)
	assert.Empty(t, got["notes.md"].Diff)
	assert.Equal(t, ChangeModified, got["bin/flip"].Change, "an exec bit that appears is a change")
}

func TestWritePinChanges_RendersHeaderItemsExecAndScriptDiffs(t *testing.T) {
	var out bytes.Buffer
	WritePinChanges(&out, []PinChange{{
		Identity: "corp/kit", FromSHA: "1111111111", ToSHA: "2222222222", FromVersion: "v1.0.0", ToVersion: "v1.1.0",
		Items: []ItemChange{
			{Kind: "mcp", Name: "srv", Change: ChangeModified, Exec: &ExecDelta{
				Before: &ExecSpec{Command: "node", Args: []string{"server.js"}, Env: map[string]string{"TOKEN": "one"}},
				After:  &ExecSpec{Command: "node", Args: []string{"server.js", "--v2"}, Env: map[string]string{"TOKEN": "two"}},
			}},
			{Kind: "mcp", Name: "hosted", Change: ChangeAdded, Exec: &ExecDelta{
				After: &ExecSpec{URL: "https://mcp.example.test/sse", Headers: map[string]string{"Authorization": "Bearer x"}},
			}},
			{Kind: "fragment", Name: "frag", Change: ChangeRemoved},
		},
		Files: []FileChange{{Path: "scripts/run.sh", Change: ChangeModified, Diff: "--- a/scripts/run.sh\n+++ b/scripts/run.sh\n-echo one\n+echo two\n"}},
	}, {
		Identity: "corp/new", ToSHA: "3333333333", ToVersion: "",
		Items: []ItemChange{{Kind: "hook", Name: "session_start/0", Change: ChangeAdded, Exec: &ExecDelta{After: &ExecSpec{Command: "./hello.sh"}}}},
	}})

	assert.Equal(t, `corp/kit  v1.0.0 -> v1.1.0  (1111111 -> 2222222)
  ~ mcp srv
      command: node
      args: "server.js" -> "server.js" "--v2"
      env: TOKEN=one -> TOKEN=two
  + mcp hosted
      url: https://mcp.example.test/sse
      headers: Authorization: Bearer x
  - fragment frag
  ~ file scripts/run.sh
      --- a/scripts/run.sh
      +++ b/scripts/run.sh
      -echo one
      +echo two
corp/new  first pin -> unversioned  (new -> 3333333)
  + hook session_start/0
      command: ./hello.sh
`, out.String())
}
