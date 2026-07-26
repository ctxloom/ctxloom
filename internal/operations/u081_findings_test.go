package operations

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// ---------------------------------------------------------------------------
// U081-F01 — SetAgent silently destroys fields the caller did not set.
// ---------------------------------------------------------------------------

// TestSetAgent_OmittedFieldsSurvive is the CORRECTED contract for `agent set`
// on an EXISTING binding: a request that carries only the field the user
// actually named must leave every other field alone. The old behaviour was a
// whole-record replace, so `ctxloom agent set dev --runtime container` wiped
// the engine, the profiles, the permission posture, the coordinator flag and
// the approval-escalation ladder — none of which the user mentioned.
func TestSetAgent_OmittedFieldsSurvive(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")
	mgr := managerFor(appDir)

	_, err := SetAgent(mgr, cfg, SetAgentRequest{
		Name:        "dev",
		Engine:      ptr("claude-code"),
		Profiles:    ptr([]string{"go-developer"}),
		Permissions: ptr("acceptEdits"),
		Coordinator: ptr(true),
	})
	require.NoError(t, err)

	// An escalation ladder the request type cannot even express, written by
	// hand into config.yaml the way a user or a bundle would.
	require.NoError(t, mgr.Update(func(d *config.Draft) error {
		a := d.Agents["dev"]
		a.Escalation = []agents.EscalationRung{{Action: "surface_to_human", Role: "parent"}}
		d.Agents["dev"] = a
		return nil
	}))

	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)

	// The user changes ONE axis.
	_, err = SetAgent(mgr, reloaded, SetAgentRequest{Name: "dev", Runtime: ptr("container")})
	require.NoError(t, err)

	final, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	got, ok := final.Agent("dev")
	require.True(t, ok)
	assert.Equal(t, "container", got.Runtime, "the named field must change")
	assert.Equal(t, "claude-code", got.Engine, "engine must survive an unrelated set")
	assert.Equal(t, []string{"go-developer"}, got.Profiles, "profiles must survive an unrelated set")
	assert.Equal(t, "acceptEdits", got.Permissions, "permissions must survive an unrelated set")
	assert.True(t, got.Coordinator, "coordinator flag must survive an unrelated set")
	assert.Len(t, got.Escalation, 1, "the escalation ladder must survive an unrelated set")
}

// TestSetAgent_ExplicitEmptyClears proves "unset" and "clear" stay
// distinguishable: an explicitly-supplied empty value still clears the field,
// which is what the pointer-valued request buys over merge-on-zero.
func TestSetAgent_ExplicitEmptyClears(t *testing.T) {
	cfg, appDir := loadConfigDir(t, "version: 5\n")
	mgr := managerFor(appDir)

	_, err := SetAgent(mgr, cfg, SetAgentRequest{Name: "dev", Engine: ptr("claude-code"), Profiles: ptr([]string{"p"})})
	require.NoError(t, err)
	reloaded, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)

	_, err = SetAgent(mgr, reloaded, SetAgentRequest{Name: "dev", Engine: ptr("")})
	require.NoError(t, err)

	final, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	got, ok := final.Agent("dev")
	require.True(t, ok)
	assert.Empty(t, got.Engine, "an explicit empty engine clears it")
	assert.Equal(t, []string{"p"}, got.Profiles, "clearing one field does not clear another")
}

// ---------------------------------------------------------------------------
// U081-F04 — an empty distillation is reported as a success.
// ---------------------------------------------------------------------------

// emptyDistiller returns no error and no content — the shape a structural
// compressor hits when it compresses to nothing, and the shape the LLM branch
// already guards against.
type emptyDistiller struct{}

func (emptyDistiller) Distill(context.Context, DistillRequest) (DistillResult, error) {
	return DistillResult{Distilled: "", ModelID: "fake-model"}, nil
}

