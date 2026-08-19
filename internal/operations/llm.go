package operations

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
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

// =============================================================================
// LLM CRUD: `llm create`/`llm edit`/`llm remove` — the parity gap with
// `agent`, which already has full CRUD over its own local config-key entries
// (agents.go). `llm` had only list and default; this is the write half.
// =============================================================================

// LLMEntry is one labeled LLM registry entry's declared definition — the
// `llm create`/`llm edit`/`llm list` write-confirmation shape.
//
// LLMConfig.Body carries an "env" block holding API keys (spec: credentials
// are withheld, a security posture not a nicety). EnvKeys is deliberately
// []string of KEY NAMES ONLY — never a map that could carry a value — so a
// caller holding an LLMEntry structurally cannot echo a secret back: there
// is nowhere on this type for one to be. The real values are reachable
// through exactly one path, config.Config.LabelEnv, which only the engine
// launch path calls.
type LLMEntry struct {
	Label       string `json:"label"`
	Type        string `json:"type,omitempty"`
	Model       string `json:"model,omitempty"`
	Permissions string `json:"permissions,omitempty"`
	// EnvKeys names the env/credential keys this entry declares, sorted —
	// presence only, never values.
	EnvKeys []string `json:"env_keys,omitempty"`
}

// llmEntryFromConfig projects a config.LLMConfig into the CRUD-facing
// LLMEntry — the one place that reads Body["env"] down to key names only,
// so every caller (SetLLM's result, a future `llm show`) gets the
// withholding for free rather than re-implementing it.
func llmEntryFromConfig(label string, c config.LLMConfig) LLMEntry {
	model, _ := c.Body["model"].(string)
	return LLMEntry{
		Label:       label,
		Type:        c.Type,
		Model:       model,
		Permissions: c.Permissions,
		EnvKeys:     llmEnvKeys(c.Body),
	}
}

