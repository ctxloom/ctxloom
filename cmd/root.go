package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

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
