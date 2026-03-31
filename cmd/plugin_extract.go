package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

var pluginExtractOutput string

var pluginExtractCmd = &cobra.Command{
	Use:   "extract",
	Short: "Extract built-in plugins as standalone binaries",
	Long: `Extracts built-in AI backend plugins as standalone binary files.

These standalone plugins can be used for debugging, external deployment,
or with other tools that support the gRPC plugin protocol.

Examples:
  ctxloom plugin extract                     # Extract to .ctxloom/plugins/
  ctxloom plugin extract --output ./plugins  # Extract to ./plugins/`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Determine output directory
		outputDir := pluginExtractOutput
		if outputDir == "" {
			pwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get working directory: %w", err)
			}
			outputDir = filepath.Join(pwd, ".ctxloom", "plugins")
		}

		// Create output directory
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return fmt.Errorf("failed to create output directory: %w", err)
		}

		// Get the current executable path
		selfPath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("failed to get executable path: %w", err)
		}

		// Get list of built-in plugins
		pluginNames := backends.List()

		if len(pluginNames) == 0 {
			fmt.Println("No built-in plugins to extract.")
			return nil
		}

		fmt.Printf("Extracting %d plugin(s) to %s\n", len(pluginNames), outputDir)

		for _, name := range pluginNames {
			outputPath := filepath.Join(outputDir, fmt.Sprintf("scm-plugin-%s", name))

			// Create a wrapper script that invokes ctxloom plugin serve
			script := fmt.Sprintf(`#!/bin/sh
exec "%s" plugin serve %s "$@"
`, selfPath, name)

			if err := os.WriteFile(outputPath, []byte(script), 0755); err != nil {
				return fmt.Errorf("failed to write plugin wrapper for %s: %w", name, err)
			}

			fmt.Printf("  Extracted: %s\n", name)
		}

		fmt.Println("\nPlugins extracted successfully.")
		fmt.Println("Add this directory to your config's plugin_paths to use them.")

		return nil
	},
}

var pluginExtractBinaryCmd = &cobra.Command{
	Use:    "extract-binary",
	Short:  "Extract plugins as compiled binaries (requires go)",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		// This is for extracting actual compiled binaries
		// Requires go toolchain to be installed

		outputDir := pluginExtractOutput
		if outputDir == "" {
			pwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get working directory: %w", err)
			}
			outputDir = filepath.Join(pwd, ".ctxloom", "plugins")
		}

		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return fmt.Errorf("failed to create output directory: %w", err)
		}

		// Check if go is available
		if _, err := exec.LookPath("go"); err != nil {
			return fmt.Errorf("go toolchain required for binary extraction: %w", err)
		}

		fmt.Printf("Binary extraction to %s requires go build - use regular extract for shell wrappers\n", outputDir)
		return nil
	},
}

func init() {
	pluginCmd.AddCommand(pluginExtractCmd)
	pluginCmd.AddCommand(pluginExtractBinaryCmd)

	pluginExtractCmd.Flags().StringVarP(&pluginExtractOutput, "output", "o", "", "Output directory (default: .ctxloom/plugins/)")
	pluginExtractBinaryCmd.Flags().StringVarP(&pluginExtractOutput, "output", "o", "", "Output directory (default: .ctxloom/plugins/)")
}
