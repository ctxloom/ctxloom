package cmd

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benjaminabbitt/scm/internal/config"
)

var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Manage profiles (named fragment collections)",
	Long: `Manage profiles - named collections of context fragments.

Profiles allow you to quickly switch between different sets of context
fragments without specifying them individually each time.`,
}

var profileListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all profiles",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if len(cfg.Profiles) == 0 {
			fmt.Println("No profiles defined.")
			fmt.Println("Use 'scm profile add <name> -f <fragments...>' to create one.")
			return nil
		}

		// Sort profile names
		names := make([]string, 0, len(cfg.Profiles))
		for name := range cfg.Profiles {
			names = append(names, name)
		}
		sort.Strings(names)

		fmt.Println("Profiles:")
		for _, name := range names {
			p := cfg.Profiles[name]
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
	profileAddParents     []string
	profileAddFragments   []string
	profileAddGenerators  []string
	profileAddDescription string
)

var profileAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add a new profile",
	Long: `Add a new profile with the specified fragments, generators, and/or parents.

Example:
  scm profile add developer -f coding-standards -f go-patterns -d "Standard dev context"
  scm profile add go-developer --parent developer -t golang -d "Go development context"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		if len(profileAddParents) == 0 && len(profileAddFragments) == 0 && len(profileAddGenerators) == 0 {
			return fmt.Errorf("at least one parent (--parent), fragment (-f), or generator (-g) is required")
		}

		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if _, exists := cfg.Profiles[name]; exists {
			return fmt.Errorf("profile %q already exists (use 'profile remove' first)", name)
		}

		// Validate parent profiles exist
		for _, parent := range profileAddParents {
			if _, exists := cfg.Profiles[parent]; !exists {
				return fmt.Errorf("parent profile %q not found", parent)
			}
		}

		cfg.Profiles[name] = config.Profile{
			Description: profileAddDescription,
			Parents:     profileAddParents,
			Fragments:   profileAddFragments,
			Generators:  profileAddGenerators,
		}

		if err := cfg.Save(); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		var parts []string
		if len(profileAddParents) > 0 {
			parts = append(parts, fmt.Sprintf("parents: %s", strings.Join(profileAddParents, ", ")))
		}
		if len(profileAddFragments) > 0 {
			parts = append(parts, fmt.Sprintf("fragments: %s", strings.Join(profileAddFragments, ", ")))
		}
		if len(profileAddGenerators) > 0 {
			parts = append(parts, fmt.Sprintf("generators: %s", strings.Join(profileAddGenerators, ", ")))
		}
		fmt.Printf("Added profile %q with %s\n", name, strings.Join(parts, "; "))
		return nil
	},
}

var profileRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm"},
	Short:   "Remove a profile",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if _, exists := cfg.Profiles[name]; !exists {
			return fmt.Errorf("profile %q not found", name)
		}

		delete(cfg.Profiles, name)

		if err := cfg.Save(); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		fmt.Printf("Removed profile %q\n", name)
		return nil
	},
}

var profileShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show details of a profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		p, exists := cfg.Profiles[name]
		if !exists {
			return fmt.Errorf("profile %q not found", name)
		}

		fmt.Printf("Profile: %s\n", name)
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

var profileUpdateCmd = &cobra.Command{
	Use:   "update <name>",
	Short: "Update a profile",
	Long: `Update an existing profile by adding or removing parents, fragments, or generators.

