package composite_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The assembly golden: the bytes every consumer of an assembled package
// receives, over the corpus, for every registered engine. The corpus is the
// project's own bundle, the tree-form fixtures adapters/content reads, and
// the premise corpora — fragments, commands, skills, hooks, MCP servers,
// premised fragments, a curated and an uncurated profile. Regenerate with
// CTXLOOM_UPDATE_GOLDEN=1; the diff is the review.
// goldenPath is absolute because the sandbox moves the working directory.
var goldenPath = filepath.Join(packageDirAtStart, "testdata", "assemble.golden")

// goldenAppDir seeds a temp project with the corpus and returns its app dir.
// The corpus: the project's own bundle (a tree with real fragments),
// adapters/content's tree fixtures under a bundle.yaml envelope
// (a command with per-engine exports, a skill package, hooks, MCP servers,
// a bundle-shipped profile), and two directory profiles — an uncurated one
// and a curated one — selecting across them.
func goldenAppDir(t *testing.T) string {
	t.Helper()
	testsupport.Isolate(t)
	root := repoRoot(t)
	fs := afero.NewOsFs()
	appDir := filepath.Join(t.TempDir(), config.AppDirName)
	bundlesRoot := paths.LocalBundlesPathFor(appDir, paths.LayoutV2)
	// Provisioned as `ctxloom init` leaves it: an absent store withholds all.
	testsupport.SeedTree(t, fs, paths.ApprovalsPath(appDir), map[string]string{paths.ApprovalsPlaceholderName: ""})

	copyTree(t, fs, filepath.Join(root, ".ctxloom", "content", "bundles", "v2", "ctxloom-project"), filepath.Join(bundlesRoot, "ctxloom-project"))
	for _, name := range []string{"code-quality", "tooling"} {
		dst := filepath.Join(bundlesRoot, name)
		copyTree(t, fs, filepath.Join(root, "internal", "adapters", "content", "testdata", "tree", name), dst)
		testsupport.SeedTree(t, fs, dst, map[string]string{"bundle.yaml": "version: 1.0.0\ndescription: golden corpus " + name + "\n"})
	}
	// The skill sidecar declares its script executable; git does not carry
	// the bit for this fixture, so the copy restores it.
	require.NoError(t, fs.Chmod(filepath.Join(bundlesRoot, "code-quality", "skills", "code-reviewer", "scripts", "run.sh"), 0o755))
	bundletree.Write(t, fs, bundlesRoot, "premised", premisedBundle)
	testsupport.SeedTree(t, fs, paths.ProfilesPath(appDir), map[string]string{
		"golden-auto.yaml":    goldenAutoProfile,
		"golden-curated.yaml": goldenCuratedProfile,
	})
	testsupport.SeedTree(t, fs, appDir, map[string]string{config.ConfigFileName: goldenConfig})
	return appDir
}

// premisedBundle carries a premised fragment (withheld and indexed for a live
// consumer, written for a static one) beside an unconditional one that uses a
// profile variable.
const premisedBundle = `version: 1.0.0
description: premised corpus
fragments:
  always:
    tags: [golden]
    content: "Project {{project}}: always on."
  sometimes:
    tags: [golden]
    premise: "the agent is about to edit a signed bundle"
    content: "Do not edit a signed bundle in place."
commands:
  release:
    description: Cut a release
    content: "Prepare the release notes."
    llm:
      claude-code:
        enabled: false
        description: Release (disabled for claude)
`

const goldenConfig = `version: 6
agents:
  default:
    profiles: [golden-auto]
`

// goldenAutoProfile is the uncurated set: every command and skill of the
// selected bundles, priority-ordered fragments, a variable.
const goldenAutoProfile = `description: golden uncurated
bundles:
  - code-quality
  - ctxloom-project
  - premised
fragments:
  - name: ctxloom-project#fragments/turn-gates
    priority: 5
  - code-quality#fragments/solid
  - name: code-quality#fragments/tricky
    priority: -3
  - premised#fragments/always
  - premised#fragments/sometimes
variables:
  project: golden
`

