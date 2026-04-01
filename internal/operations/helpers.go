package operations

import (
	"fmt"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/config"
)

// getFS returns the provided filesystem or a default OS filesystem if nil.
func getFS(fs afero.Fs) afero.Fs {
	if fs == nil {
		return afero.NewOsFs()
	}
	return fs
}

// loadFreshConfig loads a fresh config, or returns testConfig if provided.
// This pattern is used by operations that modify config and need a fresh copy.
func loadFreshConfig(fs afero.Fs, appDir string, testConfig *config.Config) (*config.Config, error) {
	if testConfig != nil {
		return testConfig, nil
	}

	fs = getFS(fs)
	opts := []config.LoadOption{config.WithFS(fs)}
	if appDir != "" {
		opts = append(opts, config.WithAppDir(appDir))
	}

	cfg, err := config.Load(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	return cfg, nil
}

// requireField returns an error if value is empty.
func requireField(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

// saveConfigIfNeeded saves config unless in test mode.
func saveConfigIfNeeded(cfg *config.Config, isTestMode bool) error {
	if isTestMode {
		return nil
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	return nil
}
