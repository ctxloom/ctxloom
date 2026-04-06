package remote

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// PullOptions configures pull behavior.
type PullOptions struct {
	// Force skips the confirmation prompt but still displays content.
	Force bool

	// Blind skips both confirmation prompt AND content display.
	// Use this for automated/batch operations. Implies Force.
	Blind bool

	// LocalDir overrides the default .ctxloom directory path.
	LocalDir string

	// ItemType specifies what type of item to pull.
	ItemType ItemType

	// Cascade pulls all dependencies (bundles referenced by profiles).
	Cascade bool

	// Stdout and Stdin for output and input (for testing).
	Stdout io.Writer
	Stdin  io.Reader
}

// PullResult contains the result of a pull operation.
type PullResult struct {
	// LocalPath is where the item was saved.
	LocalPath string

	// SHA is the commit SHA of the fetched content.
	SHA string

	// Overwritten indicates if an existing file was replaced.
	Overwritten bool

	// CascadePulled lists items pulled as dependencies (for profiles).
	CascadePulled []string
}

// profileYAML is a minimal struct for parsing profile bundle references.
type profileYAML struct {
	Bundles []string `yaml:"bundles"`
}

// FetcherFactory creates Fetcher instances. Allows mocking for tests.
type FetcherFactory func(repoURL string, auth AuthConfig) (Fetcher, error)

// DefaultFetcherFactory is the production implementation that creates API-based fetchers.
func DefaultFetcherFactory(repoURL string, auth AuthConfig) (Fetcher, error) {
	return NewFetcher(repoURL, auth)
}

// TerminalChecker checks if readers/writers are terminals.
type TerminalChecker interface {
	IsTerminalReader(r io.Reader) bool
	IsTerminalWriter(w io.Writer) bool
}

// defaultTerminalChecker uses os/term for real terminal detection.
type defaultTerminalChecker struct{}

func (d *defaultTerminalChecker) IsTerminalReader(r io.Reader) bool {
	if f, ok := r.(*os.File); ok {
		return term.IsTerminal(int(f.Fd()))
	}
	return false
}

func (d *defaultTerminalChecker) IsTerminalWriter(w io.Writer) bool {
	if f, ok := w.(*os.File); ok {
		return term.IsTerminal(int(f.Fd()))
	}
	return false
}

// Puller handles pulling items from remotes.
type Puller struct {
	registry        *Registry
	auth            AuthConfig
	replaceManager  *ReplaceManager
	vendorManager   *VendorManager
	lockfileManager *LockfileManager
	fetcherFactory  FetcherFactory
	terminalChecker TerminalChecker
	fs              afero.Fs
}

// PullerOption is a functional option for configuring a Puller.
type PullerOption func(*Puller)

// WithPullerFS sets a custom filesystem implementation (for testing).
func WithPullerFS(fs afero.Fs) PullerOption {
	return func(p *Puller) {
		p.fs = fs
	}
}

// WithReplaceManager sets a custom replace manager (for testing).
func WithReplaceManager(rm *ReplaceManager) PullerOption {
	return func(p *Puller) {
		p.replaceManager = rm
	}
}

// WithVendorManager sets a custom vendor manager (for testing).
func WithVendorManager(vm *VendorManager) PullerOption {
	return func(p *Puller) {
		p.vendorManager = vm
	}
}

// WithLockfileManager sets a custom lockfile manager (for testing).
func WithLockfileManager(lm *LockfileManager) PullerOption {
	return func(p *Puller) {
		p.lockfileManager = lm
	}
}

// WithFetcherFactory sets a custom fetcher factory (for testing).
func WithFetcherFactory(ff FetcherFactory) PullerOption {
	return func(p *Puller) {
		p.fetcherFactory = ff
	}
}

// WithTerminalChecker sets a custom terminal checker (for testing).
func WithTerminalChecker(tc TerminalChecker) PullerOption {
	return func(p *Puller) {
		p.terminalChecker = tc
	}
}

