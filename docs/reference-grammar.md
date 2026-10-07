# ctxloom Reference Grammar

The canonical specification for how ctxloom references are spelled and
resolved. When behavior and this document disagree, one of them is a bug —
the entry-point tests in `internal/core/profiles/grammar_test.go` and
`internal/adapters/remote/profile_selector_test.go` pin the rules below.

## Client compatibility — canonical refs require ctxloom 0.7

The canonical `ctxloom+<class>:` grammar (`ctxloom+git://host/owner/repo//bundles/<b>`)
is parsed only by ctxloom 0.7 and later. `internal/shared/refuri` does not exist in 0.6.x,
and nothing there dispatches on the scheme.

The published bundle repositories address their bundles that way as of 2026-08-19,
so a client older than 0.7 that advances its pins onto that content resolves a
SMALLER dependency closure — measured at 9 bundles down to 2 — and says nothing
about it: same exit code, same "Pulled N items" line, less context. Existing pins
never move on their own, so the effect starts at `deps upgrade`, not at install.

Upgrade the client before upgrading pins. The accepted input spellings below
(`<canonical-url>@bundles/<b>`, `ctxloom:local@bundles/<b>`) still parse in 0.7
and are not going away in this release.

## Building blocks

```
<bundle-uri>      ctxloom+git://<host>/<owner>/<repo>//bundles/<bundle>   remote bundle
                  ctxloom+file:///<abs-repo-path>//bundles/<bundle>       bundle in a local git repo
                  ctxloom+local:<bundle>                                  bundle in this project
                  ctxloom+companion:<binary>                              a companion binary's loadout
<canonical-url>   https://github.com/owner/repo        (also git@, file://)
<bundle-ref>      <bundle-uri>                         canonical spelling
                  <canonical-url>@bundles/<bundle>     accepted input: remote bundle
                  ctxloom:local@bundles/<bundle>       accepted input: local bundle
                  <bundle>                             plain local bundle name
                  <alias>/<bundle>                     bundle via a configured remote
<selector>        #fragments/<name> | #commands/<name> | #skills/<name> | #mcp/<name>
                  | #hooks/<event>/<n> | #profiles/<name>
<version>         @[sha:|tag:|version:|branch:]<expr>   (no prefix: kind inferred from shape)
```

The `ctxloom+<class>:` scheme carries the source class, and `//` separates the
repository path from the bundle path (the go-getter convention: both halves
can be several segments deep, so no single `/` could split them). The
version-less `<bundle-uri>` is a bundle's identity and its lockfile key; the
accepted `@bundles/` input spellings parse to the same identity.
`internal/shared/refuri` owns this grammar.

`#` is a reserved character: it always introduces a bundle-item selector and
can never appear in a profile name or bundle name. A ref containing
`#profiles/` is therefore *structurally* a bundle-shipped profile reference,
never a local file.

## Profile references

Accepted wherever a profile is named (`run -p`,
`--parent`, `agents:` profile lists (including the default agent's),
MCP `assemble_context`):

| Spelling | Meaning |
|----------|---------|
| `developer` | The project bundle's profile `developer` — the same as `ctxloom:local@bundles/project#profiles/developer` |
| `tools#profiles/probe` | Profile of the **local** bundle `tools` (`ctxloom:local@bundles/tools#profiles/probe`) |
| `<alias>/<bundle>#profiles/<name>` | Profile shipped by a bundle from the configured remote `<alias>` |
| `<bundle-uri>#profiles/<name>` | Same, fully qualified (`<canonical-url>@bundles/<bundle>#profiles/<name>` is accepted input) |
| any of the above `@<sha>` | Version pins are accepted and ignored for identity (the lockfile pins the bundle) |

Resolution rules:

1. **A selector-less name is the project bundle's profile.** Every profile is
   a bundle's profile item, and a project's own profiles are the items of its
   project bundle — the local bundle named `project`
   (`.ctxloom/content/bundles/v2/project/profiles/<name>.yaml`); home uses the
   same rule under `~/.ctxloom`. A profile name is a single path segment.
   Nothing ever expands a bare parent or `-p` name against a remote — so adding
   a remote can never change the meaning of an existing name.
2. **A `#profiles/` ref resolves through the bundle-profile seed**: alias
   spellings resolve through the remote registry to the same canonical
   identity as the URL spelling, a `ctxloom+git://` URI; local bundle names
   canonicalize to `ctxloom+local:<bundle>`. A seed miss reports "bundle profile has no lockfile entry
   — run 'ctxloom deps pull'"; a miss on a local profile is a plain not-found.

## Bundle references

Accepted in `-b/--bundle`, a profile's `bundles:`/`bundle_items:` lists:

| Spelling | Meaning |
|----------|---------|
| `<bundle-uri>` | Bundle of any source class, fully qualified |
| `<canonical-url>@bundles/<name>` | Remote bundle, fully qualified (accepted input) |
| `ctxloom:local@bundles/<name>` | Local bundle, fully qualified (accepted input) |
| `<name>` | Local bundle `<name>` |
| `<alias>/<name>` | Bundle from the configured remote `<alias>` (canonicalized when written; in memory on load for a local bundle's profile) |

Bundle refs may carry an item selector (`-b bundle#fragments/tdd` cherry-picks
one fragment) and a `@<version>` constraint (semver range, tag, SHA, or
branch; empty means the default branch). Constraints live in profile YAML;
the lockfile records the resolved SHA.

## Item references

`fragment`/`command`/`skill` CLI commands and `-f` accept
`<bundle-ref>#<kind>/<name>`. A bare fragment name in `-f` is searched across
installed bundles (deterministic pick with a warning on collision).

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

- **`profile create`/`modify` store refs canonically**: an `<alias>/…` bundle
  or parent resolves through the registry, and every ref naming its source by
  URL is written as its `<bundle-uri>`. Bare names stay local and are written
  as typed.
- **A profile item is versioned by its bundle's envelope**: one whose bundle
  declares a `schema_version` older than the profile-ref step has its
  `<canonical-url>@bundles/<name>` refs read as their `<bundle-uri>`, in
  memory; `--write-upgrades` (or signing the bundle) persists that, item files
  included. Either way the bundle's identity is its `<bundle-uri>`.
- **A config's agent bindings resolve `<alias>/…` refs on load**, through the
  registry, in memory; `--write-upgrades` persists it.
- **A local bundle's profiles resolve `<alias>/…` refs on load**, in memory —
  an alias table is this machine's, so a remote bundle's profiles are never
  read against it.
