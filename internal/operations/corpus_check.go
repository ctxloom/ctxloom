package operations

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// CorpusRemote is one repository whose published bundles are in scope for a
// corpus check: the configured remote's name, for reporting, and its URL, for
// reading.
type CorpusRemote struct {
	Name string
	URL  string
}

// CorpusBundle names one bundle file in the published corpus.
type CorpusBundle struct {
	// Remote is the configured remote's name.
	Remote string
	// URL is the repository the bundle was read from.
	URL string
	// Path is the repo-relative path of the bundle YAML, e.g.
	// ".ctxloom/content/bundles/go.yaml".
	Path string
}

// String renders the bundle as "<remote>:<path>" — the form a human greps the
// publishing repo with.
func (b CorpusBundle) String() string {
	return b.Remote + ":" + b.Path
}

// CorpusViolation is one published bundle that will NOT parse under the
// bundle schema this build enforces. It carries the parse error, not just the
// name: a gate that reports "3 bundles failed" without saying what is wrong
// with them is a gate someone disables instead of fixing.
type CorpusViolation struct {
	Bundle CorpusBundle `json:"bundle"`
	Err    error        `json:"err"`
}

// CorpusGap is a piece of the corpus that could not be READ, as distinct from
// one that read fine and failed to parse. Path is empty when the whole remote
// was unreachable and set when a single bundle's bytes could not be fetched.
//
// The two outcomes are kept apart because they license opposite conclusions. A
// violation is a fact about the content; a gap is the absence of a fact, and
// folding it into "clean" is how a check comes to exit 0 having verified
// nothing.
type CorpusGap struct {
	Remote string `json:"remote"`
	URL    string `json:"url"`
	Path   string `json:"path"`
	Err    error  `json:"err"`
}

// String renders the gap's subject — the remote, or the specific bundle within
// it when the gap is one file.
func (g CorpusGap) String() string {
	if g.Path == "" {
		return g.Remote
	}
	return g.Remote + ":" + g.Path
}

// CorpusReport is the outcome of parsing a published corpus.
type CorpusReport struct {
	// RemotesConfigured is how many remotes were in scope.
	RemotesConfigured int `json:"remotes_configured"`
	// RemotesRead is how many of them yielded a bundle listing.
	RemotesRead int `json:"remotes_read"`
	// Parsed is how many bundles were read AND parsed successfully. It is the
	// evidence that the check did any work at all.
	Parsed int `json:"parsed"`
	// Violations are the bundles that read fine and would not parse.
	Violations []CorpusViolation `json:"violations"`
	// Gaps are the remotes and bundles that could not be read.
	Gaps []CorpusGap `json:"gaps"`
}

// CorpusVerdict is a corpus check's three-way outcome. Its values are the
// process exit codes deliberately: a gate's caller reads the code, and
// "violations found" and "I could not look" must not share one.
type CorpusVerdict int

const (
	// CorpusClean means every bundle in a non-empty corpus parsed.
	CorpusClean CorpusVerdict = 0
	// CorpusViolated means at least one published bundle would not parse.
	CorpusViolated CorpusVerdict = 1
	// CorpusUndetermined means the corpus could not be checked: a remote was
	// unreadable, or nothing was found to parse. It is NOT a pass.
	CorpusUndetermined CorpusVerdict = 2
)

// Verdict reduces the report to its outcome.
//
// A violation outranks a gap: a bundle that demonstrably will not parse is a
// finding, and it stays a finding whether or not some OTHER remote was also
// unreachable. Absent any violation, a gap — or a corpus in which nothing at
// all was parsed — is undetermined, never clean. Parsed == 0 is the load-bearing
// clause: without it a check that found no remotes, or listed no bundles,
// reports success having compared nothing against the schema.
func (r CorpusReport) Verdict() CorpusVerdict {
	if len(r.Violations) > 0 {
		return CorpusViolated
	}
	if len(r.Gaps) > 0 || r.Parsed == 0 {
		return CorpusUndetermined
	}
	return CorpusClean
}

// FetcherOpener opens a read handle on one repository. It is the corpus
// check's transport seam; production passes a cached-clone factory, which
// reads the local clone and touches no forge API.
type FetcherOpener func(repoURL string) (remote.Fetcher, error)

// BundleParser is the schema rule a corpus is checked AGAINST. Production
// passes parseBundleBytes, wrapping bundles.ParseBundle, so the gate always
// enforces exactly what this build enforces at load time — a schema tightening
// changes the gate's verdict with no edit here, which is the whole point.
type BundleParser func(data []byte) error

// corpusBundlesDir is the repo-relative directory a published corpus lives in.
var corpusBundlesDir = path.Join(paths.RepoContentPrefix, paths.BundlesDir)

// CheckCorpus reads every bundle published by each remote and parses it,
// returning what would not parse and what could not be read.
//
// It never aborts on the first failure: the deliverable is the COMPLETE list of
// offending bundles, because a gate that names one of three sends its reader
// round the loop three times.
func CheckCorpus(ctx context.Context, remotes []CorpusRemote, open FetcherOpener, parse BundleParser) CorpusReport {
	report := CorpusReport{RemotesConfigured: len(remotes)}
	for _, rem := range remotes {
		checkOneRemote(ctx, rem, open, parse, &report)
	}
	sortCorpusFindings(&report)
	return report
}

