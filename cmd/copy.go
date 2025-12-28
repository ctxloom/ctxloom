package cmd

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/benjaminabbitt/scm/internal/config"
	"github.com/benjaminabbitt/scm/internal/fsys"
	"github.com/benjaminabbitt/scm/internal/gitutil"
	"github.com/benjaminabbitt/scm/internal/schema"
	"github.com/benjaminabbitt/scm/resources"
)

var (
	copyForce     bool
	copyClear     bool // Clear destination .scm directory before copying
	copyFragments []string
	copyTags      []string
	copyPrompts   []string
	copyProfiles  []string
	copyVerbose   bool
	copyDev       bool // Dev mode: allow copying to resources directory
	copyConfig    bool // Copy config.yaml
)

// Location represents a copy source or destination.
type Location int

const (
	LocationResources Location = iota
	LocationHome
	LocationProject
	LocationPath // Arbitrary filesystem path
)

// ParsedLocation holds a Location type and optional path for LocationPath.
type ParsedLocation struct {
	Type Location
	Path string // Only set for LocationPath
}

func parseLocation(s string) (ParsedLocation, error) {
	switch strings.ToLower(s) {
	case "resources", "r":
		return ParsedLocation{Type: LocationResources}, nil
	case "home", "h":
		return ParsedLocation{Type: LocationHome}, nil
	case "project", "p":
		return ParsedLocation{Type: LocationProject}, nil
	default:
		// Check if it's a path (contains / or \ or is absolute)
		if strings.ContainsAny(s, "/\\") || filepath.IsAbs(s) || s == "." || s == ".." {
			absPath, err := filepath.Abs(s)
			if err != nil {
				return ParsedLocation{}, fmt.Errorf("invalid path %q: %w", s, err)
			}
			return ParsedLocation{Type: LocationPath, Path: absPath}, nil
		}
		return ParsedLocation{}, fmt.Errorf("invalid location %q: must be resources (r), home (h), project (p), or a path", s)
	}
}

func (l Location) String() string {
	switch l {
	case LocationResources:
		return "resources"
	case LocationHome:
		return "home"
	case LocationProject:
		return "project"
	case LocationPath:
		return "path"
	default:
		return "unknown"
	}
}

// CopyResult tracks what happened during a copy operation.
type CopyResult struct {
	Added     []string
	Updated   []string
	Unchanged []string
	Skipped   []string
}

func (r *CopyResult) Total() int {
	return len(r.Added) + len(r.Updated) + len(r.Unchanged)
}

func (r *CopyResult) Merge(other *CopyResult) {
	if other == nil {
		return
	}
	r.Added = append(r.Added, other.Added...)
	r.Updated = append(r.Updated, other.Updated...)
	r.Unchanged = append(r.Unchanged, other.Unchanged...)
	r.Skipped = append(r.Skipped, other.Skipped...)
}

var copyCmd = &cobra.Command{
	Use:   "copy <from> <to>",
	Short: "Copy fragments and prompts between locations",
	Long: `Copy context fragments and prompts between resources, home, project, or a path.

Locations:
  resources (r)  - Embedded default fragments and prompts
  home (h)       - ~/.scm directory
  project (p)    - .scm directory in the current project
  <path>         - Arbitrary directory path (creates .scm subdirectory)

Header behavior:
  - Copying TO project: adds a "DO NOT EDIT" header to files
  - Copying FROM project: removes the header from files

Examples:
  # Copy all embedded fragments to project (positional args)
  scm copy r p
  scm copy resources project

  # Copy to arbitrary path
  scm copy r /path/to/dir
  scm copy resources ./my-config

  # Clear destination before copying (destroys customizations)
  scm copy r p --clear

  # Copy specific fragments from home to project
  scm copy h p -f security -f golang

  # Copy fragments with specific tags
  scm copy r h -t review

  # Copy prompts from resources to project
  scm copy r p -p code-review

  # Force overwrite existing files
  scm copy r p --force

  # Copy fragments for specific profiles
  scm copy r p --profile go-developer`,
	Args: cobra.ExactArgs(2),
	RunE: runCopy,
}

