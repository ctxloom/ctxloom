# ctxloom Reference Grammar

The canonical specification for how ctxloom references are spelled and
resolved. When behavior and this document disagree, one of them is a bug —
the entry-point tests in `internal/profiles/grammar_test.go` and
`internal/remote/profile_selector_test.go` pin the rules below.

## Client compatibility — canonical refs require ctxloom 0.7

The canonical `ctxloom+<class>:` grammar (`ctxloom+git://host/owner/repo//bundles/<b>`)
is parsed only by ctxloom 0.7 and later. `internal/refuri` does not exist in 0.6.x,
and nothing there dispatches on the scheme.

The published bundle repositories address their bundles that way as of 2026-08-19,
so a client older than 0.7 that advances its pins onto that content resolves a
SMALLER dependency closure — measured at 9 bundles down to 2 — and says nothing
about it: same exit code, same "Pulled N items" line, less context. Existing pins
never move on their own, so the effect starts at `deps upgrade`, not at install.

Upgrade the client before upgrading pins. The retired `<canonical-url>@bundles/<b>`
spelling below is still accepted by 0.7 for input; it is what the lockfile keys
render, and it is not going away in this release.

## Building blocks

```
<canonical-url>   https://github.com/owner/repo        (also git@, file://)
<bundle-ref>      <canonical-url>@bundles/<bundle>     canonical remote bundle
                  ctxloom:local@bundles/<bundle>       canonical local bundle
                  <bundle>                             plain local bundle name
                  <alias>/<bundle>                     bundle via a configured remote
<selector>        #fragments/<name> | #commands/<name> | #skills/<name> | #mcp/<name>
                  | #hooks/<event>/<n> | #profiles/<name>
<version>         @<sha> | @<tag> | @<semver-range> | @<branch>
```

`#` is a reserved character: it always introduces a bundle-item selector and
can never appear in a profile name or bundle name. A ref containing
`#profiles/` is therefore *structurally* a bundle-shipped profile reference,
never a local file.

## Profile references

Accepted wherever a profile is named (`run -p`,
`--parent`, `agents:` profile lists (including the default agent's), `acp --profile`,
MCP `assemble_context`):

| Spelling | Meaning |
|----------|---------|
| `developer` | Local profile `.ctxloom/profiles/developer.yaml` |
| `personal/go-developer` | Local profile in a subdirectory (`profiles/personal/go-developer.yaml`) |
| `tools#profiles/probe` | Profile shipped by the **local** bundle `tools` |
| `<alias>/<bundle>#profiles/<name>` | Profile shipped by a bundle from the configured remote `<alias>` |
| `<canonical-url>@bundles/<bundle>#profiles/<name>` | Same, fully qualified |
| any of the above `@<sha>` | Version pins are accepted and ignored for identity (the lockfile pins the bundle) |

Resolution rules:

1. **A selector-less name is always local.** Two-segment names like
   `personal/go-developer` are subdirectory paths, never remote aliases.
   Nothing ever expands a bare parent or `-p` name against a remote — this is
   deliberate, so adding a remote can never change the meaning of an existing
   local name.
2. **A `#profiles/` ref resolves through the bundle-profile seed**: alias
   spellings resolve through the remote registry to the same canonical
   identity as the URL spelling; local bundle names canonicalize to
   `ctxloom:local`. A seed miss reports "bundle profile has no lockfile entry
   — run 'ctxloom deps pull'".

## Bundle references

Accepted in `-b/--bundle`, a profile's `bundles:`/`bundle_items:` lists:

| Spelling | Meaning |
|----------|---------|
| `<canonical-url>@bundles/<name>` | Remote bundle, fully qualified |
| `ctxloom:local@bundles/<name>` | Local bundle, fully qualified |
| `<name>` | Bare ref: expands against the profile's own remote on load, or the default remote at `profile create` |
| `<alias>/<name>` | Bundle from the configured remote `<alias>` (canonicalized on profile load) |

Bundle refs may carry an item selector (`-b bundle#fragments/tdd` cherry-picks
one fragment) and a `@<version>` constraint (semver range, tag, SHA, or
branch; empty means the default branch). Constraints live in profile YAML;
the lockfile records the resolved SHA.

## Item references

`fragment`/`command`/`skill` CLI commands and `-f` accept
`<bundle-ref>#<kind>/<name>`. A bare fragment name in `-f` is searched across
installed bundles (deterministic pick with a warning on collision).
`trust`/`blacklist` refs use the same shape and additionally accept
`@<commit>` as provenance.

`#commands/<name>` and `#skills/<name>` name two DIFFERENT item kinds that
happen to sit side by side in a bundle: a command is a user-invoked
slash-command template (plain text); a skill is a model-invoked Agent Skill
package (a `SKILL.md` directory, optionally with bundled scripts/assets) —
see GLOSSARY.md. A profile's `commands:`/`skills:` curation lists use the
same two selectors to opt into a non-default export set, mirroring each
other's shape (each entry a `<bundle>#<kind>/<name>` ref; `commands:` entries
additionally accept a trailing `@<commit>` content-version pin, which a skill
has no equivalent of).

## Where each spelling is normalized

- **Profile parents are stored as typed** — no write-time expansion; they
  resolve at read time through the rules above.
- **Bundle refs are canonicalized**: at `profile create`/`modify` (bare refs
  expand against the default remote) and on profile load (bare and alias refs
  canonicalize against the profile's remote / the registry, with a consented
  on-disk rewrite).
- **The retired top-level profile grammar** (`<url>@profiles/<name>`) is
  migrated on load to the bundle-shipped successor when exactly one installed
  bundle from that repo ships the profile; otherwise it is left verbatim and
  the resolver warns.