// checkOneRemote folds a single remote's bundles into report. Every failure
// short of a parse failure lands in Gaps, so an unreadable remote can never be
// mistaken for a clean one.
func checkOneRemote(ctx context.Context, rem CorpusRemote, open FetcherOpener, parse BundleParser, report *CorpusReport) {
	fetcher, err := open(rem.URL)
	if err != nil {
		report.Gaps = append(report.Gaps, CorpusGap{Remote: rem.Name, URL: rem.URL, Err: fmt.Errorf("open %s: %w", rem.URL, err)})
		return
	}
	owner, repo, err := remote.ParseOwnerRepo(rem.URL)
	if err != nil {
		report.Gaps = append(report.Gaps, CorpusGap{Remote: rem.Name, URL: rem.URL, Err: fmt.Errorf("parse repo URL %s: %w", rem.URL, err)})
		return
	}

	bundlePaths, err := listCorpusBundles(ctx, fetcher, owner, repo)
	if err != nil {
		report.Gaps = append(report.Gaps, CorpusGap{Remote: rem.Name, URL: rem.URL, Err: err})
		return
	}
	report.RemotesRead++

	for _, p := range bundlePaths {
		data, ferr := fetcher.FetchFile(ctx, owner, repo, p, "")
		if ferr != nil {
			report.Gaps = append(report.Gaps, CorpusGap{Remote: rem.Name, URL: rem.URL, Path: p, Err: ferr})
			continue
		}
		if perr := parse(data); perr != nil {
			report.Violations = append(report.Violations, CorpusViolation{
				Bundle: CorpusBundle{Remote: rem.Name, URL: rem.URL, Path: p},
				Err:    perr,
			})
			continue
		}
		report.Parsed++
	}
}

// listCorpusBundles walks the repo's published bundles directory and returns
// every .yaml path in it, recursing so directory-form bundles' bundle.yaml is
// included alongside single-file ones.
//
// A repo with no bundles directory lists EMPTY rather than erroring: a remote
// may legitimately publish no bundles, and calling that unreadable would fire
// the gate on a healthy corpus. The check's own "nothing was parsed" guard is
// what stops that leniency from becoming a silent pass.
func listCorpusBundles(ctx context.Context, fetcher remote.Fetcher, owner, repo string) ([]string, error) {
	var found []string
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := fetcher.ListDir(ctx, owner, repo, dir, "")
		if err != nil {
			if errors.Is(err, errs.ErrRemoteContentNotFound) {
				return nil
			}
			return fmt.Errorf("list %s: %w", dir, err)
		}
		for _, e := range entries {
			full := path.Join(dir, e.Name)
			if e.IsDir {
				if werr := walk(full); werr != nil {
					return werr
				}
				continue
			}
			if strings.HasSuffix(e.Name, ".yaml") {
				found = append(found, full)
			}
		}
		return nil
	}
	if err := walk(corpusBundlesDir); err != nil {
		return nil, err
	}
	return found, nil
}

// sortCorpusFindings orders both finding lists by their printed subject, so a
// gate's output is stable across runs and two runs can be diffed.
func sortCorpusFindings(report *CorpusReport) {
	sort.Slice(report.Violations, func(i, j int) bool {
		return report.Violations[i].Bundle.String() < report.Violations[j].Bundle.String()
	})
	sort.Slice(report.Gaps, func(i, j int) bool {
		return report.Gaps[i].String() < report.Gaps[j].String()
	})
}

// parseBundleBytes adapts bundles.ParseBundle to BundleParser: the gate cares
// only whether the current schema accepts the bytes.
func parseBundleBytes(data []byte) error {
	_, err := bundles.ParseBundle(data)
	return err
}

// ConfiguredCorpus returns the remotes this project is configured against,
// read from the project's remotes registry. The corpus is never a hardcoded
// list — a project that adds a remote gets it checked with no code change.
func ConfiguredCorpus(cfg *config.Config) ([]CorpusRemote, error) {
	registry, err := getRegistry(cfg)
	if err != nil {
		return nil, fmt.Errorf("read the remotes registry %s: %w", paths.RemotesPath(getBaseDir(cfg)), err)
	}
	entries := registry.List()
	corpus := make([]CorpusRemote, 0, len(entries))
	for _, rem := range entries {
		// A registry entry with no URL addresses nothing; there is no repo to
		// read and no honest finding to report about it.
		if rem.URL == "" {
			continue
		}
		corpus = append(corpus, CorpusRemote{Name: rem.Name, URL: rem.URL})
	}
	return corpus, nil
}

// CheckConfiguredCorpus parses every bundle published by every configured
// remote, reading the LOCAL clone cache.
//
// It deliberately does not fetch: with a warm cache the whole check is offline,
// and a cache miss surfaces as a gap (undetermined) rather than as either a
// silent pass or a network dependency the acceptance suite and CI cannot meet.
func CheckConfiguredCorpus(ctx context.Context, cfg *config.Config) (CorpusReport, error) {
	corpus, err := ConfiguredCorpus(cfg)
	if err != nil {
		return CorpusReport{}, err
	}
	factory := NewCachedFetcherFactory(cfg)
	auth := remote.LoadAuth(getBaseDir(cfg))
	open := func(repoURL string) (remote.Fetcher, error) { return factory(repoURL, auth) }
	return CheckCorpus(ctx, corpus, open, parseBundleBytes), nil
}