func runCopy(cmd *cobra.Command, args []string) error {
	from, err := parseLocation(args[0])
	if err != nil {
		return err
	}

	to, err := parseLocation(args[1])
	if err != nil {
		return err
	}

	if from.Type == to.Type && from.Path == to.Path {
		return fmt.Errorf("source and destination cannot be the same")
	}

	// Resources cannot be a destination unless in dev mode
	if to.Type == LocationResources && !copyDev {
		return fmt.Errorf("cannot copy to resources (use --dev flag when working on scm itself)")
	}

	// Get source and destination paths
	srcFragDir, srcPromptDir, err := getLocationPaths(from)
	if err != nil {
		return fmt.Errorf("failed to resolve source: %w", err)
	}

	dstFragDir, dstPromptDir, err := getLocationPaths(to)
	if err != nil {
		return fmt.Errorf("failed to resolve destination: %w", err)
	}

	// Handle --clear flag: destroy destination .scm directory before copying
	if copyClear {
		var scmDir string
		switch to.Type {
		case LocationHome:
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("failed to get home directory: %w", err)
			}
			scmDir = filepath.Join(home, config.SCMDirName)
		case LocationProject:
			rootDir := findGitRoot()
			if rootDir == "" {
				rootDir, _ = os.Getwd()
			}
			scmDir = filepath.Join(rootDir, config.SCMDirName)
		case LocationPath:
			scmDir = filepath.Join(to.Path, config.SCMDirName)
		case LocationResources:
			// Resources clear handled in dev mode - clear resources directory
			pwd, _ := os.Getwd()
			scmDir = filepath.Join(pwd, "resources")
		}

		if scmDir != "" {
			if _, err := os.Stat(scmDir); err == nil {
				fmt.Printf("Clearing %s...\n", scmDir)
				if err := os.RemoveAll(scmDir); err != nil {
					return fmt.Errorf("failed to clear destination: %w", err)
				}
			}
		}
	}

	// Determine header behavior
	addHeader := to.Type == LocationProject
	removeHeader := from.Type == LocationProject

	// Build fragment filter from flags
	fragmentFilter, err := buildFragmentFilter()
	if err != nil {
		return err
	}

	// Copy fragments
	fragResult := &CopyResult{}
	if len(copyPrompts) == 0 || len(copyFragments) > 0 || len(copyTags) > 0 || len(copyProfiles) > 0 {
		fmt.Printf("Copying fragments from %s to %s\n", from.Type, to.Type)

		result, err := copyFragmentsWithOptions(from.Type, srcFragDir, dstFragDir, fragmentFilter, addHeader, removeHeader)
		if err != nil {
			return fmt.Errorf("failed to copy fragments: %w", err)
		}
		fragResult.Merge(result)
		printCopyResultVerbose(fragResult, "fragments", copyVerbose)
	}

	// Copy prompts
	promptResult := &CopyResult{}
	if len(copyPrompts) > 0 || (len(copyFragments) == 0 && len(copyTags) == 0 && len(copyProfiles) == 0) {
		fmt.Printf("Copying prompts from %s to %s\n", from.Type, to.Type)

		result, err := copyPromptsWithOptions(from.Type, srcPromptDir, dstPromptDir, copyPrompts, addHeader, removeHeader)
		if err != nil {
			return fmt.Errorf("failed to copy prompts: %w", err)
		}
		promptResult.Merge(result)
		printCopyResultVerbose(promptResult, "prompts", copyVerbose)
	}

	// Copy config if requested
	configResult := &CopyResult{}
	if copyConfig {
		fmt.Printf("Copying config from %s to %s\n", from.Type, to.Type)

		result, err := copyConfigWithOptions(from, to.Type, addHeader, removeHeader)
		if err != nil {
			return fmt.Errorf("failed to copy config: %w", err)
		}
		configResult.Merge(result)
		printCopyResultVerbose(configResult, "config", copyVerbose)
	}

	// Summary
	totalAdded := len(fragResult.Added) + len(promptResult.Added) + len(configResult.Added)
	totalUpdated := len(fragResult.Updated) + len(promptResult.Updated) + len(configResult.Updated)
	totalUnchanged := len(fragResult.Unchanged) + len(promptResult.Unchanged) + len(configResult.Unchanged)
	totalSkipped := len(fragResult.Skipped) + len(promptResult.Skipped) + len(configResult.Skipped)

	fmt.Printf("\nCopy complete: %d added, %d updated", totalAdded, totalUpdated)
	if totalUnchanged > 0 {
		fmt.Printf(", %d unchanged", totalUnchanged)
	}
	if totalSkipped > 0 {
		fmt.Printf(", %d skipped", totalSkipped)
	}
	fmt.Println()

	// Initialize home directory as git repo if copying to home
	if to.Type == LocationHome {
		if err := ensureHomeGitRepo(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to initialize git repo: %v\n", err)
		}
	}

	return nil
}

