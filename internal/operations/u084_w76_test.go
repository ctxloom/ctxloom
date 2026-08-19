package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// sortContentInfos took an unvalidated sortBy and had no
// default branch, so any value other than "name"/"source" returned the input in
// whatever order the loader produced it — silently, with no warning. That is
// ctxloom's characteristic silent no-op wearing a sort's clothes: the caller
// asked for an ordering, got none, and was told nothing.
//
// The sibling ListProfiles (profiles.go) already had the correct shape — warn
// on an unknown sort_by and fall back to name — so this pin fixes the taxonomy
// in place rather than inventing a second one.
func TestSortContentInfos_UnknownSortByFallsBackToNameNotNoOp(t *testing.T) {
	// The fixture must be in an order that is NOT already
	// name-ascending, otherwise a no-op sort would pass for the wrong reason.
	infos := []bundles.ContentInfo{
		{Name: "zebra", Source: "a"},
		{Name: "apple", Source: "b"},
		{Name: "mango", Source: "c"},
	}
	require.False(t, namesAreAscendingW76(infos),
		"fixture must start out of order, else a no-op sort passes vacuously")

	sortContentInfos(infos, "bogus-field", "asc")

	assert.Equal(t, []string{"apple", "mango", "zebra"}, namesOfW76(infos),
		"an unknown sort_by must still yield a deterministic ordering, not the input order")
}

// The fallback must respect sort_order too —
// a "desc" request with an unknown field must not silently become ascending.
func TestSortContentInfos_UnknownSortByHonoursDescendingOrder(t *testing.T) {
	infos := []bundles.ContentInfo{
		{Name: "apple"},
		{Name: "zebra"},
		{Name: "mango"},
	}
	// Start ASCENDING-leaning so a no-op cannot look descending.
	require.NotEqual(t, []string{"zebra", "mango", "apple"}, namesOfW76(infos),
		"fixture must not already be in the asserted order")

	sortContentInfos(infos, "bogus-field", "desc")

	assert.Equal(t, []string{"zebra", "mango", "apple"}, namesOfW76(infos))
}

// containsTag folded the case of each TAG but not of the
// QUERY, so it carried an undocumented "caller must lowercase query"
// precondition. Every in-tree caller happens to honour it (ListFragments and
// SearchContent both pre-lower the query), which is exactly why it is a trap:
// the next caller inherits a silent false-negative with nothing to warn them.
//
// This defect is NOT observable at any public seam today — both public
// callers pre-lower — so this pin is deliberately at the UNIT altitude. The
// public-seam altitude is covered by the sort pins above; there is no
// public-seam pin for this half because there is no public seam that can reach
// it, and inventing one would mean removing the callers' own ToLower.
func TestContainsTag_QueryCaseIsNotACallerPrecondition(t *testing.T) {
	const query = "GoLang"
	// The query must genuinely carry upper case, and the tag
	// must genuinely be lower case, or the assertion proves nothing.
	require.NotEqual(t, strings.ToLower(query), query, "query fixture must be mixed-case")
	tags := []string{"golang", "best-practices"}
	for _, tag := range tags {
		require.Equal(t, strings.ToLower(tag), tag, "tag fixture must be lower-case")
	}

	assert.True(t, containsTag(tags, query),
		"containsTag must fold the query's case itself rather than require the caller to")
}

// At the public seam, ListFragments is the exported entry
// point that hands sort_by straight through, so an unknown value must still
// come back ordered.
func TestListFragments_UnknownSortByStillReturnsOrderedResults(t *testing.T) {
	_, loader := setupBundleTestFS(t)

	res, err := ListFragments(context.Background(), nil, ListFragmentsRequest{
		SortBy: "bogus-field",
		Loader: loader,
	})
	require.NoError(t, err)
	require.Equal(t, 4, res.Count)

	names := make([]string, 0, len(res.Fragments))
	for _, f := range res.Fragments {
		names = append(names, f.Name)
	}
	assert.Equal(t, []string{"golang", "python", "security", "testing"}, names,
		"an unknown sort_by must fall back to name order, not the loader's order")
}

func namesOfW76(infos []bundles.ContentInfo) []string {
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.Name)
	}
	return out
}

func namesAreAscendingW76(infos []bundles.ContentInfo) bool {
	for i := 1; i < len(infos); i++ {
		if strings.ToLower(infos[i-1].Name) > strings.ToLower(infos[i].Name) {
			return false
		}
	}
	return true
}


