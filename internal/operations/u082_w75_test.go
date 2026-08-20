package operations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cbroglie/mustache"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
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

	return bundles.NewLoader(bundles.NewProjectReader(fs, []string{paths.LocalBundlesPath(testBaseDir)}))
}

// TestGetCommand_StripsOnlyAnATXH1 pins that the "drop a single leading H1
// title line" cleanup must recognise an ATX H1 (a run of exactly one '#'
// followed by space/tab or end of line) and nothing else. A prefix test on "#"
// alone also eats an H2 sub-heading, a shebang, and a "#tag" word — silently
// deleting the first real line of the command body.
func TestGetCommand_StripsOnlyAnATXH1(t *testing.T) {
	loader := setupHeadingTestFS(t)

	cases := []struct {
		name    string
		cmd     string
		rawLead string // what the loader hands the code under test
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
			// Prove the fixture is hostile from GetCommand's vantage —
			// the loader really does deliver the leading line under test.
			raw, err := opPipe(nil, loader).GetCommand(tc.cmd)
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(raw.Content, tc.rawLead),
				"fixture never reached the stripper: raw content = %q", raw.Content)

			res, err := GetCommand(context.Background(), nil, GetCommandRequest{
				Name:     tc.cmd,
				Pipeline: opPipe(nil, loader),
			})
			require.NoError(t, err)
			assert.Equal(t, tc.want, res.Content)
		})
	}
}

// TestUpdateBundle_FailedRedistillDropsStaleDistillation pins the
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

	// Prove the fixture is hostile — there really IS a prior
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

// TestUpdateBundle_IdenticalSetIsNoChanges pins that UpdateBundleResult's
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
	// proves that the second, identical request is being compared
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

// TestCreateBundle_ConcurrentCreateIsNotClobbered pins that CreateBundle
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

	// The fixture is only hostile if the distiller actually ran — that
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

// TestConstraintResolver_SkipWarningNamesTheCause pins that when a
// dependency drops out of the lockfile the user gets one line, and it is the
// only signal that anything went wrong — the walk continues and the build
// proceeds without the item. Three unrelated failures reached that line with
// their errors discarded: no fetcher could be built for the URL, the URL was
// not parseable as owner/repo, and the constraint did not resolve against the
// repo's version space. "could not resolve X@Y; skipping" is the same sentence
// for an auth problem, a typo'd URL, and a tag that does not exist yet, and it
// tells the user nothing they can act on.
func TestConstraintResolver_SkipWarningNamesTheCause(t *testing.T) {
	// badURL must be a URL the fetcher factory happily accepts but
	// ParseOwnerRepo rejects; asserted below so the case cannot silently
	// degenerate into the resolve-failure case.
	const badURL = "::not-a-repo-url::"

	cases := []struct {
		name     string
		factory  remote.FetcherFactory
		ref      *remote.Reference
		precheck func(t *testing.T)
		want     string
	}{
		{
			name: "no fetcher for the URL",
			factory: func(string, remote.AuthConfig) (remote.Fetcher, error) {
				return nil, errors.New("no credentials for host")
			},
			ref:  mustRef(t, "feature-x"),
			want: "no credentials for host",
		},
		{
			name: "URL is not parseable as owner/repo",
			factory: func(string, remote.AuthConfig) (remote.Fetcher, error) {
				return remote.NewMockFetcher(), nil
			},
			ref: &remote.Reference{
				Path:           "bundles/x",
				URL:            badURL,
				ContentVersion: "feature-x",
				ItemType:       remote.ItemTypeBundle,
			},
			precheck: func(t *testing.T) {
				_, _, err := remote.ParseOwnerRepo(badURL)
				require.Error(t, err, "fixture is not hostile: this URL parses fine")
			},
			// Deliberately NOT the URL itself: the URL already appears in the
			// existing message via the ref identity, so asserting on it would
			// pass against the unfixed code and prove nothing.
			want: "unparseable repository URL",
		},
		{
			name: "constraint does not resolve",
			factory: func(string, remote.AuthConfig) (remote.Fetcher, error) {
				m := remote.NewMockFetcher()
				m.ResolveRefErr = errors.New("no tag matches ^9.0")
				return m, nil
			},
			ref: mustRef(t, "^9.0"),
			// ResolveConstraint's own diagnosis, which the skip line discarded.
			want: "no tag satisfies version constraint",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.precheck != nil {
				tc.precheck(t)
			}
			var sink strings.Builder
			restore := clidiag.SetSink(&sink)
			defer restore()

			resolve := newConstraintResolver(context.Background(), nil, tc.factory, remote.AuthConfig{}, false)
			_, _, _, ok := resolve(tc.ref)

			// The fixture is only hostile if the resolver actually took
			// the skip path — otherwise there is no warning to inspect.
			require.False(t, ok, "this fixture must reach the skip-with-warning path")
			require.NotEmpty(t, sink.String(), "the skip path must warn")

			assert.Contains(t, sink.String(), "could not resolve",
				"the existing skip wording must survive")
			assert.Contains(t, sink.String(), tc.want,
				"the warning must name which of the three failures happened")
		})
	}
}

