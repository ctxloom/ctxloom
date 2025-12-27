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

	"mlcm/internal/config"
	"mlcm/internal/fsys"
	"mlcm/internal/gitutil"
	"mlcm/internal/schema"
	"mlcm/resources"
)

var (
	copyFrom      string
	copyTo        string
	copyForce     bool
	copyFragments []string
	copyTags      []string
	copyPrompts   []string
	copyPersonas  []string
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
)

func parseLocation(s string) (Location, error) {
	switch strings.ToLower(s) {
	case "resources", "r":
		return LocationResources, nil
	case "home", "h":
		return LocationHome, nil
	case "project", "p":
		return LocationProject, nil
	default:
		return 0, fmt.Errorf("invalid location %q: must be resources, home, or project", s)
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
	Use:   "copy",
	Short: "Copy fragments and prompts between locations",
	Long: `Copy context fragments and prompts between resources, home, and project.

Locations:
  resources (r)  - Embedded default fragments and prompts
  home (h)       - ~/.mlcm directory
  project (p)    - .mlcm directory in the current project

Header behavior:
  - Copying TO project: adds a "DO NOT EDIT" header to files
  - Copying FROM project: removes the header from files

Examples:
  # Copy all embedded fragments to project
  mlcm copy --from resources --to project

  # Copy specific fragments from home to project
  mlcm copy --from home --to project -f security -f golang

  # Copy fragments with specific tags
  mlcm copy --from resources --to home -t review

  # Copy prompts from resources to project
  mlcm copy --from resources --to project -p code-review

  # Force overwrite existing files
  mlcm copy --from resources --to project --force

  # Copy fragments for specific personas
  mlcm copy --from resources --to project --persona go-developer`,
	RunE: runCopy,
}

func runCopy(cmd *cobra.Command, args []string) error {
	from, err := parseLocation(copyFrom)
	if err != nil {
		return err
	}

	to, err := parseLocation(copyTo)
	if err != nil {
		return err
	}

	if from == to {
		return fmt.Errorf("source and destination cannot be the same")
	}

	// Resources cannot be a destination unless in dev mode
	if to == LocationResources && !copyDev {
		return fmt.Errorf("cannot copy to resources (use --dev flag when working on mlcm itself)")
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

	// Determine header behavior
	addHeader := to == LocationProject
	removeHeader := from == LocationProject

	// Build fragment filter from flags
	fragmentFilter, err := buildFragmentFilter()
	if err != nil {
		return err
	}

	// Copy fragments
	fragResult := &CopyResult{}
	if len(copyPrompts) == 0 || len(copyFragments) > 0 || len(copyTags) > 0 || len(copyPersonas) > 0 {
		fmt.Printf("Copying fragments from %s to %s\n", from, to)

		result, err := copyFragmentsWithOptions(from, srcFragDir, dstFragDir, fragmentFilter, addHeader, removeHeader)
		if err != nil {
			return fmt.Errorf("failed to copy fragments: %w", err)
		}
		fragResult.Merge(result)
		printCopyResultVerbose(fragResult, "fragments", copyVerbose)
	}

	// Copy prompts
	promptResult := &CopyResult{}
	if len(copyPrompts) > 0 || (len(copyFragments) == 0 && len(copyTags) == 0 && len(copyPersonas) == 0) {
		fmt.Printf("Copying prompts from %s to %s\n", from, to)

		result, err := copyPromptsWithOptions(from, srcPromptDir, dstPromptDir, copyPrompts, addHeader, removeHeader)
		if err != nil {
			return fmt.Errorf("failed to copy prompts: %w", err)
		}
		promptResult.Merge(result)
		printCopyResultVerbose(promptResult, "prompts", copyVerbose)
	}

	// Copy config if requested
	configResult := &CopyResult{}
	if copyConfig {
		fmt.Printf("Copying config from %s to %s\n", from, to)

		result, err := copyConfigWithOptions(from, to, addHeader, removeHeader)
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
	if to == LocationHome {
		if err := ensureHomeGitRepo(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to initialize git repo: %v\n", err)
		}
	}

	return nil
}

func getLocationPaths(loc Location) (fragDir, promptDir string, err error) {
	switch loc {
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
		mlcmDir := filepath.Join(home, config.MLCMDirName)
		return filepath.Join(mlcmDir, config.ContextFragmentsDir),
			filepath.Join(mlcmDir, config.PromptsDir), nil

	case LocationProject:
		rootDir := findGitRoot()
		if rootDir == "" {
			rootDir, err = os.Getwd()
			if err != nil {
				return "", "", err
			}
		}
		mlcmDir := filepath.Join(rootDir, config.MLCMDirName)
		return filepath.Join(mlcmDir, config.ContextFragmentsDir),
			filepath.Join(mlcmDir, config.PromptsDir), nil

	default:
		return "", "", fmt.Errorf("unknown location")
	}
}

func getConfigPath(loc Location) (string, error) {
	switch loc {
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
		return filepath.Join(home, config.MLCMDirName, "config.yaml"), nil

	case LocationProject:
		rootDir := findGitRoot()
		if rootDir == "" {
			var err error
			rootDir, err = os.Getwd()
			if err != nil {
				return "", err
			}
		}
		return filepath.Join(rootDir, config.MLCMDirName, "config.yaml"), nil

	default:
		return "", fmt.Errorf("unknown location")
	}
}

func copyConfigWithOptions(from, to Location, _, removeHeader bool) (*CopyResult, error) {
	result := &CopyResult{}

	// Get destination path
	dstPath, err := getConfigPath(to)
	if err != nil {
		return nil, err
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return nil, err
	}

	// Get source data
	var data []byte
	if from == LocationResources {
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

	// Add fragments from personas
	if len(copyPersonas) > 0 {
		embeddedCfg, err := config.LoadEmbeddedConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to load config: %w", err)
		}

		homeCfg, _ := config.LoadHomeConfig()
		allPersonas := make(map[string]config.Persona)
		config.MergePersonas(allPersonas, embeddedCfg.Personas)
		if homeCfg != nil {
			config.MergePersonas(allPersonas, homeCfg.Personas)
		}

		personaFrags, err := config.CollectFragmentsForPersonas(allPersonas, copyPersonas)
		if err != nil {
			return nil, err
		}
		filter = append(filter, personaFrags...)
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

	// If filters are specified but nothing matched, exclude
	return allowed == nil && tagFilter == nil
}

func extractTags(data []byte) []string {
	// Simple YAML tag extraction
	var fragment struct {
		Tags []string `yaml:"tags"`
	}
	// Use yaml.Unmarshal but ignore errors - just return empty tags
	_ = yaml.Unmarshal(data, &fragment)
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

// ensureHomeGitRepo initializes ~/.mlcm as a git repository if it isn't already.
func ensureHomeGitRepo() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	mlcmDir := filepath.Join(home, config.MLCMDirName)
	gitDir := filepath.Join(mlcmDir, ".git")

	// Check if already a git repo
	if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
		return nil
	}

	// Initialize git repo
	fmt.Printf("Initializing %s as git repository...\n", mlcmDir)

	cmd := exec.Command("git", "init")
	cmd.Dir = mlcmDir
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init failed: %w\n%s", err, output)
	}


	return nil
}

func init() {
	rootCmd.AddCommand(copyCmd)

	copyCmd.Flags().StringVar(&copyFrom, "from", "", "Source location: resources (r), home (h), or project (p)")
	copyCmd.Flags().StringVar(&copyTo, "to", "", "Destination location: home (h) or project (p)")
	copyCmd.Flags().BoolVar(&copyForce, "force", false, "Overwrite existing files")
	copyCmd.Flags().StringArrayVarP(&copyFragments, "fragment", "f", nil, "Fragment(s) to copy")
	copyCmd.Flags().StringArrayVarP(&copyTags, "tag", "t", nil, "Copy fragments with these tags")
	copyCmd.Flags().StringArrayVarP(&copyPrompts, "prompt", "p", nil, "Prompt(s) to copy")
	copyCmd.Flags().StringArrayVarP(&copyPersonas, "persona", "P", nil, "Copy fragments for these personas")
	copyCmd.Flags().BoolVarP(&copyVerbose, "verbose", "v", false, "List individual files")

	copyCmd.Flags().BoolVar(&copyDev, "dev", false, "Dev mode: allow copying to resources directory (for mlcm development)")
	copyCmd.Flags().BoolVar(&copyConfig, "include-config", true, "Include config.yaml in copy (use --include-config=false to skip)")

	copyCmd.MarkFlagRequired("from")
	copyCmd.MarkFlagRequired("to")
}