// TestDistillBundleFile_EmptyDistillationIsNotSuccess is the silent-no-op
// guard: zero bytes delivered must be reported as a failure, and the previous
// good distillation must not be overwritten with "".
func TestDistillBundleFile_EmptyDistillationIsNotSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bundle.yaml")
	bundleYAML := `version: 1.0.0
fragments:
  rules:
    content: "fresh content the stale distillation no longer matches"
    distilled: "previously good distilled text"
    distilled_by: "old-model"
    content_hash: "0000000000000000000000000000000000000000000000000000000000000000"
`
	require.NoError(t, os.WriteFile(path, []byte(bundleYAML), 0o644))

	res, err := DistillBundleFile(context.Background(), DistillBundleFileRequest{
		Path:      path,
		Distiller: emptyDistiller{},
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 1)
	item := res.Items[0]
	assert.Equal(t, DistillStatusSkipped, item.Status, "zero bytes distilled is not a success")
	assert.Equal(t, "distill_failed", item.Reason)
	assert.Empty(t, item.ModelID, "a failed distillation must not report a model id")

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(b), "previously good distilled text",
		"a failed distillation must not destroy the previous good one")
}

// TestDistillOutcome_CannotReportSuccessForZeroBytes pins the classifier
// itself: even handed a stamped model id, an empty distilled body may not be
// classified as distilled. The classifier must SEE the content — a classifier
// that cannot is one that can be lied to.
func TestDistillOutcome_CannotReportSuccessForZeroBytes(t *testing.T) {
	got := distillOutcome(ItemKindFragment, "frag", "", "some-model", false)
	assert.Equal(t, DistillStatusSkipped, got.Status)
	assert.Equal(t, "distill_failed", got.Reason)
	assert.Empty(t, got.ModelID)
}

// ---------------------------------------------------------------------------
// U081-F06 — a stat error on the .sig silently downgrades a signed bundle.
// ---------------------------------------------------------------------------

// denyStatFs fails Stat (and therefore afero.Exists) for the named paths with
// a non-IsNotExist error — the EACCES-on-the-directory case, which
// afero.Exists reports as (false, err).
type denyStatFs struct {
	afero.Fs
	deny map[string]error
}

func (f denyStatFs) Stat(name string) (os.FileInfo, error) {
	if err, ok := f.deny[name]; ok {
		return nil, err
	}
	return f.Fs.Stat(name)
}

// TestReadSignature_StatErrorIsLoud is the exact contract readSignature's own
// doc states: a signature that exists but cannot be READ is an error, because
// treating it as absent silently downgrades a signed bundle to an unsigned
// one. Absent ≠ unreadable.
func TestReadSignature_StatErrorIsLoud(t *testing.T) {
	base := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(base, "/in/seed.yaml", []byte("version: 1.0.0\n"), 0644))
	require.NoError(t, afero.WriteFile(base, "/in/seed.yaml.sig", []byte("SIG"), 0644))
	fs := denyStatFs{Fs: base, deny: map[string]error{"/in/seed.yaml.sig": os.ErrPermission}}

	_, err := readSignature(fs, "/in/seed.yaml")
	require.Error(t, err, "an unreadable signature must not read as unsigned")
	assert.Contains(t, err.Error(), "seed.yaml.sig")
}

// TestReadSignature_AbsentIsNotAnError is the control: genuinely unsigned is
// normal and must stay silent.
func TestReadSignature_AbsentIsNotAnError(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/in/seed.yaml", []byte("version: 1.0.0\n"), 0644))
	sig, err := readSignature(fs, "/in/seed.yaml")
	require.NoError(t, err)
	assert.Nil(t, sig)
}

// TestImportBundle_UnreadableSignatureFailsLoudly walks the same failure up
// through a real caller: import must refuse rather than land the bundle with
// its trust quietly stripped.
func TestImportBundle_UnreadableSignatureFailsLoudly(t *testing.T) {
	base := afero.NewMemMapFs()
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{filepath.Join("/proj", ".ctxloom")}})
	require.NoError(t, base.MkdirAll("/incoming", 0755))
	require.NoError(t, afero.WriteFile(base, "/incoming/seed.yaml", []byte("version: 1.0.0\nfragments:\n  a:\n    content: hi\n"), 0644))
	require.NoError(t, afero.WriteFile(base, "/incoming/seed.yaml.sig", []byte("SIG"), 0644))
	fs := denyStatFs{Fs: base, deny: map[string]error{"/incoming/seed.yaml.sig": os.ErrPermission}}

	_, err := ImportBundle(context.Background(), cfg, ImportBundleRequest{SourcePath: "/incoming/seed.yaml", FS: fs})
	require.Error(t, err, "import must not silently drop an unreadable signature")
}

// ---------------------------------------------------------------------------
// U081-F10 — ImportBundle "validates a bundle file" but validates almost
// nothing.
// ---------------------------------------------------------------------------