// TestResolveProfile_BrokenProfileReportsItsActualFault pins that a profile
// which EXISTS and is BROKEN is reported as broken, not as absent.
//
// The original defect was in the retired inline arm: resolveProfile discarded
// config.ResolveProfile's error to trigger the directory fallback, so
// "no such profile" and "this profile exists and is broken" became the same
// event, and a circular parent chain vanished behind a bare "profile not
// found" naming the wrong place to look. The inline arm is gone; the claim is
// not, because the directory resolver can fail the same two ways.
func TestResolveProfile_BrokenProfileReportsItsActualFault(t *testing.T) {
	// A two-node cycle: resolving "looper" revisits itself.
	fs := afero.NewMemMapFs()
	appDir := t.TempDir() + "/" + paths.AppDirName
	cfg := cfgWithDirProfiles(t, fs, appDir, map[string]config.Profile{
		"looper": {Parents: []string{"other"}},
		"other":  {Parents: []string{"looper"}},
	}, config.Fixture{})

	var sink strings.Builder
	restore := clidiag.SetSink(&sink)
	defer restore()

	_, err := resolveProfile(cfg, "looper", nil, nil)

	require.Error(t, err)
	said := strings.ToLower(sink.String() + err.Error())
	assert.Contains(t, said, "looper")
	assert.Contains(t, said, "circular",
		"the profile's ACTUAL fault must reach the user, not a not-found naming the wrong problem")
	assert.NotContains(t, said, "not found",
		"reporting a cycle as absence is the defect this pins")
}

// TestCreateUpdateBundle_DistillFailuresAreStderrOnly is the characterization
// pin for an ESCALATED finding: CreateBundle and UpdateBundle discard
// the failure set distillFragments/distillPrompts return, so their result DTOs
// report "created"/"updated" with a change line per item and carry no field a
// programmatic consumer could read to learn that an item has no distillation.
// Both DTOs publish a JSON Schema (schematargets.go), so adding that field is
// a schema change and the human's call.
//
// This records exactly what a caller can observe TODAY, so the escalation is
// grounded in measurement: the per-item clidiag warnings are the ONLY signal,
// and they reach stderr, not the result. It goes red when the DTO grows the
// field — which is the point.
func TestCreateUpdateBundle_DistillFailuresAreStderrOnly(t *testing.T) {
	_, cfg := setupBundleTestDir(t)

	var sink strings.Builder
	restore := clidiag.SetSink(&sink)
	defer restore()

	failing := &recordingDistiller{returnErr: fmt.Errorf("llm unavailable")}
	created, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{
		Name:      "seed",
		Distiller: failing,
		Fragments: map[string]BundleFragmentInput{
			"intro": {Content: "a fragment body long enough to be worth distilling"},
		},
		Commands: map[string]BundleCommandInput{
			"review": {Content: "a command body long enough to be worth distilling"},
		},
	})
	require.NoError(t, err)

	// The fixture is only hostile if distillation was actually attempted
	// and actually failed.
	require.Len(t, failing.calls, 2, "both items must have been offered to the distiller")
	require.Empty(t, readBundleFile(t, created.Path).Fragments["intro"].Distilled,
		"the fixture must leave the item genuinely undistilled")

	// What the caller gets back: unqualified success.
	assert.Equal(t, "created", created.Status)

	// The only signal, and it is on stderr.
	assert.Contains(t, sink.String(), `distill of fragment "intro" failed`)
	assert.Contains(t, sink.String(), `distill of prompt "review" failed`)

	// Same on the update path.
	sink.Reset()
	failing2 := &recordingDistiller{returnErr: fmt.Errorf("llm unavailable")}
	updated, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{
		Name:      "seed",
		Distiller: failing2,
		SetFragments: map[string]BundleFragmentInput{
			"intro": {Content: "a rewritten fragment body, still worth distilling"},
		},
	})
	require.NoError(t, err)
	require.Len(t, failing2.calls, 1)

	assert.Equal(t, "updated", updated.Status)
	assert.Equal(t, []string{"set fragment: intro"}, updated.Changes,
		"the change line claims the item was set, saying nothing about its distillation")
	assert.Contains(t, sink.String(), `distill of fragment "intro" failed`)
}

