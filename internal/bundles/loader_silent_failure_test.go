package bundles

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// ---------------------------------------------------------------------------
// Three silent failures in the bundle loader.
//
// All three share one shape, the one this project calls characteristic: a
// determination FAILED, and the caller was handed the same value it would get
// if there had legitimately been nothing to do. `nil` commands because the
// bundle would not load looks exactly like `nil` commands because the bundle
// ships none; an empty bundle list because the directory is unreadable looks
// exactly like an empty list because there are no bundles. Exit 0, no
// diagnostic, zero bytes written.
//
// The discriminator this project applies: "nothing to do, legitimately" is a
// correct exit 0; "nothing happened because determining what to do failed" must
// be loud.
// ---------------------------------------------------------------------------

// captureBundleWarner gives the calling test a FRESH process-wide dedup set —
// the production warner dedups per ref for the life of the process, so a second
// test asking about the same ref would otherwise see silence and read it as "no
// warning emitted" — and returns the buffer the test must hand the loader via
// WithWarnWriter, which is where the warner now emits.
func captureBundleWarner(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := unresolvedBundleWarner
	unresolvedBundleWarner = newBundleWarner()
	t.Cleanup(func() { unresolvedBundleWarner = prev })
	return &buf
}

// TestCommandsFromBundleRef_WarnsWhenBundleUnloadable pins the fix for commands.
//
// CommandsFromBundleRef feeds the export path that WRITES per-engine command
// files. A profile naming a bundle that will not load exported zero commands,
// silently, exit 0 — indistinguishable from a bundle that ships no commands.
// The sibling expandBundleRef already warns in the identical situation through
// the identical warner; the inconsistency was the tell.
func TestCommandsFromBundleRef_WarnsWhenBundleUnloadable(t *testing.T) {
	buf := captureBundleWarner(t)
	l := NewLoader(NewProjectReader(afero.NewMemMapFs(), nil)).WithWarnWriter(buf)

	got := ungated(l, false).CommandsFromBundleRef("no-such-bundle-cmds")

	require.Empty(t, got, "an unloadable bundle still yields no commands")
	require.Contains(t, buf.String(), "no-such-bundle-cmds",
		"the ref that failed to load must be named in a diagnostic: writing zero command "+
			"files because the bundle would not load is indistinguishable from a bundle that "+
			"ships no commands unless someone says so")
}

// TestSkillsFromBundleRef_WarnsWhenBundleUnloadable pins the same fix for skills —
// the same defect, the same export path, the same silence.
func TestSkillsFromBundleRef_WarnsWhenBundleUnloadable(t *testing.T) {
	buf := captureBundleWarner(t)
	l := NewLoader(NewProjectReader(afero.NewMemMapFs(), nil)).WithWarnWriter(buf)

	got := ungated(l, false).SkillsFromBundleRef("no-such-bundle-skills")

	require.Empty(t, got, "an unloadable bundle still yields no skills")
	require.Contains(t, buf.String(), "no-such-bundle-skills",
		"the ref that failed to load must be named in a diagnostic")
}