// llmEnvKeys extracts the SORTED key names of Body["env"] — never values.
func llmEnvKeys(body map[string]any) []string {
	raw, ok := body["env"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SetLLMRequest is the input for SetLLM: create-or-edit one labeled LLM
// registry entry under the local `llm.configs` config key. A nil pointer
// field means "the caller did not name this field" and keeps whatever the
// existing entry holds; an explicitly-supplied empty value clears it —
// mirroring SetAgentRequest's contract.
type SetLLMRequest struct {
	Label string `json:"label"`
	// Type is the backend discriminator (claude-code|codex|kiro|...).
	// A non-empty value is REJECTED unless backends.Exists names it — an
	// unknown type leaves EffectiveType silently degrading to DefaultLLM at
	// resolve time, exactly the "written already broken" defect
	// SetAgent.validateAgentAxes' engine check exists to prevent. Empty
	// clears it (EffectiveType then defaults to claude-code).
	Type *string `json:"type,omitempty"`
	// Model sets Body["model"]. Empty clears it.
	Model *string `json:"model,omitempty"`
	// Permissions sets the launch-time posture (default|acceptEdits|plan|
	// bypass). An unknown value is stored as written (advisory warn only,
	// like Runtime/Permissions on SetAgentRequest) since it degrades to a
	// working default at resolve time rather than breaking outright.
	Permissions *string `json:"permissions,omitempty"`
	// Env, when non-nil, REPLACES the entry's entire declared env block —
	// see cli/llm_write.go's --env-file, the only production caller: it
	// always supplies the whole desired set (never a per-key merge), so a
	// key is added or dropped by editing the file, never by the caller
	// having to remember what is already stored. An empty non-nil map
	// clears the block entirely. nil means "not named, keep what's there".
	//
	// Values here are real credentials in memory for exactly as long as
	// this call takes; nothing on the RETURNED LLMEntry carries them
	// forward — see LLMEntry.EnvKeys.
	Env map[string]string `json:"-"`
}

// warnLLMPermissionsTypo is SetLLM's advisory-only axis check, split out to
// mirror SetAgent's warnAgentAxisTypos/validateAgentAxes split: an unknown
// permissions value still resolves to a working default at run time, so it
// is stored as written but flagged now rather than only at first launch.
func warnLLMPermissionsTypo(label string, permissions *string) {
	if permissions == nil || *permissions == "" {
		return
	}
	if _, ok := agent.ParsePermissionMode(*permissions); !ok {
		clidiag.Warn("ctxloom",
			"llm %q declares unknown permissions %q (known: %s); it will use the default posture",
			label, *permissions, strings.Join(agent.PermissionModeNames(), "|"))
	}
}

// SetLLM adds or updates a LOCAL LLM registry entry under the `llm.configs`
// config key, inside one Manager.Update transaction for its NON-credential
// fields (type, model, permissions) — the same locked, freshly-reloaded
// read-modify-write SetAgent uses, so a concurrent writer cannot land
// between the read of the existing entry and the write of the merged one.
//
// req.Env, when non-nil, is handled SEPARATELY (see setLLMHomeEnv): it is a
// credential block (layerscope: llm.configs.*.env is ScopeMachine — "a
// committed value is a leaked secret") and must never land in a committed
// project file, so it is written straight to the user's HOME config.yaml
// regardless of what mgr itself targets. A failure there is reported even
// though the non-credential fields already committed — the caller sees a
// real error, never a silent partial success.
func SetLLM(mgr *config.Manager, req SetLLMRequest) (*LLMEntry, error) {
	if mgr == nil {
		return nil, fmt.Errorf("manager is required")
	}
	if req.Label == "" {
		return nil, fmt.Errorf("label is required")
	}
	if req.Type != nil && *req.Type != "" {
		// The stored discriminator is CANONICAL, not what the caller typed:
		// config.json pins llm.configs.*.type to a const per backend, so
		// persisting an accepted alias ("claude") or a case variant
		// ("Claude-Code") would write an entry that resolves at every read and
		// still fails schema validation on every subsequent load. The registry
		// resolves aliases; only this write decides what lands on disk.
		canonical := agent.CanonicalEngineName(*req.Type)
		if !backends.Exists(canonical) {
			return nil, fmt.Errorf("llm %q: unknown type %q; known: %s", req.Label, *req.Type, strings.Join(backends.List(), ", "))
		}
		req.Type = &canonical
	}
	warnLLMPermissionsTypo(req.Label, req.Permissions)

	err := mgr.Update(func(d *config.Draft) error {
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
		entry.Permissions = orKeep(req.Permissions, entry.Permissions)
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

	if req.Env != nil {
		if err := setLLMHomeEnv(req.Label, req.Env); err != nil {
			return nil, fmt.Errorf("save llm %q env: %w", req.Label, err)
		}
	}

	// Re-read the FULLY MERGED view (home env layered under whatever mgr
	// itself targets) for the returned entry, through mgr's OWN resolution
	// (Options() carries any WithFS/WithAppDir test seam), so the confirmed
	// entry reflects both writes regardless of which one just ran.
	reloaded, rerr := config.Load(mgr.Options()...)
	if rerr != nil {
		return nil, fmt.Errorf("reload llm %q after save: %w", req.Label, rerr)
	}
	got, _ := reloaded.GetLLMEntry(req.Label)
	result := llmEntryFromConfig(req.Label, got)
	return &result, nil
}

// setLLMHomeEnv persists label's env block DIRECTLY to the user's home
// config.yaml (~/.ctxloom/config.yaml), independent of whatever project (if
// any) the caller's own Manager targets — the ONLY way an env block
// survives a save at all. llm.configs.*.env is ScopeMachine
// (layerscope/policy_default.go: "credential passthrough; a committed value
// is a leaked secret"), and saveLocked's layerscope filter strips any
// ScopeMachine value the moment a write resolves as the PROJECT layer. A
// Manager built with WithAppDir(HomeConfigDir()) resolves as SourceHome
// (config.go's loadUncached: an explicit appDir naming home exactly is
// recognized as home, not an arbitrary project — see
// TestLoad_ExplicitAppDirEqualToHome_ResolvesSourceHome), so this write is
// never filtered. An empty env map clears the block entirely.
func setLLMHomeEnv(label string, env map[string]string) error {
	homeDir, err := config.HomeConfigDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	homeMgr := config.NewManager(config.WithAppDir(homeDir))
	return homeMgr.Update(func(d *config.Draft) error {
		if d.LM.Configs == nil {
			d.LM.Configs = make(map[string]config.LLMConfig)
		}
		entry := d.LM.Configs[label]
		if len(env) == 0 {
			if entry.Body != nil {
				delete(entry.Body, "env")
				if len(entry.Body) == 0 {
					entry.Body = nil
				}
			}
		} else {
			if entry.Body == nil {
				entry.Body = map[string]any{}
			}
			envAny := make(map[string]any, len(env))
			for k, v := range env {
				envAny[k] = v
			}
			entry.Body["env"] = envAny
		}
		d.LM.Configs[label] = entry
		return nil
	})
}

// RemoveLLM deletes a LOCAL LLM registry entry from the `llm.configs`
// config key, inside one Manager.Update transaction — mirroring
// RemoveAgent. cfg is consulted (via IsLLMUserAuthored) to distinguish a
// genuinely user-declared entry from one mergeDefaultConfig's
// whole-registry fallback merely filled in for a project that configured no
// LLMs at all (e.g. "claude-code" on an empty llm.configs) — without that
// check, removing a never-configured built-in would report success while
// persisting no change at all (there was nothing on disk to delete). An
// unknown or not-user-authored label is an error, never a silent
// zero-effect success.
//
// This does NOT clear any home-stored env for label: the credential is a
// MACHINE-scoped resource keyed only by label name, potentially shared by
// another project that binds the same label — removing one project's
// declaration must not reach out and delete a credential a different
// checkout may still depend on.
func RemoveLLM(mgr *config.Manager, cfg *config.Config, label string) error {
	if mgr == nil {
		return fmt.Errorf("manager is required")
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
	return mgr.Update(func(d *config.Draft) error {
		delete(d.LM.Configs, label)
		if d.LM.Defaults.Primary == label {
			clidiag.Warn("ctxloom", "llm %q was the configured default (llm.defaults.primary); set a new one with `ctxloom llm default <label>`", label)
		}
		if d.LM.Defaults.Fast == label {
			clidiag.Warn("ctxloom", "llm %q was the configured fast default (llm.defaults.fast)", label)
		}
		return nil
	})
}
