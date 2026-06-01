package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/profiles"
)

// editProfileFile opens a profile's YAML file in the editor.
func editProfileFile(name string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	profileDirs := profiles.GetProfileDirs(cfg.AppPaths)
	if len(profileDirs) == 0 {
		return fmt.Errorf("no profiles directory found")
	}

	loader := profiles.NewLoader(profileDirs)
	profile, err := loader.Load(name)
	if err != nil {
		return fmt.Errorf("profile not found: %s", name)
	}

	// Read current content
	content, err := os.ReadFile(profile.Path)
	if err != nil {
		return fmt.Errorf("failed to read profile: %w", err)
	}

	// Edit in editor
	newContent, err := editInEditor(cfg, string(content), filepath.Base(profile.Path))
	if err != nil {
		return fmt.Errorf("editor failed: %w", err)
	}

	if newContent == string(content) {
		fmt.Println("No changes made.")
		return nil
	}

	// Write back
	if err := os.WriteFile(profile.Path, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("failed to save profile: %w", err)
	}

	fmt.Printf("Updated profile: %s\n", profile.Path)
	return nil
}

// printPushReminder prints a reminder to push a modified bundle, using the
// bundle reference the user supplied to the edit command.
func printPushReminder(bundleRef string) {
	fmt.Println()
	fmt.Println("Bundle modified. To publish changes:")
	fmt.Printf("  ctxloom bundle push %s [remote]\n", bundleRef)
}
