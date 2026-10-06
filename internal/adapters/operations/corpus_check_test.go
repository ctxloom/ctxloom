package operations

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// corpusTipSHA is the commit every fixture remote's default branch resolves
// to. The check must read at exactly this commit; a read at any other ref is
// a read of a tree nobody pinned.
const corpusTipSHA = "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00"

// cleanEnvelope is a tree bundle's bundle.yaml: version and nothing inline.
const cleanEnvelope = "schema_version: 1\nversion: 1.0.0\n"

// violatingEnvelope carries a key the Bundle schema does not model. This is
// the exact shape that broke the published corpus when ParseBundle went strict
// (merge 91a7bacd): legal under the old permissive decode, refused under the
// new one.
const violatingEnvelope = "schema_version: 1\nversion: 1.0.0\nhoooks:\n  pre: echo typo\n"

// cleanTree is one healthy published bundle: an envelope and one item.
func cleanTree() map[string]string {
	return map[string]string{
		bundles.DirectoryFormManifest: cleanEnvelope,
		"fragments/greeting.md":       "hello\n",
	}
}

// treeOf lays doc out with the production tree writer and returns the
// bundle's files keyed bundle-relative, so a fixture publishes exactly the
// bytes an authored bundle would.
func treeOf(t *testing.T, doc string) map[string]string {
	t.Helper()
	fsys := afero.NewMemMapFs()
	dir := path.Dir(filepath.ToSlash(bundletree.Write(t, fsys, "/root", "b", doc)))
	out := map[string]string{}
	require.NoError(t, afero.Walk(fsys, dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, err := afero.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		out[strings.TrimPrefix(filepath.ToSlash(p), dir+"/")] = string(data)
		return nil
	}))
	return out
}

// publishCorpusFile serves body at the repo path p on fetcher, listing p in every
// directory above it.
func publishCorpusFile(fetcher *remote.MockFetcher, p, body string) {
	fetcher.WithFile(p, []byte(body))
	child, isDir := path.Base(p), false
	for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
		listed := false
		for _, e := range fetcher.Dirs[dir] {
			listed = listed || e.Name == child
		}
		if !listed {
			fetcher.WithDir(dir, append(fetcher.Dirs[dir], remote.DirEntry{Name: child, IsDir: isDir}))
		}
		child, isDir = path.Base(dir), true
	}
}

// tippedFetcher is a mock remote whose default branch resolves to corpusTipSHA.
func tippedFetcher() *remote.MockFetcher {
	fetcher := remote.NewMockFetcher()
	return fetcher.WithRef(fetcher.DefaultBranch, corpusTipSHA)
}

// corpusFixture builds a mock remote publishing each bundle's files under
// <bundles-root>/<name>/, plus the FetcherOpener that serves it.
func corpusFixture(t *testing.T, published map[string]map[string]string) (*remote.MockFetcher, FetcherOpener) {
	t.Helper()
	fetcher := tippedFetcher()
	for name, files := range published {
		for rel, body := range files {
			publishCorpusFile(fetcher, corpusBundlesDir+"/"+name+"/"+rel, body)
		}
	}
	return fetcher, func(string) (remote.Fetcher, error) { return fetcher, nil }
}

func oneRemote() []CorpusRemote {
	return []CorpusRemote{{Name: "origin", URL: "https://github.com/acme/ctxloom-content"}}
}

// TestCorpusViolatingBundleFailsAndIsNamed is the gate's reason to exist: a
// published bundle that will not read must fail the check AND be named with
// the reason. It runs the REAL reader (readBundleTree → bundles.ReadTree), so
// it tracks whatever the schema enforces today rather than a stand-in.
func TestCorpusViolatingBundleFailsAndIsNamed(t *testing.T) {
	bad := cleanTree()
	bad[bundles.DirectoryFormManifest] = violatingEnvelope
	_, open := corpusFixture(t, map[string]map[string]string{"good": cleanTree(), "bad": bad})

	report := CheckCorpus(context.Background(), oneRemote(), open, readBundleTree)

	assert.Equal(t, CorpusViolated, report.Verdict(), "a corpus with an unreadable bundle must not pass")
	require.Len(t, report.Violations, 1)
	v := report.Violations[0]
	assert.Equal(t, corpusBundlesDir+"/bad/bundle.yaml", v.Bundle.Path, "the offending bundle must be named by its repo path")
	assert.Equal(t, "origin", v.Bundle.Remote)
	require.Error(t, v.Err)
	// A count is not actionable; the offending KEY has to reach the reader.
	assert.Contains(t, v.Err.Error(), "hoooks", "the parse error must name what is wrong, not merely that something is")
	// The clean sibling still parsed: one bad bundle does not abort the sweep,
	// so a corpus with three violations reports three, not the first.
	assert.Equal(t, 1, report.Parsed)
	assert.Empty(t, report.Gaps)
}

