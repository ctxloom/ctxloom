package trust

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// storeVersion is the on-disk schema version of trust.yaml.
const storeVersion = 1

// keySep separates components in composite in-memory map keys. Repo URLs and
// item refs never contain a null byte, so it cannot collide.
const keySep = "\x00"

// Grant is a per-item trust grant: it blesses one exact content version of one
// item (repo + ref + content hash). Form and SHAAtGrant are provenance only —
// validity is "the recomputed effective-content hash equals ContentHash", never
// a form or SHA comparison.
type Grant struct {
	RepoURL     string // canonicalized repo URL (ctxloom:local for local items)
	Ref         string // repo-relative item key, e.g. "code-quality#fragments/solid"
	ContentHash string // EFFECTIVE-content hash this grant is bound to
	Form        string // raw | distilled — provenance for which form was blessed
	SHAAtGrant  string // commit the grant was minted against — provenance only
}

// Store is the afero-backed per-item trust store persisted to trust.yaml. It
// holds grants, a sticky ref-level blacklist, a content-hash denylist, and
// per-bundle postures. It is read by the resolver and written only via the
// mutation methods, each of which rolls back its in-memory change if the save
// fails — a half-applied blacklist that fails to persist would otherwise let a
// process believe an item is blocked while the next load reopens it.
type Store struct {
	mu         sync.RWMutex
	configPath string
	fs         afero.Fs

	grants    map[string]Grant    // grantKey -> grant
	blacklist map[string]struct{} // blKey -> {} (sticky ref-level)
	denylist  map[string]struct{} // content hash -> {} (repo/ref-agnostic)
	bundles   map[string]Decision // bundle name -> trusted/untrusted posture
}

// Option configures a Store.
type Option func(*Store)

// WithFS sets a custom filesystem (for testing), mirroring the registry store.
func WithFS(fs afero.Fs) Option {
	return func(s *Store) { s.fs = fs }
}

// New creates a trust store persisting to configPath. If configPath is empty it
// defaults to .ctxloom/trust.yaml in the current directory. A missing file is
// fine (empty store); a corrupt/unreadable file returns an error so the caller
// can fail closed rather than silently degrade to an empty (allow-by-default)
// store.
func New(configPath string, opts ...Option) (*Store, error) {
	if configPath == "" {
		configPath = paths.DefaultTrustPath()
	}
	s := &Store{
		configPath: configPath,
		fs:         afero.NewOsFs(),
		grants:     make(map[string]Grant),
		blacklist:  make(map[string]struct{}),
		denylist:   make(map[string]struct{}),
		bundles:    make(map[string]Decision),
	}
	for _, opt := range opts {
		opt(s)
	}
	if err := s.load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to load trust store: %w", err)
	}
	return s, nil
}

// storeFile is the on-disk YAML shape of trust.yaml.
type storeFile struct {
	Version   int               `yaml:"version"`
	Grants    []grantRecord     `yaml:"grants,omitempty"`
	Blacklist []blacklistRecord `yaml:"blacklist,omitempty"`
	Denylist  []string          `yaml:"denylist,omitempty"`
	Bundles   []bundleRecord    `yaml:"bundles,omitempty"`
}

type grantRecord struct {
	RepoURL     string `yaml:"repo_url"`
	Ref         string `yaml:"ref"`
	ContentHash string `yaml:"content_hash"`
	Form        string `yaml:"form,omitempty"`
	SHAAtGrant  string `yaml:"sha_at_grant,omitempty"`
}

type blacklistRecord struct {
	RepoURL string `yaml:"repo_url"`
	Ref     string `yaml:"ref"`
}

type bundleRecord struct {
	Bundle   string `yaml:"bundle"`
	Decision string `yaml:"decision"`
}

// load reads the store from disk into the in-memory maps.
func (s *Store) load() error {
	data, err := afero.ReadFile(s.fs, s.configPath)
	if err != nil {
		return err
	}
	var cfg storeFile
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse trust store: %w", err)
	}
	for _, g := range cfg.Grants {
		repo := CanonicalRepoURL(g.RepoURL)
		s.grants[grantKey(repo, g.Ref, g.ContentHash)] = Grant{
			RepoURL:     repo,
			Ref:         g.Ref,
			ContentHash: g.ContentHash,
			Form:        g.Form,
			SHAAtGrant:  g.SHAAtGrant,
		}
	}
	for _, b := range cfg.Blacklist {
		s.blacklist[blKey(CanonicalRepoURL(b.RepoURL), b.Ref)] = struct{}{}
	}
	for _, h := range cfg.Denylist {
		if h != "" {
			s.denylist[h] = struct{}{}
		}
	}
	for _, b := range cfg.Bundles {
		s.bundles[b.Bundle] = postureFromYAML(b.Decision)
	}
	return nil
}

// postureToYAML renders a bundle posture in the plan's storage vocabulary
// (trusted | untrusted) rather than the resolver's allow/deny.
func postureToYAML(d Decision) string {
	if d == Allow {
		return "trusted"
	}
	return "untrusted"
}

// postureFromYAML parses a stored bundle posture. An unknown/garbled value
// resolves to Deny (fail closed) rather than silently trusting.
func postureFromYAML(s string) Decision {
	switch s {
	case "trusted", string(Allow):
		return Allow
	default:
		return Deny
	}
}