Examples:
  scm profile update go-developer --add-parent developer
  scm profile update go-developer --remove-parent base
  scm profile update developer --add-fragment error-handling
  scm profile update developer --remove-fragment old-patterns
  scm profile update developer --add-generator my-generator
  scm profile update developer --remove-generator old-gen
  scm profile update developer -d "New description"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		p, exists := cfg.Profiles[name]
		if !exists {
			return fmt.Errorf("profile %q not found", name)
		}

		modified := false

		// Update description if provided
		if cmd.Flags().Changed("description") {
			p.Description = profileUpdateDescription
			modified = true
		}

		// Add parents
		for _, parent := range profileUpdateAddParents {
			// Validate parent exists
			if _, exists := cfg.Profiles[parent]; !exists {
				return fmt.Errorf("parent profile %q not found", parent)
			}
			if !slices.Contains(p.Parents, parent) {
				p.Parents = append(p.Parents, parent)
				fmt.Printf("Added parent: %s\n", parent)
				modified = true
			} else {
				fmt.Printf("Parent already present: %s\n", parent)
			}
		}

		// Remove parents
		for _, parent := range profileUpdateRemoveParents {
			if idx := slices.Index(p.Parents, parent); idx >= 0 {
				p.Parents = slices.Delete(p.Parents, idx, idx+1)
				fmt.Printf("Removed parent: %s\n", parent)
				modified = true
			} else {
				fmt.Printf("Parent not found: %s\n", parent)
			}
		}

		// Add fragments
		for _, f := range profileUpdateAddFragments {
			if !slices.Contains(p.Fragments, f) {
				p.Fragments = append(p.Fragments, f)
				fmt.Printf("Added fragment: %s\n", f)
				modified = true
			} else {
				fmt.Printf("Fragment already present: %s\n", f)
			}
		}

		// Remove fragments
		for _, f := range profileUpdateRemoveFragments {
			if idx := slices.Index(p.Fragments, f); idx >= 0 {
				p.Fragments = slices.Delete(p.Fragments, idx, idx+1)
				fmt.Printf("Removed fragment: %s\n", f)
				modified = true
			} else {
				fmt.Printf("Fragment not found: %s\n", f)
			}
		}

		// Add generators
		for _, g := range profileUpdateAddGenerators {
			if !slices.Contains(p.Generators, g) {
				p.Generators = append(p.Generators, g)
				fmt.Printf("Added generator: %s\n", g)
				modified = true
			} else {
				fmt.Printf("Generator already present: %s\n", g)
			}
		}

		// Remove generators
		for _, g := range profileUpdateRemoveGenerators {
			if idx := slices.Index(p.Generators, g); idx >= 0 {
				p.Generators = slices.Delete(p.Generators, idx, idx+1)
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

		cfg.Profiles[name] = p
		if err := cfg.Save(); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		fmt.Printf("Updated profile %q\n", name)
		return nil
	},
}

var (
	profileUpdateAddParents       []string
	profileUpdateRemoveParents    []string
	profileUpdateAddFragments     []string
	profileUpdateRemoveFragments  []string
	profileUpdateAddGenerators    []string
	profileUpdateRemoveGenerators []string
	profileUpdateDescription      string
)

func init() {
	rootCmd.AddCommand(profileCmd)

	profileCmd.AddCommand(profileListCmd)
	profileCmd.AddCommand(profileAddCmd)
	profileCmd.AddCommand(profileRemoveCmd)
	profileCmd.AddCommand(profileShowCmd)
	profileCmd.AddCommand(profileUpdateCmd)

	profileAddCmd.Flags().StringSliceVar(&profileAddParents, "parent", nil, "Parent profile(s) to inherit from (can be repeated)")
	profileAddCmd.Flags().StringSliceVarP(&profileAddFragments, "fragment", "f", nil, "Fragment(s) to include (can be repeated)")
	profileAddCmd.Flags().StringSliceVarP(&profileAddGenerators, "generator", "g", nil, "Generator(s) to run (can be repeated)")
	profileAddCmd.Flags().StringVarP(&profileAddDescription, "description", "d", "", "Description of the profile")

	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateAddParents, "add-parent", nil, "Parent profile(s) to add (can be repeated)")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateRemoveParents, "remove-parent", nil, "Parent profile(s) to remove (can be repeated)")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateAddFragments, "add-fragment", nil, "Fragment(s) to add (can be repeated)")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateRemoveFragments, "remove-fragment", nil, "Fragment(s) to remove (can be repeated)")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateAddGenerators, "add-generator", nil, "Generator(s) to add (can be repeated)")
	profileUpdateCmd.Flags().StringSliceVar(&profileUpdateRemoveGenerators, "remove-generator", nil, "Generator(s) to remove (can be repeated)")
	profileUpdateCmd.Flags().StringVarP(&profileUpdateDescription, "description", "d", "", "New description for the profile")
}
