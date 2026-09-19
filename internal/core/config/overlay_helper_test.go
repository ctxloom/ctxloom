package config

import "github.com/ctxloom/ctxloom/resources"

// overlayDefaultRegistry applies the shipped default registry to cfg the way
// the reader does through Builder.OverlayDefaultRegistry, for tests that
// construct a Config directly.
func overlayDefaultRegistry(cfg *Config) {
	data, err := resources.GetDefaultConfig()
	if err != nil {
		return
	}
	(&Builder{cfg: cfg}).OverlayDefaultRegistry(data)
}
