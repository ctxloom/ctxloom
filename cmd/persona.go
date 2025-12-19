package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"mlcm/internal/config"
)

var personaCmd = &cobra.Command{
	Use:   "persona",
	Short: "Manage personas (named fragment collections)",
	Long: `Manage personas - named collections of context fragments.

Personas allow you to quickly switch between different sets of context
fragments without specifying them individually each time.`,
}

var personaListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all personas",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if len(cfg.Personas) == 0 {
			fmt.Println("No personas defined.")
			fmt.Println("Use 'mlcm persona add <name> -f <fragments...>' to create one.")
			return nil
		}

		// Sort persona names
		names := make([]string, 0, len(cfg.Personas))
		for name := range cfg.Personas {
			names = append(names, name)
		}
		sort.Strings(names)

		fmt.Println("Personas:")
		for _, name := range names {
			p := cfg.Personas[name]
			fmt.Printf("  %s\n", name)
			if p.Description != "" {
				fmt.Printf("    Description: %s\n", p.Description)
			}
			if len(p.Fragments) > 0 {
				fmt.Printf("    Fragments: %s\n", strings.Join(p.Fragments, ", "))
			}
			if len(p.Generators) > 0 {
				fmt.Printf("    Generators: %s\n", strings.Join(p.Generators, ", "))
			}
		}

		return nil
	},
}

var (
	personaAddFragments   []string
	personaAddGenerators  []string
	personaAddDescription string
)

var personaAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add a new persona",
	Long: `Add a new persona with the specified fragments and optional generators.

Example:
  mlcm persona add developer -f coding-standards -f go-patterns -d "Standard dev context"
  mlcm persona add git-aware -f test-fragment -g mlcm-gen-git-context -d "Context with git info"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		if len(personaAddFragments) == 0 && len(personaAddGenerators) == 0 {
			return fmt.Errorf("at least one fragment (-f) or generator (-g) is required")
		}

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if _, exists := cfg.Personas[name]; exists {
			return fmt.Errorf("persona %q already exists (use 'persona remove' first)", name)
		}

		cfg.Personas[name] = config.Persona{
			Description: personaAddDescription,
			Fragments:   personaAddFragments,
			Generators:  personaAddGenerators,
		}

		if err := cfg.Save(); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		var parts []string
		if len(personaAddFragments) > 0 {
			parts = append(parts, fmt.Sprintf("fragments: %s", strings.Join(personaAddFragments, ", ")))
		}
		if len(personaAddGenerators) > 0 {
			parts = append(parts, fmt.Sprintf("generators: %s", strings.Join(personaAddGenerators, ", ")))
		}
		fmt.Printf("Added persona %q with %s\n", name, strings.Join(parts, "; "))
		return nil
	},
}

var personaRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm"},
	Short:   "Remove a persona",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if _, exists := cfg.Personas[name]; !exists {
			return fmt.Errorf("persona %q not found", name)
		}

		delete(cfg.Personas, name)

		if err := cfg.Save(); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		fmt.Printf("Removed persona %q\n", name)
		return nil
	},
}

var personaShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show details of a persona",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		p, exists := cfg.Personas[name]
		if !exists {
			return fmt.Errorf("persona %q not found", name)
		}

		fmt.Printf("Persona: %s\n", name)
		if p.Description != "" {
			fmt.Printf("Description: %s\n", p.Description)
		}
		if len(p.Fragments) > 0 {
			fmt.Println("Fragments:")
			for _, f := range p.Fragments {
				fmt.Printf("  - %s\n", f)
			}
		}
		if len(p.Generators) > 0 {
			fmt.Println("Generators:")
			for _, g := range p.Generators {
				fmt.Printf("  - %s\n", g)
			}
		}
		if len(p.Variables) > 0 {
			fmt.Println("Variables:")
			for k, v := range p.Variables {
				fmt.Printf("  %s: %s\n", k, v)
			}
		}

		return nil
	},
}

var personaUpdateCmd = &cobra.Command{
	Use:   "update <name>",
	Short: "Update a persona",
	Long: `Update an existing persona by adding or removing fragments/generators.

