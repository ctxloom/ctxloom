package operations

import (
	"context"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/config"
)

// SetDefaultPluginRequest is the input for SetDefaultPlugin.
type SetDefaultPluginRequest struct {
	Name string `json:"name"`
}

// SetDefaultPluginResult reports the outcome. Status is "set" or "unchanged".
type SetDefaultPluginResult struct {
	Status string `json:"status"`
	Name   string `json:"name"`
}

// SetDefaultPlugin records the default LLM plugin in config and persists it.
// Frontends validate that the name is a known plugin (a frontend concern — it
// depends on the caller's plugin discovery) before calling; this owns the
// mutation + save so no frontend writes config directly.
func SetDefaultPlugin(_ context.Context, cfg *config.Config, req SetDefaultPluginRequest) (*SetDefaultPluginResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if cfg.GetDefaultLLMPlugin() == req.Name {
		return &SetDefaultPluginResult{Status: "unchanged", Name: req.Name}, nil
	}
	cfg.SetDefaultLLMPlugin(req.Name)
	if err := cfg.Save(); err != nil {
		return nil, fmt.Errorf("failed to save config: %w", err)
	}
	return &SetDefaultPluginResult{Status: "set", Name: req.Name}, nil
}
