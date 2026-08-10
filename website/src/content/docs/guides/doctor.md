---
title: "Diagnosing a Setup: ctxloom doctor"
---

Something is subtly broken — a hook didn't fire, an agent silently fell back to a profile you didn't expect, `ctxloom review` refused with a cryptic signing error — and the honest first step is usually a real session: run it, watch it fail somewhere downstream, and guess which of a dozen moving pieces (a missing binary, an unresolved agent, an unregistered hook, an empty trust store) was actually the cause. That's slow, and the failure you see is rarely next to the thing that caused it.

`ctxloom doctor` collapses that guesswork into one deterministic pass over the same pieces: PATH binaries, whether every configured agent actually resolves, whether hooks and MCP registration are wired for each configured engine, whether the trust store has signers, whether a signing key and git identity resolve, whether the seeded dependency lockfile parses, and whether a real context assembly succeeds end to end. Run it before you file a bug, before you ask an assistant "why isn't this working," or right after `ctxloom init` to confirm setup actually landed.

```
ctxloom doctor
```

## Why one command, not a checklist

Every check emits a line prefixed with a shared `DOCTOR-CHECK-*` marker — the same vocabulary the `ctxloom-doctor` Agent Skill uses. That's deliberate: a human staring at terminal output and an LLM you've asked to triage the same report are reading the same language, not two different ones that have to be reconciled by hand. If you paste doctor's output to an assistant, it can reason about `DOCTOR-CHECK-HOOKS-TRUST-d4: warn` exactly the way you would.

Doctor is diagnostic only — it always exits `0` and never blocks or changes anything. A `warn` status *is* the fail-loud signal here; read the report, don't script against the exit code.

## What it checks

Run with no flags on an already-set-up project and doctor runs the full set:

- **`DOCTOR-CHECK-SETUP-MARKER-e5`** — the `.ctxloom` marker directory is present and config loaded without error
- **`DOCTOR-CHECK-DEPS-a1`** — `git` and a container runtime are on `PATH` (required); each configured engine's native client (`claude`, `codex`, `kiro-cli`, `agy`, `opencode`) is on `PATH` (required); `ssh` and `ssh-keygen` are present (recommended — `ssh` is what `git` itself needs for an `ssh://` remote, `ssh-keygen` is only for generating a *new* signing key by hand; ctxloom's own signing is pure Go over the ssh-agent protocol and never execs either)
- **`DOCTOR-CHECK-SIGNKEY-k1`** — a signing identity resolves via the exact resolver `ctxloom review`'s approve path and `ctxloom bundle sign`/`--sign` use (explicit `sign.key`, then `git config user.signingkey`, then ssh-agent's sole identity)
- **`DOCTOR-CHECK-GITIDENT-l2`** — `git config user.name` and `user.email` both resolve, because agents ctxloom launches commit their own work inside isolated worktrees, and an unset identity means a commit fails or gets silently mis-attributed to whatever the OS account derives
- **`DOCTOR-CHECK-ACPADAPTER-m3`** — for every configured `claude-code`/`codex` engine (the two that need one), the separate npm-installed ACP adapter binary (`claude-code-acp`, `codex-acp`) is on `PATH`; a missing adapter hard-fails host-runtime structured chat, but containerized agents carry their own adapter in-image and aren't affected
- **`DOCTOR-CHECK-AGENTS-b2`** — every configured agent resolves (profile composition + engine/runtime), and the roster isn't empty
- **`DOCTOR-CHECK-VERSION-c3`** — informational only: reports the running version; comparing it against the newest remote tag is left to the `ctxloom-doctor` skill or a human, since there's no built-in update check yet
- **`DOCTOR-CHECK-HOOKS-TRUST-d4`** — hooks and MCP registration per configured backend (the same read `ctxloom manage check` exposes), plus how many active signers the trust store carries
- **`DOCTOR-CHECK-MCP-INVOCATION-g7`** — reads the ctxloom entry inside every engine's own MCP registry (`.mcp.json`, `.agents/mcp_config.json`, `.kiro/settings/mcp.json`, `.codex/config.toml`, `opencode.json`) and names any that invokes a ctxloom subcommand which doesn't speak the protocol. This is the one broken state nothing else can see: the entry is *present*, so every wiring check calls it healthy and the engine starts fine — but the client waits on a handshake that never arrives, and the session comes up with none of ctxloom's tools. Re-run `ctxloom init` to rewrite the settings
- **`DOCTOR-CHECK-CONTENT-TRUST-n4`** — names any remote bundle whose content this machine can't attribute to a publisher it trusts, so its content is being withheld from your assistant. Something you can act on locally: trust the key, or ask the publisher to sign
- **`DOCTOR-CHECK-UPSTREAM-SIGNATURES-o5`** — names any revision `ctxloom remote upgrade` *refused* to advance onto, because the publisher's signature at that commit doesn't cover the bytes beside it, along with the pin you're being kept at instead. Nothing is wrong on your machine and nothing is withheld — you're being served the last content that verified — so this is the one check whose fix isn't yours: the publisher has to re-sign and republish. It clears itself the next time an upgrade advances that pin
- **`DOCTOR-CHECK-SETUP-DEPS-h8`** — the seeded dependency lockfile parses, and a real context assembly succeeds for the configured default profile(s)
- **`DOCTOR-CHECK-SETUP-COMPANIONS-i9`** — companion detection and loadout probing (taskloom, ltk, ...); absence is informational, not a warning, since companions are optional
- **`DOCTOR-CHECK-SETUP-AUTHPING-j0`** — informational placeholder: there's no deterministic pre-launch auth ping yet, so this line names that gap explicitly rather than staying silent about it

Deliberately out of scope: doctor never parses a third-party ACP client's own config (Zed settings, a VSCode `acp-client` config, Toad, ...). Verifying that a specific client is wired correctly is that client's own job — ctxloom stays unbound to any one frontend.

## `--deps`: before a project is even set up

```
ctxloom doctor --deps
```

Running the full report against a brand-new, never-initialized project is a wall of expected-missing state — no agents, no profiles, no hooks — that would needlessly alarm you at the very start of setup. `--deps` scopes the report to just the machine-capability questions that are true or false regardless of setup state: `DEPS-a1`, `SIGNKEY-k1`, `GITIDENT-l2`, and `ACPADAPTER-m3`. This is the mode `ctxloom init`'s PRIME phase and the setup skill's phase 1 use internally, before there's anything else to check.

## `--format json`

```
ctxloom --format json doctor
```

The same `DOCTOR-CHECK-*` markers, structured as `{"checks": [{"marker": ..., "status": ..., "detail": ...}, ...]}` — for scripting a health check into CI, a pre-flight step, or anything else that wants to parse doctor's output rather than grep it.

## See also

- [`ctxloom doctor` CLI reference](/reference/cli/ctxloom_doctor/) — flags and full generated help text