// NewPuller creates a new puller.
func NewPuller(registry *Registry, auth AuthConfig, opts ...PullerOption) *Puller {
	p := &Puller{
		registry:        registry,
		auth:            auth,
		fetcherFactory:  DefaultFetcherFactory,
		terminalChecker: &defaultTerminalChecker{},
		fs:              afero.NewOsFs(),
	}

	// Apply options first to allow overrides
	for _, opt := range opts {
		opt(p)
	}

	// Initialize defaults for nil dependencies (allows tests to override)
	if p.replaceManager == nil {
		p.replaceManager, _ = NewReplaceManager("")
	}
	if p.vendorManager == nil {
		p.vendorManager = NewVendorManager(".ctxloom")
	}
	if p.lockfileManager == nil {
		p.lockfileManager = NewLockfileManager(".ctxloom")
	}

	return p
}

// Pull downloads an item from a remote with security review.
func (p *Puller) Pull(ctx context.Context, refStr string, opts PullOptions) (*PullResult, error) {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}

	// Parse reference
	ref, err := ParseReference(refStr)
	if err != nil {
		return nil, fmt.Errorf("invalid reference: %w", err)
	}

	// Check for replace directive first
	if p.replaceManager != nil {
		if localPath, ok := p.replaceManager.Get(refStr); ok {
			_, _ = fmt.Fprintf(opts.Stdout, "Using local replace: %s → %s\n", refStr, localPath)
			replacedContent, err := p.replaceManager.LoadReplaced(refStr)
			if err != nil {
				return nil, fmt.Errorf("failed to load replaced file: %w", err)
			}
			if err := p.writeContent(ref, opts, replacedContent, "local"); err != nil {
				return nil, err
			}
			return &PullResult{
				LocalPath:   localPath,
				SHA:         "local",
				Overwritten: false,
			}, nil
		}
	}

	// Check vendor mode
	if p.vendorManager != nil && p.vendorManager.IsVendored() {
		if p.vendorManager.HasVendored(opts.ItemType, ref) {
			vendoredContent, err := p.vendorManager.GetVendored(opts.ItemType, ref)
			if err != nil {
				return nil, fmt.Errorf("failed to load vendored file: %w", err)
			}
			_, _ = fmt.Fprintf(opts.Stdout, "Using vendored: %s (%d bytes)\n", refStr, len(vendoredContent))
			return &PullResult{
				LocalPath:   filepath.Join(p.vendorManager.VendorDir(), opts.ItemType.DirName(), ref.Remote, ref.Path+".yaml"),
				SHA:         "vendored",
				Overwritten: false,
			}, nil
		}
	}

	// Get remote URL and version - either from registry or from canonical URL
	var repoURL, version string
	var rem *Remote
	var localName string // The local name to use for lockfile key

	if ref.IsCanonical {
		// Use URL from canonical reference
		repoURL = ref.URL
		version = ref.Version

		// Auto-register the remote (or get existing one)
		var err error
		rem, err = p.registry.GetOrCreateByURL(repoURL, version)
		if err != nil {
			return nil, fmt.Errorf("failed to register remote: %w", err)
		}

		// Build local name: remoteName/path
		localName = fmt.Sprintf("%s/%s", rem.Name, ref.Path)
	} else {
		// Look up remote in registry
		var err error
		rem, err = p.registry.Get(ref.Remote)
		if err != nil {
			return nil, err
		}
		repoURL = rem.URL
		version = rem.Version

		// Local name is the original reference
		localName = fmt.Sprintf("%s/%s", ref.Remote, ref.Path)
	}

	// Create fetcher
	fetcher, err := p.fetcherFactory(repoURL, p.auth)
	if err != nil {
		return nil, fmt.Errorf("failed to create fetcher: %w", err)
	}

	// Parse repo URL
	owner, repo, err := ParseRepoURL(repoURL)
	if err != nil {
		return nil, fmt.Errorf("invalid remote URL: %w", err)
	}

	// Check for retracted version
	retracted, reason, _ := CheckRetracted(ctx, fetcher, owner, repo, version, ref, opts.ItemType)
	if retracted {
		_, _ = fmt.Fprintf(opts.Stdout, "\n⚠️  WARNING: This version has been retracted!\n")
		_, _ = fmt.Fprintf(opts.Stdout, "Reason: %s\n\n", reason)
		if !opts.Force {
			confirmed, err := promptConfirmation(opts.Stdout, opts.Stdin, "Continue anyway?")
			if err != nil {
				return nil, err
			}
			if !confirmed {
				return nil, fmt.Errorf("installation cancelled: version retracted")
			}
		}
	}

	// Resolve ref to SHA - use ContentVersion if specified, otherwise use default branch
	contentVersion := ref.EffectiveContentVersion()
	requestedVersion := contentVersion // Store what user requested for export reconstruction
	if contentVersion == "" {
		contentVersion, err = fetcher.GetDefaultBranch(ctx, owner, repo)
		if err != nil {
			return nil, fmt.Errorf("failed to get default branch: %w", err)
		}
		requestedVersion = "" // User didn't specify a version
	}

	sha, err := fetcher.ResolveRef(ctx, owner, repo, contentVersion)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve ref '%s': %w", contentVersion, err)
	}

	// Build file path and fetch content
	filePath := ref.BuildFilePath(opts.ItemType, version)
	content, err := fetcher.FetchFile(ctx, owner, repo, filePath, sha)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch: %w", err)
	}

	// Blind mode implies Force (skip prompts and content display)
	effectiveForce := opts.Force || opts.Blind

	// Warn when using blind mode
	if opts.Blind {
		_, _ = fmt.Fprintf(opts.Stdout, "⚠️  Blind mode: skipping security review for %s\n", refStr)
	}

	// Require interactive terminal unless force/blind
	if !effectiveForce && !p.terminalChecker.IsTerminalReader(opts.Stdin) {
		return nil, fmt.Errorf("interactive terminal required for pull; use --force to skip confirmation")
	}

	// Display security warning and content (unless blind)
	if !opts.Blind {
		shortSHA := sha
		if len(sha) > 7 {
			shortSHA = sha[:7]
		}

		// Parse content to get type-specific security warning
		secure, err := ParseSecureContent(opts.ItemType, content)
		if err != nil {
			return nil, fmt.Errorf("failed to parse content: %w", err)
		}

		displaySecurityWarning(opts.Stdout, ref, rem, shortSHA, filePath, content, secure, p.terminalChecker)
	}

	// Prompt for confirmation unless force/blind
	if !effectiveForce {
		confirmed, err := promptConfirmation(opts.Stdout, opts.Stdin, "Install this item?")
		if err != nil {
			return nil, fmt.Errorf("failed to read confirmation: %w", err)
		}
		if !confirmed {
			return nil, fmt.Errorf("installation cancelled")
		}
	}

	// Determine local path
	baseDir := opts.LocalDir
	if baseDir == "" {
		baseDir = ".ctxloom"
	}

	localPath := ref.LocalPath(baseDir, opts.ItemType)

	// Transform profile content if needed (convert canonical URLs to local names)
	if opts.ItemType == ItemTypeProfile {
		content, err = p.transformProfileContent(content, opts.Stdout)
		if err != nil {
			return nil, fmt.Errorf("failed to transform profile: %w", err)
		}
	}

	// Check for existing file
	overwritten := false
	if _, err := p.fs.Stat(localPath); err == nil {
		overwritten = true
		// Show diff
		existingContent, _ := afero.ReadFile(p.fs, localPath)
		if string(existingContent) != string(content) {
			_, _ = fmt.Fprintln(opts.Stdout, "\n--- Existing file differs ---")
			_, _ = fmt.Fprintln(opts.Stdout, "Use a diff tool to compare if needed.")
			if !opts.Force {
				confirmed, err := promptConfirmation(opts.Stdout, opts.Stdin, "Overwrite existing file?")
				if err != nil {
					return nil, fmt.Errorf("failed to read confirmation: %w", err)
				}
				if !confirmed {
					return nil, fmt.Errorf("overwrite cancelled")
				}
			}
		}
	}

	// Write file
	if err := p.fs.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	if err := afero.WriteFile(p.fs, localPath, content, 0644); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	// Update lockfile with provenance (use local name as key)
	if err := p.updateLockfile(localName, opts.ItemType, rem, sha, requestedVersion); err != nil {
		// Log warning but don't fail the pull
		_, _ = fmt.Fprintf(opts.Stdout, "Warning: failed to update lockfile: %v\n", err)
	}

	result := &PullResult{
		LocalPath:   localPath,
		SHA:         sha,
		Overwritten: overwritten,
	}

	// Cascade pull dependencies for profiles
	if opts.Cascade && opts.ItemType == ItemTypeProfile {
		cascaded, err := p.cascadePullProfile(ctx, content, opts)
		if err != nil {
			return result, fmt.Errorf("cascade pull failed: %w", err)
		}
		result.CascadePulled = cascaded
	}

	return result, nil
}