// NewRepoCache built its per-forge resolver inside
// `if registry, err := remote.NewRegistry(...); err == nil { ... }` — so a
// remotes.yaml that exists but cannot be parsed or read dropped every
// per-forge token_env with no warning at all. remote.NewRegistry already
// swallows os.IsNotExist internally, so a non-nil error from it is
// unambiguously "the file is there and broken", never "there is no file";
// the swallow could only ever hide a real failure.
//
// The user-visible consequence is a private-repo clone failing much later
// with a git auth error that says nothing about remotes.yaml.
func TestNewRepoCache_MalformedRemotesRegistryWarnsRatherThanSilentlyDroppingTokenEnv(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	remotesPath := paths.RemotesPath(dir)
	require.NoError(t, os.MkdirAll(filepath.Dir(remotesPath), 0o755))
	require.NoError(t, os.WriteFile(remotesPath, []byte("remotes: [this is not: a mapping\n"), 0o644))

	// Prove the fixture is hostile from the code-under-test's
	// vantage point — NewRegistry must actually FAIL on it. A merely odd but
	// parseable file would make the assertion below vacuous.
	_, regErr := remote.NewRegistry(remotesPath)
	require.Error(t, regErr, "fixture is not hostile: remote.NewRegistry parsed it fine")

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{dir}})
	var cache *remote.RepoCache
	stderr := captureStderr(t, func() { cache = NewRepoCache(cfg) })

	assert.NotNil(t, cache, "the cache is still built — this is a warning, not a hard failure")
	assert.Contains(t, stderr, "remotes registry", "the drop must be announced")
	assert.Contains(t, stderr, remotesPath, "the warning must name the unreadable file")
	assert.Contains(t, stderr, "token_env", "the warning must say what was lost")
}

// A project with NO remotes.yaml is the ordinary case and
// must stay silent, or the fix above trades one bug for a permanent nag.
func TestNewRepoCache_MissingRemotesRegistryStaysSilent(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, statErr := os.Stat(paths.RemotesPath(dir))
	require.True(t, os.IsNotExist(statErr), "fixture must have no remotes.yaml")

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{dir}})
	stderr := captureStderr(t, func() { _ = NewRepoCache(cfg) })

	assert.NotContains(t, stderr, "remotes registry",
		"a project that has simply never configured a remote must not be warned at")
}

// When EVERY backend the request asked for failed, ApplyHooks
// answered Status "partial", Backends [] and a nil error — so
// `ctxloom manage hooks install` printed "Hooks partial for: []" and exited 0
// having written nothing at all. That is ctxloom's characteristic silent
// no-op: exit 0, a success-shaped message, zero bytes on disk.
//
// Total failure is now Status "failed" plus a non-nil error, which every
// caller already routes (manage.go returns it, trust.go and mcp_server.go
// warn). Partial success — at least one backend took — is deliberately
// untouched and still a nil error; the control below pins that.
func TestApplyHooks_TotalFailureIsNotReportedAsPartialSuccess(t *testing.T) {
	readOnly := afero.NewReadOnlyFs(afero.NewMemMapFs())
	const workDir = "/project"

	loader := func() (*config.Config, error) {
		return config.NewFixture(config.Fixture{
			Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{
				SessionStart: []wire.Hook{{Command: "echo test", Type: "command"}},
			}},
		}), nil
	}

	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:      "claude-code",
		FS:           readOnly,
		ConfigLoader: loader,
		WorkDir:      workDir,
	})

	// Prove the fixture actually defeated the write from the
	// code-under-test's vantage point. If a settings file HAD appeared, or no
	// per-backend error had been recorded, the assertions below would be
	// asserting something other than total failure.
	require.NotNil(t, result)
	require.NotEmpty(t, result.Errors, "fixture is not hostile: no backend actually failed")
	require.Empty(t, result.Backends, "fixture is not hostile: a backend still succeeded")
	exists, existsErr := afero.Exists(readOnly, workDir+"/.claude/settings.json")
	require.NoError(t, existsErr)
	require.False(t, exists, "fixture is not hostile: settings.json was written anyway")

	require.Error(t, err, "an apply that configured NOTHING must not return a nil error")
	assert.Equal(t, "failed", result.Status,
		`"partial" needs something on both sides of it; nothing was applied`)
}

// Genuine partial success — one backend took, another did
// not — must stay a nil error and Status "partial". The fix must not
// promote every per-backend hiccup into a hard failure.
func TestApplyHooks_PartialSuccessStaysANilError(t *testing.T) {
	fs := afero.NewMemMapFs()
	tmpDir := "/project"

	loader := func() (*config.Config, error) {
		return config.NewFixture(config.Fixture{
			Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{
				SessionStart: []wire.Hook{{Command: "echo test", Type: "command"}},
			}},
		}), nil
	}

	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:      "claude-code",
		FS:           fs,
		ConfigLoader: loader,
		WorkDir:      tmpDir,
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Backends, "control fixture must have at least one backend succeed")
	assert.Equal(t, "applied", result.Status)
}

