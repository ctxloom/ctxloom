# The `Backend` abstraction and the engine registry

An engine is an `engine.Engine` value (`internal/core/engine`): its
`Definition` — one typed approach per surface kind, the modes and their argv
grammars, the permission facts, the export schema, the version command
(`Definition.Version`) and the hook events it declares it cannot carry
(`Definition.HookLosses`) — plus the instance half (`Instance`, `Home`,
`Container`, `Transcripts`, `Hooks`, `Exports`). Each engine package builds
its kind in ONE plain constructor (`claude.Build`, `mock.Build`); the port
and the conformance suite (`core/engine/conformance`) are the contract.

## Composition, not registration

`internal/engines` is the composition root: `Build()` composes the shipped
kinds into an `engine.Registry` value, handing each kind the adapters an
engine package must not import (the readers of its own transcript store,
`claude.WithTranscripts`; the reading of `claude --version`,
`claude.WithVersion`). `Compose()` does it once per process and `Registry()`
is what every adapter that resolves an engine by NAME reads —
`Registry().Lookup(name)` by EXACT match, `Registry().Names(keep)` over the
Definitions, `engines.NamesWhere(keep)` over the values. There is no
package-level table anywhere else and no `init()`: adding an engine is
creating its package, writing its constructor, and adding it to `Build`.

The cells adapter reads engine facts by name through `isolation.Facts`;
the root that composed installs `isolation.RegistryFacts{Registry}` beside
the registry (`cli.composeEngines`; a TestMain uses
`enginefixture.MustComposeShipped()`). A test that stands a synthetic engine
in front of the adapters composes a mock kind under its own name
(`enginefixture.Kind`) and installs it with `enginefixture.Install`
(`engines.Use` + the facts), restored at cleanup.

## What `agent.Backend` still is, and where its remainder lives

`agent.Backend` (`internal/core/agent`) is the seam the interactive launch and
the legacy one-shot still drive: `Execute` runs the engine's process over the
injected `agent.Launcher` — the runner's `runner.RunLaunchSpec`, a pty
(`ptyrunner.RunInteractive`) for an interactive launch, pipes otherwise. The
structured drive does NOT go through it: a headless turn is the port's
`Instance.Drivers()[0].Turn` over `Instance.Exec`.

What a `Backend` needs that the port does not carry is `agent.Hosted`,
implemented by the engine VALUE (`claude.Claude`, `mock.Mock`) and asserted by
the adapters on the registry's value (`engines.Hosted(name)`):

| `agent.Hosted` | what it is for | read by |
|---|---|---|
| `Backend(Launcher) Backend` | a fresh backend over the runner's launcher | `cli.runRunner` (the interactive launch), `operations.HistoryForBackend` |
| `NewConfig() BackendConfig` | the zero typed config a labeled LLM entry's body decodes into | `operations.DecodeEngineConfig` |
| `Declaration() Declaration` | the named-form table a binding's `surfaces:` is validated against | `operations.ResolveAgentSurfaces`, `operations.KnownApproachNames` |
| `SettingsWriter(SettingsOptions) SettingsWriter` | the writer whose `Status` `manage status` reports and whose `RemoveSettings` strips ctxloom's wiring | `operations.engineSettingsStatus` |
| `HookGlobalScope() (HookGlobalScope, bool)` | the project/global settings-path collision `manage hooks install` refuses | `operations.checkHookTargetScopeOf` |

The L1 process-surface grammar (`agent.EngineCLI`: binary, flags, prompt
delivery, env, probes — richer than the port's `CLIGrammar`) is
`agent.EngineCLIProvider` on the same value; `engines.EngineCLIs(name)` is
how the standalone mock engine (`cmd/mockengine`) impersonates a vendor CLI
from the SAME declaration the driver reads. `TestBuild_EveryShippedEngineIsHosted`
(`internal/engines`) pins that every shipped kind carries the remainder.

Every shipped engine is `Hosted`, so "the engines with a settings writer"
is every composed engine; "shippable" is `Distribution != TestOnly`, read off
the Definition, never a name.

## The name-keyed reads

The adapters' name-keyed questions are `operations`' (`engine_*.go`):
`EngineExists`, `EngineNames`, `EngineNamesWhere`, `DefaultEngineName`,
`IsTestOnlyEngine`, `EffectivePosture`/`PostureName`/`PostureNames` (named
by the engine's permission model), `EngineBinary` (the interactive
grammar's binary), `EngineAvailability`/`EngineAvailable` (resolved on PATH
or the login-shell PATH), `ProbeEngineVersion` (the shared cached prober
over `Definition.Version`), `DecodeEngineConfig`, `KnownApproachNames`. None
branches on an engine's name: `tests/arch`'s `no-engine-name-in-core` gate
holds that.

## The doubles

`mock.Doubles` composes the mock kind and its three doubles — each the same
kind under another name with ONE declared difference (`mock.NameLossy`
drops two hook events, declared in `Definition.HookLosses`;
`mock.NameLaunch` keeps only its context surface; `mock.NameNoSkills`
exports no skill). Their backend, typed config (`mock.Config`, naming its
own double), settings writer and named forms are the mock package's own.
The mock is a COMPLETE engine with no model behind it, deliberately: a gap
in the double is a gap something quietly comes to depend on.
