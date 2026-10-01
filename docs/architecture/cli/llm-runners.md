# `ctxloom llm` — engine labels; `ctxloom runner` — the one runner process

`ctxloom llm` manages the configured engine labels (`internal/adapters/cli`,
`llm*.go`): `llm list`, `llm default`, `llm create`, `llm edit`, `llm remove`.
A label names an engine type and its options in `.ctxloom/config.yaml`
(`llm.configs.<label>`); `llm_resolve.go` decodes a label's backend config
for the launch. There is no runner transport under this noun any more.

The runner is ONE process, `ctxloom runner <engine>` (`runner.go`, hidden;
`runner_deps.go` composes its dependencies; the body is
`internal/adapters/runner.Main`): the coordinator starts it for every launch
— the session owner's own run included — with the reach-back trio on its
env, it dials home, receives its Launch over `StartRun`, binds the session's
MCP endpoint (`internal/adapters/runner/interaction`), delivers the launch into the
session's home and drives the engine. On the host it runs on a pty the
originator holds (`internal/adapters/hostpty`); in a container it is the
container's foreground (`internal/adapters/attach`). The runner holds no
config: what it serves comes off the Launch.

See `docs/architecture/agentcoord/overview.md` (process topology),
`docs/architecture/cli/run.md` (the session host) and
`docs/architecture/cli/mcp.md` (the MCP surface).
