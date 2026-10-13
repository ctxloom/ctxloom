// Package kit holds the shared parts every engine assembles: scaffolding
// with a hole for the engine's own content, never the content itself. An
// engine receives a component and hands it the engine-specific part as a
// value (a func, a name, a mapper); a component never branches on which
// engine it serves.
//
// Two rules keep it from becoming a dumping ground:
//
//   - kit imports no engine package and spells no engine's name (both
//     arch-guarded: the enginekit-imports-no-engine layering rule, and the
//     no-engine-name scan in tests/arch, which treats kit as a non-home);
//   - a component enters kit only when two engines use it in the same slice
//     (docs/architecture/engines/README.md, DECISIONS.md).
//
// Today it holds the per-turn process driver (ProcessTurn over a Transport),
// the Exec composition every engine's Instance.Exec starts from (ComposeEnv,
// PresentedArgs), the backend projected onto the kind (ProjectedBackend),
// a permission model's --degraded fallback to its floor (ResolveDegraded),
// and the static delivery scaffolding every typed approach assembles from:
// its name, traits and rooting rule (Approach), the appended-section context
// claim (AppendedSection), the framed context file (FramedFile), the MCP
// server table (MCPTable), the managed command and skill trees
// (DeliverCommands, DeliverSkills) with the Agent Skills rules
// (AgentSkillRules), the export block decoder (ExportDecoder), and the
// interactive parts: the unified⇄native hook event table (HookEvents),
// ctxloom's own hook wire for an engine whose hooks ctxloom fires itself
// (NativeHookCodec), and the unix-socket wake (SocketWake).
package kit