// TestCorpusItemFailingSchemaIsAViolation is why the check reads the whole
// tree rather than the envelope: an item file a load refuses is a bundle a load
// refuses, however clean its bundle.yaml is.
func TestCorpusItemFailingSchemaIsAViolation(t *testing.T) {
	tree := cleanTree()
	tree["profiles/broken.yaml"] = "fragments: 42\n"
	_, open := corpusFixture(t, map[string]map[string]string{"itembad": tree})

	report := CheckCorpus(context.Background(), oneRemote(), open, readBundleTree)

	assert.Equal(t, CorpusViolated, report.Verdict(), "an item the reader refuses must fail the gate")
	require.Len(t, report.Violations, 1)
	assert.Equal(t, corpusBundlesDir+"/itembad/bundle.yaml", report.Violations[0].Bundle.Path)
	assert.Contains(t, report.Violations[0].Err.Error(), "profiles/broken.yaml", "the violation must name the offending item file")
	assert.Zero(t, report.Parsed)
}

// TestCorpusItemlessBundleIsAViolation: a published bundle that declares no
// items is refused by the remote load path, so the gate must refuse it too —
// otherwise it passes content every consumer then fails to load.
func TestCorpusItemlessBundleIsAViolation(t *testing.T) {
	_, open := corpusFixture(t, map[string]map[string]string{
		"hollow": {bundles.DirectoryFormManifest: cleanEnvelope},
	})

	report := CheckCorpus(context.Background(), oneRemote(), open, readBundleTree)

	assert.Equal(t, CorpusViolated, report.Verdict(), "an item-less published bundle must fail the gate")
	require.Len(t, report.Violations, 1)
	assert.Equal(t, corpusBundlesDir+"/hollow/bundle.yaml", report.Violations[0].Bundle.Path)
	assert.Zero(t, report.Parsed)
}

// TestCorpusCleanPasses pins the other side: a corpus whose every bundle reads
// is clean and exits 0. Without this the gate could satisfy every other test by
// always failing.
func TestCorpusCleanPasses(t *testing.T) {
	_, open := corpusFixture(t, map[string]map[string]string{"alpha": cleanTree(), "beta": cleanTree()})

	report := CheckCorpus(context.Background(), oneRemote(), open, readBundleTree)

	assert.Equal(t, CorpusClean, report.Verdict())
	assert.Empty(t, report.Violations)
	assert.Empty(t, report.Gaps)
	assert.Equal(t, 2, report.Parsed, "a clean verdict must rest on bundles actually parsed")
	assert.Equal(t, 1, report.RemotesRead)
}

// TestCorpusEmptyIsUndeterminedNotClean covers this project's characteristic
// failure: exit 0, success message, zero work done. A remote publishing no
// bundles at all yields nothing to compare against the schema, and reporting
// that as a pass is a gate guarding nothing.
func TestCorpusEmptyIsUndeterminedNotClean(t *testing.T) {
	// No WithDir at all: the mock reports the bundles directory as absent,
	// exactly as a repo that publishes no bundles does.
	fetcher := tippedFetcher()
	open := func(string) (remote.Fetcher, error) { return fetcher, nil }

	report := CheckCorpus(context.Background(), oneRemote(), open, readBundleTree)

	assert.Equal(t, CorpusUndetermined, report.Verdict(), "an empty corpus must not report success")
	assert.NotEqual(t, CorpusClean, report.Verdict())
	assert.Zero(t, report.Parsed)
	assert.Empty(t, report.Violations)
	// Publishing no bundles is legal, not unreadable: an absent bundles
	// directory must not be reported as a gap, or the gate fires on a healthy
	// remote. The Parsed == 0 guard above is what keeps that leniency honest.
	assert.Empty(t, report.Gaps)
}