func TestImportBundle_RejectsEmptyBundle(t *testing.T) {
	for name, body := range map[string]string{
		"empty":        "",
		"comment_only": "# nothing here\n",
		"whitespace":   "\n   \n",
	} {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			cfg := config.NewFixture(config.Fixture{AppPaths: []string{filepath.Join("/proj", ".ctxloom")}})
			require.NoError(t, fs.MkdirAll("/incoming", 0755))
			require.NoError(t, afero.WriteFile(fs, "/incoming/hollow.yaml", []byte(body), 0644))

			_, err := ImportBundle(context.Background(), cfg, ImportBundleRequest{SourcePath: "/incoming/hollow.yaml", FS: fs})
			require.Error(t, err, "a bundle with no items must not import as a success")
			assert.Contains(t, err.Error(), "no items")
		})
	}
}

// TestImport_RejectsNameTheLoaderCannotFind is the CLASS gate for U081-F10.
//
// Every importer here writes the SOURCE basename into the target directory
// verbatim, and every loader filters that directory by extension. So the
// question "could the loader ever find what I just wrote?" has to be asked by
// each of them, and the accepted set is not the same: bundles.Loader.Find
// stats only "<name>.yaml" (a .yml bundle is as unloadable as a .txt one),
// while the profile scan takes .yaml and .yml alike.
//
// Blind spot, stated: this covers the two importers that exist today. It is a
// table, not a reflective sweep over every writer — a third importer added
// later must be added here by hand.
func TestImport_RejectsNameTheLoaderCannotFind(t *testing.T) {
	const bundleBody = "version: 1.0.0\nfragments:\n  a:\n    content: hi\n"
	const profileBody = "name: p\ndescription: d\nfragments:\n  - ctxloom:local@fragments/a\n"

	importBundle := func(t *testing.T, fs afero.Fs, cfg *config.Config, src string) error {
		_, err := ImportBundle(context.Background(), cfg, ImportBundleRequest{SourcePath: src, FS: fs})
		return err
	}
	importProfile := func(t *testing.T, fs afero.Fs, cfg *config.Config, src string) error {
		_, err := ImportProfile(context.Background(), cfg, ImportProfileRequest{SourcePath: src, FS: fs})
		return err
	}

	cases := []struct {
		kind     string
		body     string
		imp      func(*testing.T, afero.Fs, *config.Config, string) error
		loadable []string
		unusable []string
	}{
		{"bundle", bundleBody, importBundle, []string{"seed.yaml"}, []string{"seed.txt", "seed", "seed.yml", "seed.yaml.bak", "seed.json"}},
		{"profile", profileBody, importProfile, []string{"p.yaml", "p.yml"}, []string{"p.txt", "p", "p.yaml.bak", "p.json"}},
	}

	for _, tc := range cases {
		for _, name := range tc.unusable {
			t.Run(tc.kind+"/rejects/"+name, func(t *testing.T) {
				fs := afero.NewMemMapFs()
				cfg := config.NewFixture(config.Fixture{AppPaths: []string{filepath.Join("/proj", ".ctxloom")}})
				require.NoError(t, fs.MkdirAll("/incoming", 0755))
				require.NoError(t, afero.WriteFile(fs, "/incoming/"+name, []byte(tc.body), 0644))

				err := tc.imp(t, fs, cfg, "/incoming/"+name)
				require.Error(t, err, "a %s the loader can never find must not import as a success", tc.kind)
				assert.Contains(t, err.Error(), ".yaml", "the error must say what the name has to be")
			})
		}
		for _, name := range tc.loadable {
			t.Run(tc.kind+"/accepts/"+name, func(t *testing.T) {
				fs := afero.NewMemMapFs()
				cfg := config.NewFixture(config.Fixture{AppPaths: []string{filepath.Join("/proj", ".ctxloom")}})
				require.NoError(t, fs.MkdirAll("/incoming", 0755))
				require.NoError(t, afero.WriteFile(fs, "/incoming/"+name, []byte(tc.body), 0644))

				assert.NoError(t, tc.imp(t, fs, cfg, "/incoming/"+name), "a loadable %s name must still import", tc.kind)
			})
		}
	}
}

