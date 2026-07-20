package operations

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

// errDefaultLLMUnchanged abandons a SetDefaultLLM transaction when the
// freshly-reloaded Draft already names the requested label — nothing to
// write, so nothing is saved. It never escapes SetDefaultLLM.
var errDefaultLLMUnchanged = errors.New("default llm unchanged")

// SetDefaultLLMRequest is the input for SetDefaultLLM.
type SetDefaultLLMRequest struct {
	Name string `json:"name"`
}

// SetDefaultLLMResult reports the outcome. Status is "set" or "unchanged".
type SetDefaultLLMResult struct {
	Status string `json:"status"`
	Name   string `json:"name"`
}

// SetDefaultLLM records the default LLM plugin in config, inside one
// Manager.Update transaction: the "is this already the default" check reads
// the same locked, freshly-reloaded Draft the write applies to, so the
// answer is never a statement about a config another writer has since
// replaced. Frontends validate that the name is a known plugin (a frontend
// concern — it depends on the caller's plugin discovery) before calling;
// this owns the mutation + save so no frontend writes config directly.
func SetDefaultLLM(_ context.Context, mgr *config.Manager, req SetDefaultLLMRequest) (*SetDefaultLLMResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if mgr == nil {
		return nil, fmt.Errorf("manager is required")
	}

	err := mgr.Update(func(d *config.Draft) error {
		if d.LM.Defaults.Primary == req.Name {
			return errDefaultLLMUnchanged
		}
		d.LM.Defaults.Primary = req.Name
		return nil
	})
	if err != nil {
		if errors.Is(err, errDefaultLLMUnchanged) {
			return &SetDefaultLLMResult{Status: "unchanged", Name: req.Name}, nil
		}
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
