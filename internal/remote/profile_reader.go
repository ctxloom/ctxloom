package remote

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// ErrProfileNotInLockfile is returned when a caller asks for a profile that has
// no lockfile entry. Read sites use this to distinguish "not a remote profile,
// fall back to local fs lookup" from real errors.
var ErrProfileNotInLockfile = errors.New("profile not in lockfile")

// ProfileReader serves profile YAML for remote profiles, version-pinned to the
// SHA recorded in the lockfile, by reading from the local git clone cache. It
// is the profile-side mirror of BundleReader: remote profiles are pure
// references (never materialized to disk) read straight from the clone at the
// locked SHA.
//
// Like BundleReader it never writes to disk and never hits a forge API — the
// supplied FetcherFactory MUST be a cached factory (see NewCachedFetcherFactory)
// — and it is bytes-only: it has no knowledge of profile YAML structure. The
// caller (config seed / profiles loader) parses and resolves short same-repo
// refs against the profile's repo URL, which is intrinsic to the canonical
// lockfile key.
type ProfileReader struct {
	factory FetcherFactory
	auth    AuthConfig
	lock    *Lockfile
}

// NewProfileReader constructs a reader bound to a specific lockfile snapshot.
func NewProfileReader(factory FetcherFactory, auth AuthConfig, lock *Lockfile) *ProfileReader {
	return &ProfileReader{factory: factory, auth: auth, lock: lock}
}

// ListProfileNames returns the lockfile profile keys (canonical refs), sorted.
func (r *ProfileReader) ListProfileNames() []string {
	if r == nil || r.lock == nil {
		return nil
	}
	names := make([]string, 0, len(r.lock.Profiles))
	for k := range r.lock.Profiles {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// HasProfile reports whether profileName is a known remote profile in this
// reader's lockfile snapshot.
func (r *ProfileReader) HasProfile(profileName string) bool {
	if r == nil || r.lock == nil {
		return false
	}
	_, ok := r.lock.Profiles[profileName]
	return ok
}

// LockEntryFor returns the lockfile entry for profileName (or zero+false).
func (r *ProfileReader) LockEntryFor(profileName string) (LockEntry, bool) {
	if r == nil || r.lock == nil {
		return LockEntry{}, false
	}
	e, ok := r.lock.Profiles[profileName]
	return e, ok
}

// ReadProfileBytes returns the raw profile YAML for profileName at its locked
// SHA. profileName matches lockfile keys (canonical refs
// "<url>@profiles/<path>"). Returns ErrProfileNotInLockfile if no entry exists.
func (r *ProfileReader) ReadProfileBytes(ctx context.Context, profileName string) ([]byte, error) {
	if r == nil || r.lock == nil {
		return nil, fmt.Errorf("%w: %s", ErrProfileNotInLockfile, profileName)
	}
	entry, ok := r.lock.Profiles[profileName]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProfileNotInLockfile, profileName)
	}

	ref, err := ParseReference(profileName)
	if err != nil || !ref.IsCanonical() {
		return nil, fmt.Errorf("invalid lockfile profile key %q (expected a canonical ref): %w", profileName, err)
	}

	repoURL := ref.URL
	if repoURL == "" {
		repoURL = entry.URL
	}

	fetcher, ferr := r.factory(repoURL, r.auth)
	if ferr != nil {
		return nil, fmt.Errorf("create fetcher for %s: %w", repoURL, ferr)
	}

	owner, repo, perr := ParseRepoURL(repoURL)
	if perr != nil {
		return nil, fmt.Errorf("parse repo URL %s: %w", repoURL, perr)
	}

	filePath := ref.BuildFilePath(ref.ItemType)

	data, err := fetcher.FetchFile(ctx, owner, repo, filePath, entry.SHA)
	if err != nil {
		return nil, fmt.Errorf("fetch %s@%s: %w", filePath, entry.SHA, err)
	}
	return data, nil
}
