---
title: "Diagnosing a Setup: ctxloom doctor"
---

Something is subtly broken — a hook didn't fire, an agent silently fell back to a profile you didn't expect, `ctxloom review` refused with a cryptic signing error — and the honest first step is usually a real session: run it, watch it fail somewhere downstream, and guess which of a dozen moving pieces (a missing binary, an unresolved agent, an unregistered hook, an empty trust store) was actually the cause. That's slow, and the failure you see is rarely next to the thing that caused it.

`ctxloom doctor` collapses that guesswork into one deterministic pass over the same pieces: PATH binaries, whether every configured agent actually resolves, whether hooks and MCP registration are wired for each configured engine, whether the trust store has signers, whether a signing key and git identity resolve, whether the seeded dependency lockfile parses, and whether a real context assembly succeeds end to end. Run it before you file a bug, before you ask an assistant "why isn't this working," or right after `ctxloom init` to confirm setup actually landed.

```
ctxloom doctor
```

## Why one command, not a checklist

Every check emits a line prefixed with a stable `DOCTOR-CHECK-*` marker. That's deliberate: a human staring at terminal output and an LLM you've asked to triage the same report are reading the same language, not two different ones that have to be reconciled by hand. If you paste doctor's output to an assistant, it can reason about `DOCTOR-CHECK-HOOKS-TRUST-d4: warn` exactly the way you would.

Doctor is diagnostic only — it always exits `0` and never blocks or changes anything. A `warn` status *is* the fail-loud signal here; read the report, don't script against the exit code.

## What it checks

Run with no flags on an already-set-up project and doctor runs every check it has. The report itself is the complete list; these are the ones you will act on most:

- **`DOCTOR-CHECK-SETUP-MARKER-e5`** — the `.ctxloom` marker directory is present and config loaded without error
- **`DOCTOR-CHECK-DEPS-a1`** — `git` and each configured engine's native client (e.g. `claude`) are on `PATH` (required); a container runtime is required only when an agent uses a container runtime; `ssh` and `ssh-keygen` are present (recommended — `ssh` is what `git` itself needs for an `ssh://` remote, `ssh-keygen` is only for generating a *new* signing key by hand; ctxloom's own signing is pure Go over the ssh-agent protocol and never execs either)
- **`DOCTOR-CHECK-SIGNKEY-k1`** — a signing identity resolves via the exact resolver `ctxloom review`'s approve path and `ctxloom bundle sign`/`--sign` use (explicit `sign.key`, then `git config user.signingkey`, then ssh-agent's sole identity). The key must be an SSH key: ctxloom signs only with SSH signatures (the sshsig format `ssh-keygen -Y sign` writes, verified against `allowed_signers`), never GPG/PGP
- **`DOCTOR-CHECK-GITIDENT-l2`** — `git config user.name` and `user.email` both resolve, because agents ctxloom launches commit their own work inside isolated worktrees, and an unset identity means a commit fails or gets silently mis-attributed to whatever the OS account derives
- **`DOCTOR-CHECK-AGENTS-b2`** — every configured agent resolves (profile composition + engine/runtime), and the roster isn't empty
- **`DOCTOR-CHECK-CAPABILITY-LOSS-u1`** — names, per configured agent, what the engine it resolves to has *no structural place for*: hooks its profiles declare that this engine can never fire (an engine may have no hook mechanism at all, or no native event for a hook kind). This is the one breakage the wiring checks are blind to by construction — they report what landed, and every line of that is true, so a hook that could never land anywhere is invisible in it. Move an agent to a different engine and your guardrail stays in your config, the binding stays valid, and it silently stops running. It's the same loss `ctxloom profile materialize` and `ctxloom agent show` report as "NOT carried", read here across the whole roster
- **`DOCTOR-CHECK-VERSION-c3`** — informational only: reports the running version; comparing it against the newest remote tag is left to you or an assistant, since there's no built-in update check yet
- **`DOCTOR-CHECK-HOOKS-TRUST-d4`** — the delivery posture per configured backend (the same read `ctxloom manage check` exposes): a `ctxloom run` session carries its own hooks and MCP in its session home, so nothing project-side is the healthy default; a backend that also has them registered in the project got them through the explicit `ctxloom manage hooks install` door. Plus how many active signers the trust store carries
- **`DOCTOR-CHECK-MCP-INVOCATION-g7`** — reads every registered engine's own project MCP registry (e.g. `.mcp.json`) and names any ctxloom entry that *launches* ctxloom as a stdio server. ctxloom ships no stdio MCP server: its tools are served by the running session's endpoint, which the session registers for itself at start, so a project-side entry that launches ctxloom is stale. This is the one broken state nothing else can see: the entry is *present*, so every wiring check calls it healthy and the engine starts fine, but the client waits on a handshake that never arrives and the session comes up with none of ctxloom's tools. Re-run `ctxloom manage hooks install` to rewrite the project's registry without the entry
- **`DOCTOR-CHECK-CONTENT-TRUST-n4`** — names any remote bundle whose content this machine can't attribute to a publisher it trusts, so its content is being withheld from your assistant. Something you can act on locally: trust the key, or ask the publisher to sign
- **`DOCTOR-CHECK-UPSTREAM-SIGNATURES-o5`** — names any revision `ctxloom deps upgrade` *refused* to advance onto, because the publisher's signature at that commit doesn't cover the bytes beside it, along with the pin you're being kept at instead. Nothing is wrong on your machine and nothing is withheld — you're being served the last content that verified — so this is the one check whose fix isn't yours: the publisher has to re-sign and republish. It clears itself the next time an upgrade advances that pin
- **`DOCTOR-CHECK-SETUP-DEPS-h8`** — the seeded dependency lockfile parses, and a real context assembly succeeds for the configured default profile(s)
- **`DOCTOR-CHECK-SETUP-COMPANIONS-i9`** — companion detection and loadout probing (taskloom, ltk, ...); absence is informational, not a warning, since companions are optional
- **`DOCTOR-CHECK-SETUP-AUTHPING-j0`** — informational placeholder: there's no deterministic pre-launch auth ping yet, so this line names that gap explicitly rather than staying silent about it

## `--deps`: before a project is even set up

```
ctxloom doctor --deps
```

Running the full report against a brand-new, never-initialized project is a wall of expected-missing state — no agents, no profiles, no hooks — that would needlessly alarm you at the very start of setup. `--deps` scopes the report to just the machine-capability questions that are true or false regardless of setup state: `DEPS-a1`, `SIGNKEY-k1` and `GITIDENT-l2`. The `ctxloom-init` setup command runs this mode in its first phase, before there's anything else to check. (`ctxloom init` itself runs a narrower dependency gate of its own.)

## `--format json`

```
ctxloom --format json doctor
```

The same `DOCTOR-CHECK-*` markers, structured as `{"checks": [{"marker": ..., "status": ..., "detail": ...}, ...]}` — for scripting a health check into CI, a pre-flight step, or anything else that wants to parse doctor's output rather than grep it.

## See also

- [`ctxloom doctor` CLI reference](/reference/cli/ctxloom_doctor/) — flags and full generated help text