// TestCorpusNoRemotesIsUndetermined is the same guard one level up: a project
// with nothing configured has checked nothing.
func TestCorpusNoRemotesIsUndetermined(t *testing.T) {
	open := func(string) (remote.Fetcher, error) {
		t.Fatal("no remote is configured, so nothing may be opened")
		return nil, nil
	}

	report := CheckCorpus(context.Background(), nil, open, readBundleTree)

	assert.Equal(t, CorpusUndetermined, report.Verdict())
	assert.Zero(t, report.Parsed)
}

// TestCorpusUnreadableRemoteIsUndeterminedAndNamed proves an unreadable remote
// is reported as a GAP rather than folded into either verdict. It must name the
// remote and carry the cause, or the operator cannot tell a missing clone from
// a broken one.
func TestCorpusUnreadableRemoteIsUndeterminedAndNamed(t *testing.T) {
	boom := errors.New("git clone failed: no such host")
	fetcher := tippedFetcher()
	fetcher.ListDirErr = boom
	open := func(string) (remote.Fetcher, error) { return fetcher, nil }

	report := CheckCorpus(context.Background(), oneRemote(), open, readBundleTree)

	assert.Equal(t, CorpusUndetermined, report.Verdict())
	require.Len(t, report.Gaps, 1)
	assert.Equal(t, "origin", report.Gaps[0].Remote)
	assert.ErrorIs(t, report.Gaps[0].Err, boom, "the gap must carry why the corpus could not be read")
	assert.Zero(t, report.RemotesRead, "a remote whose listing failed was not read")
	assert.Empty(t, report.Violations, "an unreadable remote is not a content violation")
}

// TestCorpusUnresolvableTipIsAGap: a remote whose tip cannot be pinned to a
// commit is not read at all. Reading it unpinned would let the listing and the
// file reads see different trees, and the verdict would describe neither.
func TestCorpusUnresolvableTipIsAGap(t *testing.T) {
	noTip := errors.New("ref not found: main")
	fetcher, open := corpusFixture(t, map[string]map[string]string{"alpha": cleanTree()})
	fetcher.ResolveRefErr = noTip

	report := CheckCorpus(context.Background(), oneRemote(), open, readBundleTree)

	assert.Equal(t, CorpusUndetermined, report.Verdict())
	require.Len(t, report.Gaps, 1)
	assert.ErrorIs(t, report.Gaps[0].Err, noTip)
	assert.Empty(t, fetcher.ListDirCalls, "an unpinned remote must not be listed")
	assert.Zero(t, report.Parsed)
}

// TestCorpusPartialReadIsUndeterminedNotClean is the anti-false-green case: one
// remote reads clean while another cannot be read at all. Counting only what
// was successfully parsed would report OK for a corpus half of which was never
// looked at.
func TestCorpusPartialReadIsUndeterminedNotClean(t *testing.T) {
	good, _ := corpusFixture(t, map[string]map[string]string{"alpha": cleanTree()})
	broken := tippedFetcher()
	broken.ListDirErr = errors.New("clone missing")

	open := func(url string) (remote.Fetcher, error) {
		if strings.Contains(url, "broken") {
			return broken, nil
		}
		return good, nil
	}
	remotes := []CorpusRemote{
		{Name: "good", URL: "https://github.com/acme/good"},
		{Name: "broken", URL: "https://github.com/acme/broken"},
	}

	report := CheckCorpus(context.Background(), remotes, open, readBundleTree)

	assert.Equal(t, CorpusUndetermined, report.Verdict(),
		"bundles that did parse cannot vouch for a remote that was never read")
	assert.Equal(t, 1, report.Parsed)
	require.Len(t, report.Gaps, 1)
	assert.Equal(t, "broken", report.Gaps[0].Remote)
}

