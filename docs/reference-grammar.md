# ctxloom Reference Grammar

The canonical specification for how ctxloom references are spelled and
resolved. When behavior and this document disagree, one of them is a bug —
the entry-point tests in `internal/profiles/grammar_test.go` and
`internal/remote/profile_selector_test.go` pin the rules below.

## Building blocks

```
<canonical-url>   https://github.com/owner/repo        (also git@, file://)
<bundle-ref>      <canonical-url>@bundles/<bundle>     canonical remote bundle
                  ctxloom:local@bundles/<bundle>       canonical local bundle
                  <bundle>                             plain local bundle name
                  <alias>/<bundle>                     bundle via a configured remote
<selector>        #fragments/<name> | #skills/<name> | #mcp/<name>
                  | #hooks/<event>/<n> | #profiles/<name>
<version>         @<sha> | @<tag> | @<semver-range> | @<branch>
```

`#` is a reserved character: it always introduces a bundle-item selector and
can never appear in a profile name or bundle name. A ref containing
`#profiles/` is therefore *structurally* a bundle-shipped profile reference,
never a local file.

## Profile references

Accepted wherever a profile is named (`run -p`, `map`/`weave -p`, `weave -s`,
`--parent`, `profiles.defaults`, `agents:` profile lists, `acp --profile`,
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
   — run 'ctxloom remote pull'".
3. In `map`/`weave`, `-p`/`--agents` member names are checked against
   **agents first**, then profiles. `weave -s` is profile-only.

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

`fragment`/`skill` CLI commands and `-f` accept `<bundle-ref>#<kind>/<name>`.
A bare fragment name in `-f` is searched across installed bundles
(deterministic pick with a warning on collision). `trust`/`blacklist` refs use
the same shape and additionally accept `@<commit>` as provenance.

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
