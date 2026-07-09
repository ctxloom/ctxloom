package cli

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// editProfileFile opens a profile's YAML file in the editor, then writes the
// edited content back through the operations core (which validates and saves).
// The $EDITOR round-trip is the only CLI-specific part.
func editProfileFile(name string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	cur, err := operations.GetProfileContent(context.Background(), cfg, operations.GetProfileContentRequest{Name: name})
	if err != nil {
		return err
	}

	newContent, err := editInEditor(cfg, cur.Content, filepath.Base(cur.Path))
	if err != nil {
		return fmt.Errorf("editor failed: %w", err)
	}
	if newContent == cur.Content {
		fmt.Println("No changes made.")
		return nil
	}

	if _, err := operations.SetProfileContent(context.Background(), cfg, operations.SetProfileContentRequest{Name: name, Content: newContent}); err != nil {
		return err
	}

	fmt.Printf("Updated profile: %s\n", cur.Path)
	return nil
}

// printPushReminder prints a reminder to push a modified bundle, using the
// bundle reference the user supplied to the edit command.
func printPushReminder(bundleRef string) {
	fmt.Println()
	fmt.Println("Bundle modified. To publish changes:")
	fmt.Printf("  ctxloom bundle push %s [remote]\n", bundleRef)
}
