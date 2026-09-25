package remote

import (
	"fmt"
	"github.com/ctxloom/ctxloom/internal/shared/refuri"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// Registry manages configured remote sources.
// It persists remotes to .ctxloom/persistent/remotes.yaml.
type Registry struct {
	mu            sync.RWMutex
	remotes       map[string]*Remote
	forges        map[string]ForgeConfig
	defaultRemote string
	configPath    string
	fs            afero.Fs
}

// RegistryOption is a functional option for configuring a Registry.
type RegistryOption func(*Registry)

// WithRegistryFS reads and writes the registry through fs. A nil fs keeps the
// OS filesystem, so a caller holding an optional filesystem passes it as-is.
func WithRegistryFS(fs afero.Fs) RegistryOption {
	return func(r *Registry) {
		if fs != nil {
			r.fs = fs
		}
	}
}

// NewRegistry creates a new registry that persists to the given config path.
// If configPath is empty, defaults to .ctxloom/persistent/remotes.yaml in current directory.
func NewRegistry(configPath string, opts ...RegistryOption) (*Registry, error) {
	if configPath == "" {
		configPath = paths.DefaultRemotesPath()
	}

	r := &Registry{
		remotes:    make(map[string]*Remote),
		forges:     make(map[string]ForgeConfig),
		configPath: configPath,
		fs:         afero.NewOsFs(),
	}

	for _, opt := range opts {
		opt(r)
	}

	// Load existing config if it exists
	if err := r.load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	return r, nil
}

// configFile represents the structure of the config file.
// Only contains remotes-related fields to avoid overwriting other config.
type configFile struct {
	Remotes map[string]Remote      `yaml:"remotes,omitempty"`
	Forges  map[string]ForgeConfig `yaml:"forges,omitempty"`
	Default string                 `yaml:"default,omitempty"`
}

// load reads remotes from the config file.
func (r *Registry) load() error {
	data, err := afero.ReadFile(r.fs, r.configPath)
	if err != nil {
		return err
	}

	var cfg configFile
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	for name, remote := range cfg.Remotes {
		remote := remote // Copy to avoid pointer issues
		remote.Name = name
		r.remotes[name] = &remote
	}

	for name, fc := range cfg.Forges {
		if err := validateForgeConfig(name, fc); err != nil {
			return err
		}
		r.forges[name] = fc
	}

	r.defaultRemote = cfg.Default

	return nil
}

// save writes remotes to the config file, preserving other fields.
func (r *Registry) save() error {
	// Read existing config to preserve other fields
	var existingRaw map[string]interface{}
	data, err := afero.ReadFile(r.fs, r.configPath)
	if err == nil {
		// An unparseable existing file must NOT be silently treated
		// as empty — that discarded every key save() doesn't itself manage
		// (a corrupt file or a concurrent partial write would otherwise lose
		// unrelated config on the next save, with no error at all).
		if uerr := yaml.Unmarshal(data, &existingRaw); uerr != nil {
			return fmt.Errorf("failed to parse existing config %s (refusing to overwrite it): %w", r.configPath, uerr)
		}
	}
	if existingRaw == nil {
		existingRaw = make(map[string]interface{})
	}

	// Update remotes
	remotesMap := make(map[string]Remote)
	for name, remote := range r.remotes {
		remotesMap[name] = Remote{
			URL:   remote.URL,
			Forge: remote.Forge,
		}
	}
	if len(remotesMap) > 0 {
		existingRaw["remotes"] = remotesMap
	} else {
		delete(existingRaw, "remotes")
	}

	// Update forges (built-in defaults are implicit and not persisted)
	if len(r.forges) > 0 {
		existingRaw["forges"] = r.forges
	} else {
		delete(existingRaw, "forges")
	}

	// Update default remote
	if r.defaultRemote != "" {
		existingRaw["default"] = r.defaultRemote
	} else {
		delete(existingRaw, "default")
	}

	// Ensure directory exists
	if err := r.fs.MkdirAll(filepath.Dir(r.configPath), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Marshal and write
	out, err := yaml.Marshal(existingRaw)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// A plain afero.WriteFile truncates the file in place — a crash
	// or a concurrent writer (GetOrCreateByURL auto-registers on every pull,
	// and agent children run concurrently) can observe or leave a half-written
	// remotes.yaml. LockfileManager.write already uses this same atomic
	// temp-file-then-rename primitive for the same reason, same directory.
	if err := iox.WriteFileAtomicFs(r.fs, r.configPath, out, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// Add registers a new remote (explicit user command).
// Returns error if:
//   - A remote with the same name already exists
//   - A remote already points to the same URL (use that one instead)
func (r *Registry) Add(name, repoURL string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.remotes[name]; exists {
		return fmt.Errorf("remote already exists: %s", name)
	}
	if err := requireRepository(repoURL); err != nil {
		return err
	}

	normalizedURL := storedRepoURL(repoURL)

	// Check if any existing remote points to this URL
	if existingName, found := r.findByURLLocked(repoURL); found {
		return fmt.Errorf("remote '%s' already points to this URL; use 'ctxloom deps pull %s/<path>' instead", existingName, existingName)
	}

	remote := &Remote{
		Name: name,
		URL:  normalizedURL,
	}

	r.remotes[name] = remote

	if err := r.save(); err != nil {
		delete(r.remotes, name) // Rollback
		return err
	}

	return nil
}

// GetOrCreateByURL finds an existing remote by URL or creates a new one.
// Used for auto-registration during pull - returns existing remote if URL already registered.
// New remotes are named using the repository name extracted from the URL.
// If a name conflict exists (same repo name, different URL), appends a numeric suffix.
func (r *Registry) GetOrCreateByURL(repoURL string) (*Remote, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := requireRepository(repoURL); err != nil {
		return nil, err
	}
	normalizedURL := storedRepoURL(repoURL)

	// Check if any existing remote points to this URL
	if existing, found := r.findByURLLocked(repoURL); found {
		remoteCopy := *r.remotes[existing]
		return &remoteCopy, nil
	}

	// Auto-register using repo name
	baseName := ExtractRepoName(repoURL)
	name := baseName

	// Handle name conflicts with numeric suffix
	suffix := 2
	for {
		if _, exists := r.remotes[name]; !exists {
			break
		}
		name = fmt.Sprintf("%s-%d", baseName, suffix)
		suffix++
	}

	remote := &Remote{
		Name: name,
		URL:  normalizedURL,
	}

	r.remotes[name] = remote

	if err := r.save(); err != nil {
		delete(r.remotes, name) // Rollback
		return nil, err
	}

	remoteCopy := *remote
	return &remoteCopy, nil
}

// RemoteEdit names the fields Update may change. A nil field is unchanged.
//
// The pointers are load-bearing for Forge, where the empty string is a VALUE
// ("resolve by URL host") and not an omission — a plain string could not tell
// "--forge was not passed" from "--forge ”", so clearing a binding would be
// unexpressible.
type RemoteEdit struct {
	Name  *string
	URL   *string
	Forge *string
}

// Update applies edit to the named remote and persists the registry, returning
// the remote as it now stands.
//
// It is one method rather than three setters because an edit spanning several
// fields must not half-land: separate calls each save, so a failure partway
// leaves a remote renamed but still addressing the old URL, and nothing
// records which half applied. Here every field is validated before anything
// mutates, one save covers them all, and a save failure restores the whole
// prior state.
//
// A rename carries the default pointer, which is stored by NAME and would
// otherwise be left naming a remote that no longer exists.
func (r *Registry) Update(name string, edit RemoteEdit) (*Remote, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	rem, ok := r.remotes[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", errs.ErrRemoteNotFound, name)
	}

	// Validate everything BEFORE mutating anything, so a refusal on the third
	// field cannot leave the first two applied.
	newName := name
	if edit.Name != nil && *edit.Name != name {
		if *edit.Name == "" {
			return nil, fmt.Errorf("new name must not be empty")
		}
		if _, exists := r.remotes[*edit.Name]; exists {
			return nil, fmt.Errorf("remote already exists: %s", *edit.Name)
		}
		newName = *edit.Name
	}

	newURL := rem.URL
	if edit.URL != nil {
		if err := requireRepository(*edit.URL); err != nil {
			return nil, err
		}
		normalized := storedRepoURL(*edit.URL)
		if existing, found := r.findByURLLocked(*edit.URL); found && existing != name {
			return nil, fmt.Errorf("remote '%s' already points to this URL", existing)
		}
		newURL = normalized
	}

	newForge := rem.Forge
	if edit.Forge != nil {
		if err := r.checkForgeLabelLocked(*edit.Forge); err != nil {
			return nil, err
		}
		newForge = *edit.Forge
	}

	prev := *rem
	prevDefault := r.defaultRemote

	rem.Name, rem.URL, rem.Forge = newName, newURL, newForge
	if newName != name {
		delete(r.remotes, name)
		r.remotes[newName] = rem
		if prevDefault == name {
			r.defaultRemote = newName
		}
	}

	if err := r.save(); err != nil {
		*rem = prev
		if newName != name {
			delete(r.remotes, newName)
			r.remotes[name] = rem
			r.defaultRemote = prevDefault
		}
		return nil, err
	}

	out := *rem
	return &out, nil
}

// SetForge binds the named remote to a forge label and persists the registry.
// The label must name a configured or built-in forge (github / git / a forges:
// entry); an unknown label is rejected so a typo surfaces at bind time rather
// than silently falling back to host-based resolution. An empty label clears
// the binding, restoring URL-host resolution.
func (r *Registry) SetForge(name, label string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	rem, ok := r.remotes[name]
	if !ok {
		return fmt.Errorf("remote not found: %s", name)
	}
	if err := r.checkForgeLabelLocked(label); err != nil {
		return err
	}
	if rem.Forge == label {
		return nil
	}
	prev := rem.Forge
	rem.Forge = label
	if err := r.save(); err != nil {
		rem.Forge = prev // rollback
		return err
	}
	return nil
}

// checkForgeLabelLocked refuses a forge label that names neither a configured
// nor a built-in forge (must hold lock). The empty label is accepted: it clears
// a binding, restoring URL-host resolution.
func (r *Registry) checkForgeLabelLocked(label string) error {
	if label == "" {
		return nil
	}
	if _, known := MergeForges(r.forges)[label]; !known {
		return fmt.Errorf("unknown forge %q: configure it under forges: or use a built-in (%q, %q)",
			label, ForgeGitHub, ForgeGitGeneric)
	}
	return nil
}

// findByURLLocked searches for a remote by URL (must hold lock).
func (r *Registry) findByURLLocked(repoURL string) (string, bool) {
	for name, remote := range r.remotes {
		if SameRepository(remote.URL, repoURL) {
			return name, true
		}
	}
	return "", false
}

// SameRepository reports whether a and b name one repository, compared on
// refuri.CanonicalRepoURL — the one repo-level canonicalizer — so a remote
// registered as git@host:o/r and a reference spelled https://Host/o/r/ match
// here exactly as they key the same lockfile entry. A URL that does not
// canonicalize names no repository, so it is the same repository as nothing —
// not even itself; the registry refuses such a URL at the door
// (requireRepository), so none is ever stored to be compared.
func SameRepository(a, b string) bool {
	ca, aerr := refuri.CanonicalRepoURL(a)
	cb, berr := refuri.CanonicalRepoURL(b)
	return aerr == nil && berr == nil && ca == cb
}

// requireRepository refuses a URL that names no repository. Without it an
// unreadable URL matches no registered remote, and every attempt to register
// it would add another.
func requireRepository(repoURL string) error {
	if _, err := refuri.CanonicalRepoURL(repoURL); err != nil {
		return fmt.Errorf("remote URL %q names no repository: %w", repoURL, err)
	}
	return nil
}

// storedRepoURL is the spelling a remote's URL is STORED and fetched from: the
// repo-URL grammar's normalized rendering, which folds scp and shorthand onto
// https but keeps credentials and a port, because a fetch may need them. It is
// never compared — SameRepository is.
func storedRepoURL(raw string) string {
	parsed, err := refuri.ParseRepoURL(raw)
	if err != nil {
		return ""
	}
	return parsed.Normalized()
}

// ResolveItemRemote returns the short remote name an installed item (profile or
// bundle) belongs to, inferred from its local name. A local name is stored under
// either the remote's short name (`personal/go-developer`) or its URL-derived
// local name (`github.com/owner/repo/go-developer`); this matches whichever and
// returns the short name. The longest matching prefix wins so a URL local name
// is preferred over a coincidentally-shorter short name. Reports false when no
// remote owns the name (a local project item).
func (r *Registry) ResolveItemRemote(localName string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	best, bestLen := "", -1
	consider := func(prefix, short string) {
		if prefix == "" || len(prefix) <= bestLen {
			return
		}
		if localName == prefix || strings.HasPrefix(localName, prefix+"/") {
			best, bestLen = short, len(prefix)
		}
	}
	for short, remote := range r.remotes {
		consider(short, short)
		consider((&Reference{URL: remote.URL}).LocalRemoteName(), short)
	}
	return best, bestLen >= 0
}

// Remove deletes a remote by name.
// Returns error if the remote doesn't exist.
func (r *Registry) Remove(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	removed, exists := r.remotes[name]
	if !exists {
		return fmt.Errorf("%w: %s", errs.ErrRemoteNotFound, name)
	}

	delete(r.remotes, name)
	if err := r.save(); err != nil {
		r.remotes[name] = removed // rollback, matching Add/SetForge
		return err
	}
	return nil
}

// Get retrieves a remote by name.
// Returns nil and error if not found.
func (r *Registry) Get(name string) (*Remote, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	remote, ok := r.remotes[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", errs.ErrRemoteNotFound, name)
	}

	// Return a copy to prevent mutation
	remoteCopy := *remote
	return &remoteCopy, nil
}

// List returns all configured remotes, sorted by name.
func (r *Registry) List() []*Remote {
	r.mu.RLock()
	defer r.mu.RUnlock()

	remotes := make([]*Remote, 0, len(r.remotes))
	for _, remote := range r.remotes {
		remoteCopy := *remote
		remotes = append(remotes, &remoteCopy)
	}

	sort.Slice(remotes, func(i, j int) bool {
		return remotes[i].Name < remotes[j].Name
	})

	return remotes
}

// Has checks if a remote exists.
// reprise:ignore — a trivial RLock/map-membership accessor; reprise groups it by
// structure alone with unrelated cross-package lookups (e.g. countersign.Store's
// unsigned-marker accessors), which legitimately differ. Not real duplication.
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.remotes[name]
	return ok
}

// GetDefault returns the default remote name, or empty string if not set.
func (r *Registry) GetDefault() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defaultRemote
}

// SetDefault sets the default remote.
// Returns error if the remote doesn't exist.
func (r *Registry) SetDefault(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	prev := r.defaultRemote

	// Allow clearing the default
	if name == "" {
		r.defaultRemote = ""
		if err := r.save(); err != nil {
			r.defaultRemote = prev // rollback, matching Add/SetForge
			return err
		}
		return nil
	}

	// Verify remote exists
	if _, ok := r.remotes[name]; !ok {
		return fmt.Errorf("%w: %s", errs.ErrRemoteNotFound, name)
	}

	r.defaultRemote = name
	if err := r.save(); err != nil {
		r.defaultRemote = prev // rollback, matching Add/SetForge
		return err
	}
	return nil
}

// Forges returns the configured forge instances merged over the built-in
// defaults, so callers always see github + git even with no forges: block.
func (r *Registry) Forges() map[string]ForgeConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return MergeForges(r.forges)
}
