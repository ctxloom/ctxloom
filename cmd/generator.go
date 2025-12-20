package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"mlcm/internal/config"
	"mlcm/internal/fragments"
)

var generatorCmd = &cobra.Command{
	Use:     "generator",
	Aliases: []string{"gen"},
	Short:   "Manage context generators",
	Long: `Manage context generators - executables that produce dynamic context.

Generators are programs that output context fragments to stdout in markdown format.
They can provide dynamic information like git status, environment info, or project state.`,
}

var generatorListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all registered generators",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if len(cfg.Generators) == 0 {
			fmt.Println("No generators registered.")
			fmt.Println("Use 'mlcm generator add <name> -c <command>' to register one.")
			return nil
		}

		// Sort generator names
		names := make([]string, 0, len(cfg.Generators))
		for name := range cfg.Generators {
			names = append(names, name)
		}
		sort.Strings(names)

		fmt.Println("Generators:")
		for _, name := range names {
			g := cfg.Generators[name]
			fmt.Printf("  %s\n", name)
			if g.Description != "" {
				fmt.Printf("    Description: %s\n", g.Description)
			}
			cmdStr := g.Command
			if len(g.Args) > 0 {
				cmdStr += " " + strings.Join(g.Args, " ")
			}
			fmt.Printf("    Command: %s\n", cmdStr)
		}

		return nil
	},
}

var (
	generatorAddCommand     string
	generatorAddArgs        []string
	generatorAddDescription string
)

var generatorAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Register a new generator",
	Long: `Register a new context generator.

Examples:
  mlcm generator add git-context -c mlcm-gen-git-context -d "Git repository info"
  mlcm generator add env-info -c ./scripts/env-gen.sh -d "Environment variables"
  mlcm generator add date -c date -a "+%Y-%m-%d" -d "Current date"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		if generatorAddCommand == "" {
			return fmt.Errorf("command is required (use -c)")
		}

		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if _, exists := cfg.Generators[name]; exists {
			return fmt.Errorf("generator %q already exists (use 'generator remove' first)", name)
		}

		cfg.Generators[name] = config.Generator{
			Description: generatorAddDescription,
			Command:     generatorAddCommand,
			Args:        generatorAddArgs,
		}

		if err := cfg.Save(); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		cmdStr := generatorAddCommand
		if len(generatorAddArgs) > 0 {
			cmdStr += " " + strings.Join(generatorAddArgs, " ")
		}
		fmt.Printf("Added generator %q: %s\n", name, cmdStr)
		return nil
	},
}

var generatorRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm"},
	Short:   "Remove a generator",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if _, exists := cfg.Generators[name]; !exists {
			return fmt.Errorf("generator %q not found", name)
		}

		delete(cfg.Generators, name)

		if err := cfg.Save(); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		fmt.Printf("Removed generator %q\n", name)
		return nil
	},
}

var generatorShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show details of a generator",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		g, exists := cfg.Generators[name]
		if !exists {
			return fmt.Errorf("generator %q not found", name)
		}

		fmt.Printf("Generator: %s\n", name)
		if g.Description != "" {
			fmt.Printf("Description: %s\n", g.Description)
		}
		fmt.Printf("Command: %s\n", g.Command)
		if len(g.Args) > 0 {
			fmt.Printf("Arguments: %s\n", strings.Join(g.Args, " "))
		}

		return nil
	},
}

var generatorRunCmd = &cobra.Command{
	Use:   "run <name>",
	Short: "Run a generator and display its output",
	Long: `Run a generator and display the generated context fragment.

This is useful for testing generators or seeing what context they produce.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := GetConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		g, exists := cfg.Generators[name]
		if !exists {
			return fmt.Errorf("generator %q not found", name)
		}

		output, err := runGenerator(g)
		if err != nil {
			return fmt.Errorf("generator failed: %w", err)
		}

		fmt.Println(output)
		return nil
	},
}

// generatorTimeout is the maximum time a generator can run.
const generatorTimeout = 30 * time.Second