// cascadePullProfile parses a profile and pulls all referenced bundles.
func (p *Puller) cascadePullProfile(ctx context.Context, profileContent []byte, opts PullOptions) ([]string, error) {
	var profile profileYAML
	if err := yaml.Unmarshal(profileContent, &profile); err != nil {
		return nil, fmt.Errorf("failed to parse profile: %w", err)
	}

	if len(profile.Bundles) == 0 {
		return nil, nil
	}

	_, _ = fmt.Fprintf(opts.Stdout, "\nProfile references %d bundles:\n", len(profile.Bundles))
	for _, bundle := range profile.Bundles {
		_, _ = fmt.Fprintf(opts.Stdout, "  - %s\n", bundle)
	}
	_, _ = fmt.Fprintln(opts.Stdout)

	var pulled []string
	for _, bundleRef := range profile.Bundles {
		// Check if already exists locally
		ref, err := ParseReference(bundleRef)
		if err != nil {
			_, _ = fmt.Fprintf(opts.Stdout, "Warning: invalid bundle reference %q: %v\n", bundleRef, err)
			continue
		}

		baseDir := opts.LocalDir
		if baseDir == "" {
			baseDir = ".ctxloom"
		}
		localPath := ref.LocalPath(baseDir, ItemTypeBundle)

		if _, err := p.fs.Stat(localPath); err == nil {
			_, _ = fmt.Fprintf(opts.Stdout, "  [cached] %s\n", bundleRef)
			continue
		}

		// Pull the bundle
		_, _ = fmt.Fprintf(opts.Stdout, "  Pulling %s...\n", bundleRef)
		bundleOpts := PullOptions{
			Force:    opts.Force,
			LocalDir: opts.LocalDir,
			ItemType: ItemTypeBundle,
			Cascade:  false, // Don't cascade further
			Stdout:   opts.Stdout,
			Stdin:    opts.Stdin,
		}

		_, err = p.Pull(ctx, bundleRef, bundleOpts)
		if err != nil {
			if strings.Contains(err.Error(), "cancelled") {
				_, _ = fmt.Fprintf(opts.Stdout, "    Skipped\n")
				continue
			}
			return pulled, fmt.Errorf("failed to pull bundle %s: %w", bundleRef, err)
		}

		pulled = append(pulled, bundleRef)
		_, _ = fmt.Fprintf(opts.Stdout, "    Done\n")
	}

	return pulled, nil
}