// This pin is a refutation of a prior review claim.
//
// The claim says ListFragments "returns the loader's error verbatim with no
// context — the caller cannot tell whether tag-listing or full-listing
// failed". Measured against the code, that consequence cannot occur:
//
//  1. bundles.Loader.List() has exactly one non-callback return statement,
//     `return bundles, nil`. It structurally cannot return a non-nil error.
//     Every read fault (unreadable bundles root, un-walkable directory,
//     corrupt bundle file) is reported through strictness.Fail, which streams
//     a diagnostic to stderr in BOTH modes and records a fatal-class finding
//     in strict mode. So the information the claim says is lost is delivered —
//     just on a different channel than the return value.
//  2. ListAllFragments and ListByTags each propagate only that error, so both
//     are equally incapable of failing; ListByTags is IMPLEMENTED as a filter
//     over ListAllFragments, so even a hypothetical error would be the same
//     error from the same origin. The distinction the claim asks ListFragments
//     to draw does not exist below it.
//
// The `if err != nil` at fragments.go:60 is therefore an unreachable branch,
// not a lossy one. Wrapping it would add context to an error that is never
// constructed — and no pin could drive that wrapping red, because
// ListFragments takes a CONCRETE *bundles.Loader with no failure seam.
//
// What this test holds instead: a genuinely unreadable bundles root produces
// a nil error AND a loud stderr line, which is the behaviour the claim assumed
// was missing.
func TestListFragments_UnreadableBundlesRootIsLoudNotALostError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the 0000 mode bit, so the fixture cannot be made hostile")
	}
	root := t.TempDir()
	bundlesDir := filepath.Join(root, "bundles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "b.yaml"),
		[]byte("version: \"1.0\"\nfragments:\n  hidden:\n    content: hi\n"), 0o644))
	require.NoError(t, os.Chmod(bundlesDir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(bundlesDir, 0o755) })

	// The directory must genuinely be unreadable from the
	// process's vantage point, or "nil error" below proves nothing.
	_, readErr := os.ReadDir(bundlesDir)
	require.Error(t, readErr, "fixture is not hostile: the bundles dir is still readable")

	loader := bundles.NewLoader(bundles.NewProjectReader(nil, []string{bundlesDir}))

	var res *ListFragmentsResult
	var err error
	stderr := captureStderr(t, func() {
		res, err = ListFragments(context.Background(), nil, ListFragmentsRequest{Loader: loader})
	})

	require.NoError(t, err, "the loader cannot return an error here — the branch U084-F12 targets is unreachable")
	require.NotNil(t, res)
	assert.Zero(t, res.Count, "nothing could be read, so nothing is listed")
	assert.Contains(t, stderr, bundlesDir,
		"the fault is reported on the diagnostic channel, naming the directory — it is not lost")
}

// `mustResource` was named for a contract it did not implement. Go's
// `must*` convention means "panic on failure"; this one did
// `data, _ := read(); return data` — it swallowed. Nothing downstream caught
// it either: config.ParseConfig(nil) is yaml.Unmarshal of nil bytes, which
// SUCCEEDS and yields an empty *Config. So a build with a broken embed made
// `ctxloom init` write a config.yaml with no settings, no mcp block and no llm
// registry, at exit 0, and call it a project.
//
// TWO ALTITUDES, and they are not equivalent:
//
//	UNIT altitude (this test): readResource takes the reader as a parameter, so
//	a failing reader can be injected. This is where the swallow is actually
//	observable.
//
//	PUBLIC-SEAM altitude (the second test below): BuildInitialConfig reads from
//	an embed.FS whose contents are asserted present at build time by
//	internal/config/arch_test.go, so the failure cannot be provoked there. That
//	test pins the shape the swallow used to corrupt — a real init config is
//	never hollow — rather than the failure itself.
func TestReadResource_PropagatesTheReadFailureInsteadOfReturningNilBytes(t *testing.T) {
	sentinel := errors.New("embed is broken")
	failing := func() ([]byte, error) { return nil, sentinel }

	// Prove the injected reader really fails, and really
	// returns nil bytes — the exact shape the old helper laundered into a
	// successful empty parse.
	data, readErr := failing()
	require.Error(t, readErr, "fixture is not hostile: the reader succeeded")
	require.Nil(t, data, "fixture is not hostile: the reader returned bytes")
	parsed, parseErr := config.ParseConfig(nil)
	require.NoError(t, parseErr,
		"the premise of this row: ParseConfig(nil) does NOT fail, so nothing downstream would have caught the swallow")
	require.NotNil(t, parsed)

	got, err := readResource(failing, "init scaffold")

	require.Error(t, err, "a failed embed read must not be laundered into nil bytes")
	assert.ErrorIs(t, err, sentinel, "the underlying cause must survive")
	assert.Contains(t, err.Error(), "init scaffold", "the error must name which resource failed")
	assert.Nil(t, got)
}