// ---------------------------------------------------------------------------
// Parity pins taken BEFORE the fragment/command clone collapse.
//
// The clones are behaviourally equivalent modulo the item type, so
// a parity test CANNOT be red. Its job is the other one — pin the shared
// behaviour so the collapse is provably behaviour-preserving, at two
// altitudes, labelled.
// ---------------------------------------------------------------------------

// TestDistillParity_PublicSeam is the PUBLIC-SEAM characterization pin: green
// before and after the collapse. It drives CreateBundle with a fragment and a
// command carrying identical content through identical distiller behaviour,
// and asserts the two kinds are treated the same at every arm of the
// distillation policy — accept, reject-empty, reject-too-short.
func TestDistillParity_PublicSeam(t *testing.T) {
	const body = "an item body of a perfectly ordinary and sufficient length"

	cases := []struct {
		name          string
		distilled     string
		wantDistilled string
	}{
		{name: "a good distillation is accepted", distilled: "a real summary of the body", wantDistilled: "a real summary of the body"},
		{name: "an empty distillation is rejected", distilled: "", wantDistilled: ""},
		{name: "a truncated distillation is rejected", distilled: "x", wantDistilled: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, cfg := setupBundleTestDir(t)
			d := &recordingDistiller{returnValue: tc.distilled, returnModel: "m"}

			res, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{
				Name:      "parity",
				Distiller: d,
				Fragments: map[string]BundleFragmentInput{"item": {Content: body}},
				Commands:  map[string]BundleCommandInput{"item": {Content: body}},
			})
			require.NoError(t, err)

			// Both kinds must actually have been offered to the
			// distiller, or the arms below compare two no-ops.
			require.Len(t, d.calls, 2)

			b := readBundleFile(t, res.Path)
			frag, cmd := b.Fragments["item"], b.Commands["item"]

			assert.Equal(t, tc.wantDistilled, frag.Distilled)
			assert.Equal(t, frag.Distilled, cmd.Distilled, "fragment and command must distill alike")
			assert.Equal(t, frag.DistilledBy, cmd.DistilledBy, "fragment and command must stamp alike")
			assert.Equal(t, frag.ContentHash == "", cmd.ContentHash == "",
				"fragment and command must agree on whether a content hash was stamped")
			assert.Equal(t, body, frag.Content)
			assert.Equal(t, body, cmd.Content)
		})
	}
}