func getLocationPaths(loc ParsedLocation) (fragDir, promptDir string, err error) {
	switch loc.Type {
	case LocationResources:
		// For dev mode, resources are in the current directory
		// When reading, resources are embedded (handled specially)
		// When writing (dev mode), use the resources directory in pwd
		pwd, err := os.Getwd()
		if err != nil {
			return "", "", err
		}
		return filepath.Join(pwd, "resources", "context-fragments"),
			filepath.Join(pwd, "resources", "prompts"), nil

	case LocationHome:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", err
		}
		scmDir := filepath.Join(home, config.SCMDirName)
		return filepath.Join(scmDir, config.ContextFragmentsDir),
			filepath.Join(scmDir, config.PromptsDir), nil

	case LocationProject:
		rootDir := findGitRoot()
		if rootDir == "" {
			rootDir, err = os.Getwd()
			if err != nil {
				return "", "", err
			}
		}
		scmDir := filepath.Join(rootDir, config.SCMDirName)
		return filepath.Join(scmDir, config.ContextFragmentsDir),
			filepath.Join(scmDir, config.PromptsDir), nil

	case LocationPath:
		scmDir := filepath.Join(loc.Path, config.SCMDirName)
		return filepath.Join(scmDir, config.ContextFragmentsDir),
			filepath.Join(scmDir, config.PromptsDir), nil

	default:
		return "", "", fmt.Errorf("unknown location")
	}
}

func getConfigPath(loc ParsedLocation) (string, error) {
	switch loc.Type {
	case LocationResources:
		// For dev mode, return path in resources directory
		pwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(pwd, "resources", "config.yaml"), nil

	case LocationHome:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, config.SCMDirName, "config.yaml"), nil

	case LocationProject:
		rootDir := findGitRoot()
		if rootDir == "" {
			var err error
			rootDir, err = os.Getwd()
			if err != nil {
				return "", err
			}
		}
		return filepath.Join(rootDir, config.SCMDirName, "config.yaml"), nil

	case LocationPath:
		return filepath.Join(loc.Path, config.SCMDirName, "config.yaml"), nil

	default:
		return "", fmt.Errorf("unknown location")
	}
}

// copyConfigWithOptions copies config.yaml between locations.
// Note: addHeader is intentionally ignored - config is user-editable and should not have DO NOT EDIT header.
func copyConfigWithOptions(from ParsedLocation, to Location, _ /* addHeader */, removeHeader bool) (*CopyResult, error) {
	result := &CopyResult{}

	// Get destination path
	dstPath, err := getConfigPath(ParsedLocation{Type: to})
	if err != nil {
		return nil, err
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return nil, err
	}

	// Get source data
	var data []byte
	if from.Type == LocationResources {
		data, err = resources.GetEmbeddedConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to read embedded config: %w", err)
		}
	} else {
		srcPath, err := getConfigPath(from)
		if err != nil {
			return nil, err
		}
		data, err = os.ReadFile(srcPath)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "Warning: no config.yaml at %s\n", srcPath)
				return result, nil
			}
			return nil, fmt.Errorf("failed to read config: %w", err)
		}
	}

	// Handle header transformations
	if removeHeader {
		data = stripHeader(data)
	}

	// Config is user-editable, never add DO NOT EDIT header
	status, err := copyDataToFile(data, dstPath, false, false)
	if err != nil {
		return nil, err
	}

	appendByStatus(result, "config.yaml", status)
	return result, nil
}

