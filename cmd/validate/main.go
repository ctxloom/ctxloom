// validate checks that all fragment YAML files conform to the JSON schema.
// Run before build to catch schema violations early.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"mlcm/internal/schema"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "validation failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	validator, err := schema.NewValidator()
	if err != nil {
		return err
	}

	var errors []string
	var validated int

	// Validate resources/context-fragments
	fragmentsDir := "resources/context-fragments"
	err = filepath.WalkDir(fragmentsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.Contains(path, "standards/") {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			errors = append(errors, fmt.Sprintf("  %s: %v", path, err))
			return nil
		}

		if err := validator.ValidateBytes(data); err != nil {
			errors = append(errors, fmt.Sprintf("  %s: %v", path, err))
		} else {
			validated++
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to walk fragments: %w", err)
	}

	// Validate resources/prompts
	promptsDir := "resources/prompts"
	if _, err := os.Stat(promptsDir); err == nil {
		err = filepath.WalkDir(promptsDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			name := d.Name()
			if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
				return nil
			}

			data, err := os.ReadFile(path)
			if err != nil {
				errors = append(errors, fmt.Sprintf("  %s: %v", path, err))
				return nil
			}

			if err := validator.ValidateBytes(data); err != nil {
				errors = append(errors, fmt.Sprintf("  %s: %v", path, err))
			} else {
				validated++
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("failed to walk prompts: %w", err)
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("schema validation errors:\n%s", strings.Join(errors, "\n"))
	}

	fmt.Printf("Validated %d files against schema\n", validated)
	return nil
}