// At the public seam, the failure itself is unprovokable here —
// the embed is asserted present at build time — so this pins the CONSEQUENCE
// the swallow used to produce. A scaffolded config must never come out hollow.
func TestBuildInitialConfig_NeverScaffoldsAHollowConfig(t *testing.T) {
	data, err := BuildInitialConfig("mock", "")
	require.NoError(t, err)
	require.NotEmpty(t, data)

	cfg, err := config.ParseConfig(data)
	require.NoError(t, err)
	assert.NotEmpty(t, cfg.GetLMConfig().Configs,
		"an llm registry is the thing the swallowed-embed path lost first")
	assert.NotEmpty(t, cfg.DefaultAgentProfiles(),
		"the seed agent must be bound to a profile, not left empty")
}

// This test is the refutation, made mechanical, of a prior review claim.
//
// The claim asserts of an 8-file subset of this package: "These files share
// only a package name. Nine unrelated concerns, 42 internal dependencies, no
// common type, no common invariant." Measured, three of those four claims do
// not hold, and the framing of the fourth is an artifact of the review's own
// slicing.
//
// MEASURED (production files only, at this commit):
//
//	                      the cited 8-file subset   whole internal/operations
//	files                          8                        61
//	NLOC (lizard)              1,264                    12,770
//	functions                     57                       623
//	average CCN                  4.0                       4.4
//	functions over CCN 10      4 (7.0%)                 43 (6.9%)
//	distinct internal/ deps       15                        39
//
// So the set is marginally LESS complex than the package it is drawn from —
// it is not a hotspot, and 15 distinct dependencies across eight facade files
// is what a facade over remote/, bundles/, config/ and lm/backends/ costs by
// construction, not evidence of incohesion. (The claim's "42" matches neither
// the per-file nor the package-wide distinct-dependency count.)
//
// "No common type, no common invariant" is the claim this test answers
// directly. The package's contract is written down in doc.go — operations take
// *config.Config, return JSON-serializable structs, and do no formatting, so
// that MCP tools and CLI commands are thin adapters over ONE implementation.
// Six of the eight files declare <Verb>Request/<Verb>Result pairs against it
// (14 pairs in these files; 77 Request types package-wide). The two that do
// not — helpers.go and legacy_cleanup.go — are that contract's shared
// substrate, not a ninth unrelated concern.
//
// And "all 9 files" describes a review-tool PARTITION of a 61-file package,
// not a grouping anyone designed. Observing that 9 of 61 files cover 9
// concerns is a statement about the slicing.
//
// What is left, and is real, is that internal/operations is a large package.
// That is a package-decomposition question, not this cohesion claim, and
// is not settled by a test.
func TestOperationsRequestResultEnvelopesShareOneJSONContract(t *testing.T) {
	// One instance per Request/Result envelope declared in the cited file set.
	envelopes := []any{
		ApplyHooksRequest{}, ApplyHooksResult{},
		ListFragmentsRequest{}, ListFragmentsResult{},
		GetFragmentRequest{}, GetFragmentResult{},
		InitializeProjectRequest{}, InitializeProjectResult{},
		GetItemRequest{}, GetItemResult{},
		AddItemRequest{}, AddItemResult{},
		DeleteItemRequest{}, DeleteItemResult{},
		SetItemContentRequest{}, SetItemContentResult{},
		DistillItemRequest{}, DistillItemResult{},
		GetBundleMCPRequest{}, GetBundleMCPResult{},
		SetBundleMCPRequest{}, SetBundleMCPResult{},
	}
	// The map/weave fan (MapProfilesRequest, WeaveRequest, WeaveResult, Part,
	// Synthesizer) that once sat here was retired along with `ctxloom weave`;
	// it is no longer part of this package's envelope set.
	// An empty list, or one whose members carried no exported
	// fields, would make every assertion below vacuously true.
	require.Greater(t, len(envelopes), 20, "the sample must be the real envelope set")

	for _, e := range envelopes {
		typ := reflect.TypeOf(e)
		t.Run(typ.Name(), func(t *testing.T) {
			raw, err := json.Marshal(e)
			require.NoError(t, err, "doc.go: operations return JSON-serializable structs")
			assert.True(t, bytes.HasPrefix(raw, []byte("{")),
				"an operation envelope marshals as a JSON object, not a scalar")

			exported := 0
			for i := range typ.NumField() {
				f := typ.Field(i)
				if f.PkgPath != "" {
					continue // unexported
				}
				exported++
				assert.NotEmpty(t, f.Tag.Get("json"),
					"%s.%s crosses the MCP/CLI adapter boundary and must carry an explicit json tag (or `json:\"-\"`)",
					typ.Name(), f.Name)
			}
			assert.Positive(t, exported, "an envelope with no exported fields would make this test vacuous")
		})
	}
}