func buildFragmentFilter() ([]string, error) {
	var filter []string

	// Add explicit fragments
	filter = append(filter, copyFragments...)

	// Add fragments from profiles
	if len(copyProfiles) > 0 {
		embeddedCfg, err := config.LoadEmbeddedConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to load config: %w", err)
		}

		homeCfg, _ := config.LoadHomeConfig()
		allProfiles := make(map[string]config.Profile)
		config.MergeProfiles(allProfiles, embeddedCfg.Profiles)
		if homeCfg != nil {
			config.MergeProfiles(allProfiles, homeCfg.Profiles)
		}

		profileFrags, err := config.CollectFragmentsForProfiles(allProfiles, copyProfiles)
		if err != nil {
			return nil, err
		}
		filter = append(filter, profileFrags...)
	}

	return filter, nil
}

func copyFragmentsWithOptions(from Location, srcDir, dstDir string, filter []string, addHeader, removeHeader bool) (*CopyResult, error) {
	// Ensure destination exists
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return nil, err
	}

	if from == LocationResources {
		return copyFragmentsFromResources(dstDir, filter, addHeader)
	}

	return copyFragmentsFromDir(srcDir, dstDir, filter, addHeader, removeHeader)
}

func copyFragmentsFromResources(dstDir string, filter []string, addHeader bool) (*CopyResult, error) {
	result := &CopyResult{}

	validator, err := schema.NewValidator()
	if err != nil {
		return nil, fmt.Errorf("failed to create validator: %w", err)
	}

	// Build filter sets
	allowed := buildFilterSet(filter)
	tagFilter := buildTagSet(copyTags)

	err = fs.WalkDir(resources.FragmentsFS(), "context-fragments", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, err := filepath.Rel("context-fragments", path)
		if err != nil {
			return err
		}

		if relPath == "." {
			return nil
		}

		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dstDir, relPath), 0755)
		}

		name := d.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			return nil
		}

		data, err := resources.FragmentsFS().ReadFile(path)
		if err != nil {
			return err
		}

		// Check filters
		baseName := strings.TrimSuffix(relPath, filepath.Ext(relPath))
		if !matchesFilters(baseName, data, allowed, tagFilter) {
			return nil
		}

		// Validate
		if err := validator.ValidateBytes(data); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: skipping invalid fragment %s: %v\n", path, err)
			result.Skipped = append(result.Skipped, relPath)
			return nil
		}

		dstPath := filepath.Join(dstDir, relPath)
		status, err := copyDataToFile(data, dstPath, addHeader, false)
		if err != nil {
			return err
		}

		appendByStatus(result, relPath, status)
		return nil
	})

	return result, err
}

func copyFragmentsFromDir(srcDir, dstDir string, filter []string, addHeader, removeHeader bool) (*CopyResult, error) {
	result := &CopyResult{}

	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		return result, nil // Source doesn't exist, nothing to copy
	}

	validator, err := schema.NewValidator()
	if err != nil {
		return nil, fmt.Errorf("failed to create validator: %w", err)
	}

	allowed := buildFilterSet(filter)
	tagFilter := buildTagSet(copyTags)

	err = filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}

		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dstDir, relPath), 0755)
		}

		name := d.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cannot read %s: %v\n", path, err)
			return nil
		}

		// Check filters
		baseName := strings.TrimSuffix(relPath, filepath.Ext(relPath))
		if !matchesFilters(baseName, data, allowed, tagFilter) {
			return nil
		}

		// Validate
		cleanData := data
		if removeHeader {
			cleanData = stripHeader(data)
		}
		if err := validator.ValidateBytes(cleanData); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: skipping invalid fragment %s: %v\n", path, err)
			result.Skipped = append(result.Skipped, relPath)
			return nil
		}

		dstPath := filepath.Join(dstDir, relPath)
		status, err := copyDataToFile(cleanData, dstPath, addHeader, false)
		if err != nil {
			return err
		}

		appendByStatus(result, relPath, status)
		return nil
	})

	return result, err
}

func copyPromptsWithOptions(from Location, srcDir, dstDir string, filter []string, addHeader, removeHeader bool) (*CopyResult, error) {
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return nil, err
	}

	if from == LocationResources {
		return copyPromptsFromResources(dstDir, filter, addHeader)
	}

	return copyPromptsFromDir(srcDir, dstDir, filter, addHeader, removeHeader)
}

