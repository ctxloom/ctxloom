//go:build !memory

package cmd

import "github.com/SophisticatedContextManager/scm/internal/config"

// memoryLoadRecent is a no-op stub when memory feature is disabled.
func memoryLoadRecent(_ *config.Config) (string, error) {
	return "", nil
}