// displaySecurityWarning shows the security warning and full content.
func displaySecurityWarning(w io.Writer, ref *Reference, rem *Remote, sha, filePath string, content []byte, secure SecureContent, tc TerminalChecker) {
	warning := secure.SecurityWarning()

	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "┌─────────────────────────────────────────────────────────────────┐")
	_, _ = fmt.Fprintf(w, "│  ⚠️  WARNING: %-50s│\n", warning.Title)
	_, _ = fmt.Fprintln(w, "│                                                                 │")
	_, _ = fmt.Fprintf(w, "│  %-62s│\n", warning.Context)
	_, _ = fmt.Fprintln(w, "│  Malicious content can:                                         │")
	for _, risk := range warning.Risks {
		_, _ = fmt.Fprintf(w, "│    • %-58s│\n", risk)
	}
	_, _ = fmt.Fprintln(w, "│                                                                 │")
	_, _ = fmt.Fprintln(w, "│  REVIEW THE FULL CONTENT BELOW BEFORE ACCEPTING                │")
	_, _ = fmt.Fprintln(w, "└─────────────────────────────────────────────────────────────────┘")
	_, _ = fmt.Fprintln(w, "")

	// Source info
	_, _ = fmt.Fprintf(w, "Source: %s @ %s\n", rem.URL, sha)
	_, _ = fmt.Fprintf(w, "Org:    %s\n", ref.Remote)
	_, _ = fmt.Fprintf(w, "Name:   %s\n", ref.Path)
	_, _ = fmt.Fprintf(w, "Path:   %s\n", filePath)

	// Display note if present
	if note := secure.Note(); note != "" {
		// Truncate very long notes (max 4K chars)
		const maxNoteLen = 4096
		if len(note) > maxNoteLen {
			note = note[:maxNoteLen-3] + "..."
		}
		_, _ = fmt.Fprintln(w, "")
		_, _ = fmt.Fprintf(w, "Note: %s\n", note)
	}

	_, _ = fmt.Fprintln(w, "")

	// Content with pager for long content
	contentStr := string(content)
	lineCount := strings.Count(contentStr, "\n") + 1

	_, _ = fmt.Fprintln(w, "─────────────────── CONTENT START ───────────────────")

	// Use pager for long content if terminal.
	// Security note: PAGER is user-controlled. This is standard Unix behavior
	// but users should be aware that PAGER could execute arbitrary commands.
	if lineCount > 50 && tc.IsTerminalWriter(w) {
		pager := os.Getenv("PAGER")
		if pager == "" {
			pager = "less"
		}

		cmd := exec.Command(pager)
		cmd.Stdin = strings.NewReader(contentStr)
		cmd.Stdout = w
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			// Fallback to direct output
			_, _ = fmt.Fprint(w, contentStr)
		}
	} else {
		_, _ = fmt.Fprint(w, contentStr)
	}

	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "─────────────────── CONTENT END ─────────────────────")
	_, _ = fmt.Fprintln(w, "")
}

