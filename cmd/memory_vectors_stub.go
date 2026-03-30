//go:build !vectors

package cmd

import "github.com/SophisticatedContextManager/scm/internal/config"

// indexSessionToVectorDB is a no-op when vectors feature is disabled.
func indexSessionToVectorDB(_ *config.Config, _ string) error {
	return nil
}