// TestDistillParity_InternalSeam is the INTERNAL-SEAM pin, directly over the
// cloned helpers the collapse replaces. It is the one that would go red if the
// canonical implementation changed either clone's behaviour.
func TestDistillParity_InternalSeam(t *testing.T) {
	t.Run("namesNeeding*Distill agree", func(t *testing.T) {
		b := &bundles.Bundle{
			Fragments: map[string]bundles.BundleFragment{"present": {}, "skipped": {}},
			Commands:  map[string]bundles.BundleCommand{"present": {}, "skipped": {}},
		}
		fragIn := map[string]BundleFragmentInput{
			"present": {Content: "c"},
			"skipped": {Content: "c", NoDistill: true},
			"absent":  {Content: "c"}, // never landed in the bundle
		}
		cmdIn := map[string]BundleCommandInput{
			"present": {Content: "c"},
			"skipped": {Content: "c", NoDistill: true},
			"absent":  {Content: "c"},
		}
		gotF := namesNeedingFragmentDistill(b, fragIn)
		gotP := namesNeedingPromptDistill(b, cmdIn)
		assert.Equal(t, []string{"present"}, gotF)
		assert.Equal(t, gotF, gotP, "the two selectors must pick identically")

		assert.Nil(t, namesNeedingFragmentDistill(b, nil))
		assert.Nil(t, namesNeedingPromptDistill(b, nil))
	})

	t.Run("distill* agree on the failure set", func(t *testing.T) {
		const body = "an item body of a perfectly ordinary and sufficient length"
		for _, tc := range []struct {
			name       string
			d          Distiller
			wantFailed bool
		}{
			{name: "nil distiller", d: nil, wantFailed: false},
			{name: "error", d: &recordingDistiller{returnErr: fmt.Errorf("boom")}, wantFailed: true},
			{name: "empty", d: &recordingDistiller{returnValue: "", returnModel: "m"}, wantFailed: true},
			{name: "too short", d: &recordingDistiller{returnValue: "x", returnModel: "m"}, wantFailed: true},
			{name: "good", d: &recordingDistiller{returnValue: "a real summary", returnModel: "m"}, wantFailed: false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				b := &bundles.Bundle{
					Fragments: map[string]bundles.BundleFragment{"item": {Content: body}},
					Commands:  map[string]bundles.BundleCommand{"item": {Content: body}},
				}
				fFailed := distillFragments(context.Background(), b, []string{"item"}, tc.d)
				pFailed := distillPrompts(context.Background(), b, []string{"item"}, tc.d)

				assert.Equal(t, tc.wantFailed, fFailed.Has("item"))
				assert.Equal(t, fFailed.Has("item"), pFailed.Has("item"),
					"the two distillers must agree on what counts as a failure")
				assert.Equal(t, b.Fragments["item"].Distilled, b.Commands["item"].Distilled,
					"the two distillers must write the same post-state")
				assert.Equal(t, b.Fragments["item"].DistilledBy, b.Commands["item"].DistilledBy)
			})
		}
	})

	t.Run("apply*Inputs agree on shape", func(t *testing.T) {
		b := &bundles.Bundle{}
		applyFragmentInputs(b, map[string]BundleFragmentInput{
			"i": {Content: "c", Tags: []string{"t"}, Notes: "n", Installation: "inst", NoDistill: true},
		})
		applyPromptInputs(b, map[string]BundleCommandInput{
			"i": {Content: "c", Tags: []string{"t"}, Notes: "n", Installation: "inst", NoDistill: true, Description: "d"},
		})
		f, c := b.Fragments["i"], b.Commands["i"]
		assert.Equal(t, f.Content, c.Content)
		assert.Equal(t, f.Tags, c.Tags)
		assert.Equal(t, f.Notes, c.Notes)
		assert.Equal(t, f.Installation, c.Installation)
		assert.Equal(t, f.NoDistill, c.NoDistill)
		assert.Equal(t, "d", c.Description, "only the command carries a description")

		// An empty input map leaves the maps untouched (all three arms).
		empty := &bundles.Bundle{}
		applyFragmentInputs(empty, nil)
		applyPromptInputs(empty, nil)
		applyMCPInputs(empty, nil)
		assert.Nil(t, empty.Fragments)
		assert.Nil(t, empty.Commands)
		assert.Nil(t, empty.MCP)
	})
}

