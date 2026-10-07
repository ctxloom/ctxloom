package operations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/ident"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
      TOKEN: SECRET-ENV-ONE
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
      TOKEN: SECRET-ENV-TWO
  hosted:
    url: https://mcp.example.test/sse
    headers:
      Authorization: Bearer SECRET-HDR
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
	assert.Equal(t, &ExecSpec{Command: "node", Args: []string{"server.js"}, Env: map[string]string{"TOKEN": fp("SECRET-ENV-ONE")}}, srv.Exec.After)
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
		Before: &ExecSpec{Command: "node", Args: []string{"server.js"}, Env: map[string]string{"TOKEN": fp("SECRET-ENV-ONE")}},
		After:  &ExecSpec{Command: "node", Args: []string{"server.js", "--v2"}, Env: map[string]string{"TOKEN": fp("SECRET-ENV-TWO")}},
	}, srv.Exec)
	hosted := itemChange(t, pc, "mcp", "hosted")
	assert.Equal(t, ChangeAdded, hosted.Change)
	assert.Equal(t, &ExecSpec{URL: "https://mcp.example.test/sse", Headers: map[string]string{"Authorization": fp("Bearer SECRET-HDR")}}, hosted.Exec.After)
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
				Before: &ExecSpec{Command: "node", Args: []string{"server.js"}, Env: map[string]string{"TOKEN": "<11111111>", "KEEP": "<22222222>"}},
				After:  &ExecSpec{Command: "node", Args: []string{"server.js", "--v2"}, Env: map[string]string{"TOKEN": "<33333333>", "KEEP": "<22222222>", "NEW": "<44444444>"}},
			}},
			{Kind: "mcp", Name: "hosted", Change: ChangeAdded, Exec: &ExecDelta{
				After: &ExecSpec{URL: "https://mcp.example.test/sse", Headers: map[string]string{"Authorization": "<55555555>"}},
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
      env:
          KEEP: <22222222>
        + NEW: <44444444>
        ~ TOKEN: <11111111> -> <33333333>
  + mcp hosted
      url: https://mcp.example.test/sse
      headers:
          Authorization: <55555555>
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

// fp is the fingerprint a disclosure shows in place of an env or header value,
// computed here independently of the code under test.
func fp(v string) string {
	sum := sha256.Sum256([]byte(v))
	return "<" + hex.EncodeToString(sum[:])[:8] + ">"
}

// No raw env or header value may appear anywhere a disclosure is rendered —
// text or JSON — and a changed value must still read as changed.
func TestDiffPin_MasksEnvAndHeaderValuesInEveryRendering(t *testing.T) {
	d := newDisclosureRepo(t)
	c1 := d.commit(t, kitV1, "one")
	c2 := d.commit(t, kitV2, "two")
	first, err := diffPin(context.Background(), d.cfg, d.pin(t, c1), remote.LockEntry{}, false)
	require.NoError(t, err)
	move, err := diffPin(context.Background(), d.cfg, d.pin(t, c2), remote.LockEntry{SHA: c1}, true)
	require.NoError(t, err)

	var text bytes.Buffer
	WritePinChanges(&text, []PinChange{first, move})
	js, err := json.Marshal([]PinChange{first, move})
	require.NoError(t, err)
	for _, raw := range []string{"SECRET-ENV-ONE", "SECRET-ENV-TWO", "SECRET-HDR"} {
		assert.NotContains(t, text.String(), raw, "text rendering leaks a raw value")
		assert.NotContains(t, string(js), raw, "JSON rendering leaks a raw value")
	}
	assert.Contains(t, text.String(), "~ TOKEN: "+fp("SECRET-ENV-ONE")+" -> "+fp("SECRET-ENV-TWO"))
	assert.Contains(t, text.String(), "Authorization: "+fp("Bearer SECRET-HDR"))
	assert.Contains(t, text.String(), `args: "server.js" -> "server.js" "--v2"`, "args stay in clear")
}

// An unchanged item does not end the listing: every later item of the kind is
// still compared.
func TestDiffItems_UnchangedItemDoesNotHideLaterChanges(t *testing.T) {
	same := bundles.BundleMCP{Command: "node"}
	got := diffItems("mcp",
		map[string]bundles.BundleMCP{"a": same, "b": {Command: "node"}},
		map[string]bundles.BundleMCP{"a": same, "b": {Command: "deno"}},
		mcpExec)
	require.Len(t, got, 1)
	assert.Equal(t, "b", got[0].Name)
	assert.Equal(t, ChangeModified, got[0].Change)
}

// A REMOVED MCP server still discloses what it ran — with its env and header
// values fingerprinted, in the item and in every rendering — and a key a
// two-sided change drops is marked removed, by fingerprint too.
func TestDiffItems_RemovedServerShowsWhatItRanMasked(t *testing.T) {
	got := diffItems("mcp",
		map[string]bundles.BundleMCP{
			"gone": {Command: "node", Env: map[string]string{"TOKEN": "SECRET-GONE"}},
			"kept": {URL: "https://mcp.example.test", Headers: map[string]string{"Authorization": "SECRET-DROPPED", "X-Keep": "SECRET-KEEP"}},
		},
		map[string]bundles.BundleMCP{
			"kept": {URL: "https://mcp.example.test", Headers: map[string]string{"X-Keep": "SECRET-KEEP"}},
		},
		mcpExec)
	require.Len(t, got, 2)
	gone := got[0]
	assert.Equal(t, "gone", gone.Name)
	assert.Equal(t, ChangeRemoved, gone.Change)
	require.NotNil(t, gone.Exec, "a removed server still says what it ran")
	assert.Equal(t, &ExecSpec{Command: "node", Env: map[string]string{"TOKEN": fp("SECRET-GONE")}}, gone.Exec.Before)
	assert.Nil(t, gone.Exec.After)

	var text bytes.Buffer
	WritePinChanges(&text, []PinChange{{Identity: "corp/kit", FromSHA: "1111111111", ToSHA: "2222222222", Items: got}})
	js, err := json.Marshal(got)
	require.NoError(t, err)
	for _, raw := range []string{"SECRET-GONE", "SECRET-DROPPED", "SECRET-KEEP"} {
		assert.NotContains(t, text.String(), raw, "text rendering leaks a raw value")
		assert.NotContains(t, string(js), raw, "JSON rendering leaks a raw value")
	}
	assert.Contains(t, text.String(), "  - mcp gone\n      command: node\n      env:\n          TOKEN: "+fp("SECRET-GONE")+"\n")
	assert.Contains(t, text.String(), "        - Authorization: "+fp("SECRET-DROPPED")+"\n")
	assert.Contains(t, text.String(), "          X-Keep: "+fp("SECRET-KEEP")+"\n")
}

// pinChanges lists every new or moved pin in ref order, never a kept one, and
// lists a pin whose content cannot be read by its header, warning the reason.
func TestPinChanges_RefOrderedAndUnreadablePinsWarned(t *testing.T) {
	testsupport.Isolate(t)
	for _, ref := range []string{"corp/added", "corp/moved"} {
		_, err := remote.ParseReference(ref)
		require.Error(t, err, "%s must be unreadable, or this test reaches the network", ref)
	}
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()
	before := &remote.Lockfile{Bundles: map[ident.BundleKey]remote.LockEntry{
		"corp/kept":  {SHA: "1111111111"},
		"corp/moved": {SHA: "2222222222"},
	}}
	after := &remote.Lockfile{Bundles: map[ident.BundleKey]remote.LockEntry{
		"corp/kept":  {SHA: "1111111111"},
		"corp/moved": {SHA: "3333333333"},
		"corp/added": {SHA: "4444444444"},
	}}

	got := pinChanges(context.Background(), testConfigWithSCMPath(t.TempDir()), before, after)

	require.Len(t, got, 2)
	assert.Equal(t, "corp/added", got[0].Identity)
	assert.Equal(t, "", got[0].FromSHA)
	assert.Equal(t, "corp/moved", got[1].Identity)
	assert.Equal(t, "2222222222", got[1].FromSHA)
	assert.Equal(t, "3333333333", got[1].ToSHA)
	assert.Contains(t, buf.String(), "could not list what corp/added at 4444444 brings in")
	assert.Contains(t, buf.String(), "could not list what corp/moved at 3333333 brings in")
	assert.NotContains(t, buf.String(), "corp/kept")
}
