package operations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// cleanBundleYAML parses under the current bundle schema.
const cleanBundleYAML = `version: 1.0.0
fragments:
  greeting:
    content: hello
`

// violatingBundleYAML carries a key the Bundle schema does not model. This is
// the exact shape that broke the published corpus when ParseBundle went strict
// (merge 91a7bacd): legal under the old permissive decode, refused under the
// new one.
const violatingBundleYAML = `version: 1.0.0
fragments:
  greeting:
    content: hello
hoooks:
  pre: echo typo
`

// corpusFixture builds a mock remote publishing bundles at
// .ctxloom/content/bundles/<name>.yaml, plus the FetcherOpener that serves it.
func corpusFixture(t *testing.T, bundles map[string]string) (*remote.MockFetcher, FetcherOpener) {
	t.Helper()
	fetcher := remote.NewMockFetcher()
	entries := make([]remote.DirEntry, 0, len(bundles))
	for name, body := range bundles {
		entries = append(entries, remote.DirEntry{Name: name + ".yaml"})
		fetcher.WithFile(corpusBundlesDir+"/"+name+".yaml", []byte(body))
	}
	fetcher.WithDir(corpusBundlesDir, entries)
	return fetcher, func(string) (remote.Fetcher, error) { return fetcher, nil }
}

func oneRemote() []CorpusRemote {
	return []CorpusRemote{{Name: "origin", URL: "https://github.com/acme/ctxloom-content"}}
}

// TestCorpusViolatingBundleFailsAndIsNamed is the gate's reason to exist: a
// published bundle that will not parse must fail the check AND be named with
// the reason. It runs the REAL parser (parseBundleBytes → bundles.ParseBundle),
// so it tracks whatever the schema enforces today rather than a stand-in.
func TestCorpusViolatingBundleFailsAndIsNamed(t *testing.T) {
	_, open := corpusFixture(t, map[string]string{
		"good": cleanBundleYAML,
		"bad":  violatingBundleYAML,
	})

	report := CheckCorpus(context.Background(), oneRemote(), open, parseBundleBytes)

	assert.Equal(t, CorpusViolated, report.Verdict(), "a corpus with an unparseable bundle must not pass")
	require.Len(t, report.Violations, 1)
	v := report.Violations[0]
	assert.Equal(t, corpusBundlesDir+"/bad.yaml", v.Bundle.Path, "the offending bundle must be named by its repo path")
	assert.Equal(t, "origin", v.Bundle.Remote)
	require.Error(t, v.Err)
	// A count is not actionable; the offending KEY has to reach the reader.
	assert.Contains(t, v.Err.Error(), "hoooks", "the parse error must name what is wrong, not merely that something is")
	// The clean sibling still parsed: one bad bundle does not abort the sweep,
	// so a corpus with three violations reports three, not the first.
	assert.Equal(t, 1, report.Parsed)
	assert.Empty(t, report.Gaps)
}

// TestCorpusCleanPasses pins the other side: a corpus whose every bundle parses
// is clean and exits 0. Without this the gate could satisfy every other test by
// always failing.
func TestCorpusCleanPasses(t *testing.T) {
	_, open := corpusFixture(t, map[string]string{
		"alpha": cleanBundleYAML,
		"beta":  cleanBundleYAML,
	})

	report := CheckCorpus(context.Background(), oneRemote(), open, parseBundleBytes)

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
	fetcher := remote.NewMockFetcher()
	open := func(string) (remote.Fetcher, error) { return fetcher, nil }

	report := CheckCorpus(context.Background(), oneRemote(), open, parseBundleBytes)

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

	report := CheckCorpus(context.Background(), nil, open, parseBundleBytes)

	assert.Equal(t, CorpusUndetermined, report.Verdict())
	assert.Zero(t, report.Parsed)
}

// TestCorpusUnreadableRemoteIsUndeterminedAndNamed proves an unreadable remote
// is reported as a GAP rather than folded into either verdict. It must name the
// remote and carry the cause, or the operator cannot tell a missing clone from
// a broken one.
func TestCorpusUnreadableRemoteIsUndeterminedAndNamed(t *testing.T) {
	boom := errors.New("git clone failed: no such host")
	fetcher := remote.NewMockFetcher()
	fetcher.ListDirErr = boom
	open := func(string) (remote.Fetcher, error) { return fetcher, nil }

	report := CheckCorpus(context.Background(), oneRemote(), open, parseBundleBytes)

	assert.Equal(t, CorpusUndetermined, report.Verdict())
	require.Len(t, report.Gaps, 1)
	assert.Equal(t, "origin", report.Gaps[0].Remote)
	assert.ErrorIs(t, report.Gaps[0].Err, boom, "the gap must carry why the corpus could not be read")
	assert.Zero(t, report.RemotesRead, "a remote whose listing failed was not read")
	assert.Empty(t, report.Violations, "an unreadable remote is not a content violation")
}

