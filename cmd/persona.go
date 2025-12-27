package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benjaminabbitt/scm/internal/config"
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
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if len(cfg.Personas) == 0 {
			fmt.Println("No personas defined.")
			fmt.Println("Use 'scm persona add <name> -f <fragments...>' to create one.")
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
			if len(p.Parents) > 0 {
				fmt.Printf("    Parents: %s\n", strings.Join(p.Parents, ", "))
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
	personaAddParents     []string
	personaAddFragments   []string
	personaAddGenerators  []string
	personaAddDescription string
)

var personaAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add a new persona",
	Long: `Add a new persona with the specified fragments, generators, and/or parents.

Example:
  scm persona add developer -f coding-standards -f go-patterns -d "Standard dev context"
  scm persona add go-developer --parent developer -t golang -d "Go development context"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		if len(personaAddParents) == 0 && len(personaAddFragments) == 0 && len(personaAddGenerators) == 0 {
			return fmt.Errorf("at least one parent (--parent), fragment (-f), or generator (-g) is required")
		}

		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if _, exists := cfg.Personas[name]; exists {
			return fmt.Errorf("persona %q already exists (use 'persona remove' first)", name)
		}

		// Validate parent personas exist
		for _, parent := range personaAddParents {
			if _, exists := cfg.Personas[parent]; !exists {
				return fmt.Errorf("parent persona %q not found", parent)
			}
		}

		cfg.Personas[name] = config.Persona{
			Description: personaAddDescription,
			Parents:     personaAddParents,
			Fragments:   personaAddFragments,
			Generators:  personaAddGenerators,
		}

		if err := cfg.Save(); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		var parts []string
		if len(personaAddParents) > 0 {
			parts = append(parts, fmt.Sprintf("parents: %s", strings.Join(personaAddParents, ", ")))
		}
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

		cfg, err := GetConfig()
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

		cfg, err := GetConfig()
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
		if len(p.Parents) > 0 {
			fmt.Println("Parents:")
			for _, parent := range p.Parents {
				fmt.Printf("  - %s\n", parent)
			}
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
	Long: `Update an existing persona by adding or removing parents, fragments, or generators.

Examples:
  scm persona update go-developer --add-parent developer
  scm persona update go-developer --remove-parent base
  scm persona update developer --add-fragment error-handling
  scm persona update developer --remove-fragment old-patterns
  scm persona update developer --add-generator my-generator
  scm persona update developer --remove-generator old-gen
  scm persona update developer -d "New description"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := GetConfig()
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

		// Add parents
		for _, parent := range personaUpdateAddParents {
			// Validate parent exists
			if _, exists := cfg.Personas[parent]; !exists {
				return fmt.Errorf("parent persona %q not found", parent)
			}
			if !contains(p.Parents, parent) {
				p.Parents = append(p.Parents, parent)
				fmt.Printf("Added parent: %s\n", parent)
				modified = true
			} else {
				fmt.Printf("Parent already present: %s\n", parent)
			}
		}

		// Remove parents
		for _, parent := range personaUpdateRemoveParents {
			if idx := indexOf(p.Parents, parent); idx >= 0 {
				p.Parents = append(p.Parents[:idx], p.Parents[idx+1:]...)
				fmt.Printf("Removed parent: %s\n", parent)
				modified = true
			} else {
				fmt.Printf("Parent not found: %s\n", parent)
			}
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
	personaUpdateAddParents       []string
	personaUpdateRemoveParents    []string
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

	personaAddCmd.Flags().StringSliceVar(&personaAddParents, "parent", nil, "Parent persona(s) to inherit from (can be repeated)")
	personaAddCmd.Flags().StringSliceVarP(&personaAddFragments, "fragment", "f", nil, "Fragment(s) to include (can be repeated)")
	personaAddCmd.Flags().StringSliceVarP(&personaAddGenerators, "generator", "g", nil, "Generator(s) to run (can be repeated)")
	personaAddCmd.Flags().StringVarP(&personaAddDescription, "description", "d", "", "Description of the persona")

	personaUpdateCmd.Flags().StringSliceVar(&personaUpdateAddParents, "add-parent", nil, "Parent persona(s) to add (can be repeated)")
	personaUpdateCmd.Flags().StringSliceVar(&personaUpdateRemoveParents, "remove-parent", nil, "Parent persona(s) to remove (can be repeated)")
	personaUpdateCmd.Flags().StringSliceVar(&personaUpdateAddFragments, "add-fragment", nil, "Fragment(s) to add (can be repeated)")
	personaUpdateCmd.Flags().StringSliceVar(&personaUpdateRemoveFragments, "remove-fragment", nil, "Fragment(s) to remove (can be repeated)")
	personaUpdateCmd.Flags().StringSliceVar(&personaUpdateAddGenerators, "add-generator", nil, "Generator(s) to add (can be repeated)")
	personaUpdateCmd.Flags().StringSliceVar(&personaUpdateRemoveGenerators, "remove-generator", nil, "Generator(s) to remove (can be repeated)")
	personaUpdateCmd.Flags().StringVarP(&personaUpdateDescription, "description", "d", "", "New description for the persona")
}
