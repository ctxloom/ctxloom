# 0029 — Every data-emitting CLI command offers `--json` for jq

**Date:** 2026-06-02.

## Status

Accepted.

## Context

ctxloom is a CLI, and CLIs get composed into pipelines. The default human view
is a formatted table — aligned columns, a leading `[x]` check, status names that
contain spaces (`In Progress`). That reads well but parses badly: a script
splitting on whitespace breaks on the space inside a status, and column
alignment shifts with content width. Forcing callers to parse the human table
makes the table format a de-facto API, so it can never be improved for humans
without breaking scripts.

The operations layer already returns structured result types (ADR
[0019](0019-cli-pure-frontend.md)), and the MCP surface serializes those to JSON.
The CLI was throwing that structure away at the last step and printing text.

## Decision

**Every CLI command that emits data offers a `--json` flag that writes JSON (or
JSON Lines) to stdout, suitable for piping into `jq`.** The human-readable
table/text stays the default; `--json` is the machine contract.

Rules:

- **JSON to stdout, everything else to stderr.** Warnings, progress, and
  diagnostics go to stderr so `--json` stdout is a clean, parseable stream. A
  command's `--json` output must contain nothing but the JSON.
- **The JSON shape is the operations result type**, the same structure the MCP
  layer returns. One data contract across frontends — CLI `--json` and MCP can
  never drift, because they serialize the same structs.
- **Lists may emit a JSON array or JSON Lines.** JSONL (one object per line) is
  preferred for large or streaming sets (`jq -c` per line, line-oriented tools);
  a bounded result may use an array. Either way it is valid for `jq`.
- **The JSON is the stable contract; the table is not.** The human table is free
  to change for readability (column widths, ordering, decoration) precisely
  because nobody should be parsing it — `--json` is there for that.

`task list` is the first command brought to this bar: `--json` emits the task
array; warnings go through stderr; the table is free to use a fixed-width id
column for the eye.

## Consequences

- ctxloom is scriptable without brittle text parsing: `ctxloom <cmd> --json | jq`
  is the supported path.
- There is one data contract per operation, shared by the CLI's `--json` and the
  MCP tool result — neither can silently diverge from the other.
- Table rendering is freed to optimize for humans, since it is no longer an
  implicit parsing target.
- Each data-emitting command carries the small cost of a `--json` branch and a
  serializable result. Purely side-effecting commands (e.g. `remote add`) need
  not add `--json`, but when their confirmation is worth scripting against they
  should emit a structured result the same way rather than parseable prose.

**Revive trigger:** if a command's `--json` ever needs content on stdout that is
not the JSON (a prompt, a progress bar), that is the signal it has mixed concerns
— move the non-JSON to stderr rather than weakening the "stdout is only JSON"
guarantee that makes the pipe reliable.
