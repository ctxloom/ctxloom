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
// Today it holds the per-turn process driver (ProcessTurn over a Transport)
// and the Exec composition every engine's Instance.Exec starts from
// (ComposeEnv, PresentedArgs).
package kit
