package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/benjaminabbitt/scm/internal/collections"
	"github.com/benjaminabbitt/scm/internal/config"
	"github.com/benjaminabbitt/scm/internal/fragments"
	pb "github.com/benjaminabbitt/scm/internal/lm/grpc"
)

// generatorPrefix is the naming convention for external generator binaries.
const generatorPrefix = "scm-generator-"

// expandTilde expands a leading ~ in a path to the user's home directory.
// Returns the original path if expansion fails or path doesn't start with ~.
func expandTilde(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}

var generatorCmd = &cobra.Command{
	Use:     "generator",
	Aliases: []string{"gen"},
	Short:   "Manage context generators",
	Long: `Manage context generators - executables that produce dynamic context.

Generators are programs that output context fragments in a structured format.
They can provide dynamic information like git status, environment info, or project state.

External generators can be added by placing binaries named 'scm-generator-<name>'
in ~/.scm/generators/ or .scm/generators/, or by configuring them in config.yaml.`,
}

var generatorListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List available generators",
	Long:    `Lists all available context generators.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		// Find external generators
		externalGenerators := findExternalGenerators(cfg.GetGeneratorPaths())

		// Print config-based generators
		if len(cfg.Generators) > 0 {
			fmt.Println("Config generators:")
			for name := range cfg.Generators {
				fmt.Printf("  %s\n", name)
			}
		}

		// Print external generators
		if len(externalGenerators) > 0 {
			fmt.Println("External generators:")
			for _, g := range externalGenerators {
				fmt.Printf("  %s (%s)\n", g.name, g.path)
			}
		}

		if len(cfg.Generators) == 0 && len(externalGenerators) == 0 {
			fmt.Println("No generators found.")
		}

		return nil
	},
}

type externalGenerator struct {
	name string
	path string
}

// findExternalGenerators searches for generator binaries in the given paths.
func findExternalGenerators(paths []string) []externalGenerator {
	var gens []externalGenerator
	seen := collections.NewSet[string]()

	for _, dir := range paths {
		dir = expandTilde(dir)

		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			name := entry.Name()
			if strings.HasPrefix(name, generatorPrefix) {
				genName := strings.TrimPrefix(name, generatorPrefix)
				if !seen.Has(genName) {
					seen.Add(genName)
					gens = append(gens, externalGenerator{
						name: genName,
						path: filepath.Join(dir, name),
					})
				}
			}
		}
	}

	sort.Slice(gens, func(i, j int) bool {
		return gens[i].name < gens[j].name
	})

	return gens
}

var generatorRunCmd = &cobra.Command{
	Use:   "run <name>",
	Short: "Run a generator and display its output",
	Long: `Run a generator and display the generated context fragment.

This is useful for testing generators or seeing what context they produce.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		// Try to run the generator
		frags, err := RunGenerators(cfg, []string{name}, func(msg string) {
			fmt.Fprintf(os.Stderr, "warning: %s\n", msg)
		})
		if err != nil {
			return fmt.Errorf("generator failed: %w", err)
		}

		if len(frags) == 0 {
			return fmt.Errorf("generator %q not found or produced no output", name)
		}

		// Display the generated content
		for _, frag := range frags {
			if frag.Content != "" {
				fmt.Println(frag.Content)
			}
			if len(frag.Exports) > 0 {
				fmt.Println("\n--- Exports ---")
				for k, v := range frag.Exports {
					fmt.Printf("%s: %s\n", k, v)
				}
			}
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(generatorCmd)

	generatorCmd.AddCommand(generatorListCmd)
	generatorCmd.AddCommand(generatorRunCmd)
}

// GeneratorType indicates how a generator should be executed.
type GeneratorType int

const (
	GeneratorNotFound GeneratorType = iota
	GeneratorExternal
	GeneratorConfig
)

// ResolveGeneratorType finds a generator and returns its type and path (if external).
func ResolveGeneratorType(cfg *config.Config, name string) (path string, genType GeneratorType) {
	// Check config-based generators first
	if cfg.Generators != nil {
		if _, ok := cfg.Generators[name]; ok {
			return "", GeneratorConfig
		}
	}

	// Check external generators in generator paths
	for _, dir := range cfg.GetGeneratorPaths() {
		dir = expandTilde(dir)
		binaryPath := filepath.Join(dir, generatorPrefix+name)
		if info, err := os.Stat(binaryPath); err == nil && !info.IsDir() {
			return binaryPath, GeneratorExternal
		}
	}

	return "", GeneratorNotFound
}

// RunGenerators runs multiple generators and returns their fragments.
func RunGenerators(cfg *config.Config, names []string, warnFunc func(string)) ([]*fragments.Fragment, error) {
	var frags []*fragments.Fragment

	for _, name := range names {
		path, genType := ResolveGeneratorType(cfg, name)

		if genType == GeneratorNotFound {
			if warnFunc != nil {
				warnFunc(fmt.Sprintf("generator %q not found", name))
			}
			continue
		}

		var content string
		var exports map[string]string

		switch genType {
		case GeneratorConfig:
			// Config-based generator - run command directly
			genConfig := cfg.Generators[name]
			var stdout, stderr bytes.Buffer
			cmd := exec.Command(genConfig.Command, genConfig.Args...)
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			if err := cmd.Run(); err != nil {
				if warnFunc != nil {
					warnFunc(fmt.Sprintf("generator %q failed: %v: %s", name, err, stderr.String()))
				}
				continue
			}

			// Parse YAML output from generator
			var output struct {
				Content string            `yaml:"content"`
				Exports map[string]string `yaml:"exports"`
			}
			if err := yaml.Unmarshal(stdout.Bytes(), &output); err != nil {
				// If not valid YAML, use raw output as content
				content = stdout.String()
			} else {
				content = output.Content
				exports = output.Exports
			}

		case GeneratorExternal:
			// External generator - spawn the binary directly
			client, err := pb.NewExternalGeneratorClient(path)
			if err != nil {
				if warnFunc != nil {
					warnFunc(fmt.Sprintf("generator %q failed to start: %v", name, err))
				}
				continue
			}

			resp, err := client.Generate(context.Background(), &pb.GenerateRequest{})
			client.Kill()

			if err != nil {
				if warnFunc != nil {
					warnFunc(fmt.Sprintf("generator %q failed: %v", name, err))
				}
				continue
			}

			if resp.ExitCode != 0 {
				if warnFunc != nil {
					warnFunc(fmt.Sprintf("generator %q failed: %s", name, resp.Error))
				}
				continue
			}

			content = resp.Content
			exports = resp.Exports
		}

		// Create fragment from response
		frag := &fragments.Fragment{
			Name:    name,
			Content: content,
			Exports: exports,
		}
		frags = append(frags, frag)
	}

	return frags, nil
}
