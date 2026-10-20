# Architecture — companion and session subsystems

These pages describe the parts of ctxloom that either **ship as their own binaries** (`ltk`,
`taskloom`) or that own the **session lifecycle around a run** (transcript capture and the
session index), plus the small leaf packages the CLIs are built on.

Each page opens with what the subsystem **is** and the **contract it owns**, then gives a
structure diagram, an inventory of the exported types and functions that matter with `file:line`
references, and an explicit **invariants** section split into what holds and what diverges from
its own documentation.

## Pages

| Page | Subsystem | What it owns |
|---|---|---|
| [ltk.md](ltk.md) | `cmd/ltk` + `internal/ltk/{app,engine,frontend,frontend/shell,frontend/pwsh,frontend/cmd,ir,rules,state,scm,shellenv,tools/extract-defaults}` | The command guard: a PreToolUse hook that parses a tool call into a shell-agnostic IR, matches it against a YAML rule file, and emits an allow-or-deny decision in the harness's own wire format |
| [taskloom.md](taskloom.md) | `cmd/taskloom` + `internal/taskloom/{config,engine,workdir}` | Per-project task tracking over an append-only harp-keyed log, exposed as both a CLI and an MCP server, with its own config surface and project-root resolution that do not depend on ctxloom |
| [transcript.md](transcript.md) | `internal/adapters/transcript` + `internal/adapters/transcript/vendorreader` and its per-engine readers | ctxloom's own canonical conversation record: the versioned append-only JSONL schema, the writer every capture path shares, the reader, and the vendor-format readers |
| [sessions.md](sessions.md) | `internal/core/sessions` | The harp-keyed session store (one `session.yaml` sidecar per session directory) binding harp → backend session ID → project dir → transcript path, and the transcript-retention rule every destroyer of transcripts asks, behind a two-adapter storage port |
| [hostpty.md](hostpty.md) | `internal/adapters/hostpty` + `internal/adapters/attach` | The pty the originator holds around `ctxloom runner`: a host process (hostpty) or `docker run -i -t … ctxloom runner` as a container's foreground (attach) |
| [docsgen.md](docsgen.md) | `internal/shared/docsgen` | Deterministic generation of man pages, per-command markdown, an MCP tool page, and a config page from a product's live cobra tree, live MCP registrations, and tracked JSON Schema |
| [selfexec.md](selfexec.md) | `internal/adapters/selfexec` | Resolving the path of the running ctxloom binary, so a materialized engine surface names the binary that materialized it |
| [clifmt.md](clifmt.md) | `pkg/clifmt` (+ `cobrafmt`, `clidiag`) | The CLI output library: one data contract rendered as json / yaml / toml / text / markdown, custom views as options, and the cobra adapter for `--format` and the process tail |

## How these fit together

```mermaid
flowchart TD
    subgraph binaries["Independent binaries"]
      LTK["ltk<br/>PreToolUse command guard"]
      TL["taskloom<br/>task tracking (CLI + MCP)"]
    end

    subgraph run["A ctxloom run"]
      SESS["internal/core/sessions<br/>mint harp → bind session ID"]
      TERM["internal/adapters/cli/tui<br/>the split view"]
      VP["internal/adapters/hostpty · attach<br/>the runner on a pty"]
      TR["internal/adapters/transcript<br/>capture"]
    end

    subgraph leaves["Shared leaves"]
      CF["pkg/clifmt"]
      SE["internal/adapters/selfexec"]
      DG["internal/shared/docsgen"]
    end

    SESS -->|"harp"| TR
    SESS -->|"harp"| TERM
    TR -->|"transcript.jsonl: the session's title and record"| SESS
    VP --> TERM
    TERM -->|"keys · resize; the pane takes its output"| VP

    LTK -->|"loadout.yaml, once registered (companion add)"| CTX["ctxloom"]
    TL -->|"loadout.yaml, once registered (companion add)"| CTX
    TL -.->|"reuses internal/ltk/engine's<br/>hooks.json merge machinery"| LTK

    CF --> LTK & TL & CTX
    SE -->|"the path baked into every<br/>materialized engine surface"| CTX
    DG --> LTK & TL & CTX
```

## Recurring shapes worth knowing before reading any single page

Three patterns show up in more than one of these subsystems, and knowing them makes each page
shorter to read:

1. **The type has nowhere to put the bad news.** `ltk`'s `engine.Response` has no "unanalyzed"
   state, so a parse failure is encoded as an allow. `vendorreader.VendorAdapter.Convert` returns only
   `error` with no count, so "the format drifted and I recognized nothing" is encoded as success.
   In each case the diagnostic exists at the point of failure and is destroyed by the signature
   it has to pass through.

2. **A guard whose precondition is checked at the call site, not in the writer.**
   `sessions.BindSession` accepts an empty session ID and the guard lives in
   `internal/adapters/operations`, reachable through a second caller that does not replicate it.
   A sibling instance (a summary writer erasing fields unconditionally while its caller held
   the non-empty guard) was **RESOLVED `07abd892`** by moving the refusal into the writer, and
   later deleted with session compaction. The pattern is left described here
   because `BindSession` still has it, and because the fix is the pattern's answer:
   move the guard to the writer rather than replicating it at each call site.

3. **Two individually-correct decisions composing into the failure both were written to prevent.**
   The clearest instance is in `transcript`: the `Recorder`'s lazy file open is documented as
   *upholding* the silent-no-op discipline (no events ⇒ no file, so "absent" means "nothing was
   recorded"), and a presence-only "already imported" guard over the canonical file was a
   reasonable idempotency check — composed, a zero-entry import reported success and was retried
   forever. That guard is gone (`operations.RefreshVendorTranscript` rebuilds unconditionally and
   reports a rebuild that wrote nothing as not converted); the shape is left described here
   because it is the one to look for wherever an existence check stands in for a completeness
   check.

## Scope note

These pages describe **structure and contract**. Defects, severities and remediation are assembled
separately in `FINDINGS.md`; where a page notes that documented and real behaviour diverge, it
records the real behaviour as a one-line factual note and nothing more.
