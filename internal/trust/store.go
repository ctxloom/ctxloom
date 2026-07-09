package trust

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// storeVersion is the on-disk schema version of trust.yaml. Version 2 is the
// three-state review model (trust-simplify): per-item accepted/rejected states
// bound to a (raw, distilled) hash pair, plus the content-hash denylist. A
// version-1 file (grants/blacklist/bundles/baseline) migrates in memory on
// load and is persisted in v2 form on the first successful mutation.
const storeVersion = 2

// keySep separates components in composite in-memory map keys. Repo URLs and
// item refs never contain a null byte, so it cannot collide.
const keySep = "\x00"

// Item is one reviewed item's persisted state. An item with no entry is
// implicitly pending (withheld). Accepted items are bound to the hash pair
// present at review: a change to either exposed form returns the item to
// pending because the recomputed hash no longer matches. A hash slot may be
// empty (lazy v1 migration recorded only the form that was granted); the
// resolver treats an empty slot for the current form as pending (fail closed).
type Item struct {
	RepoURL       string // canonicalized repo URL (ctxloom:local for local items)
	Ref           string // repo-relative item key, e.g. "code-quality#fragments/solid"
	State         State  // StateAccepted | StateRejected (pending is never stored)
	RawHash       string // accepted only; hash of the raw form ("" = unknown/unreviewed)
	DistilledHash string // accepted only; hash of the distilled form ("" = none/unknown)
	ReviewedAt    string // RFC3339 timestamp of the recording mutation (provenance)
}

// Store is the afero-backed per-item review-state store persisted to
// trust.yaml. It holds the accepted/rejected items and the content-hash
// denylist of rejected content. It is read by the resolver and written only
// via the mutation methods, each of which rolls back its in-memory change if
// the save fails — a half-applied rejection that fails to persist would
// otherwise let a process believe an item is blocked while the next load
// reopens it.
type Store struct {
	mu         sync.RWMutex
	configPath string
	fs         afero.Fs

	items    map[string]Item     // itemKey -> item state
	denylist map[string]struct{} // content hash -> {} (repo/ref-agnostic)
}

// Option configures a Store.
type Option func(*Store)

// WithFS sets a custom filesystem (for testing), mirroring the registry store.
func WithFS(fs afero.Fs) Option {
	return func(s *Store) { s.fs = fs }
}

