package operations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// setupHeadingTestFS builds a bundle whose commands lead with each of the
// shapes GetCommand's title-stripping has to tell apart: a real ATX H1, an H2
// sub-heading, a shebang, and a bare "#tag" word. Only the H1 is a title.
func setupHeadingTestFS(t *testing.T) *bundles.Loader {
	t.Helper()
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(paths.LocalBundlesPath(testBaseDir), 0755))

	bundleContent := `version: "1.0"
description: Heading-stripping fixtures
commands:
  h1-title:
    description: leads with a real H1
    content: |
      # Code Review
      body of the h1 command
  h2-first:
    description: leads with an H2 sub-heading
    content: |
      ## Deep Dive
      body of the h2 command
  shebang-first:
    description: leads with a shebang
    content: |
      #!/usr/bin/env bash
      echo hello
  hashtag-first:
    description: leads with a bare hashtag word
    content: |
      #urgent
      body of the hashtag command
`
	require.NoError(t, afero.WriteFile(fs,
		paths.LocalBundlesPath(testBaseDir)+"/headings.yaml", []byte(bundleContent), 0644))

	return bundles.NewLoader([]string{paths.LocalBundlesPath(testBaseDir)}, false, bundles.WithFS(fs))
}

// TestGetCommand_StripsOnlyAnATXH1 pins U082-F10: the "drop a single leading H1
// title line" cleanup must recognise an ATX H1 (a run of exactly one '#'
// followed by space/tab or end of line) and nothing else. A prefix test on "#"
// alone also eats an H2 sub-heading, a shebang, and a "#tag" word — silently
// deleting the first real line of the command body.
func TestGetCommand_StripsOnlyAnATXH1(t *testing.T) {
	loader := setupHeadingTestFS(t)

	cases := []struct {
		name    string
		cmd     string
		rawLead string // §11k: what the loader hands the code under test
		want    string
	}{
		{
			name:    "real H1 title is dropped",
			cmd:     "headings#commands/h1-title",
			rawLead: "# Code Review",
			want:    "body of the h1 command",
		},
		{
			name:    "H2 sub-heading is body, not a title",
			cmd:     "headings#commands/h2-first",
			rawLead: "## Deep Dive",
			want:    "## Deep Dive\nbody of the h2 command",
		},
		{
			name:    "shebang is body, not a title",
			cmd:     "headings#commands/shebang-first",
			rawLead: "#!/usr/bin/env bash",
			want:    "#!/usr/bin/env bash\necho hello",
		},
		{
			name:    "bare hashtag word is body, not a title",
			cmd:     "headings#commands/hashtag-first",
			rawLead: "#urgent",
			want:    "#urgent\nbody of the hashtag command",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// §11k: prove the fixture is hostile from GetCommand's vantage —
			// the loader really does deliver the leading line under test.
			raw, err := loader.GetCommand(tc.cmd)
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(raw.Content, tc.rawLead),
				"fixture never reached the stripper: raw content = %q", raw.Content)

			res, err := GetCommand(context.Background(), nil, GetCommandRequest{
				Name:   tc.cmd,
				Loader: loader,
			})
			require.NoError(t, err)
			assert.Equal(t, tc.want, res.Content)
		})
	}
}

// TestUpdateBundle_FailedRedistillDropsStaleDistillation pins U082-F12: the
// invariant the corrected distillFragments prose now asserts. On the
// UpdateBundle path applyFragmentEdits has ALREADY cleared
// Distilled/DistilledBy/ContentHash for any item whose content changed, so a
// failed re-distill leaves them EMPTY — the old distillation is not "left
// intact", and cannot be, because it described the superseded content. The
// preserve-on-failure behaviour the old comment described belongs to the
// DistillBundleFile path, which reads the item straight off disk.
func TestUpdateBundle_FailedRedistillDropsStaleDistillation(t *testing.T) {
	_, cfg := setupBundleTestDir(t)

	// Seed a fragment that carries a genuine distillation.
	seed := &recordingDistiller{returnValue: "OLD DISTILLED SUMMARY", returnModel: "old-model"}
	created, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{
		Name:      "seed",
		Distiller: seed,
		Fragments: map[string]BundleFragmentInput{
			"intro": {Content: "the original fragment body, long enough to distill"},
		},
	})
	require.NoError(t, err)

	// §11k: prove the fixture is hostile — there really IS a prior
	// distillation on disk for the failed re-distill to be asked to preserve.
	before := readBundleFile(t, created.Path)
	require.Equal(t, "OLD DISTILLED SUMMARY", before.Fragments["intro"].Distilled)
	require.Equal(t, "old-model", before.Fragments["intro"].DistilledBy)
	require.NotEmpty(t, before.Fragments["intro"].ContentHash)

	// Change the content; the distiller fails.
	failing := &recordingDistiller{returnErr: fmt.Errorf("llm unavailable")}
	updated, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name:      "seed",
		Distiller: failing,
		SetFragments: map[string]BundleFragmentInput{
			"intro": {Content: "a rewritten fragment body the old summary no longer describes"},
		},
	})
	require.NoError(t, err)
	require.Len(t, failing.calls, 1, "the content change must have queued a re-distill attempt")

	after := readBundleFile(t, updated.Path)
	got := after.Fragments["intro"]
	assert.Equal(t, "a rewritten fragment body the old summary no longer describes", got.Content)
	assert.Empty(t, got.Distilled,
		"a failed re-distill must not leave a summary of the superseded content behind")
	assert.Empty(t, got.DistilledBy)
	assert.Empty(t, got.ContentHash)
}

