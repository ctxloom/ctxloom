package agent

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/config"
)

// ContextInjectionTimeout is the timeout for the context injection hook in seconds.
const ContextInjectionTimeout = 60

// NewContextInjectionHook creates the SessionStart hook that injects
// assembled context into the agent. The Command is emitted as the bare
// name `ctxloom` so it re-resolves via PATH at fire time and never goes
// stale when the binary moves.
//
// workDir is the project directory where the context file lives.
// Resolved to an absolute path because Claude Code can launch the
// hook from a different cwd.
func NewContextInjectionHook(hash, workDir string) config.Hook {
	return config.Hook{
		Command: fmt.Sprintf("ctxloom hook inject-context --project %s %s", shellSingleQuote(absOrSelf(workDir)), hash),
		Type:    "command",
		Timeout: ContextInjectionTimeout,
	}
}

// NewContextInjectionChunkHook builds one of N ordered context-injection hooks.
// Each invocation emits a single sub-cap chunk (part k of total) and uses the
// flock rendezvous (AwaitTurn) to complete in order, so the harness — which
// injects parallel hook output in completion order — sees the chunks in
// sequence. See NewContextInjectionHooks for when chunking kicks in.
func NewContextInjectionChunkHook(hash, workDir string, part, total int) config.Hook {
	return config.Hook{
		Command: fmt.Sprintf("ctxloom hook inject-context --project %s --part %d --of %d %s",
			shellSingleQuote(absOrSelf(workDir)), part, total, hash),
		Type:    "command",
		Timeout: ContextInjectionTimeout,
	}
}

// absOrSelf resolves workDir to an absolute path (Claude Code may launch the
// hook from a different cwd), falling back to the input on error.
func absOrSelf(workDir string) string {
	if abs, err := filepath.Abs(workDir); err == nil {
		return abs
	}
	return workDir
}

// NewContextInjectionHooks returns the SessionStart context-injection hook(s)
// for the given content hash. It reads the (content-addressed, immutable)
// context file to decide the split: content that fits in one sub-cap chunk —
// or a missing/unreadable file — yields a single legacy whole-content hook;
// larger content yields N ordered chunk hooks. Reading the file here and in the
// hook with the same ChunkContext guarantees write-time and run-time agree on
// N. Best-effort by design: any read error falls back to the single hook (the
// runtime hook then emits nothing if the file is truly empty).
func NewContextInjectionHooks(hash, workDir string) []config.Hook {
	content, _ := ReadContextFile(workDir, hash)
	chunks := ChunkContext(content)
	if len(chunks) <= 1 {
		return []config.Hook{NewContextInjectionHook(hash, workDir)}
	}
	hooks := make([]config.Hook, 0, len(chunks))
	for k := 1; k <= len(chunks); k++ {
		hooks = append(hooks, NewContextInjectionChunkHook(hash, workDir, k, len(chunks)))
	}
	return hooks
}

// AppendManagedDynamicHooks appends the ctxloom-managed hooks that are
// assembled dynamically (rather than read verbatim from one config block) onto
// unified: the bundle-shipped hooks (SCM-tagged — e.g. `session bind`,
// `stamp-plan`) and, when contextHash is non-empty, the
// SessionStart context-injection hook.
//
// Both writers of settings.json route through this: the `ctxloom run` Setup
// path (BaseLifecycle.MergeConfigHooks) and operations.ApplyHooks. WriteSettings
// reconciles by removing ALL ctxloom hooks and re-adding only the writer's
// assembled set, so any writer that assembled a partial set silently dropped
// the rest. That is exactly what broke forward-bind: Setup never resolved
// bundle hooks, so every `ctxloom run` session launched with a settings.json
// missing `session bind`; apply-hooks could likewise drop inject-context.
// Keeping the assembly in one place guarantees every writer produces an
// identical, complete managed set.
func AppendManagedDynamicHooks(unified *config.UnifiedHooks, cfg *config.Config, workDir, contextHash string) {
	if unified == nil || cfg == nil {
		return
	}
	unified.Append(cfg.ResolveBundleHooks())
	if contextHash != "" {
		unified.SessionStart = append(unified.SessionStart, NewContextInjectionHooks(contextHash, workDir)...)
	}
}

