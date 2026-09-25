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
	"github.com/ctxloom/ctxloom/internal/shared/report"
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

// findingLines is a report.Sink that renders each finding's Text on its own
// line, so a test reads what the user would have been told without a
// process-wide sink or dedup set in the way.
type findingLines struct{ report.Collector }

func (f *findingLines) String() string {
	var b strings.Builder
	for _, t := range f.All().Texts() {
		b.WriteString(t)
		b.WriteByte('\n')
	}
	return b.String()
}

// captureBundleWarner gives the calling test its own sink — findings are
// collected per sink, so a second test asking about the same ref sees its own
// warning, not the silence of a process-wide dedup — and returns what the
// test hands the loader via WithReporter.
func captureBundleWarner(t *testing.T) *findingLines {
	t.Helper()
	return &findingLines{}
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
	l := NewLoader(NewProjectReader(afero.NewMemMapFs(), nil)).WithReporter(buf)

	got := admitAllPipe(l, false).CommandsFromBundleRef("no-such-bundle-cmds")

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
	l := NewLoader(NewProjectReader(afero.NewMemMapFs(), nil)).WithReporter(buf)

	got := admitAllPipe(l, false).SkillsFromBundleRef("no-such-bundle-skills")

	require.Empty(t, got, "an unloadable bundle still yields no skills")
	require.Contains(t, buf.String(), "no-such-bundle-skills",
		"the ref that failed to load must be named in a diagnostic")
}

// TestCommandsFromBundleRef_ItemScopedRefIsSilentEmpty pins the fix for
// subsonic-contusion: a false "skipping unresolved bundle" warning on every
// default assembly.
//
// A profile's `bundles:` list may cherry-pick a single fragment
// ("<bundle>#fragments/<name>") instead of naming the whole bundle —
// project-base.yaml does exactly this for "ctxloom-project#fragments/config-hierarchy".
// CommandsFromBundleRef used to hand that raw, selector-bearing string
// straight to l.lookup, which resolves bundles by NAME and can never match a
// string with a "#fragments/..." suffix on it — so it warned "skipping
// unresolved bundle" every time, even though the SAME ref resolves fine
// through the content path (ExpandBundleRefs -> ReadFragment, asserted below)
// and its fragment is genuinely delivered. A fragment cherry-pick never
// claimed "also export this bundle's commands", so this is the legitimate
// empty case — see config.reportBundleRefLoadFailure's identical "#"
// carve-out for the MCP/hooks siblings.
func TestCommandsFromBundleRef_ItemScopedRefIsSilentEmpty(t *testing.T) {
	buf := captureBundleWarner(t)
	fsys := afero.NewMemMapFs()
	dir := "/bundles"
	writeTree(t, fsys, bundlesRootIn(dir, ""), "proj", "version: \"1.0\"\nfragments:\n  config-hierarchy:\n    content: hi\n")

	l := NewLoader(NewProjectReader(fsys, []string{dir})).WithReporter(buf)

	// The fragment itself resolves fine through the content path (this is
	// what makes the commands-side warning a FALSE positive rather than a
	// true one).
	reads, err := l.ReadFragment("proj#fragments/config-hierarchy")
	require.NoError(t, err)
	require.NotEmpty(t, reads, "the fragment the ref names must actually resolve")

	// The commands resolver, asked about the identical ref, must neither warn
	// nor treat it as an error: it legitimately ships no commands.
	got := admitAllPipe(l, false).CommandsFromBundleRef("proj#fragments/config-hierarchy")
	require.Empty(t, got, "an item-scoped ref ships no commands")
	require.Empty(t, buf.String(),
		"an item-scoped ref must not warn 'skipping unresolved bundle': the fragment it names "+
			"resolves fine elsewhere, so this warning is a false positive that teaches readers "+
			"to skim past the true ones")
}