// readBundleFile decodes a saved bundle straight off disk, so assertions see
// what was persisted rather than the in-memory value.
func readBundleFile(t *testing.T, path string) bundles.Bundle {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var b bundles.Bundle
	require.NoError(t, yaml.Unmarshal(data, &b))
	return b
}

// TestUpdateBundle_IdenticalSetIsNoChanges pins U082-F13: UpdateBundleResult's
// documented contract is `"updated"` when at least one mutation took effect,
// otherwise `"no_changes"`, so callers can detect idempotent operations.
// apply{Fragment,Prompt,MCP}Edits appended a change line for EVERY name in the
// set map without comparing against the existing entry, so re-applying a
// byte-identical edit reported "updated" with a fabricated change line — and
// rewrote the bundle file — for a request that produced no diff at all.
func TestUpdateBundle_IdenticalSetIsNoChanges(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "seed"})
	require.NoError(t, err)

	frag := map[string]BundleFragmentInput{
		"intro": {Content: "intro body", Tags: []string{"alpha"}, Notes: "n", NoDistill: true},
	}
	cmd := map[string]BundleCommandInput{
		"review": {Content: "review body", Description: "d", Tags: []string{"beta"}, NoDistill: true},
	}
	mcp := map[string]BundleMCPInput{
		"srv": {Command: "srv-bin", Args: []string{"--flag"}, Env: map[string]string{"K": "V"}},
	}

	// First application really mutates: this both establishes the entries and
	// proves (§11k) that the second, identical request is being compared
	// against an existing entry rather than creating one.
	first, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name: "seed", SetFragments: frag, SetPrompts: cmd, SetMCPServers: mcp,
	})
	require.NoError(t, err)
	require.Equal(t, "updated", first.Status)
	require.ElementsMatch(t,
		[]string{"set fragment: intro", "set prompt: review", "set mcp: srv"}, first.Changes)

	onDisk := readBundleFile(t, first.Path)
	require.Equal(t, "intro body", onDisk.Fragments["intro"].Content)
	require.Equal(t, "review body", onDisk.Commands["review"].Content)
	require.Equal(t, "srv-bin", onDisk.MCP["srv"].Command)

	// Re-applying the identical inputs produces no diff.
	second, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name: "seed", SetFragments: frag, SetPrompts: cmd, SetMCPServers: mcp,
	})
	require.NoError(t, err)
	assert.Equal(t, "no_changes", second.Status)
	assert.Empty(t, second.Changes)

	// A genuine edit still reports, so the comparison has not disabled Set*.
	frag["intro"] = BundleFragmentInput{
		Content: "intro body", Tags: []string{"alpha"}, Notes: "different note", NoDistill: true,
	}
	third, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name: "seed", SetFragments: frag, SetPrompts: cmd, SetMCPServers: mcp,
	})
	require.NoError(t, err)
	assert.Equal(t, "updated", third.Status)
	assert.Equal(t, []string{"set fragment: intro"}, third.Changes)
}

// plantingDistiller writes rival bytes to path the first time it is asked to
// distill, standing in for a concurrent author who wins the race between
// CreateBundle's existence check and its Save.
type plantingDistiller struct {
	t       *testing.T
	path    string
	content []byte
	planted bool
}

func (d *plantingDistiller) Distill(context.Context, DistillRequest) (DistillResult, error) {
	if !d.planted {
		require.NoError(d.t, os.WriteFile(d.path, d.content, 0o644))
		d.planted = true
	}
	return DistillResult{Distilled: "a plausible distillation of the fragment", ModelID: "m"}, nil
}

// TestCreateBundle_ConcurrentCreateIsNotClobbered pins U082-F20: CreateBundle
// checked for an existing bundle with os.Stat and then wrote with a plain
// Save, so anything appearing at the path in between was silently overwritten.
// The window is not microseconds — distillation runs inside it, one LLM round
// trip per item — and what it destroys is another author's bundle.
func TestCreateBundle_ConcurrentCreateIsNotClobbered(t *testing.T) {
	appDir, cfg := setupBundleTestDir(t)
	path := filepath.Join(paths.LocalBundlesPath(appDir), "contested.yaml")

	rival := []byte("version: 1.0.0\ndescription: authored by the other writer\n")
	d := &plantingDistiller{t: t, path: path, content: rival}

	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{
		Name:      "contested",
		Distiller: d,
		Fragments: map[string]BundleFragmentInput{
			"f": {Content: "a fragment body long enough to be worth distilling"},
		},
	})

	// §11k: the fixture is only hostile if the distiller actually ran — that
	// is what puts the rival file inside the check-to-write window.
	require.True(t, d.planted, "distiller never ran; nothing occupied the TOCTOU window")

	// Payload first: what the defect destroys is bytes, not an exit status.
	got, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, string(rival), string(got),
		"the other author's bundle must survive untouched")

	require.Error(t, err, "creating over a bundle that appeared mid-flight must refuse")
	assert.Contains(t, err.Error(), "already exists")
}