// TestCorpusViolationOutranksGap fixes the precedence: a bundle demonstrably
// unreadable is a finding whether or not some other remote was also
// unreachable, so the exit code says "violations" rather than "could not look".
func TestCorpusViolationOutranksGap(t *testing.T) {
	report := CorpusReport{
		Parsed:     1,
		Violations: []CorpusViolation{{Bundle: CorpusBundle{Remote: "r", Path: "p"}, Err: errors.New("nope")}},
		Gaps:       []CorpusGap{{Remote: "other", Err: errors.New("unreachable")}},
	}
	assert.Equal(t, CorpusViolated, report.Verdict())
}

// TestCorpusOpenFailureIsAGap proves a remote whose fetcher cannot even be
// constructed is undetermined rather than skipped — a URL whose forge is
// unrecognised must not quietly drop out of the corpus.
func TestCorpusOpenFailureIsAGap(t *testing.T) {
	open := func(string) (remote.Fetcher, error) { return nil, errors.New("unknown forge") }

	report := CheckCorpus(context.Background(), oneRemote(), open, readBundleTree)

	assert.Equal(t, CorpusUndetermined, report.Verdict())
	require.Len(t, report.Gaps, 1)
	assert.Contains(t, report.Gaps[0].Err.Error(), "unknown forge")
}

// TestCorpusUnreadableBundleIsAGapNotAViolation keeps the two failure kinds
// apart at bundle granularity: a bundle listed but not fetchable was never
// seen, and calling that a schema violation would send someone editing content
// that may be perfectly fine.
func TestCorpusUnreadableBundleIsAGapNotAViolation(t *testing.T) {
	fetcher := tippedFetcher()
	// Listed, but no corresponding file: FetchFile reports not-found.
	fetcher.WithDir(corpusBundlesDir, []remote.DirEntry{{Name: "ghost", IsDir: true}})
	fetcher.WithDir(corpusBundlesDir+"/ghost", []remote.DirEntry{{Name: "bundle.yaml"}})
	open := func(string) (remote.Fetcher, error) { return fetcher, nil }

	report := CheckCorpus(context.Background(), oneRemote(), open, readBundleTree)

	assert.Equal(t, CorpusUndetermined, report.Verdict())
	assert.Empty(t, report.Violations)
	require.Len(t, report.Gaps, 1)
	assert.Equal(t, corpusBundlesDir+"/ghost/bundle.yaml", report.Gaps[0].Path)
}

// TestCorpusTraversalEntryIsAViolation: a published tree whose listing names
// an entry outside it is refused by the remote load path before a byte is
// fetched. That is a fact about the content, not a failure to look, so it is
// a violation rather than a gap.
func TestCorpusTraversalEntryIsAViolation(t *testing.T) {
	fetcher, open := corpusFixture(t, map[string]map[string]string{"sly": cleanTree()})
	dir := corpusBundlesDir + "/sly"
	fetcher.WithDir(dir, append(fetcher.Dirs[dir], remote.DirEntry{Name: ".."}))

	report := CheckCorpus(context.Background(), oneRemote(), open, readBundleTree)

	assert.Equal(t, CorpusViolated, report.Verdict())
	require.Len(t, report.Violations, 1)
	assert.ErrorIs(t, report.Violations[0].Err, content.ErrBadPath)
	assert.Empty(t, report.Gaps)
}

// TestCorpusWalksNestedBundles proves the sweep recurses through format roots,
// so a bundle published below one is checked too. Without this a whole
// publishing shape could violate the schema unnoticed.
func TestCorpusWalksNestedBundles(t *testing.T) {
	bad := cleanTree()
	bad[bundles.DirectoryFormManifest] = violatingEnvelope
	_, open := corpusFixture(t, map[string]map[string]string{"v2/tree": bad})

	report := CheckCorpus(context.Background(), oneRemote(), open, readBundleTree)

	assert.Equal(t, CorpusViolated, report.Verdict())
	require.Len(t, report.Violations, 1)
	assert.Equal(t, corpusBundlesDir+"/v2/tree/bundle.yaml", report.Violations[0].Bundle.Path)
}

