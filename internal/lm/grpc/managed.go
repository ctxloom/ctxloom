package grpc

import (
	"os"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file holds the Go<->proto converters for the host-assembled
// ManagedConfig setup payload. The host serializes its agent.ManagedConfig with
// ManagedConfigToProto onto RunStart; the plugin Run handler deserializes with
// managedConfigFromProto into SetupRequest.Managed. The proto messages mirror
// the shared/wire hook + MCP vocabulary and agent.CommandExport field-for-field,
// so these converters are mechanical — no ctxloom config/bundle type crosses the
// wire, only its resolved, wire-typed result.
//
// EMPTY IS NIL, in both directions. Protobuf cannot distinguish an empty
// repeated field or map from an absent one — both serialize to no bytes and
// decode back to nil — so an empty collection and a missing one are the same
// fact here, and every converter below answers it with nil. That keeps the
// wire minimal, keeps each pair's round trip shape-preserving, and means no
// reader downstream has to decide which of two spellings of "none" it got.

// ManagedConfigToProto converts a host-assembled agent.ManagedConfig to its
// proto form. Returns nil for nil input so skip_setup/distill paths send none.
func ManagedConfigToProto(m *agent.ManagedConfig) *ManagedConfig {
	if m == nil {
		return nil
	}
	return &ManagedConfig{
		Commands:         commandExportsToProto(m.Commands),
		Skills:           skillExportsToProto(m.Skills),
		Hooks:            hooksConfigToProto(m.Hooks),
		BundleMcp:        mcpServerMapToProto(m.BundleMCP),
		ManageStatusline: m.ManageStatusline,
		DenyTools:        m.DenyTools,
		Surfaces:         surfacesToProto(m.Surfaces),
	}
}

// managedConfigFromProto rebuilds an agent.ManagedConfig from its proto form on
// the plugin side. Returns nil for nil input.
func managedConfigFromProto(m *ManagedConfig) *agent.ManagedConfig {
	if m == nil {
		return nil
	}
	return &agent.ManagedConfig{
		Commands:         commandExportsFromProto(m.GetCommands()),
		Skills:           skillExportsFromProto(m.GetSkills()),
		Hooks:            hooksConfigFromProto(m.GetHooks()),
		BundleMCP:        mcpServerMapFromProto(m.GetBundleMcp()),
		ManageStatusline: m.GetManageStatusline(),
		DenyTools:        m.GetDenyTools(),
		Surfaces:         surfacesFromProto(m.GetSurfaces()),
	}
}

// --- command exports ---

func commandExportsToProto(in []agent.CommandExport) []*CommandExport {
	if len(in) == 0 {
		return nil
	}
	out := make([]*CommandExport, len(in))
	for i, c := range in {
		out[i] = &CommandExport{
			Name:         c.Name,
			Content:      c.Content,
			Enabled:      c.Enabled,
			Description:  c.Description,
			ArgumentHint: c.ArgumentHint,
			AllowedTools: c.AllowedTools,
			Model:        c.Model,
		}
	}
	return out
}

func commandExportsFromProto(in []*CommandExport) []agent.CommandExport {
	if len(in) == 0 {
		return nil
	}
	out := make([]agent.CommandExport, 0, len(in))
	for _, c := range in {
		if c == nil {
			continue
		}
		out = append(out, agent.CommandExport{
			Name:         c.GetName(),
			Content:      c.GetContent(),
			Enabled:      c.GetEnabled(),
			Description:  c.GetDescription(),
			ArgumentHint: c.GetArgumentHint(),
			AllowedTools: c.GetAllowedTools(),
			Model:        c.GetModel(),
		})
	}
	return out
}

// --- skill exports ---

// skillExportsToProto mirrors commandExportsToProto for whole skill PACKAGES:
// each export carries its entire materialized tree (SKILL.md plus siblings)
// with per-file modes, because the exec bit on a scripts/ entry is part of what
// the writer must reproduce.
func skillExportsToProto(in []agent.SkillExport) []*SkillExport {
	if len(in) == 0 {
		return nil
	}
	out := make([]*SkillExport, len(in))
	for i, s := range in {
		out[i] = &SkillExport{
			Name:        s.Name,
			Description: s.Description,
			Enabled:     s.Enabled,
			Files:       packageFilesToProto(s.Files),
		}
	}
	return out
}

func skillExportsFromProto(in []*SkillExport) []agent.SkillExport {
	if len(in) == 0 {
		return nil
	}
	out := make([]agent.SkillExport, 0, len(in))
	for _, s := range in {
		if s == nil {
			continue
		}
		out = append(out, agent.SkillExport{
			Name:        s.GetName(),
			Description: s.GetDescription(),
			Enabled:     s.GetEnabled(),
			Files:       packageFilesFromProto(s.GetFiles()),
		})
	}
	return out
}

func packageFilesToProto(in []agent.PackageFile) []*PackageFile {
	if len(in) == 0 {
		return nil
	}
	out := make([]*PackageFile, len(in))
	for i, f := range in {
		out[i] = &PackageFile{
			RelPath: f.RelPath,
			Content: f.Content,
			Mode:    uint32(f.Mode),
		}
	}
	return out
}

func packageFilesFromProto(in []*PackageFile) []agent.PackageFile {
	if len(in) == 0 {
		return nil
	}
	out := make([]agent.PackageFile, 0, len(in))
	for _, f := range in {
		if f == nil {
			continue
		}
		out = append(out, agent.PackageFile{
			RelPath: f.GetRelPath(),
			Content: f.GetContent(),
			Mode:    os.FileMode(f.GetMode()),
		})
	}
	return out
}

// --- hooks ---

func hookToProto(h wire.Hook) *Hook {
	return &Hook{
		Matcher:         h.Matcher,
		Command:         h.Command,
		Type:            h.Type,
		Prompt:          h.Prompt,
		Timeout:         int32Clamped(h.Timeout),
		Async:           h.Async,
		Scm:             h.SCM,
		PreToolFallback: h.PreToolFallback,
	}
}

func hookFromProto(h *Hook) wire.Hook {
	if h == nil {
		return wire.Hook{}
	}
	return wire.Hook{
		Matcher:         h.GetMatcher(),
		Command:         h.GetCommand(),
		Type:            h.GetType(),
		Prompt:          h.GetPrompt(),
		Timeout:         int(h.GetTimeout()),
		Async:           h.GetAsync(),
		SCM:             h.GetScm(),
		PreToolFallback: h.GetPreToolFallback(),
	}
}

func hooksToProto(hs []wire.Hook) []*Hook {
	if len(hs) == 0 {
		return nil
	}
	out := make([]*Hook, len(hs))
	for i, h := range hs {
		out[i] = hookToProto(h)
	}
	return out
}

func hooksFromProto(hs []*Hook) []wire.Hook {
	if len(hs) == 0 {
		return nil
	}
	out := make([]wire.Hook, 0, len(hs))
	for _, h := range hs {
		out = append(out, hookFromProto(h))
	}
	return out
}

func unifiedHooksToProto(u wire.UnifiedHooks) *UnifiedHooks {
	return &UnifiedHooks{
		PreTool:      hooksToProto(u.PreTool),
		PostTool:     hooksToProto(u.PostTool),
		SessionStart: hooksToProto(u.SessionStart),
		SessionEnd:   hooksToProto(u.SessionEnd),
		PreShell:     hooksToProto(u.PreShell),
		PostFileEdit: hooksToProto(u.PostFileEdit),
		TurnEnd:      hooksToProto(u.TurnEnd),
	}
}

func unifiedHooksFromProto(u *UnifiedHooks) wire.UnifiedHooks {
	if u == nil {
		return wire.UnifiedHooks{}
	}
	return wire.UnifiedHooks{
		PreTool:      hooksFromProto(u.GetPreTool()),
		PostTool:     hooksFromProto(u.GetPostTool()),
		SessionStart: hooksFromProto(u.GetSessionStart()),
		SessionEnd:   hooksFromProto(u.GetSessionEnd()),
		PreShell:     hooksFromProto(u.GetPreShell()),
		PostFileEdit: hooksFromProto(u.GetPostFileEdit()),
		TurnEnd:      hooksFromProto(u.GetTurnEnd()),
	}
}

func hooksConfigToProto(c *wire.HooksConfig) *HooksConfig {
	if c == nil {
		return nil
	}
	out := &HooksConfig{Unified: unifiedHooksToProto(c.Unified)}
	if len(c.Plugins) > 0 {
		out.Plugins = make(map[string]*BackendHooks, len(c.Plugins))
		for name, bh := range c.Plugins {
			events := make(map[string]*HookList, len(bh))
			for event, hooks := range bh {
				events[event] = &HookList{Hooks: hooksToProto(hooks)}
			}
			out.Plugins[name] = &BackendHooks{Events: events}
		}
	}
	return out
}

func hooksConfigFromProto(c *HooksConfig) *wire.HooksConfig {
	if c == nil {
		return nil
	}
	// Match the substrate's assembled shape: a non-nil Plugins map even when empty.
	out := &wire.HooksConfig{
		Unified: unifiedHooksFromProto(c.GetUnified()),
		Plugins: make(map[string]wire.BackendHooks),
	}
	for name, bh := range c.GetPlugins() {
		events := make(wire.BackendHooks)
		for event, list := range bh.GetEvents() {
			events[event] = hooksFromProto(list.GetHooks())
		}
		out.Plugins[name] = events
	}
	return out
}

// --- MCP ---

func mcpServerToProto(s wire.MCPServer) *MCPServer {
	return &MCPServer{
		Command:      s.Command,
		Args:         s.Args,
		Env:          s.Env,
		Notes:        s.Notes,
		Installation: s.Installation,
		Scm:          s.SCM,
	}
}

func mcpServerFromProto(s *MCPServer) wire.MCPServer {
	if s == nil {
		return wire.MCPServer{}
	}
	return wire.MCPServer{
		Command:      s.GetCommand(),
		Args:         s.GetArgs(),
		Env:          s.GetEnv(),
		Notes:        s.GetNotes(),
		Installation: s.GetInstallation(),
		SCM:          s.GetScm(),
	}
}

// mcpServerMapToProto converts a bare name→server map (the bundle MCP set) to
// its proto form.
func mcpServerMapToProto(in map[string]wire.MCPServer) map[string]*MCPServer {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]*MCPServer, len(in))
	for name, s := range in {
		out[name] = mcpServerToProto(s)
	}
	return out
}