// TestMustacheTagClassification pins the behaviour before the local
// TagType re-declaration is removed. Three untyped ints mirrored
// cbroglie/mustache's exported TagType enum by value, which is connascence of
// value with a third-party library — and it had already gone wrong once: the
// block used to be numbered one too high for every section-shaped tag, so
// checkTags never recursed into a `{{#name}}` body and a real Section was
// classified as a plain variable.
//
// This asserts the CLASSIFICATION, taken from tags the library itself parsed,
// so it survives the switch to mustache's own constants and would go red on
// any renumbering.
func TestMustacheTagClassification(t *testing.T) {
	cases := []struct {
		name          string
		src           string
		wantType      mustache.TagType
		wantHasChild  bool
		wantIsSection bool
	}{
		{name: "escaped variable", src: "{{v}}", wantType: mustache.Variable},
		{name: "raw variable braces", src: "{{{v}}}", wantType: mustache.Variable},
		{name: "raw variable ampersand", src: "{{&v}}", wantType: mustache.Variable},
		{name: "section", src: "{{#s}}body{{/s}}", wantType: mustache.Section, wantHasChild: true, wantIsSection: true},
		{name: "inverted section", src: "{{^s}}body{{/s}}", wantType: mustache.InvertedSection, wantHasChild: true, wantIsSection: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpl, err := mustache.ParseString(tc.src)
			require.NoError(t, err)
			tags := tmpl.Tags()
			require.Len(t, tags, 1, "fixture must parse to exactly one top-level tag")

			// The fixture is only hostile if the library really reports
			// the type this case is about — otherwise every arm below is
			// asserting against the same tag shape.
			require.Equal(t, tc.wantType, tags[0].Type(),
				"fixture %q did not parse to the tag type this case tests", tc.src)

			assert.Equal(t, tc.wantHasChild, hasChildTags(tags[0].Type()),
				"only section-shaped tags may be walked for children")

			// A section name must be treated as a section, never as an
			// undefined plain variable — that is the polarity flip the old
			// numbering caused.
			literals := undefinedPlainVariableLiterals(tags, map[string]string{})
			if tc.wantIsSection {
				assert.NotContains(t, literals, "s",
					"a section name must not be emitted as an undefined plain-variable literal")
			} else {
				assert.Contains(t, literals, "v",
					"an undefined plain variable must be preserved as a literal")
			}
		})
	}

	// The nested case is what the historical off-by-one actually broke:
	// checkTags never recursed into a section body, so nested undefined
	// variables went unwarned.
	t.Run("checkTags recurses into a section body", func(t *testing.T) {
		tmpl, err := mustache.ParseString("{{#outer}}{{inner_name}}{{/outer}}")
		require.NoError(t, err)

		var warnings []string
		checkTags(tmpl.Tags(), map[string]string{}, collections.NewSet[string](),
			func(w string) { warnings = append(warnings, w) })

		assert.Contains(t, warnings, "undefined variable: {{inner_name}}",
			"a variable nested inside a section must be reached")
	})
}

// TestListBundles_ThreadsTheCallersContext pins that ListBundles used to
// manufacture context.Background(), severing cancellation for the whole
// remote-aware listing path — including the removed-upstream pass, which walks
// git history across every installed clone and is the slow part a user
// interrupting `bundle list` means to stop. Both CLI callers had cmd.Context()
// in hand.
//
// This is a missing-seam row: the defect WAS the absent
// parameter, so an honest pre-fix pin does not compile rather than going red.
// What is pinned here is the contract the threading must not break — listing
// stays fault-tolerant, so a dead context still returns the locally-authored
// bundles rather than failing the whole listing.
func TestListBundles_ThreadsTheCallersContext(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "local-one"})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// The fixture is only hostile if the context is genuinely dead by
	// the time ListBundles is called.
	require.ErrorIs(t, ctx.Err(), context.Canceled)

	infos, err := ListBundles(ctx, cfg)
	require.NoError(t, err, "a cancelled context must not fail the whole listing")

	names := make([]string, 0, len(infos))
	for _, b := range infos {
		names = append(names, b.Name)
	}
	assert.Contains(t, names, "local-one",
		"locally-authored bundles do not depend on the cancellable remote pass")
}

// TestMissingFrom_BuiltinNamesWouldMaskAnExplicitMiss pins that the
// missing-explicit-asks calculation carried connascence of execution order: it
// HAD to run before appendBuiltinFragments reassigned loadedNames, and nothing
// but a comment said so. AssembleContext now computes it against loaderNames,
// which is never reassigned, so the constraint is structural rather than
// textual — but the hazard that made the ordering matter is real, and this
// records it so a future edit cannot re-fold builtin names in by accident.
//
// Pure refactor: behaviour is unchanged, so this is green
// before and after. What it demonstrates is WHY the order mattered.
func TestMissingFrom_BuiltinNamesWouldMaskAnExplicitMiss(t *testing.T) {
	requested := []string{"asked-for", "also-asked-for"}
	loaderNames := []string{"asked-for"} // "also-asked-for" never loaded

	assert.Equal(t, []string{"also-asked-for"}, missingFrom(requested, loaderNames),
		"an explicit ask the loader did not satisfy must be reported missing")

	// The hazard: an always-on builtin fragment happening to carry the same
	// name would erase the miss entirely if it were folded into the set the
	// calculation reads.
	withBuiltinsFolded := append(append([]string{}, loaderNames...), "also-asked-for")
	assert.Nil(t, missingFrom(requested, withBuiltinsFolded),
		"folding builtin names in silently satisfies an ask the user never got")

	// Order-independence of the corrected form: the input the calculation reads
	// is a snapshot, so evaluating it later cannot change the answer.
	assert.Equal(t, missingFrom(requested, loaderNames), missingFrom(requested, loaderNames))
}