// runGenerator executes a generator and returns its output.
func runGenerator(g config.Generator) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), generatorTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, g.Command, g.Args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("generator timed out after %v", generatorTimeout)
		}
		if stderr.Len() > 0 {
			return "", fmt.Errorf("%s: %s", err, stderr.String())
		}
		return "", err
	}

	return strings.TrimSpace(stdout.String()), nil
}

// RunGeneratorByName runs a generator by name and returns parsed fragment.
func RunGeneratorByName(cfg *config.Config, name string) (*fragments.Fragment, error) {
	g, exists := cfg.Generators[name]
	if !exists {
		return nil, fmt.Errorf("generator %q not found", name)
	}

	output, err := runGenerator(g)
	if err != nil {
		return nil, err
	}

	frag, err := fragments.ParseYAML(output)
	if err != nil {
		return nil, fmt.Errorf("failed to parse generator output: %w", err)
	}
	frag.Name = name

	return frag, nil
}

func init() {
	rootCmd.AddCommand(generatorCmd)

	generatorCmd.AddCommand(generatorListCmd)
	generatorCmd.AddCommand(generatorAddCmd)
	generatorCmd.AddCommand(generatorRemoveCmd)
	generatorCmd.AddCommand(generatorShowCmd)
	generatorCmd.AddCommand(generatorRunCmd)

	generatorAddCmd.Flags().StringVarP(&generatorAddCommand, "command", "c", "", "Command to execute (required)")
	generatorAddCmd.Flags().StringSliceVarP(&generatorAddArgs, "arg", "a", nil, "Arguments to pass (can be repeated)")
	generatorAddCmd.Flags().StringVarP(&generatorAddDescription, "description", "d", "", "Description of the generator")
	generatorAddCmd.MarkFlagRequired("command")
}

// GetRegisteredGenerators returns all generators from config.
func GetRegisteredGenerators(cfg *config.Config) map[string]config.Generator {
	return cfg.Generators
}

// resolveGenerator tries to find a generator by name in config,
// falling back to treating it as a direct command.
func ResolveGenerator(cfg *config.Config, name string) (config.Generator, bool) {
	if g, exists := cfg.Generators[name]; exists {
		return g, true
	}
	// Treat as direct command
	return config.Generator{Command: name}, false
}

// RunGenerators runs multiple generators and returns their fragments.
func RunGenerators(cfg *config.Config, names []string, warnFunc func(string)) ([]*fragments.Fragment, error) {
	var frags []*fragments.Fragment

	for _, name := range names {
		g, registered := ResolveGenerator(cfg, name)
		if !registered && warnFunc != nil {
			warnFunc(fmt.Sprintf("generator %q not registered, treating as command", name))
		}

		output, err := runGenerator(g)
		if err != nil {
			if warnFunc != nil {
				warnFunc(fmt.Sprintf("generator %q failed: %v", name, err))
			}
			continue
		}

		frag, err := fragments.ParseYAML(output)
		if err != nil {
			if warnFunc != nil {
				warnFunc(fmt.Sprintf("generator %q output invalid: %v", name, err))
			}
			continue
		}
		frag.Name = name
		frags = append(frags, frag)
	}

	return frags, nil
}

// EnsureGeneratorInPath checks if a generator command is available.
func EnsureGeneratorInPath(command string) error {
	_, err := exec.LookPath(command)
	if err != nil {
		return fmt.Errorf("generator command not found: %s", command)
	}
	return nil
}

// InstallBuiltinGenerators registers the built-in generators if not already present.
func InstallBuiltinGenerators(cfg *config.Config) ([]string, error) {
	builtins := map[string]config.Generator{
		"git-context": {
			Description: "Git repository information (branch, status, recent commits)",
			Command:     "mlcm-gen-git-context",
		},
	}

	var installed []string
	for name, g := range builtins {
		if _, exists := cfg.Generators[name]; !exists {
			// Check if command exists
			if err := EnsureGeneratorInPath(g.Command); err == nil {
				cfg.Generators[name] = g
				installed = append(installed, name)
			}
		}
	}

	if len(installed) > 0 {
		if err := cfg.Save(); err != nil {
			return nil, err
		}
	}

	return installed, nil
}