func copyPromptsFromResources(dstDir string, filter []string, addHeader bool) (*CopyResult, error) {
	result := &CopyResult{}

	allowed := buildFilterSet(filter)

	err := fs.WalkDir(resources.PromptsFS(), "prompts", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, err := filepath.Rel("prompts", path)
		if err != nil {
			return err
		}

		if relPath == "." {
			return nil
		}

		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dstDir, relPath), 0755)
		}

		name := d.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			return nil
		}

		// Check filter
		baseName := strings.TrimSuffix(relPath, filepath.Ext(relPath))
		if allowed != nil && !allowed[baseName] {
			return nil
		}

		data, err := resources.PromptsFS().ReadFile(path)
		if err != nil {
			return err
		}

		dstPath := filepath.Join(dstDir, relPath)
		status, err := copyDataToFile(data, dstPath, addHeader, false)
		if err != nil {
			return err
		}

		appendByStatus(result, relPath, status)
		return nil
	})

	return result, err
}

func copyPromptsFromDir(srcDir, dstDir string, filter []string, addHeader, removeHeader bool) (*CopyResult, error) {
	result := &CopyResult{}

	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		return result, nil
	}

	allowed := buildFilterSet(filter)

	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}

		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dstDir, relPath), 0755)
		}

		name := d.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			return nil
		}

		baseName := strings.TrimSuffix(relPath, filepath.Ext(relPath))
		if allowed != nil && !allowed[baseName] {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cannot read %s: %v\n", path, err)
			return nil
		}

		cleanData := data
		if removeHeader {
			cleanData = stripHeader(data)
		}

		dstPath := filepath.Join(dstDir, relPath)
		status, err := copyDataToFile(cleanData, dstPath, addHeader, false)
		if err != nil {
			return err
		}

		appendByStatus(result, relPath, status)
		return nil
	})

	return result, err
}

// copyStatus indicates what happened when copying a file.
type copyStatus int

const (
	copyStatusAdded copyStatus = iota
	copyStatusUpdated
	copyStatusUnchanged
	copyStatusSkipped
)

func copyDataToFile(data []byte, dstPath string, addHeader, removeHeader bool) (copyStatus, error) {
	finalData := data

	if removeHeader {
		finalData = stripHeader(finalData)
	}

	if addHeader && (strings.HasSuffix(dstPath, ".yaml") || strings.HasSuffix(dstPath, ".yml")) {
		finalData = append([]byte(resources.ProjectHeader), finalData...)
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return 0, err
	}

	// Check if destination exists
	existing, err := os.ReadFile(dstPath)
	if err == nil {
		// File exists
		if bytes.Equal(existing, finalData) {
			return copyStatusUnchanged, nil
		}
		if !copyForce {
			return copyStatusSkipped, nil
		}
		// Force overwrite
		if err := fsys.WriteProtected(dstPath, finalData); err != nil {
			return 0, err
		}
		return copyStatusUpdated, nil
	}

	// File doesn't exist, create it
	if err := fsys.WriteProtected(dstPath, finalData); err != nil {
		return 0, err
	}
	return copyStatusAdded, nil
}

// stripHeader removes the project header from file content.
func stripHeader(data []byte) []byte {
	header := []byte(resources.ProjectHeader)
	if bytes.HasPrefix(data, header) {
		return data[len(header):]
	}
	return data
}

func buildFilterSet(filter []string) map[string]bool {
	if len(filter) == 0 {
		return nil
	}
	allowed := make(map[string]bool)
	for _, f := range filter {
		f = strings.TrimSuffix(f, ".yaml")
		f = strings.TrimSuffix(f, ".yml")
		allowed[f] = true
	}
	return allowed
}

func buildTagSet(tags []string) map[string]bool {
	if len(tags) == 0 {
		return nil
	}
	tagSet := make(map[string]bool)
	for _, t := range tags {
		tagSet[strings.ToLower(t)] = true
	}
	return tagSet
}

func matchesFilters(baseName string, data []byte, allowed, tagFilter map[string]bool) bool {
	// If no filters, include everything
	if allowed == nil && tagFilter == nil {
		return true
	}

	// Check name filter
	if allowed != nil && allowed[baseName] {
		return true
	}

	// Check tag filter
	if tagFilter != nil {
		tags := extractTags(data)
		for _, tag := range tags {
			if tagFilter[strings.ToLower(tag)] {
				return true
			}
		}
	}

	// Filters are specified but nothing matched - exclude
	return false
}

