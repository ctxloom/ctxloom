package remote

import (
	"context"
	"fmt"
	"slices"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// MockFetcher is a test double for Fetcher that can be configured with expected responses.
// This is exported for use in other packages' tests.
type MockFetcher struct {
	Files         map[string][]byte
	Dirs          map[string][]DirEntry
	DefaultBranch string
	Refs          map[string]string
	Tags          []string
	Repos         []RepoInfo
	ValidRepos    map[string]bool // key: "owner/repo"
	ForgeType     ForgeType

	// Error injection for testing error paths
	FetchFileErr   error
	ListDirErr     error
	ResolveRefErr  error
	ResolveTagErr  error
	SearchReposErr error
	ValidateErr    error
	DefaultBrErr   error

	// Call tracking for assertions
	FetchFileCalls   []FetchFileCall
	ListDirCalls     []ListDirCall
	ResolveRefCalls  []ResolveRefCall
	ResolveTagCalls  []ResolveTagCall
	SearchReposCalls []SearchReposCall
	ValidateCalls    []ValidateCall
}

// FetchFileCall records a call to FetchFile.
type FetchFileCall struct {
	Owner, Repo, Path, Ref string
}

// ListDirCall records a call to ListDir.
type ListDirCall struct {
	Owner, Repo, Path, Ref string
}

// ResolveRefCall records a call to ResolveRef.
type ResolveRefCall struct {
	Owner, Repo, Ref string
}

// ResolveTagCall records a call to ResolveTag.
type ResolveTagCall struct {
	Owner, Repo, Tag string
}

// SearchReposCall records a call to SearchRepos.
type SearchReposCall struct {
	Query string
	Limit int
}

// ValidateCall records a call to ValidateRepo.
type ValidateCall struct {
	Owner, Repo string
}

// NewMockFetcher creates a new mock fetcher with sensible defaults.
func NewMockFetcher() *MockFetcher {
	return &MockFetcher{
		Files:         make(map[string][]byte),
		Dirs:          make(map[string][]DirEntry),
		DefaultBranch: "main",
		Refs:          make(map[string]string),
		ValidRepos:    make(map[string]bool),
		ForgeType:     ForgeGitHub,
	}
}

// ListTags satisfies the tagLister capability so semver-constraint
// resolution is exercisable against the mock.
func (m *MockFetcher) ListTags(_ context.Context, _, _ string) ([]string, error) {
	return m.Tags, nil
}

// ResolveTag satisfies the fetcherTagResolver capability, resolving through the
// TAG NAMESPACE ONLY exactly as the production fetchers do.
//
// Tags is the namespace: a name that is not in it does not exist as a tag,
// whatever Refs may say, so a branch of the same name can never answer for it.
// Without this method the mock fell through fetcherRepoVersions.ResolveTag's
// generic ResolveRef fallback, so every mock-backed semver test rehearsed the
// branch-first order that GitCloneFetcher.ResolveTag's SECURITY note exists to
// keep off the tag path.
func (m *MockFetcher) ResolveTag(_ context.Context, owner, repo, tag string) (string, error) {
	m.ResolveTagCalls = append(m.ResolveTagCalls, ResolveTagCall{owner, repo, tag})

	if m.ResolveTagErr != nil {
		return "", m.ResolveTagErr
	}
	if !slices.Contains(m.Tags, tag) {
		return "", fmt.Errorf("tag not found: %s: %w", tag, errs.ErrRemoteContentNotFound)
	}
	if sha, ok := m.Refs[tag]; ok {
		return sha, nil
	}
	return tag + "000000", nil
}

// WithFile adds a file to the mock.
func (m *MockFetcher) WithFile(path string, content []byte) *MockFetcher {
	m.Files[path] = content
	return m
}

// WithDir adds a directory listing to the mock.
func (m *MockFetcher) WithDir(path string, entries []DirEntry) *MockFetcher {
	m.Dirs[path] = entries
	return m
}

// WithRef adds a ref resolution to the mock.
func (m *MockFetcher) WithRef(ref, sha string) *MockFetcher {
	m.Refs[ref] = sha
	return m
}

// WithRepos sets the repos returned by SearchRepos.
func (m *MockFetcher) WithRepos(repos []RepoInfo) *MockFetcher {
	m.Repos = repos
	return m
}

// WithValidRepo marks a repo as valid.
func (m *MockFetcher) WithValidRepo(owner, repo string) *MockFetcher {
	m.ValidRepos[owner+"/"+repo] = true
	return m
}

// WithForge sets the forge type.
func (m *MockFetcher) WithForge(forge ForgeType) *MockFetcher {
	m.ForgeType = forge
	return m
}

func (m *MockFetcher) FetchFile(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	m.FetchFileCalls = append(m.FetchFileCalls, FetchFileCall{owner, repo, path, ref})

	if m.FetchFileErr != nil {
		return nil, m.FetchFileErr
	}

	if content, ok := m.Files[path]; ok {
		return content, nil
	}
	// Wrap the same sentinel the production fetchers do (GitCloneFetcher,
	// github). Callers distinguish "absent" from "broken" by errors.Is — a
	// missing detached .sig is how an UNSIGNED bundle is signalled — so a mock
	// that returned an untyped error here would make this double disagree with
	// production about the one thing that path is for.
	return nil, fmt.Errorf("file not found: %s: %w", path, errs.ErrRemoteContentNotFound)
}

func (m *MockFetcher) ListDir(ctx context.Context, owner, repo, path, ref string) ([]DirEntry, error) {
	m.ListDirCalls = append(m.ListDirCalls, ListDirCall{owner, repo, path, ref})

	if m.ListDirErr != nil {
		return nil, m.ListDirErr
	}

	if entries, ok := m.Dirs[path]; ok {
		return entries, nil
	}
	// Mirror GitCloneFetcher: a missing directory wraps ErrRemoteContentNotFound
	// so callers (e.g. VCS.ListItems) can treat "no such dir" as empty via
	// errors.Is rather than matching error text.
	return nil, fmt.Errorf("directory not found: %s: %w", path, errs.ErrRemoteContentNotFound)
}

func (m *MockFetcher) ResolveRef(ctx context.Context, owner, repo, ref string) (string, error) {
	m.ResolveRefCalls = append(m.ResolveRefCalls, ResolveRefCall{owner, repo, ref})

	if m.ResolveRefErr != nil {
		return "", m.ResolveRefErr
	}

	if sha, ok := m.Refs[ref]; ok {
		return sha, nil
	}
	// Default: return ref with suffix to indicate it was "resolved"
	return ref + "000000", nil
}

func (m *MockFetcher) SearchRepos(ctx context.Context, query string, limit int) ([]RepoInfo, error) {
	m.SearchReposCalls = append(m.SearchReposCalls, SearchReposCall{query, limit})

	if m.SearchReposErr != nil {
		return nil, m.SearchReposErr
	}

	if limit > 0 && len(m.Repos) > limit {
		return m.Repos[:limit], nil
	}
	return m.Repos, nil
}

func (m *MockFetcher) ValidateRepo(ctx context.Context, owner, repo string) (bool, error) {
	m.ValidateCalls = append(m.ValidateCalls, ValidateCall{owner, repo})

	if m.ValidateErr != nil {
		return false, m.ValidateErr
	}

	key := owner + "/" + repo
	if valid, ok := m.ValidRepos[key]; ok {
		return valid, nil
	}
	// Default to true for ease of testing
	return true, nil
}

func (m *MockFetcher) GetDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	if m.DefaultBrErr != nil {
		return "", m.DefaultBrErr
	}
	return m.DefaultBranch, nil
}

func (m *MockFetcher) Forge() ForgeType {
	return m.ForgeType
}

// Ensure MockFetcher implements Fetcher.
var _ Fetcher = (*MockFetcher)(nil)