// TestImportBundle_DoesNotDestroyOnRejection is the --force corollary: a
// rejected import must not have already flattened a real bundle of the same
// basename.
func TestImportBundle_DoesNotDestroyOnRejection(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := filepath.Join("/proj", ".ctxloom")
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	bdir := paths.LocalBundlesPath(appDir)
	require.NoError(t, fs.MkdirAll(bdir, 0755))
	good := []byte("version: 1.0.0\nfragments:\n  keep:\n    content: precious\n")
	require.NoError(t, afero.WriteFile(fs, filepath.Join(bdir, "seed.yaml"), good, 0644))
	require.NoError(t, fs.MkdirAll("/incoming", 0755))
	require.NoError(t, afero.WriteFile(fs, "/incoming/seed.yaml", []byte("# hollow\n"), 0644))

	_, err := ImportBundle(context.Background(), cfg, ImportBundleRequest{SourcePath: "/incoming/seed.yaml", Force: true, FS: fs})
	require.Error(t, err)

	after, readErr := afero.ReadFile(fs, filepath.Join(bdir, "seed.yaml"))
	require.NoError(t, readErr)
	assert.Equal(t, string(good), string(after), "a rejected import must not have destroyed the existing bundle")
}

// ---------------------------------------------------------------------------
// U081-F08 — a degenerate remote URL escapes the bundle cache root, and the
// escaped path is handed straight to fs.Remove.
// ---------------------------------------------------------------------------

// TestLocalItemPath_StaysInsideTheCacheRoot is the containment gate on the one
// path in this package that reaches fs.Remove. Whatever the remote URL in the
// lockfile, the computed install path must land under
// <appDir>/cache/bundles — never outside it.
//
// The assertion is CONTAINMENT, not emptiness: with remote.containRemoteName
// neutralising traversal at the root cause, a degenerate ref now resolves to a
// harmless in-cache path rather than being discarded, and cleanup keeps
// working. What must never happen is a path outside the root.
func TestLocalItemPath_StaysInsideTheCacheRoot(t *testing.T) {
	appDir := filepath.Join("/proj", ".ctxloom")
	root := filepath.Join(appDir, paths.CacheDir, paths.BundlesDir)
	escaping := []string{
		"https://x/../..@bundles/victim",
		"https://x/../../..@bundles/victim",
		"https://host/a/../../../..@bundles/victim",
	}
	for _, ref := range escaping {
		got := localItemPath(appDir, remote.ItemTypeBundle, ref)
		if got == "" {
			continue // discarded outright is also safe
		}
		rel, err := filepath.Rel(root, got)
		require.NoError(t, err, "ref %q", ref)
		assert.False(t, rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)),
			"ref %q yielded removable path %q outside the cache root", ref, got)
	}

	// Control: an ordinary canonical ref still resolves, or cleanup silently
	// stops working.
	ok := localItemPath(appDir, remote.ItemTypeBundle, "https://github.com/owner/repo@bundles/thing")
	assert.NotEmpty(t, ok, "an ordinary ref must still resolve")
	assert.Contains(t, ok, filepath.Join(paths.CacheDir, paths.BundlesDir))
}

// TestRemoveLocalItems_DoesNotRemoveOutsideCache is the end-to-end proof on a
// REAL filesystem: the file outside the cache root survives
// `remote update --cleanup`.
func TestRemoveLocalItems_DoesNotRemoveOutsideCache(t *testing.T) {
	root := t.TempDir()
	appDir := filepath.Join(root, ".ctxloom")
	fs := afero.NewOsFs()
	require.NoError(t, fs.MkdirAll(filepath.Join(appDir, paths.CacheDir, paths.BundlesDir), 0755))
	victim := filepath.Join(appDir, "victim.yaml")
	require.NoError(t, afero.WriteFile(fs, victim, []byte("do not delete me"), 0644))

	ref := "https://x/..@bundles/victim"
	lf := refLockfile(map[string]remote.LockEntry{ref: {SHA: "abc"}})
	res, err := RemoveLocalItems(RemoveLocalItemsRequest{
		AppDir:      appDir,
		Items:       []RemovedItem{{Type: remote.ItemTypeBundle, Ref: ref}},
		Lockfile:    lf,
		LockManager: remote.NewLockfileManager(appDir, remote.WithLockfileFS(fs)),
		FS:          fs,
	})
	require.NoError(t, err)
	exists, statErr := afero.Exists(fs, victim)
	require.NoError(t, statErr)
	assert.True(t, exists, "cleanup must not delete a file outside the bundle cache root")
	assert.NotContains(t, res.Removed, victim, "a file outside the cache root may never be reported removed")
}

func ptr[T any](v T) *T { return &v }
