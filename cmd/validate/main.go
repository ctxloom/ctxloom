// validate checks that config YAML files conform to their JSON schemas.
// Run before build to catch schema violations early.
package main

import (
	"fmt"
	"os"

	"github.com/SophisticatedContextManager/scm/internal/schema"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "validation failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	configValidator, err := schema.NewConfigValidator()
	if err != nil {
		return err
	}

	var validated int

	// Validate any config.yaml files if they exist
	configPaths := []string{
		".scm/config.yaml",
	}

	for _, configPath := range configPaths {
		if data, err := os.ReadFile(configPath); err == nil {
			if err := configValidator.ValidateBytes(data); err != nil {
				return fmt.Errorf("schema validation error in %s: %w", configPath, err)
			}
			validated++
		}
	}

	if validated == 0 {
		fmt.Println("No files found to validate")
	} else {
		fmt.Printf("Validated %d files against schema\n", validated)
	}
	return nil
}
