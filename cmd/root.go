package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"mlcm/internal/config"
)

// Version is set at build time via ldflags
// Example: go build -ldflags "-X mlcm/cmd.Version=v1.0.0"
var Version = "dev"

var cfgFile string

// useHomeDir is a global flag to operate on ~/.mlcm instead of project directories
var useHomeDir bool

// GetMLCMDirs returns the .mlcm directories to operate on based on the --home flag.
// If --home is set, returns only ~/.mlcm. Otherwise returns project directories from config.
func GetMLCMDirs() ([]string, error) {
	if useHomeDir {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		mlcmDir := filepath.Join(home, config.MLCMDirName)
		return []string{mlcmDir}, nil
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return cfg.MLCMPaths, nil
}

// GetFragmentDirs returns fragment directories based on the --home flag.
func GetFragmentDirs() ([]string, error) {
	if useHomeDir {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		return []string{filepath.Join(home, config.MLCMDirName, config.ContextFragmentsDir)}, nil
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return cfg.GetFragmentDirs(), nil
}

// GetPromptDirs returns prompt directories based on the --home flag.
func GetPromptDirs() ([]string, error) {
	if useHomeDir {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		return []string{filepath.Join(home, config.MLCMDirName, config.PromptsDir)}, nil
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return cfg.GetPromptDirs(), nil
}

// GetConfig returns the configuration, loading from home if --home flag is set.
// The returned config is properly configured for saving to the correct location.
func GetConfig() (*config.Config, error) {
	if useHomeDir {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		mlcmDir := filepath.Join(home, config.MLCMDirName)

		cfg, err := config.LoadHomeConfig()
		if err != nil {
			return nil, err
		}
		if cfg == nil {
			// Return empty config if home config doesn't exist
			cfg = &config.Config{
				Personas:   make(map[string]config.Persona),
				Generators: make(map[string]config.Generator),
			}
		}
		// Set MLCMPaths so Save() works correctly
		cfg.MLCMPaths = []string{mlcmDir}
		return cfg, nil
	}
	return config.Load()
}

var rootCmd = &cobra.Command{
	Use:   "mlcm",
	Short: "Machine Learning Context Manager",
	Long:  `MLCM is a CLI tool for managing context, fragments, and prompts for AI interactions.`,
}

func Execute() {
	// If no subcommand is provided, default to 'run'
	if len(os.Args) == 1 {
		// No args at all - default to run
		os.Args = append(os.Args, "run")
	} else if shouldDefaultToRun(os.Args[1:]) {
		// Insert 'run' as the subcommand
		os.Args = append([]string{os.Args[0], "run"}, os.Args[1:]...)
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// shouldDefaultToRun determines if arguments should be treated as run command args
func shouldDefaultToRun(args []string) bool {
	if len(args) == 0 {
		return false
	}

	first := args[0]

	// Don't redirect help, version, or completion
	if first == "help" || first == "--help" || first == "-h" ||
		first == "version" || first == "--version" || first == "-v" ||
		first == "completion" {
		return false
	}

	// If it's a known subcommand, don't redirect
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == first || cmd.HasAlias(first) {
			return false
		}
	}

	// Otherwise, treat as run command args (flags or prompt text)
	return true
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.mlcm.yaml)")
	rootCmd.PersistentFlags().BoolVar(&useHomeDir, "home", false, "operate on ~/.mlcm instead of project directories")
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)

		viper.AddConfigPath(home)
		viper.SetConfigType("yaml")
		viper.SetConfigName(".mlcm")
	}

	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err == nil {
		fmt.Fprintln(os.Stderr, "Using config file:", viper.ConfigFileUsed())
	}
}
