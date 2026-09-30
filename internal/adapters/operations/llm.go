package operations

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/engine"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
// Owner.Update transaction: the "is this already the default" check reads
// the same locked, freshly-reloaded Draft the write applies to, so the
// answer is never a statement about a config another writer has since
// replaced. Frontends validate that the name is a known plugin (a frontend
// concern — it depends on the caller's plugin discovery) before calling;
// this owns the mutation + save so no frontend writes config directly.
func SetDefaultLLM(ctx context.Context, app *App, req SetDefaultLLMRequest) (*SetDefaultLLMResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if app == nil {
		return nil, fmt.Errorf("app is required")
	}

	_, err := app.Update(ctx, func(d *config.Draft) error {
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
func AvailableLLMNames(reg engine.Registry, cfg *config.Config) []string {
	seen := map[string]bool{}
	var names []string
	for _, n := range EngineNames(reg) {
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

// =============================================================================
// LLM CRUD: `llm create`/`llm edit`/`llm remove` — the parity gap with
// `agent`, which already has full CRUD over its own local config-key entries
// (agents.go). `llm` had only list and default; this is the write half.
// =============================================================================

// LLMEntry is one labeled LLM registry entry's declared definition — the
// `llm create`/`llm edit`/`llm list` write-confirmation shape. An entry
// carries no credentials: an engine's are ambient, never ctxloom's
// (config.RetiredLLMEnvKey), so there is nothing on this type to withhold.
type LLMEntry struct {
	Label       string             `json:"label"`
	Type        string             `json:"type,omitempty"`
	Model       string             `json:"model,omitempty"`
	Permissions agents.LabelPermissions `json:"permissions,omitempty"`
}

// llmEntryFromConfig projects a config.LLMConfig into the CRUD-facing
// LLMEntry.
func llmEntryFromConfig(label string, c config.LLMConfig) LLMEntry {
	model, _ := c.Body["model"].(string)
	return LLMEntry{
		Label:       label,
		Type:        c.Type,
		Model:       model,
		Permissions: c.Permissions,
	}
}

// SetLLMRequest is the input for SetLLM: create-or-edit one labeled LLM
// registry entry under the local `llm.configs` config key. A nil pointer
// field means "the caller did not name this field" and keeps whatever the
// existing entry holds; an explicitly-supplied empty value clears it —
// mirroring SetAgentRequest's contract.
type SetLLMRequest struct {
	Label string `json:"label"`
	// Type is the backend discriminator: a registered engine name.
	// A non-empty value is REJECTED unless EngineExists names it — an
	// unknown type leaves EffectiveType silently degrading to DefaultLLM at
	// resolve time, exactly the "written already broken" defect
	// SetAgent.validateAgentAxes' engine check exists to prevent. Empty
	// clears it (EffectiveType then defaults to claude-code).
	Type *string `json:"type,omitempty"`
	// Model sets Body["model"]. Empty clears it.
	Model *string `json:"model,omitempty"`
	// Permissions sets the label's mode, in the vocabulary of the engine
	// the label's type names; a mode that engine does not take is REJECTED
	// (the loader would refuse it). Empty clears it.
	Permissions *string `json:"permissions,omitempty"`
}

// setLabelMode writes mode into a label's flat permission keys, checked by
// the label's engine; "" clears it.
func setLabelMode(reg engine.Registry, typ, label string, p *agents.LabelPermissions, mode string) error {
	*p = p.Clone()
	if mode == "" {
		delete(p.Engine, permissionMode)
		return nil
	}
	kind, ok := reg.Lookup(engine.Name(typ))
	if !ok {
		return fmt.Errorf("llm %q: unknown type %q", label, typ)
	}
	model, ok := kind.Permissions().Get()
	if !ok {
		return fmt.Errorf("llm %q: engine %s takes no permission mode (%s)", label, typ, kind.Permissions().AbsentReason())
	}
	if err := model.Validate(map[string]any{permissionMode: mode}); err != nil {
		return fmt.Errorf("llm %q: %w", label, err)
	}
	if p.Engine == nil {
		p.Engine = map[string]any{}
	}
	p.Engine[permissionMode] = mode
	return nil
}

// SetLLM adds or updates a LOCAL LLM registry entry under the `llm.configs`
// config key, inside one Owner.Update transaction — the same locked,
// freshly-reloaded read-modify-write SetAgent uses, so a concurrent writer
// cannot land between the read of the existing entry and the write of the
// merged one.
func SetLLM(ctx context.Context, app *App, req SetLLMRequest) (*LLMEntry, error) {
	reg := app.Engines()
	if app == nil {
		return nil, fmt.Errorf("app is required")
	}
	if req.Label == "" {
		return nil, fmt.Errorf("label is required")
	}
	if req.Type != nil && *req.Type != "" {
		// config.json pins llm.configs.*.type to a const per backend, so the
		// registry's exact-name membership check here is also what keeps an
		// entry that would fail schema validation on every later load from
		// landing on disk.
		if !EngineExists(reg, *req.Type) {
			return nil, fmt.Errorf("llm %q: unknown type %q; known: %s", req.Label, *req.Type, strings.Join(EngineNames(reg), ", "))
		}
	}
	cur, err := app.Config(ctx)
	if err != nil {
		return nil, err
	}
	next, err := app.Update(ctx, func(d *config.Draft) error {
		if d.LM.Configs == nil {
			d.LM.Configs = make(map[string]config.LLMConfig)
		}
		// Start from the record as it stands RIGHT NOW inside the
		// transaction, so a field this request does not name survives
		// untouched — same reasoning as SetAgent's identical comment.
		entry := d.LM.Configs[req.Label]
		if req.Type != nil {
			entry.Type = *req.Type
		}
		if req.Permissions != nil {
			if err := setLabelMode(reg, cur.EffectiveType(entry), req.Label, &entry.Permissions, *req.Permissions); err != nil {
				return err
			}
		}
		if req.Model != nil {
			if entry.Body == nil {
				entry.Body = map[string]any{}
			}
			if *req.Model == "" {
				delete(entry.Body, "model")
			} else {
				entry.Body["model"] = *req.Model
			}
			if len(entry.Body) == 0 {
				entry.Body = nil
			}
		}
		d.LM.Configs[req.Label] = entry
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("save llm %q: %w", req.Label, err)
	}

	// The confirmed entry is read from the generation the write produced —
	// the FULLY MERGED view a later reader will actually see.
	got, _ := next.Config.GetLLMEntry(req.Label)
	result := llmEntryFromConfig(req.Label, got)
	return &result, nil
}

// RemoveLLM deletes a LOCAL LLM registry entry from the `llm.configs`
// config key, inside one Update transaction — mirroring
// RemoveAgent. cfg is consulted (via IsLLMUserAuthored) to distinguish a
// genuinely user-declared entry from one the shipped default registry's
// whole-registry fallback merely filled in for a project that configured no
// LLMs at all (e.g. "claude-code" on an empty llm.configs) — without that
// check, removing a never-configured built-in would report success while
// persisting no change at all (there was nothing on disk to delete). An
// unknown or not-user-authored label is an error, never a silent
// zero-effect success.
func RemoveLLM(ctx context.Context, app *App, cfg *config.Config, label string) error {
	if app == nil {
		return fmt.Errorf("app is required")
	}
	if cfg == nil {
		return fmt.Errorf("config is required")
	}
	if label == "" {
		return fmt.Errorf("label is required")
	}
	if !cfg.IsLLMUserAuthored(label) {
		return fmt.Errorf("llm %q not found in config.yaml", label)
	}
	_, err := app.Update(ctx, func(d *config.Draft) error {
		delete(d.LM.Configs, label)
		if d.LM.Defaults.Primary == label {
			clidiag.Warn("ctxloom", "llm %q was the configured default (llm.defaults.primary); set a new one with `ctxloom llm default <label>`", label)
		}
		if d.LM.Defaults.Fast == label {
			clidiag.Warn("ctxloom", "llm %q was the configured fast default (llm.defaults.fast)", label)
		}
		return nil
	})
	return err
}
