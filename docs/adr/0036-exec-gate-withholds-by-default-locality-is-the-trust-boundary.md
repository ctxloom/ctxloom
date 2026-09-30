# 0036 — The executable trust gate withholds by default; locality is the trust boundary for a project-local bundle

## Status

Accepted

## Context

Content exposure was gated by one decision cascade, but the executable
surfaces — bundle MCP servers, hooks, command exports, skills — reached it
through a gate their consumers had to remember to install. Five sites
installed one (`operations.ApplyHooks`, `ResolveHooks`, `MaterializeProfile`,
the managed-config assembly, the oneshot), each by mutating the shared
configuration value (`config.SetExecutableTrustGate`), and every site that
did not — a fixture, a listing, a path added later — decided with
`bundles.AdmitAll`: the gate's absence read as "admit everything". The
decision cascade itself lived in the application-service ring
(`operations.EffectiveTrust`), so the core package that composes a package
could not decide anything on its own.

A bundle's signature had two shapes. A detached sibling `bundle.yaml.sig`
covered the envelope's bytes; a `SHA256SUMS` manifest with a `.sigs/` entry
covered the tree. The local reader read the sibling and, for a tree,
composed it with the manifest; the pull walk verified the manifest and read
the sibling for single-file documents; `ctxloom bundle sign` wrote both;
push and move carried the sibling as "the publisher signature artifact".
Two shapes meant two verification policies and a reader that could admit
what the other reader refused.

A project-local bundle whose sibling no longer covered its bytes was already
admitted with a warning. With one signature shape that row needed restating,
because an author editing a tree in place now stales a manifest rather than
a sibling.

## Decision

**The gate withholds by default.** `composite.Trust`, built per config
generation by `composite.NewTrust` over three core-owned ports
(`composite.TrustRoot`, `composite.ReviewRecords`,
`composite.RetractionRecords`; `config.Sources.TrustPorts` builds them), is
the ONE gate every exposure and executable surface decides with
(`config.Config.ExecutableTrustGate`). An executable item nothing positively
justifies is withheld until a review record approves it, and the withhold
names what would admit it. There is no admit-everything default: a surface
that forgot its gate holds none, and `bundles.Decide` withholds on a nil
authorizer and names the defect. No production-constructible Trust admits
everything; the only allow-all authorizer is the test-only
`internal/testsupport/admitall`, kept out of shipped binaries by the
`archtestsupport` analyzer.
The mutation sites are gone: a generation's Trust is the gate, and nothing
installs a second one.

**Why:** an admit-everything default that five sites had to remember to
flip is a fail-open arm. Withholding until reviewed is the only default a
trust gate can have, and one policy for ingest and exposure means a reader
cannot admit what the pull walk refused.

**One signature per bundle.** A bundle's signature is its `SHA256SUMS`
manifest and the `.sigs/` entry over it, inside the tree; every reader
verifies through ONE verifier, `attest.VerifyBundle`. The sibling
`bundle.yaml.sig` is retired: no reader parses it, `ctxloom bundle sign`
writes the manifest entry and removes a retired sibling, a bundle still
carrying one is refused until re-signed (`bundles.ErrSiblingSignatureRetired`),
a single-file bundle cannot be signed (it takes the tree form), and nothing
carries a sidecar — push, move and export ship the tree with its `.sigs/`
store and refuse a stale manifest (`operations.ErrStaleSignature`).

**A project-local bundle whose signature is invalid stays admitted as
unsigned.** The signature is treated as absent, not as a refusal, and the
admit reason stays `bundles.ReasonStaleLocalSignature` so the surface can
warn and name the fix.

**Why:** locality is the trust boundary for a bundle the human already
controls under source control; the signature is for what travels. A
project-local bundle is the local prototyping path — an author editing a
bundle in place breaks its signature on every keystroke, and refusing it
would make local iteration impossible. "Local" means the authored bundle
tree under the one app directory the process resolved
(`paths.LocalBundlesPath`, `Config.GetBundleDirs`); that is the project's
`.ctxloom` under source control whenever a project exists, and the same rule
— not a second tier — applies to the home ctxloom directory in the one case
where it acts as the app directory because no project was found. The same
facts over content that travelled admit nothing: the reader adapters refuse
such a tree before it becomes a read (`bundles.ErrTreeBundleWithheld`).

## Consequences

- An unapproved executable that used to run through a surface with no gate
  installed is withheld until reviewed. The advisory a consumer prints reads
  `withheld <ref>: awaiting review — run 'ctxloom review' (no review record
  approves this hook)`.
- Retraction records are read once per generation
  (`remote.NewLockfileRetraction`): a pull that rewrites the lockfile
  produces the next generation's records, never this one's.
- A fixture that exercises an executable surface states its gate
  (`config.Config.BindTrustForTesting`, `compositetest`); one that does not
  withholds everything. The withhold-by-default is fail-closed: a fixture
  that never had a review record gains one, the gate is not weakened.
- `attest.SignBundle` appends a `.sigs/` entry rather than replacing the
  previous principal's; re-signing accumulates entries. Changing what
  `.sigs/` holds is outside this decision.
- The remote document form carried its signature as a sibling; it was
  already unreadable by the pull walk (`bundles.ErrDocumentFormUnreadable`),
  and a pin advance onto one is neither refused nor verified — there is
  nothing to verify. The publisher republishes as a tree.

The normative statement of the cascade is `docs/trust-model.md`.