func extractTags(data []byte) []string {
	// Simple YAML tag extraction
	var fragment struct {
		Tags []string `yaml:"tags"`
	}
	if err := yaml.Unmarshal(data, &fragment); err != nil {
		// Log parse errors but continue - treat as having no tags
		if copyVerbose {
			fmt.Fprintf(os.Stderr, "Warning: failed to parse YAML for tag extraction: %v\n", err)
		}
		return nil
	}
	return fragment.Tags
}

func appendByStatus(result *CopyResult, path string, status copyStatus) {
	switch status {
	case copyStatusAdded:
		result.Added = append(result.Added, path)
	case copyStatusUpdated:
		result.Updated = append(result.Updated, path)
	case copyStatusUnchanged:
		result.Unchanged = append(result.Unchanged, path)
	case copyStatusSkipped:
		result.Skipped = append(result.Skipped, path)
	}
}

func printCopyResultVerbose(result *CopyResult, kind string, verbose bool) {
	if result.Total() == 0 && len(result.Skipped) == 0 {
		fmt.Printf("  No %s to copy\n", kind)
		return
	}

	if len(result.Added) > 0 {
		fmt.Printf("  + Added: %d %s\n", len(result.Added), kind)
		if verbose {
			for _, f := range result.Added {
				fmt.Printf("      %s\n", f)
			}
		}
	}
	if len(result.Updated) > 0 {
		fmt.Printf("  ~ Updated: %d %s\n", len(result.Updated), kind)
		if verbose {
			for _, f := range result.Updated {
				fmt.Printf("      %s\n", f)
			}
		}
	}
	if len(result.Unchanged) > 0 && verbose {
		fmt.Printf("  = Unchanged: %d %s\n", len(result.Unchanged), kind)
	}
	if len(result.Skipped) > 0 {
		fmt.Printf("  ! Skipped: %d %s (use --force to overwrite)\n", len(result.Skipped), kind)
		if verbose {
			for _, f := range result.Skipped {
				fmt.Printf("      %s\n", f)
			}
		}
	}
}

// findGitRoot returns the root of the git repository by walking up looking for .git.
func findGitRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	root, err := gitutil.FindRoot(dir)
	if err != nil {
		return ""
	}
	return root
}

// ensureHomeGitRepo initializes ~/.scm as a git repository if it isn't already.
func ensureHomeGitRepo() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	scmDir := filepath.Join(home, config.SCMDirName)
	gitDir := filepath.Join(scmDir, ".git")

	// Check if already a git repo
	if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
		return nil
	}

	// Initialize git repo
	fmt.Printf("Initializing %s as git repository...\n", scmDir)

	cmd := exec.Command("git", "init")
	cmd.Dir = scmDir
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init failed: %w\n%s", err, output)
	}


	return nil
}

func init() {
	rootCmd.AddCommand(copyCmd)

	copyCmd.Flags().BoolVar(&copyForce, "force", false, "Overwrite existing files")
	copyCmd.Flags().BoolVar(&copyClear, "clear", false, "Clear destination .scm directory before copying (destroys customizations)")
	copyCmd.Flags().StringArrayVarP(&copyFragments, "fragment", "f", nil, "Fragment(s) to copy")
	copyCmd.Flags().StringArrayVarP(&copyTags, "tag", "t", nil, "Copy fragments with these tags")
	copyCmd.Flags().StringArrayVarP(&copyPrompts, "prompt", "r", nil, "Prompt(s) to copy")
	copyCmd.Flags().StringArrayVarP(&copyProfiles, "profile", "p", nil, "Copy fragments for these profiles")
	copyCmd.Flags().BoolVarP(&copyVerbose, "verbose", "v", false, "List individual files")

	copyCmd.Flags().BoolVar(&copyDev, "dev", false, "Dev mode: allow copying to resources directory (for scm development)")
	copyCmd.Flags().BoolVar(&copyConfig, "include-config", true, "Include config.yaml in copy (use --include-config=false to skip)")
}