// goldenCuratedProfile is the curated set: named commands and skills (a
// curated command exports even where its bundle disabled it), a profile
// hook, a deny list, a tag selection.
const goldenCuratedProfile = `description: golden curated
parents: [golden-auto]
commands:
  - code-quality#prompts/review
  - premised#commands/release
skills:
  - code-quality#skills/code-reviewer
select_tags: [ctxloom]
deny_tools: [Task]
hooks:
  pre_tool:
    - matcher: Bash
      type: command
      command: echo golden
`

// renderToday renders what the assembly produces for one engine and one
// profile set, through every consumer's projection: the live context, the
// materialized context, and the managed surfaces.
func renderToday(t *testing.T, g *golden, cfg *config.Config, engine, profile, appDir string) {
	t.Helper()
	live, err := operations.AssembleContext(context.Background(), cfg, operations.AssembleContextRequest{Profiles: []string{profile}})
	require.NoError(t, err)
	g.section(t, fmt.Sprintf("engine=%s profile=%s consumer=live", engine, profile), live)
	mat, err := operations.AssembleContext(context.Background(), cfg, operations.AssembleContextRequest{Profiles: []string{profile}, Consumer: operations.MaterializedFor(engines.Registry(), engine)})
	require.NoError(t, err)
	g.section(t, fmt.Sprintf("engine=%s profile=%s consumer=materialized", engine, profile), mat)
	pkg, err := operations.AssemblePackage(context.Background(), cfg, operations.PackageRequest{Profiles: []string{profile}, WorkDir: filepath.Dir(appDir)})
	require.NoError(t, err)
	managed, err := operations.ManagedConfigOf(engines.Registry(), pkg, engine)
	require.NoError(t, err)
	g.section(t, fmt.Sprintf("engine=%s profile=%s managed", engine, profile), managed)
}

// golden accumulates sections. A string longer than blobThreshold is
// replaced by "blob:<sha256>" and printed ONCE in the trailing blob table
// (by digest), so the exact bytes are pinned without every engine
// repeating them.
type golden struct {
	out   bytes.Buffer
	blobs map[string]string
}

const blobThreshold = 120

func (g *golden) section(t *testing.T, title string, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var generic any
	require.NoError(t, json.Unmarshal(raw, &generic))
	generic = g.blobify(generic)
	data, err := json.MarshalIndent(generic, "", "  ")
	require.NoError(t, err)
	fmt.Fprintf(&g.out, "== %s\n%s\n", title, data)
}

func (g *golden) blobify(v any) any {
	switch x := v.(type) {
	case string:
		if len(x) <= blobThreshold {
			return x
		}
		sum := sha256.Sum256([]byte(x))
		key := hex.EncodeToString(sum[:])
		if g.blobs == nil {
			g.blobs = map[string]string{}
		}
		g.blobs[key] = x
		return "blob:" + key
	case []any:
		for i := range x {
			x[i] = g.blobify(x[i])
		}
		return x
	case map[string]any:
		for k := range x {
			x[k] = g.blobify(x[k])
		}
		return x
	}
	return v
}

func (g *golden) bytes() []byte {
	var b bytes.Buffer
	b.Write(g.out.Bytes())
	for _, key := range slices.Sorted(maps.Keys(g.blobs)) {
		fmt.Fprintf(&b, "== blob:%s\n%s\n", key, g.blobs[key])
	}
	return b.Bytes()
}

// normalize replaces the temp paths with stable markers so the golden is
// the same bytes on every machine.
func normalize(s, appDir string) string {
	s = strings.ReplaceAll(s, filepath.Dir(appDir), "$PROJECT")
	home, _ := os.UserHomeDir()
	if home != "" {
		s = strings.ReplaceAll(s, home, "$HOME")
	}
	return s
}

func goldenEngines() []string {
	var names []string
	for _, n := range engines.Registry().Names(nil) {
		names = append(names, string(n))
	}
	sort.Strings(names)
	return names
}