Examples:
  mlcm persona update developer --add-fragment error-handling
  mlcm persona update developer --remove-fragment old-patterns
  mlcm persona update git-aware --add-generator git-context
  mlcm persona update git-aware --remove-generator old-gen
  mlcm persona update developer -d "New description"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		p, exists := cfg.Personas[name]
		if !exists {
			return fmt.Errorf("persona %q not found", name)
		}

		modified := false

		// Update description if provided
		if cmd.Flags().Changed("description") {
			p.Description = personaUpdateDescription
			modified = true
		}

		// Add fragments
		for _, f := range personaUpdateAddFragments {
			if !contains(p.Fragments, f) {
				p.Fragments = append(p.Fragments, f)
				fmt.Printf("Added fragment: %s\n", f)
				modified = true
			} else {
				fmt.Printf("Fragment already present: %s\n", f)
			}
		}

		// Remove fragments
		for _, f := range personaUpdateRemoveFragments {
			if idx := indexOf(p.Fragments, f); idx >= 0 {
				p.Fragments = append(p.Fragments[:idx], p.Fragments[idx+1:]...)
				fmt.Printf("Removed fragment: %s\n", f)
				modified = true
			} else {
				fmt.Printf("Fragment not found: %s\n", f)
			}
		}

		// Add generators
		for _, g := range personaUpdateAddGenerators {
			if !contains(p.Generators, g) {
				p.Generators = append(p.Generators, g)
				fmt.Printf("Added generator: %s\n", g)
				modified = true
			} else {
				fmt.Printf("Generator already present: %s\n", g)
			}
		}

		// Remove generators
		for _, g := range personaUpdateRemoveGenerators {
			if idx := indexOf(p.Generators, g); idx >= 0 {
				p.Generators = append(p.Generators[:idx], p.Generators[idx+1:]...)
				fmt.Printf("Removed generator: %s\n", g)
				modified = true
			} else {
				fmt.Printf("Generator not found: %s\n", g)
			}
		}

		if !modified {
			fmt.Println("No changes made.")
			return nil
		}

		cfg.Personas[name] = p
		if err := cfg.Save(); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		fmt.Printf("Updated persona %q\n", name)
		return nil
	},
}

var (
	personaUpdateAddFragments     []string
	personaUpdateRemoveFragments  []string
	personaUpdateAddGenerators    []string
	personaUpdateRemoveGenerators []string
	personaUpdateDescription      string
)

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func indexOf(slice []string, item string) int {
	for i, s := range slice {
		if s == item {
			return i
		}
	}
	return -1
}

func init() {
	rootCmd.AddCommand(personaCmd)

	personaCmd.AddCommand(personaListCmd)
	personaCmd.AddCommand(personaAddCmd)
	personaCmd.AddCommand(personaRemoveCmd)
	personaCmd.AddCommand(personaShowCmd)
	personaCmd.AddCommand(personaUpdateCmd)

	personaAddCmd.Flags().StringSliceVarP(&personaAddFragments, "fragment", "f", nil, "Fragment(s) to include (can be repeated)")
	personaAddCmd.Flags().StringSliceVarP(&personaAddGenerators, "generator", "g", nil, "Generator(s) to run (can be repeated)")
	personaAddCmd.Flags().StringVarP(&personaAddDescription, "description", "d", "", "Description of the persona")

	personaUpdateCmd.Flags().StringSliceVar(&personaUpdateAddFragments, "add-fragment", nil, "Fragment(s) to add (can be repeated)")
	personaUpdateCmd.Flags().StringSliceVar(&personaUpdateRemoveFragments, "remove-fragment", nil, "Fragment(s) to remove (can be repeated)")
	personaUpdateCmd.Flags().StringSliceVar(&personaUpdateAddGenerators, "add-generator", nil, "Generator(s) to add (can be repeated)")
	personaUpdateCmd.Flags().StringSliceVar(&personaUpdateRemoveGenerators, "remove-generator", nil, "Generator(s) to remove (can be repeated)")
	personaUpdateCmd.Flags().StringVarP(&personaUpdateDescription, "description", "d", "", "New description for the persona")
}