// promptConfirmation asks the user for yes/no confirmation.
// Default is NO - user must explicitly type 'y' or 'yes'.
func promptConfirmation(w io.Writer, r io.Reader, prompt string) (bool, error) {
	_, _ = fmt.Fprintf(w, "%s [y/N]: ", prompt)

	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return false, err
		}
		return false, nil // EOF = no
	}

	response := strings.TrimSpace(strings.ToLower(scanner.Text()))
	return response == "y" || response == "yes", nil
}


// writeContent writes content to the local path (used for replace directive).
func (p *Puller) writeContent(ref *Reference, opts PullOptions, content []byte, sha string) error {
	baseDir := opts.LocalDir
	if baseDir == "" {
		baseDir = ".ctxloom"
	}

	localPath := ref.LocalPath(baseDir, opts.ItemType)

	if err := p.fs.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	return afero.WriteFile(p.fs, localPath, content, 0644)
}

// updateLockfile records provenance in the lockfile.
// localName is the local name format (remoteName/path) used as the key.
// requestedVersion is the original tag/SHA user specified (empty if used HEAD).
func (p *Puller) updateLockfile(localName string, itemType ItemType, remote *Remote, sha string, requestedVersion string) error {
	lockfile, err := p.lockfileManager.Load()
	if err != nil {
		return fmt.Errorf("failed to load lockfile: %w", err)
	}

	entry := LockEntry{
		SHA:              sha,
		URL:              remote.URL,
		CtxloomVersion:       remote.Version,
		RequestedVersion: requestedVersion,
		FetchedAt:        time.Now().UTC(),
	}

	lockfile.AddEntry(itemType, localName, entry)

	if err := p.lockfileManager.Save(lockfile); err != nil {
		return fmt.Errorf("failed to save lockfile: %w", err)
	}

	return nil
}