// goldenLoad loads the corpus the way a real ctxloom does: the project's
// readers plus ctxloom's OWN companion loadout — the repo's
// cmd/ctxloom/loadout.yaml, read as the self-probe would read it — so the
// golden carries what every consumer receives on ctxloom's own behalf (its
// MCP server entry, its always-on guidance), not only the profile-selected
// content.
func goldenLoad(t *testing.T, appDir string) *config.Config {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join(repoRoot(t), "cmd", "ctxloom", "loadout.yaml"))
	require.NoError(t, err)
	self := bundles.CompanionLoadout{Bin: "ctxloom", Path: "/opt/build/ctxloom", Document: doc, Self: true}
	probe := func(context.Context) (bundles.CompanionProbe, error) {
		return bundles.CompanionProbe{Loadouts: []bundles.CompanionLoadout{self}}, nil
	}
	cfg, err := configload.Load(configload.WithAppDir(appDir), configload.WithReaderSource(func(cfg *config.Config) []bundles.Reader {
		return []bundles.Reader{bundles.NewCompanionReader(probe, bundles.WithTrustRoot(cfg.Trust().Root()))}
	}))
	require.NoError(t, err)
	return cfg
}

func TestAssemble_Golden(t *testing.T) {
	update := os.Getenv("CTXLOOM_UPDATE_GOLDEN") != ""
	appDir := goldenAppDir(t)
	cfg := goldenLoad(t, appDir)

	g := &golden{}
	for _, engine := range goldenEngines() {
		for _, profile := range []string{"golden-auto", "golden-curated"} {
			renderToday(t, g, cfg, engine, profile, appDir)
		}
	}
	got := normalize(string(g.bytes()), appDir)
	if update {
		testsupport.WriteFileString(t, afero.NewOsFs(), goldenPath, got, 0o644)
		t.Fatalf("golden %s rewritten; re-run without CTXLOOM_UPDATE_GOLDEN", goldenPath)
	}
	want, err := os.ReadFile(goldenPath)
	require.NoError(t, err, "no golden: regenerate with CTXLOOM_UPDATE_GOLDEN=1")
	require.Equal(t, string(want), got)
}

// packageDirAtStart is the test binary's working directory before TestMain's
// sandbox moves it: this package's directory, three levels below the repo
// root (-trimpath strips runtime.Caller's path, so the directory is the
// only anchor).
var packageDirAtStart, _ = os.Getwd()

func repoRoot(t *testing.T) string {
	t.Helper()
	require.NotEmpty(t, packageDirAtStart)
	return filepath.Clean(filepath.Join(packageDirAtStart, "..", "..", ".."))
}

func copyTree(t *testing.T, fsys afero.Fs, src, dst string) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		testsupport.WriteFile(t, fsys, filepath.Join(dst, rel), data, info.Mode().Perm())
		return nil
	}))
}

// Every bundle in the corpus decodes through the engine: each command's and
// skill's claude-code block — authored in the document form, the tree form
// or not at all — is accepted by claude.DecodeExportBlock, so the migration
// to opaque blocks refuses nothing that loaded before it. The golden above
// pins WHAT they decode to; this pins that none is refused.
func TestCorpus_EveryExportBlockDecodesThroughTheEngine(t *testing.T) {
	appDir := goldenAppDir(t)
	cfg := goldenLoad(t, appDir)
	pkg, err := operations.AssemblePackage(context.Background(), cfg, operations.PackageRequest{Profiles: []string{"golden-auto"}})
	require.NoError(t, err)
	require.NotEmpty(t, pkg.Commands)
	require.NotEmpty(t, pkg.Skills)

	items := pkg.EngineItems(engine.Name(claude.EngineName))
	for _, c := range items.Commands {
		_, err := claude.DecodeExportBlock(c.Exports)
		assert.NoError(t, err, "command %s", c.Ref)
	}
	for _, s := range items.Skills {
		_, err := claude.DecodeExportBlock(s.Exports)
		assert.NoError(t, err, "skill %s", s.Ref)
	}
	_, err = operations.ExportsFor(engines.Registry(), pkg, claude.EngineName)
	assert.NoError(t, err, "the engine exports the whole corpus without a refusal")
}