func mcpServerMapFromProto(in map[string]*MCPServer) map[string]wire.MCPServer {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]wire.MCPServer, len(in))
	for name, s := range in {
		out[name] = mcpServerFromProto(s)
	}
	return out
}

// --- surface preference ---
//
// Carried as the stable lowercase LABELS, never enum numbers: the wire stays
// readable, and a label the receiver does not know fails loudly through the
// Parse* functions rather than resolving to iota 0 — which for both enums is
// the least safe value (SurfaceContext, ApproachUnsafeFile).

func surfacesToProto(in map[agent.SurfaceKind]agent.Approach) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, a := range in {
		out[k.String()] = a.String()
	}
	return out
}

// surfacesFromProto drops a pair it cannot parse, with a warning, rather than
// failing the whole launch: a preference is an optimisation over the engine's
// default, so an unreadable one costs the caller its preference and not its
// session. Silence is what is refused — an agent that quietly ran with a
// different delivery than it asked for is the defect this field exists inside.
func surfacesFromProto(in map[string]string) map[agent.SurfaceKind]agent.Approach {
	if len(in) == 0 {
		return nil
	}
	out := make(map[agent.SurfaceKind]agent.Approach, len(in))
	for name, approach := range in {
		k, err := agent.ParseSurfaceKind(name)
		if err != nil {
			clidiag.Warn("ctxloom", "agent surface preference: %v (using the engine default for it)", err)
			continue
		}
		a, aerr := agent.ParseApproach(approach)
		if aerr != nil {
			clidiag.Warn("ctxloom", "agent surface preference for %s: %v (using the engine default)", name, aerr)
			continue
		}
		out[k] = a
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