// TestCorpusPartialReadIsUndeterminedNotClean is the anti-false-green case: one
// remote reads clean while another cannot be read at all. Counting only what
// was successfully parsed would report OK for a corpus half of which was never
// looked at.
func TestCorpusPartialReadIsUndeterminedNotClean(t *testing.T) {
	good, _ := corpusFixture(t, map[string]string{"alpha": cleanBundleYAML})
	broken := remote.NewMockFetcher()
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

	report := CheckCorpus(context.Background(), remotes, open, parseBundleBytes)

	assert.Equal(t, CorpusUndetermined, report.Verdict(),
		"bundles that did parse cannot vouch for a remote that was never read")
	assert.Equal(t, 1, report.Parsed)
	require.Len(t, report.Gaps, 1)
	assert.Equal(t, "broken", report.Gaps[0].Remote)
}

// TestCorpusViolationOutranksGap fixes the precedence: a bundle demonstrably
// unparseable is a finding whether or not some other remote was also
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

	report := CheckCorpus(context.Background(), oneRemote(), open, parseBundleBytes)

	assert.Equal(t, CorpusUndetermined, report.Verdict())
	require.Len(t, report.Gaps, 1)
	assert.Contains(t, report.Gaps[0].Err.Error(), "unknown forge")
}

// TestCorpusUnreadableBundleIsAGapNotAViolation keeps the two failure kinds
// apart at file granularity: a bundle listed but not fetchable was never seen,
// and calling that a schema violation would send someone editing content that
// may be perfectly fine.
func TestCorpusUnreadableBundleIsAGapNotAViolation(t *testing.T) {
	fetcher := remote.NewMockFetcher()
	// Listed, but no corresponding file: FetchFile reports not-found.
	fetcher.WithDir(corpusBundlesDir, []remote.DirEntry{{Name: "ghost.yaml"}})
	open := func(string) (remote.Fetcher, error) { return fetcher, nil }

	report := CheckCorpus(context.Background(), oneRemote(), open, parseBundleBytes)

	assert.Equal(t, CorpusUndetermined, report.Verdict())
	assert.Empty(t, report.Violations)
	require.Len(t, report.Gaps, 1)
	assert.Equal(t, corpusBundlesDir+"/ghost.yaml", report.Gaps[0].Path)
}

// TestCorpusWalksDirectoryFormBundles proves the sweep recurses, so a
// directory-form bundle (<name>/bundle.yaml) is checked too. Without this a
// whole publishing shape could violate the schema unnoticed.
func TestCorpusWalksDirectoryFormBundles(t *testing.T) {
	fetcher := remote.NewMockFetcher()
	fetcher.WithDir(corpusBundlesDir, []remote.DirEntry{{Name: "tree", IsDir: true}})
	fetcher.WithDir(corpusBundlesDir+"/tree", []remote.DirEntry{{Name: "bundle.yaml"}})
	fetcher.WithFile(corpusBundlesDir+"/tree/bundle.yaml", []byte(violatingBundleYAML))
	open := func(string) (remote.Fetcher, error) { return fetcher, nil }

	report := CheckCorpus(context.Background(), oneRemote(), open, parseBundleBytes)

	assert.Equal(t, CorpusViolated, report.Verdict())
	require.Len(t, report.Violations, 1)
	assert.Equal(t, corpusBundlesDir+"/tree/bundle.yaml", report.Violations[0].Bundle.Path)
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

// TestCorpusReadsAtTheCachedCloneWithoutFetching pins the offline contract: the
// check only ever READS, so a warm cache needs no network. A fetch smuggled in
// here would make the gate unrunnable in CI and the acceptance suite.
func TestCorpusReadsAtTheCachedCloneWithoutFetching(t *testing.T) {
	fetcher, open := corpusFixture(t, map[string]string{"alpha": cleanBundleYAML})

	CheckCorpus(context.Background(), oneRemote(), open, parseBundleBytes)

	assert.NotEmpty(t, fetcher.ListDirCalls)
	assert.NotEmpty(t, fetcher.FetchFileCalls)
	assert.Empty(t, fetcher.SearchReposCalls, "the corpus check must not reach a forge API")
}