// TestList_UnreadableBundlesDirIsLoud pins the fix below.
//
// A bundles root that cannot be read returned an EMPTY LIST WITH A NIL ERROR,
// and every downstream sweep then reported "fragment not found" — blaming the
// user's ref for what was actually a permissions fault on their bundles
// directory. Two lines below, the PER-BUNDLE failure path already routes
// through strictness.Fail; only the directory-level one was silent.
//
// Real chmod on a real OsFs: afero's MemMapFs has no permission enforcement, so
// a fake here would report success against a defect that only exists on a real
// filesystem.
func TestList_UnreadableBundlesDirIsLoud(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not deny root, so this defect cannot be reproduced")
	}

	dir := filepath.Join(t.TempDir(), "bundles")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	// A real bundle inside, so "the list is empty" is unambiguously the fault
	// and not an genuinely empty directory.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "demo"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "demo", "bundle.yaml"), []byte("version: 1.0.0\n"), 0o644))
	require.NoError(t, os.Chmod(dir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(strictness.Reset)
	restore := clidiag.SetSink(&bytes.Buffer{})
	t.Cleanup(restore)

	mark := strictness.Checkpoint()
	l := NewLoader(NewProjectReader(afero.NewOsFs(), []string{dir}))
	got, err := l.List()
	require.NoError(t, err, "List keeps its signature; loudness rides the strictness choke")

	require.Empty(t, got, "the directory genuinely cannot be read, so nothing can be listed")
	findings := strictness.Since(mark)
	require.NotEmpty(t, findings,
		"an unreadable bundles root must record a fatal-class finding: an empty list with a "+
			"nil error tells every downstream sweep 'you have no bundles', so the user is told "+
			"their fragment does not exist when in truth their bundles directory could not be read")
}

// TestSkillContent_RefusesNonFilesystemBundlePath pins the fix below.
//
// Bundle.Path is overloaded: a real filesystem path for on-disk bundles, but ""
// for a companion loadout and a synthetic "<remote>:…"/"<remote-version>:…"
// sentinel for pinned remote content. skillContent took filepath.Dir of it
// unconditionally, and filepath.Dir("") is ".", so a companion bundle's skills
// resolved against the PROCESS WORKING DIRECTORY — arbitrary project-local
// files loaded, trusted and materialized as that companion's skill package.
//
// The test builds exactly that: a bundle with an empty Path declaring a skill,
// and a "skills/<name>" tree at the filesystem root that is NOT the bundle's.
// Before the fix the tree is happily loaded; after it, the skill is withheld.
func TestSkillContent_RefusesNonFilesystemBundlePath(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{"companion loadout (empty Path)", ""},
		{"remote sentinel", "<remote>:some/bundle@abc123"},
		{"remote-version sentinel", "<remote-version>:some/bundle@abc123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			// The cwd-relative tree the overloaded Path would resolve onto.
			require.NoError(t, afero.WriteFile(fs, filepath.Join("skills", "helper", "SKILL.md"),
				[]byte("---\nname: helper\ndescription: not the bundle's skill\n---\n\nbody\n"), 0o644))

			b := &Bundle{
				Name:   "companion",
				Path:   tc.path,
				Skills: map[string]BundleSkill{"helper": {}},
			}
			l := NewLoader(NewProjectReader(fs, nil), seedLocal(map[string]*Bundle{"companion": b}))

			var warnings bytes.Buffer
			restore := clidiag.SetSink(&warnings)
			t.Cleanup(restore)

			read, ok := l.lookup("companion")
			require.True(t, ok)
			got := l.skillContent(read, "helper", b.Skills["helper"])

			require.Nil(t, got,
				"a bundle whose Path is not a real filesystem path has no directory to resolve "+
					"skills against; resolving anyway loads whatever happens to sit at that "+
					"relative path in the process working directory")
			require.True(t, strings.Contains(warnings.String(), "helper"),
				"the withheld skill must be named — a skill that vanishes silently is "+
					"indistinguishable from a bundle that ships none (got: %q)", warnings.String())
		})
	}
}

// TestSkillPreimageDir_RefusesToDeriveFromCwd widens the same fix.
//
// The original fix covered ONE call site (the loader). Eight more in
// internal/operations took filepath.Dir(bundle.Path) the same way, including
// three on TRUST paths — `ctxloom review`'s approval surface, SetItemTrust's
// grant preimage, and the review snapshot. On a pathless bundle those hashed
// whatever sat in the process working directory into a trust decision.
//
// The rule is conditional, and the condition is load-bearing in both
// directions: a manifest-LESS skill must fail (it would derive from the tree),
// an AUTHORED one must succeed (it never touches the filesystem, so a
// companion bundle carrying one is legitimate and refusing it would be a
// regression).
func TestSkillPreimageDir_RefusesToDeriveFromCwd(t *testing.T) {
	pathless := &Bundle{Name: "companion", Path: ""}

	t.Run("manifest-less skill on a pathless bundle fails", func(t *testing.T) {
		_, err := pathless.SkillPreimageDir(BundleSkill{})
		require.Error(t, err,
			"a manifest-less skill derives its preimage by parsing the tree; with no bundle "+
				"directory that tree would be resolved from the process working directory, and "+
				"those bytes would be hashed into a trust grant")
	})

	t.Run("authored manifest on a pathless bundle is legitimate", func(t *testing.T) {
		dir, err := pathless.SkillPreimageDir(BundleSkill{
			Files: map[string]SkillFileMeta{"SKILL.md": {SHA256: "sha256:abc", Mode: "0644"}},
		})
		require.NoError(t, err,
			"an authored manifest is hashed without touching the filesystem, so a companion "+
				"bundle can legitimately carry one")
		require.NotEqual(t, ".", dir, "must never hand back the process working directory")
		require.NotEqual(t, "", dir,
			`"" joins to a cwd-relative "skills/<name>" just as "." does — the stand-in must be unusable`)
	})

	t.Run("a real bundle path is returned unchanged", func(t *testing.T) {
		dir, err := (&Bundle{Name: "demo", Path: filepath.Join("some", "where", "bundle.yaml")}).
			SkillPreimageDir(BundleSkill{})
		require.NoError(t, err)
		require.Equal(t, filepath.Join("some", "where"), dir)
	})
}
