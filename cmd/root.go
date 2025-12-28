package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/benjaminabbitt/scm/internal/config"
)

// Version is set at build time via ldflags
// Example: go build -ldflags "-X scm/cmd.Version=v1.0.0"
var Version = "dev"

var cfgFile string

// ExitError is returned when a command needs to exit with a specific code.
// This allows deferred cleanup to run before the process exits.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit code %d", e.Code)
}

// GetSCMDirs returns the .scm directories from project config.
func GetSCMDirs() ([]string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return cfg.SCMPaths, nil
}

// GetFragmentDirs returns fragment directories from project config.
func GetFragmentDirs() ([]string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return cfg.GetFragmentDirs(), nil
}

// GetPromptDirs returns prompt directories from project config.
func GetPromptDirs() ([]string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return cfg.GetPromptDirs(), nil
}

// GetConfig returns the project configuration.
func GetConfig() (*config.Config, error) {
	return config.Load()
}

var rootCmd = &cobra.Command{
	Use:   "scm",
	Short: "Sophisticated Context Management",
	Long:  `SCM is a CLI tool for managing context, fragments, and prompts for AI interactions.`,
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
		// Check for ExitError to preserve specific exit codes
		if exitErr, ok := err.(*ExitError); ok {
			os.Exit(exitErr.Code)
		}
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

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.scm.yaml)")
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)

		viper.AddConfigPath(home)
		viper.SetConfigType("yaml")
		viper.SetConfigName(".scm")
	}

	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err == nil {
		fmt.Fprintln(os.Stderr, "Using config file:", viper.ConfigFileUsed())
	}
}