// TestSkillsFromBundleRef_ItemScopedRefIsSilentEmpty is the skills-side twin
// of TestCommandsFromBundleRef_ItemScopedRefIsSilentEmpty — see it for the
// full defect writeup. ReadBundleSkills had the identical bug.
func TestSkillsFromBundleRef_ItemScopedRefIsSilentEmpty(t *testing.T) {
	buf := captureBundleWarner(t)
	fsys := afero.NewMemMapFs()
	dir := "/bundles"
	writeTree(t, fsys, bundlesRootIn(dir, ""), "proj", "version: \"1.0\"\nfragments:\n  config-hierarchy:\n    content: hi\n")

	l := NewLoader(NewProjectReader(fsys, []string{dir})).WithReporter(buf)

	got := admitAllPipe(l, false).SkillsFromBundleRef("proj#fragments/config-hierarchy")
	require.Empty(t, got, "an item-scoped ref ships no skills")
	require.Empty(t, buf.String(), "an item-scoped ref must not warn 'skipping unresolved bundle'")
}

// TestCommandsFromBundleRef_CommandSelectorResolvesNotSilent pins the
// narrowing on top of ItemScopedRefIsSilentEmpty: NOT every "#"-bearing ref
// legitimately ships zero commands. A fragment cherry-pick ("#fragments/")
// does; an explicit "#commands/" selector NAMES a command, and collapsing the
// two into the same silent-nil branch would convert a confusing warning into
// worse silence — this project's characteristic defect (content withheld,
// exit 0, nothing said, see the task's own "402 withheld items" history). A
// command selector must actually resolve to the command it names.
func TestCommandsFromBundleRef_CommandSelectorResolvesNotSilent(t *testing.T) {
	buf := captureBundleWarner(t)
	fsys := afero.NewMemMapFs()
	dir := "/bundles"
	writeTree(t, fsys, bundlesRootIn(dir, ""), "proj", "version: \"1.0\"\ncommands:\n  deploy:\n    content: run the deploy script\n")

	l := NewLoader(NewProjectReader(fsys, []string{dir})).WithReporter(buf)

	got := admitAllPipe(l, false).CommandsFromBundleRef("proj#commands/deploy")

	require.Len(t, got, 1, "a command selector must resolve to the ONE command it names, not silently to zero")
	require.Equal(t, "deploy", got[0].Item)
	require.Contains(t, got[0].Content, "run the deploy script")
	require.Empty(t, buf.String(), "a command that resolved cleanly warns about nothing")
}

// TestSkillsFromBundleRef_SkillSelectorResolvesNotSilent is the skills-side
// twin of TestCommandsFromBundleRef_CommandSelectorResolvesNotSilent — see it
// for the full writeup. An explicit "#skills/" selector must resolve too.
// Uses writeSkillBundle (loader_skills_test.go), the same directory-form
// bundle+SKILL.md fixture the rest of this file's skill tests already build,
// rather than a hand-rolled shape that might not match what skillContent
// actually expects on disk.
func TestSkillsFromBundleRef_SkillSelectorResolvesNotSilent(t *testing.T) {
	buf := captureBundleWarner(t)
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	writeSkillBundle(t, fsys, bundlesDir, "proj", "reviewer", true)

	l := NewLoader(NewProjectReader(fsys, []string{bundlesDir})).WithReporter(buf)

	got := admitAllPipe(l, false).SkillsFromBundleRef("proj#skills/reviewer")

	require.Len(t, got, 1, "a skill selector must resolve to the ONE skill it names, not silently to zero")
	require.Equal(t, "reviewer", got[0].Item)
	require.Empty(t, buf.String(), "a skill that resolved cleanly warns about nothing")
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
	t.Cleanup(strictness.Reset)
	restore := clidiag.SetSink(&bytes.Buffer{})
	t.Cleanup(restore)

	mark := strictness.Checkpoint()
	l := NewLoader(NewProjectReader(afero.NewOsFs(), []string{dir}, WithReaderReporter(ledger())))
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
			l := NewLoader(NewProjectReader(fs, nil), seedLocal(map[string]*Bundle{"companion": b})).WithReporter(ledger())

			var warnings bytes.Buffer
			restore := clidiag.SetSink(&warnings)
			t.Cleanup(restore)

			cat := l.Catalog()
			read, err := cat.Read("companion")
			require.NoError(t, err)
			got := cat.skillContent(read, "helper", b.Skills["helper"])

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