// AssembleManagedHooks builds the COMPLETE ctxloom-managed hook set that every
// writer of a backend settings file must produce identically: config-level
// hooks, default-profile-shipped hooks, bundle-shipped hooks, and (when
// contextHash is non-empty) the context-injection hook.
//
// Both writers route through this — the `ctxloom run` Setup path
// (BaseLifecycle.MergeConfigHooks) and operations.ApplyHooks. WriteSettings
// reconciles by removing ALL ctxloom hooks and re-adding only the writer's
// assembled set, so any divergence between the writers silently drops whatever
// one assembled but the other didn't. Setup used to merge default-profile
// hooks while apply-hooks did not, so a profile-shipped SessionStart hook would
// be written at Setup and dropped by the next apply-hooks reconcile — the same
// class of failure that broke forward-bind. Keeping the full assembly here
// guarantees both writers produce an identical, complete set.
//
// Returns a fresh HooksConfig each call (never aliases cfg.Hooks), so callers
// that invoke it in a loop — e.g. apply-hooks across every backend — cannot
// accumulate duplicate hooks by mutating shared config state.
func AssembleManagedHooks(cfg *config.Config, workDir, contextHash string) *config.HooksConfig {
	hooks := &config.HooksConfig{Plugins: make(map[string]config.BackendHooks)}
	if cfg == nil {
		return hooks
	}
	// Config-level hooks.
	mergeHooksConfig(hooks, &cfg.Hooks)
	// Default-profile-shipped hooks.
	for _, profileName := range cfg.GetDefaultProfiles() {
		resolved, err := config.ResolveProfile(cfg.Profiles.Definitions, profileName)
		if err != nil {
			continue
		}
		mergeHooksConfig(hooks, &resolved.Hooks)
	}
	// Bundle-shipped hooks + the context-injection hook.
	AppendManagedDynamicHooks(&hooks.Unified, cfg, workDir, contextHash)
	return hooks
}

// shellSingleQuote wraps s in single quotes for safe interpolation into a
// /bin/sh command string, escaping embedded single quotes as the standard
// '\'' idiom. Unlike double-quoting, single quotes neutralize spaces, $,
// backticks, and backslashes — so a project path containing any of those
// can't break the command split or inject shell behavior.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// mergeHooksConfig merges source hooks into dest hooks.
func mergeHooksConfig(dest *config.HooksConfig, src *config.HooksConfig) {
	if src == nil || dest == nil {
		return
	}

	// Merge unified hooks
	dest.Unified.PreTool = append(dest.Unified.PreTool, src.Unified.PreTool...)
	dest.Unified.PostTool = append(dest.Unified.PostTool, src.Unified.PostTool...)
	dest.Unified.SessionStart = append(dest.Unified.SessionStart, src.Unified.SessionStart...)
	dest.Unified.SessionEnd = append(dest.Unified.SessionEnd, src.Unified.SessionEnd...)
	dest.Unified.PreShell = append(dest.Unified.PreShell, src.Unified.PreShell...)
	dest.Unified.PostFileEdit = append(dest.Unified.PostFileEdit, src.Unified.PostFileEdit...)

	// Merge plugin-specific hooks
	if dest.Plugins == nil {
		dest.Plugins = make(map[string]config.BackendHooks)
	}
	for name, hooks := range src.Plugins {
		if dest.Plugins[name] == nil {
			dest.Plugins[name] = make(config.BackendHooks)
		}
		for event, eventHooks := range hooks {
			dest.Plugins[name][event] = append(dest.Plugins[name][event], eventHooks...)
		}
	}
}
