package operations

import (
	"context"
	"fmt"
	"sort"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
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
	if cfg.PrimaryLabel() == req.Name {
		return &SetDefaultLLMResult{Status: "unchanged", Name: req.Name}, nil
	}
	cfg.SetPrimaryLabel(req.Name)
	if err := cfg.Save(); err != nil {
		return nil, fmt.Errorf("failed to save config: %w", err)
	}
	return &SetDefaultLLMResult{Status: "set", Name: req.Name}, nil
}

// AvailableLLMNames returns a sorted list of all known LLM names:
// registered built-ins plus any with an explicit config entry.
func AvailableLLMNames(cfg *config.Config) []string {
	seen := map[string]bool{}
	var names []string
	for _, n := range backends.List() {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for _, n := range cfg.GetLLMLabels() {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}
