package operations

import (
	"context"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/config"
)

// SetDefaultLLMRequest is the input for SetDefaultLLM.
type SetDefaultLLMRequest struct {
	Name string `json:"name"`
}

// SetDefaultLLMResult reports the outcome. Status is "set" or "unchanged".
type SetDefaultLLMResult struct {
	Status string `json:"status"`
	Name   string `json:"name"`
}

// SetDefaultLLM records the default LLM plugin in config and persists it.
// Frontends validate that the name is a known plugin (a frontend concern — it
// depends on the caller's plugin discovery) before calling; this owns the
// mutation + save so no frontend writes config directly.
func SetDefaultLLM(_ context.Context, cfg *config.Config, req SetDefaultLLMRequest) (*SetDefaultLLMResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if cfg.GetDefaultLLM() == req.Name {
		return &SetDefaultLLMResult{Status: "unchanged", Name: req.Name}, nil
	}
	cfg.SetDefaultLLM(req.Name)
	if err := cfg.Save(); err != nil {
		return nil, fmt.Errorf("failed to save config: %w", err)
	}
	return &SetDefaultLLMResult{Status: "set", Name: req.Name}, nil
}