// New creates a trust store persisting to configPath. If configPath is empty it
// defaults to .ctxloom/trust.yaml in the current directory. A missing file is
// fine (empty store — everything pending); a corrupt/unreadable file returns an
// error so the caller can fail closed rather than silently degrade to an empty
// store.
func New(configPath string, opts ...Option) (*Store, error) {
	if configPath == "" {
		configPath = paths.DefaultTrustPath()
	}
	s := &Store{
		configPath: configPath,
		fs:         afero.NewOsFs(),
		items:      make(map[string]Item),
		denylist:   make(map[string]struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	if err := s.load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to load trust store: %w", err)
	}
	return s, nil
}

// storeFile is the on-disk YAML shape of trust.yaml. The v2 fields (Items,
// Denylist) are the persisted schema; the legacy v1 fields (Grants, Blacklist)
// are read-only migration inputs, never written back. v1 bundle postures and
// the baseline marker are dropped on migration (their keys are simply ignored
// by the YAML decoder).
type storeFile struct {
	Version  int          `yaml:"version"`
	Items    []itemRecord `yaml:"items,omitempty"`
	Denylist []string     `yaml:"denylist,omitempty"`

	// Legacy v1 fields, parsed only to migrate. Never written.
	Grants    []grantRecord     `yaml:"grants,omitempty"`
	Blacklist []blacklistRecord `yaml:"blacklist,omitempty"`
}

type itemRecord struct {
	RepoURL       string `yaml:"repo_url"`
	Ref           string `yaml:"ref"`
	State         string `yaml:"state"`
	RawHash       string `yaml:"raw_hash,omitempty"`
	DistilledHash string `yaml:"distilled_hash,omitempty"`
	ReviewedAt    string `yaml:"reviewed_at,omitempty"`
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

// load reads the store from disk into the in-memory maps. A version-1 file is
// migrated in memory (see migrateV1); the file itself is NOT rewritten on a
// pure read — the migrated form persists on the first successful mutation.
func (s *Store) load() error {
	data, err := afero.ReadFile(s.fs, s.configPath)
	if err != nil {
		return err
	}
	var cfg storeFile
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse trust store: %w", err)
	}
	for _, h := range cfg.Denylist {
		if h != "" {
			s.denylist[h] = struct{}{}
		}
	}
	if cfg.Version >= storeVersion {
		for _, it := range cfg.Items {
			state, err := stateFromYAML(it.State)
			if err != nil {
				// A garbled state cannot be trusted — fail the load so the caller
				// fails closed (denies everything) rather than guessing.
				return fmt.Errorf("failed to parse trust store: item %q: %w", it.Ref, err)
			}
			repo := CanonicalRepoURL(it.RepoURL)
			item := Item{
				RepoURL:    repo,
				Ref:        it.Ref,
				State:      state,
				ReviewedAt: it.ReviewedAt,
			}
			if state == StateAccepted {
				// Hashes are meaningful for accepted items only.
				item.RawHash = it.RawHash
				item.DistilledHash = it.DistilledHash
			}
			s.items[itemKey(repo, it.Ref)] = item
		}
		return nil
	}
	s.migrateV1(cfg)
	return nil
}

// stateFromYAML parses a persisted item state. Only the two persisted states
// are valid; anything else is a corrupt store (the caller fails closed).
func stateFromYAML(raw string) (State, error) {
	switch State(raw) {
	case StateAccepted:
		return StateAccepted, nil
	case StateRejected:
		return StateRejected, nil
	default:
		return "", fmt.Errorf("unknown item state %q", raw)
	}
}

// migrateV1 translates a version-1 store into the three-state model, in
// memory only:
//
//   - grants → accepted items. The migration is lazy on hash pairs: the
//     grant's known form-hash fills the matching slot (Form "distilled" →
//     DistilledHash, anything else → RawHash) and the other slot stays empty —
//     the resolver requires the CURRENT form's hash to match, so an empty slot
//     gates (fail closed) until a review/accept records both forms. Grants are
//     applied in sorted (repo, ref, hash) order so multiple v1 grants for one
//     ref migrate deterministically (per form slot, the greatest hash wins).
//   - blacklist entries → rejected items, overwriting any migrated grant for
//     the same ref (the v1 cascade also let blacklist beat grants).
//   - the denylist carries over (already loaded by the caller).
//   - bundle postures and the baseline marker are dropped: the content they
//     covered lands pending, awaiting one review session.
func (s *Store) migrateV1(cfg storeFile) {
	grants := append([]grantRecord(nil), cfg.Grants...)
	sort.Slice(grants, func(i, j int) bool {
		a, b := grants[i], grants[j]
		if a.RepoURL != b.RepoURL {
			return a.RepoURL < b.RepoURL
		}
		if a.Ref != b.Ref {
			return a.Ref < b.Ref
		}
		return a.ContentHash < b.ContentHash
	})
	for _, g := range grants {
		if g.ContentHash == "" {
			continue // a hash-less grant blessed nothing — nothing to migrate
		}
		repo := CanonicalRepoURL(g.RepoURL)
		key := itemKey(repo, g.Ref)
		item, ok := s.items[key]
		if !ok {
			item = Item{RepoURL: repo, Ref: g.Ref, State: StateAccepted}
		}
		if g.Form == "distilled" {
			item.DistilledHash = g.ContentHash
		} else {
			item.RawHash = g.ContentHash
		}
		s.items[key] = item
	}
	for _, b := range cfg.Blacklist {
		repo := CanonicalRepoURL(b.RepoURL)
		// Rejected wins over any migrated grant for the same ref; hashes are
		// cleared (rejected items carry no acceptance hashes).
		s.items[itemKey(repo, b.Ref)] = Item{RepoURL: repo, Ref: b.Ref, State: StateRejected}
	}
}

// snapshot reconstructs the on-disk shape from the in-memory maps with
// deterministic ordering. trust.yaml is owned entirely by this store (unlike
// remotes.yaml, which is shared), so a full marshal is correct. The legacy v1
// fields are never written.
func (s *Store) snapshot() storeFile {
	out := storeFile{Version: storeVersion}
	for _, it := range s.items {
		rec := itemRecord{
			RepoURL:    it.RepoURL,
			Ref:        it.Ref,
			State:      string(it.State),
			ReviewedAt: it.ReviewedAt,
		}
		if it.State == StateAccepted {
			rec.RawHash = it.RawHash
			rec.DistilledHash = it.DistilledHash
		}
		out.Items = append(out.Items, rec)
	}
	sort.Slice(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if a.RepoURL != b.RepoURL {
			return a.RepoURL < b.RepoURL
		}
		return a.Ref < b.Ref
	})
	for h := range s.denylist {
		out.Denylist = append(out.Denylist, h)
	}
	sort.Strings(out.Denylist)
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

func itemKey(repoURL, ref string) string {
	return repoURL + keySep + ref
}

// nowRFC3339 stamps mutation provenance; a package var so tests can pin it.
var nowRFC3339 = func() string { return time.Now().UTC().Format(time.RFC3339) }

// SetAccepted records (or replaces) an item's accepted state, bound to the
// (raw, distilled) hash pair present at review. distilledHash may be empty
// when the item has no distilled form; at least one hash must be non-empty —
// an acceptance that pins no content would be meaningless (and could mask a
// hashing bug as an always-pending item). Overwrites a previous rejected
// state for the same ref (the explicit un-reject path). Rolls back the
// in-memory change if the save fails.
func (s *Store) SetAccepted(repoURL, ref string, rawHash, distilledHash string) error {
	if rawHash == "" && distilledHash == "" {
		return fmt.Errorf("accepting %q requires at least one content hash", ref)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	repo := CanonicalRepoURL(repoURL)
	key := itemKey(repo, ref)
	prev, existed := s.items[key]
	s.items[key] = Item{
		RepoURL:       repo,
		Ref:           ref,
		State:         StateAccepted,
		RawHash:       rawHash,
		DistilledHash: distilledHash,
		ReviewedAt:    nowRFC3339(),
	}
	if err := s.save(); err != nil {
		if existed {
			s.items[key] = prev
		} else {
			delete(s.items, key)
		}
		return err
	}
	return nil
}

// SetRejected records an item's rejected state and adds every non-empty
// content hash to the repo/ref-agnostic denylist — the two companion
// components written together so a renamed/moved copy of the same content
// stays rejected. Both persist or neither: a save failure rolls back every
// in-memory change. The ref-level rejection is recorded even with no hashes
// (e.g. the content is already gone) — it is the durable guarantee.
func (s *Store) SetRejected(repoURL, ref string, contentHashes ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	repo := CanonicalRepoURL(repoURL)
	key := itemKey(repo, ref)
	prev, existed := s.items[key]
	var addedHashes []string
	for _, h := range contentHashes {
		if h == "" {
			continue
		}
		if _, ok := s.denylist[h]; !ok {
			s.denylist[h] = struct{}{}
			addedHashes = append(addedHashes, h)
		}
	}
	s.items[key] = Item{
		RepoURL:    repo,
		Ref:        ref,
		State:      StateRejected,
		ReviewedAt: nowRFC3339(),
	}
	if err := s.save(); err != nil {
		if existed {
			s.items[key] = prev
		} else {
			delete(s.items, key)
		}
		for _, h := range addedHashes {
			delete(s.denylist, h)
		}
		return err
	}
	return nil
}

// Remove drops an item's recorded state, returning it to (implicit) pending.
// Removing an absent item is a no-op. It does NOT scrub denylist hashes a
// rejection may have added — bad content stays blocked until explicitly
// re-accepted at its current hash. Rolls back on save failure.
func (s *Store) Remove(repoURL, ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := itemKey(CanonicalRepoURL(repoURL), ref)
	prev, existed := s.items[key]
	if !existed {
		return nil
	}
	delete(s.items, key)
	if err := s.save(); err != nil {
		s.items[key] = prev
		return err
	}
	return nil
}

// Lookup returns the recorded state for the (canonical repo, ref) pair, and
// whether one exists. No entry means the item is pending.
func (s *Store) Lookup(repoURL, ref string) (Item, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[itemKey(CanonicalRepoURL(repoURL), ref)]
	return item, ok
}

// DeniedHash reports whether the content hash is on the denylist (rejected
// content stays blocked regardless of ref or repo).
func (s *Store) DeniedHash(hash string) bool {
	if hash == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.denylist[hash]
	return ok
}