// snapshot reconstructs the on-disk shape from the in-memory maps with
// deterministic ordering. trust.yaml is owned entirely by this store (unlike
// remotes.yaml, which is shared), so a full marshal is correct.
func (s *Store) snapshot() storeFile {
	out := storeFile{Version: storeVersion}
	for _, g := range s.grants {
		out.Grants = append(out.Grants, grantRecord{
			RepoURL:     g.RepoURL,
			Ref:         g.Ref,
			ContentHash: g.ContentHash,
			Form:        g.Form,
			SHAAtGrant:  g.SHAAtGrant,
		})
	}
	sort.Slice(out.Grants, func(i, j int) bool {
		a, b := out.Grants[i], out.Grants[j]
		if a.RepoURL != b.RepoURL {
			return a.RepoURL < b.RepoURL
		}
		if a.Ref != b.Ref {
			return a.Ref < b.Ref
		}
		return a.ContentHash < b.ContentHash
	})
	for k := range s.blacklist {
		repo, ref, _ := strings.Cut(k, keySep)
		out.Blacklist = append(out.Blacklist, blacklistRecord{RepoURL: repo, Ref: ref})
	}
	sort.Slice(out.Blacklist, func(i, j int) bool {
		a, b := out.Blacklist[i], out.Blacklist[j]
		if a.RepoURL != b.RepoURL {
			return a.RepoURL < b.RepoURL
		}
		return a.Ref < b.Ref
	})
	for h := range s.denylist {
		out.Denylist = append(out.Denylist, h)
	}
	sort.Strings(out.Denylist)
	for name, dec := range s.bundles {
		out.Bundles = append(out.Bundles, bundleRecord{Bundle: name, Decision: postureToYAML(dec)})
	}
	sort.Slice(out.Bundles, func(i, j int) bool { return out.Bundles[i].Bundle < out.Bundles[j].Bundle })
	return out
}

// save writes the in-memory store to disk. Callers hold the write lock and
// must roll back their in-memory change on a non-nil return.
func (s *Store) save() error {
	out, err := yaml.Marshal(s.snapshot())
	if err != nil {
		return fmt.Errorf("failed to marshal trust store: %w", err)
	}
	if err := s.fs.MkdirAll(filepath.Dir(s.configPath), 0o755); err != nil {
		return fmt.Errorf("failed to create trust dir: %w", err)
	}
	if err := afero.WriteFile(s.fs, s.configPath, out, 0o644); err != nil {
		return fmt.Errorf("failed to write trust store: %w", err)
	}
	return nil
}

func grantKey(repoURL, ref, hash string) string {
	return repoURL + keySep + ref + keySep + hash
}

func blKey(repoURL, ref string) string {
	return repoURL + keySep + ref
}

// AddGrant records (or replaces) a per-item trust grant. repoURL is
// canonicalized before keying. Rolls back the in-memory change if the save
// fails.
func (s *Store) AddGrant(repoURL, ref, contentHash, form, shaAtGrant string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	repo := CanonicalRepoURL(repoURL)
	key := grantKey(repo, ref, contentHash)
	prev, existed := s.grants[key]
	s.grants[key] = Grant{
		RepoURL:     repo,
		Ref:         ref,
		ContentHash: contentHash,
		Form:        form,
		SHAAtGrant:  shaAtGrant,
	}
	if err := s.save(); err != nil {
		if existed {
			s.grants[key] = prev
		} else {
			delete(s.grants, key)
		}
		return err
	}
	return nil
}

// Blacklist records a sticky ref-level blacklist entry and, when contentHash is
// non-empty, the matching content-hash denylist entry — the two companion
// components written together so a renamed/moved copy of the same content stays
// blocked. Both persist or neither: a save failure rolls back both in-memory
// changes.
func (s *Store) Blacklist(repoURL, ref, contentHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	repo := CanonicalRepoURL(repoURL)
	bk := blKey(repo, ref)
	_, blExisted := s.blacklist[bk]
	_, dlExisted := s.denylist[contentHash]

	s.blacklist[bk] = struct{}{}
	if contentHash != "" {
		s.denylist[contentHash] = struct{}{}
	}

	if err := s.save(); err != nil {
		if !blExisted {
			delete(s.blacklist, bk)
		}
		if contentHash != "" && !dlExisted {
			delete(s.denylist, contentHash)
		}
		return err
	}
	return nil
}

// SetBundle sets a SHA-agnostic posture for a bundle. Rolls back on save
// failure.
func (s *Store) SetBundle(bundle string, decision Decision) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	prev, existed := s.bundles[bundle]
	s.bundles[bundle] = decision
	if err := s.save(); err != nil {
		if existed {
			s.bundles[bundle] = prev
		} else {
			delete(s.bundles, bundle)
		}
		return err
	}
	return nil
}

// DenylistMatch reports whether the content hash is on the denylist (highest
// precedence — exact bad content blocked regardless of ref or repo).
func (s *Store) DenylistMatch(contentHash string) bool {
	if contentHash == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.denylist[contentHash]
	return ok
}

// BlacklistMatch reports whether a sticky ref-level blacklist entry exists for
// the (canonical repo, ref) pair.
func (s *Store) BlacklistMatch(repoURL, ref string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.blacklist[blKey(CanonicalRepoURL(repoURL), ref)]
	return ok
}

// GrantMatch returns the grant blessing exactly (canonical repo, ref, content
// hash), and whether one exists. The content hash is part of the key, so
// historical versions of a ref coexist and a content change drops the grant.
func (s *Store) GrantMatch(repoURL, ref, contentHash string) (Grant, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.grants[grantKey(CanonicalRepoURL(repoURL), ref, contentHash)]
	return g, ok
}

// BundlePosture returns the bundle's posture and whether one is set.
func (s *Store) BundlePosture(bundle string) (Decision, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	dec, ok := s.bundles[bundle]
	return dec, ok
}
