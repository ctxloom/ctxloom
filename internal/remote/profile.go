package remote

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/afero"
)

// ProfileDeps handles remote dependencies in profiles.
type ProfileDeps struct {
	registry *Registry
	auth     AuthConfig
	puller   *Puller
	fs       afero.Fs
}

// ProfileDepsOption is a functional option for configuring ProfileDeps.
type ProfileDepsOption func(*ProfileDeps)

// WithProfileDepsFS sets a custom filesystem implementation (for testing).
func WithProfileDepsFS(fs afero.Fs) ProfileDepsOption {
	return func(p *ProfileDeps) {
		p.fs = fs
	}
}

// WithProfileDepsPuller sets a custom puller (for testing).
func WithProfileDepsPuller(puller *Puller) ProfileDepsOption {
	return func(p *ProfileDeps) {
		p.puller = puller
	}
}

// NewProfileDeps creates a new profile dependency handler.
func NewProfileDeps(registry *Registry, auth AuthConfig, opts ...ProfileDepsOption) *ProfileDeps {
	p := &ProfileDeps{
		registry: registry,
		auth:     auth,
		fs:       afero.NewOsFs(),
	}

	// Apply options first to allow overrides
	for _, opt := range opts {
		opt(p)
	}

	// Create default puller if not provided
	if p.puller == nil {
		p.puller = NewPuller(registry, auth)
	}

	return p
}

// RemoteRef represents a remote reference in a profile.
type RemoteRef struct {
	Ref      string   // e.g., "alice/security@v1.0.0"
	ItemType ItemType // fragment, prompt, or profile
	Cached   bool     // Whether it's already cached locally
}

// ParseProfileRefs parses a list of references (local or remote) from a profile.
// Remote refs are in format: remote/name or remote/name@version
// Local refs are just: name
func ParseProfileRefs(refs []string, itemType ItemType) []RemoteRef {
	var results []RemoteRef
	for _, ref := range refs {
		// Check if it's a remote reference (contains /)
		if strings.Contains(ref, "/") {
			results = append(results, RemoteRef{
				Ref:      ref,
				ItemType: itemType,
				Cached:   false,
			})
		}
		// Local refs are handled separately, not returned here
	}
	return results
}

// CheckCached checks if remote refs are cached locally.
func (p *ProfileDeps) CheckCached(refs []RemoteRef, baseDir string) []RemoteRef {
	if baseDir == "" {
		baseDir = ".ctxloom"
	}

	for i := range refs {
		ref, err := ParseReference(refs[i].Ref)
		if err != nil {
			continue
		}

		localPath := ref.LocalPath(baseDir, refs[i].ItemType)
		if _, err := p.fs.Stat(localPath); err == nil {
			refs[i].Cached = true
		}
	}

	return refs
}

// PullResult contains results from pulling profile dependencies.
type ProfilePullResult struct {
	Pulled  []string // Successfully pulled refs
	Skipped []string // User skipped these
	Failed  []string // Failed to pull
	Errors  []error  // Errors encountered
}

// PullDeps pulls all uncached remote dependencies for a profile.
// Returns error if any required dependency fails to pull.
func (p *ProfileDeps) PullDeps(ctx context.Context, refs []RemoteRef, opts PullOptions) (*ProfilePullResult, error) {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}

	result := &ProfilePullResult{}

	// Filter to uncached refs
	var uncached []RemoteRef
	for _, ref := range refs {
		if !ref.Cached {
			uncached = append(uncached, ref)
		}
	}

	if len(uncached) == 0 {
		return result, nil
	}

	_, _ = fmt.Fprintf(opts.Stdout, "\nProfile requires %d remote dependencies:\n", len(uncached))
	for _, ref := range uncached {
		_, _ = fmt.Fprintf(opts.Stdout, "  • %s (%s)\n", ref.Ref, ref.ItemType)
	}
	_, _ = fmt.Fprintln(opts.Stdout)

	// Pull each dependency
	for _, ref := range uncached {
		_, _ = fmt.Fprintf(opts.Stdout, "Pulling %s...\n", ref.Ref)

		pullOpts := PullOptions{
			Force:    opts.Force,
			LocalDir: opts.LocalDir,
			ItemType: ref.ItemType,
			Stdout:   opts.Stdout,
			Stdin:    opts.Stdin,
		}

		_, err := p.puller.Pull(ctx, ref.Ref, pullOpts)
		if err != nil {
			if strings.Contains(err.Error(), "cancelled") {
				result.Skipped = append(result.Skipped, ref.Ref)
				_, _ = fmt.Fprintf(opts.Stdout, "  Skipped: %s\n", ref.Ref)
			} else {
				result.Failed = append(result.Failed, ref.Ref)
				result.Errors = append(result.Errors, err)
				_, _ = fmt.Fprintf(opts.Stdout, "  Error: %v\n", err)
			}
			continue
		}

		result.Pulled = append(result.Pulled, ref.Ref)
		_, _ = fmt.Fprintf(opts.Stdout, "  ✓ Pulled %s\n", ref.Ref)
	}

	// Check if any critical deps failed
	if len(result.Failed) > 0 {
		return result, fmt.Errorf("%d dependencies failed to pull", len(result.Failed))
	}

	if len(result.Skipped) > 0 {
		return result, fmt.Errorf("%d dependencies were skipped", len(result.Skipped))
	}

	return result, nil
}

// ResolveProfileDeps is a convenience function to resolve all remote deps in a profile.
// With the bundles-only model, profiles reference bundles which contain all content.
func ResolveProfileDeps(ctx context.Context, bundles []string, registry *Registry, auth AuthConfig, stdout io.Writer, stdin io.Reader) error {
	deps := NewProfileDeps(registry, auth)

	// Parse bundle refs
	allRefs := ParseProfileRefs(bundles, ItemTypeBundle)

	if len(allRefs) == 0 {
		return nil // No remote deps
	}

	// Check which are cached
	allRefs = deps.CheckCached(allRefs, "")

	// Count uncached
	uncached := 0
	for _, ref := range allRefs {
		if !ref.Cached {
			uncached++
		}
	}

	if uncached == 0 {
		return nil // All cached
	}

	// Pull uncached deps
	opts := PullOptions{
		Stdout: stdout,
		Stdin:  stdin,
	}

	_, err := deps.PullDeps(ctx, allRefs, opts)
	return err
}