// TestConfiguredCorpusComesFromTheRemotesRegistry pins the requirement that the
// gate is pointed at the corpus the PROJECT is configured against and never at
// a list baked into the code: adding a remote must extend the gate with no
// edit here.
func TestConfiguredCorpusComesFromTheRemotesRegistry(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "remotes.yaml"), []byte(
		"remotes:\n"+
			"    alpha:\n"+
			"        url: https://github.com/acme/alpha\n"+
			"    beta:\n"+
			"        url: https://github.com/acme/beta\n"), 0o644))
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	corpus, err := ConfiguredCorpus(cfg)
	require.NoError(t, err)

	byName := map[string]string{}
	for _, rem := range corpus {
		byName[rem.Name] = rem.URL
	}
	assert.Equal(t, map[string]string{
		"alpha": "https://github.com/acme/alpha",
		"beta":  "https://github.com/acme/beta",
	}, byName, "the corpus must be exactly what remotes.yaml declares")
}

// TestCorpusReadsAtTheResolvedTipWithoutFetching pins two contracts at once.
// Every read names the commit the default branch resolved to, so the listing
// that decided what the corpus holds and the reads that produced its bytes see
// one tree. And the check only ever READS, so a warm cache needs no network —
// a forge call smuggled in here would make the gate unrunnable in CI and the
// acceptance suite.
func TestCorpusReadsAtTheResolvedTipWithoutFetching(t *testing.T) {
	fetcher, open := corpusFixture(t, map[string]map[string]string{"alpha": cleanTree()})

	report := CheckCorpus(context.Background(), oneRemote(), open, readBundleTree)

	require.Equal(t, CorpusClean, report.Verdict())
	require.NotEmpty(t, fetcher.ResolveRefCalls)
	assert.Equal(t, fetcher.DefaultBranch, fetcher.ResolveRefCalls[0].Ref, "the tip is the default branch's")
	require.NotEmpty(t, fetcher.ListDirCalls)
	require.NotEmpty(t, fetcher.FetchFileCalls)
	for _, c := range fetcher.ListDirCalls {
		assert.Equal(t, corpusTipSHA, c.Ref, "listing %s must be pinned to the resolved tip", c.Path)
	}
	for _, c := range fetcher.FetchFileCalls {
		assert.Equal(t, corpusTipSHA, c.Ref, "reading %s must be pinned to the resolved tip", c.Path)
	}
	assert.Empty(t, fetcher.SearchReposCalls, "the corpus check must not reach a forge API")
}

// TestCorpusTreeBundleIsOneBundleNotItsSidecars pins the bundle BOUNDARY: a
// directory holding a bundle manifest is one bundle, and the item files beneath
// it — an MCP server's `.meta.yaml` sidecar among them — are that bundle's
// payload, not more bundles to check.
//
// A walk that treats every `.yaml` beneath the bundles root as a bundle hands
// each sidecar to the envelope parser, which refuses it — and the gate reports
// a violation per item against a corpus that has none. That is the expensive
// direction for a gate to fail in: it fires on healthy content, and the remedy
// a reader reaches for is to stop running it.
func TestCorpusTreeBundleIsOneBundleNotItsSidecars(t *testing.T) {
	tree := treeOf(t, `version: 1.0.0
mcp:
  postgres:
    command: mcp-postgres
    notes: Read-only connection to the app database.
    installation: brew install mcp-postgres
`)
	var sidecars int
	for rel := range tree {
		if content.IsMetaPath(rel) {
			sidecars++
		}
	}
	require.NotZero(t, sidecars, "the fixture must publish an item sidecar, or it cannot test the boundary")
	_, open := corpusFixture(t, map[string]map[string]string{"v2/atelier": tree})

	report := CheckCorpus(context.Background(), oneRemote(), open, readBundleTree)

	for _, v := range report.Violations {
		t.Errorf("healthy tree bundle reported a violation at %s: %v", v.Bundle.Path, v.Err)
	}
	// The bundle must still have been READ. Without this the assertion above
	// is satisfied by a walk that skips tree directories entirely — no
	// violations, because nothing was checked, which is the silent pass the
	// Parsed counter exists to expose.
	assert.Equal(t, 1, report.Parsed, "the tree is the one bundle this corpus publishes")
	assert.Empty(t, report.Gaps)
	assert.Equal(t, CorpusClean, report.Verdict())
}