// transformProfileContent converts canonical URLs in a profile to local names.
// The actual lockfile entries are created when bundles are pulled (via cascade or manually).
func (p *Puller) transformProfileContent(content []byte, w io.Writer) ([]byte, error) {
	// Parse the profile
	var rawProfile map[string]interface{}
	if err := yaml.Unmarshal(content, &rawProfile); err != nil {
		return content, nil // Not valid YAML, return as-is
	}

	// Check if there are bundles to transform
	bundlesRaw, ok := rawProfile["bundles"]
	if !ok {
		return content, nil // No bundles, return as-is
	}

	bundles, ok := bundlesRaw.([]interface{})
	if !ok {
		return content, nil // Not a list, return as-is
	}

	// Check if any bundles need transformation (canonical URLs)
	needsTransform := false
	for _, b := range bundles {
		bundleStr, ok := b.(string)
		if !ok {
			continue
		}
		if IsCanonicalRef(bundleStr) {
			needsTransform = true
			break
		}
	}

	if !needsTransform {
		return content, nil
	}

	// Transform the bundles
	_, _ = fmt.Fprintf(w, "\nTransforming canonical URLs to local names...\n")

	transformedBundles := make([]string, 0, len(bundles))

	for _, b := range bundles {
		bundleStr, ok := b.(string)
		if !ok {
			continue
		}

		// Check if this is a canonical URL
		if !IsCanonicalRef(bundleStr) {
			// Already local - normalize to ensure consistent format
			// (strips version suffixes, normalizes paths)
			local, err := ToLocalRef(bundleStr)
			if err != nil {
				transformedBundles = append(transformedBundles, bundleStr)
			} else {
				transformedBundles = append(transformedBundles, local)
			}
			continue
		}

		// For canonical URLs, we need to register the remote first
		// Handle item path suffix (e.g., #fragments/name)
		var itemPath string
		urlPart := bundleStr
		if hashIdx := strings.Index(bundleStr, "#"); hashIdx != -1 {
			urlPart = bundleStr[:hashIdx]
			itemPath = bundleStr[hashIdx:]
		}

		parsed, err := ParseReference(urlPart)
		if err != nil {
			_, _ = fmt.Fprintf(w, "  Warning: could not parse %q: %v\n", bundleStr, err)
			transformedBundles = append(transformedBundles, bundleStr)
			continue
		}

		// Get or create a local remote for this URL
		// This is essential: it ensures the remote is registered so cascade pull can find it
		localRemote, err := p.registry.GetOrCreateByURL(parsed.URL, parsed.Version)
		if err != nil {
			_, _ = fmt.Fprintf(w, "  Warning: could not register remote for %q: %v\n", bundleStr, err)
			transformedBundles = append(transformedBundles, bundleStr)
			continue
		}

		// Build local reference: remoteName/path with item path if present
		localRef := fmt.Sprintf("%s/%s%s", localRemote.Name, parsed.Path, itemPath)

		_, _ = fmt.Fprintf(w, "  %s -> %s\n", bundleStr, localRef)
		transformedBundles = append(transformedBundles, localRef)
	}

	// Update the profile with transformed bundles
	rawProfile["bundles"] = transformedBundles

	// Re-marshal the profile
	transformed, err := yaml.Marshal(rawProfile)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal transformed profile: %w", err)
	}

	return transformed, nil
}
