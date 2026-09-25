package config

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/ctxloom/ctxloom/internal/shared/report"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// hookPreimage and mcpPreimage are the executable-surface preimage builders for
// a bundle hook / MCP server, indirected through package vars so a test can
// force the (in production near-unreachable) build failure that
// extractHooksFromBundle / extractMCPFromBundle must report rather than swallow
// (U049-F17). SetPreimageBuildersForTesting swaps them and returns a restore.
var (
	hookPreimage = func(h bundles.BundleHook) ([]byte, error) { return h.ContentPayload() }
	mcpPreimage  = func(m bundles.BundleMCP) ([]byte, error) { return m.ContentPayload() }
)

// SetPreimageBuildersForTesting swaps the hook/MCP preimage builders (either may
// be nil to leave that one unchanged) and returns a function that restores both.
func SetPreimageBuildersForTesting(hook func(bundles.BundleHook) ([]byte, error), mcp func(bundles.BundleMCP) ([]byte, error)) func() {
	prevHook, prevMCP := hookPreimage, mcpPreimage
	if hook != nil {
		hookPreimage = hook
	}
	if mcp != nil {
		mcpPreimage = mcp
	}
	return func() { hookPreimage, mcpPreimage = prevHook, prevMCP }
}

// bindTrust and bindCatalog attach the generation's Trust and resolved
// Catalog to the Config the Owner is about to publish, so a consumer reaching
// this generation through its *Config sees exactly what the Snapshot carries.
// Trust is bound first because the readers the catalog resolves verify
// against its root. Called once per generation, before publication; never on
// a published value.
func (c *Config) bindTrust(trust composite.Trust) { c.trust = trust }

func (c *Config) bindCatalog(catalog func() bundles.Catalog) { c.catalog = catalog }

// Catalog returns the generation's bundle catalog. Every Config an Owner
// published had one bound before publication (bindCatalog) and returns
// that same resolved set for its life. A Config no Owner published — a
// fixture — has no generation to pin: it resolves the one reader core
// itself can build, the project's authored bundles, on every call, and never
// sees remote or companion content, which only the composition root's
// Sources supply. It verifies against its Trust's root, which for a fixture
// nobody bound trusts no signer (trust.NoSigners): a signed bundle reads as
// untrusted rather than as whatever the machine's signer files say.
func (c *Config) Catalog() bundles.Catalog {
	if c.catalog != nil {
		return c.catalog()
	}
	root := c.trust.Root()
	return bundles.Resolve(context.Background(), c.rep.Sink,
		bundles.NewProjectReader(c.getFS(), c.BundleReaderDirs(), bundles.WithTrustRoot(root), bundles.WithReaderReporter(c.rep.Sink)))
}

// Trust is the generation's gate holder, bound before publication
// (bindTrust). A Config no Owner published — a fixture — holds a ZERO
// Trust, whose nil authorizer bundles.Decide withholds on and names
// (ReasonUngoverned): a surface that forgot its gate is a defect, never an
// admit.
func (c *Config) Trust() composite.Trust { return c.trust }

// ExecutableTrustGate returns the authorizer the bundle executable surfaces
// decide with: the generation's Trust. Nil for a fixture nobody bound, which
// bundles.Decide withholds on loudly.
func (c *Config) ExecutableTrustGate() bundles.Authorizer { return c.trust.Authorizer() }

// ErrTrustUnbound is the refusal a delivery entry point gives a Config that
// carries no gate: it was constructed outside the Owner (config.Open
// publishes every generation with its Trust) and never bound (a fixture
// states its gate with BindTrustForTesting). Refused at the entry, by
// sentinel — a construction bug is caught first, not surfaced as one
// withheld item per executable later.
var ErrTrustUnbound = errors.New("config: this configuration carries no trust gate — it was constructed outside the Owner and never bound")

// RequireTrust returns the bound Trust, or ErrTrustUnbound for a Config
// nobody bound. Every operation that delivers content asks this at entry.
func (c *Config) RequireTrust() (composite.Trust, error) {
	if c == nil || c.trust.Authorizer() == nil {
		return composite.Trust{}, ErrTrustUnbound
	}
	return c.trust, nil
}

// BindTrustForTesting binds tr as this Config's generation gate, exactly as
// the Owner does before publishing a Snapshot. A fixture that exercises an
// executable surface states its gate this way — compositetest.Trust over
// fake ports for a decision.
func (c *Config) BindTrustForTesting(tr composite.Trust) { c.trust = tr }

// mcpNameClaims settles the MCP server-name contest at the BUNDLE-RESOLUTION
// layer: two ctxloom source refs both declaring one server name, before any
// engine registry writer sees the composed set.
//
// The ruling (human, 2026-08-17): a contest between two DIFFERENT source refs
// is a LOUD ERROR — a report.KindBundle finding naming both refs and the
// contested name, and the later claim is WITHHELD — while the SAME ref reached
// twice (a bundle listed by two profiles in scope, or a companion loadout that
// a profile also references) dedupes silently.
//
// Silence here is a delivery-substitution path: one bundle's server quietly
// answering for another's declared name, up to a profile bundle shadowing
// ctxloom's own server (declared by its own companion loadout). Nothing
// downstream can detect it — the resolved set is keyed BY NAME, so the loser
// leaves no trace, and the surviving entry carries the WINNER's SCM, so
// reconciliation attributes it to the wrong bundle. The incumbent is kept
// rather than dropped because withholding both would, in degraded mode, also
// remove a working companion server over a contest the user did not create;
// and in strict mode the finding aborts the launch anyway, so nothing runs on
// the composed set either way.
//
// This arbitrates ctxloom-vs-ctxloom, and every part of its shape follows from
// that: its predicate is ref IDENTITY rather than mere presence (the same ref
// must be allowed to win twice, which a presence check would refuse), its
// verdict is a fatal finding rather than a warning, and it keeps no ledger
// because nothing downstream removes by claim order.
//
// There is deliberately no ctxloom-vs-USER counterpart any more. A writer no
// longer asks an engine's registry which entries are its own — confpatch
// reverses what ctxloom wrote last time before writing what it wants now, so
// ownership is recorded rather than inferred.
type mcpNameClaims struct {
	rep report.Reporter
	// claimedBy records the source ref that first claimed each name.
	claimedBy map[string]string
}

// claim reports whether sourceRef may write name into the resolved server set,
// recording the first claimant and reporting a contest between two different
// refs. It is the ONLY place the contest is decided.
func (c *mcpNameClaims) claim(name, sourceRef string) bool {
	if c.claimedBy == nil {
		c.claimedBy = make(map[string]string)
	}
	holder, held := c.claimedBy[name]
	if !held {
		c.claimedBy[name] = sourceRef
		return true
	}
	if holder == sourceRef {
		// Same bundle reached twice — dedupe, silently and by ruling.
		return true
	}
	c.rep.Failf(report.KindBundle,
		"rename the server in one of the two bundles, or exclude it with `exclude_mcp:` in your profile",
		"MCP server name %q is claimed by two different sources: %q claimed it first and %q also declares it; %q's server is withheld so one bundle's server cannot silently answer for another's declared name",
		name, holder, sourceRef, sourceRef)
	return false
}

// ResolveBundleMCPServers loads MCP servers from bundles referenced in the
// caller's selected profiles (or the configured defaults when none are passed),
// plus servers shipped by every discovered COMPANION's loadout — ctxloom's
// own included, which is how ctxloom's own MCP server is registered. A
// companion's MCP registration is unconditional but never exempt: it is
// routed through the identical extraction+gate path a profile-referenced
// bundle uses, so rejection applies. Mirrors ResolveBundleHooks. Each server
// is tagged with the SCM of the bundle that shipped it so reconciliation can
// identify it.
func (c *Config) ResolveBundleMCPServers(profileNames []string) map[string]wire.MCPServer {
	return c.ResolveBundleMCPServersFor(c.ResolveProfileSet(profileNames))
}

// ResolveProfileSet resolves the profile scope — the caller's selection, or
// the configured defaults when none are passed — through the recursive
// resolver, so bundles inherited from parent profiles are included. A
// profile that does not resolve is reported once and skipped rather than
// aborting the set, so one broken profile cannot silence the rest. With no
// app paths there is nothing to read profiles from and the set is empty.
func (c *Config) ResolveProfileSet(profileNames []string) []profiles.ResolvedProfile {
	if len(c.appPaths) == 0 {
		return nil
	}
	profileLoader := c.GetProfileLoader()
	var set []profiles.ResolvedProfile
	for _, profileName := range c.resolveProfileScope(profileNames) {
		resolved, ok := resolveProfileOrReport(c.rep, profileLoader, profileName)
		if !ok {
			continue
		}
		p := *resolved
		p.Name = profileName
		set = append(set, p)
	}
	return set
}

// ResolveBundleMCPServersFor is ResolveBundleMCPServers over an already
// resolved profile set — the one assembly resolved, so the set is resolved
// and its faults reported once.
func (c *Config) ResolveBundleMCPServersFor(set []profiles.ResolvedProfile) map[string]wire.MCPServer {
	result := make(map[string]wire.MCPServer)

	// Resolve the profile scope BEFORE any merge, because its exclude_mcp
	// names have to apply to EVERY write into result, not just the
	// profile-bundle branch. They used to be collected inside the per-profile
	// loop at the bottom of this function — after the builtin merge and the
	// companion loop had already put their servers in — so a profile saying
	// `exclude_mcp: [taskloom]` silently got taskloom's companion server
	// anyway, with no diagnostic. exclude_mcp now means the same thing
	// whichever source offered the server.
	//
	// The set is the UNION across every profile in scope, i.e. an exclusion is
	// a VETO rather than a per-profile-local filter. That is a deliberate
	// widening: profiles in one scope compose into a single session's server
	// set, one server name resolves to one server, and "profile A wanted this
	// server withheld but profile B pulled it back in" is not a distinction a
	// user can act on. Vetoing is also the safe direction — the failure mode it
	// forecloses is an unwanted server being launched.
	//
	// Scope: the caller's selected profiles (e.g. `run -p`); when none are
	// passed, the configured defaults, so the `manage`/apply-hooks path keeps
	// its project-default behavior.
	excluded := make(map[string]bool)
	for _, resolved := range set {
		for _, name := range resolved.ExcludeMCP {
			excluded[name] = true
		}
	}

	// addServers is the ONLY way a server reaches result, so no source can
	// bypass the exclusion filter or the name arbiter by construction.
	//
	// Names are visited in sorted order so that when one source contests two
	// names at once, the findings it records come out in a stable order.
	claims := mcpNameClaims{rep: c.rep}
	addServers := func(sourceRef string, servers map[string]wire.MCPServer) {
		names := make([]string, 0, len(servers))
		for name := range servers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if excluded[name] {
				continue
			}
			if !claims.claim(name, sourceRef) {
				continue
			}
			result[name] = servers[name]
		}
	}

	// BundleLoader includes remote bundles from the active lockfile AND every
	// discovered companion's loadout — ctxloom's own included — read under
	// its ctxloom:companion@<bin> ref; without them, MCP servers shipped in
	// remote bundles (or a companion loadout) silently disappear (see
	// docs/bundle-review-plan.md Phase 1.2).
	bundleLoader := c.BundleLoader()

	// Companion loadouts first, resolved through loadMCPFromBundleRef — the
	// SAME path a profile-referenced bundle uses (Load -> extractMCPFromBundle)
	// — so a companion's server is judged by ITS bundle's own source ref and
	// verified Signer(). Sorted for a deterministic result across runs.
	// Resolving them first only fixes who is the INCUMBENT of a contested
	// name; it grants no precedence to anyone, because a later source that
	// declares a name a companion already claimed is refused loudly rather
	// than allowed to override (see mcpNameClaims).
	cat := bundleLoader.Catalog()
	for _, ref := range companionRefs(cat) {
		addServers(ref, loadMCPFromBundleRef(c.rep, ref, cat, c.ExecutableTrustGate()))
	}

	// Finally the profile-referenced bundles. A bundle listed by two profiles
	// in scope is the SAME ref reached twice and dedupes silently; a bundle
	// that declares a name a companion or another profile bundle already
	// claimed is a contest between two different refs, and mcpNameClaims
	// withholds it loudly.
	for _, resolved := range set {
		for _, bundleRef := range resolved.Bundles {
			addServers(bundleRef, loadMCPFromBundleRef(c.rep, bundleRef, cat, c.ExecutableTrustGate()))
		}
	}

	return result
}

// bundleSCM is the marker a resolved MCP server carries to name the bundle
// that shipped it (wire.MCPServer.SCM). extractMCPFromBundle stamps it and
// LinkGrant reads it back, so "granted from THIS bundle" is one spelling.
func bundleSCM(src trust.BundleRef) string { return bundles.BundleSCM(src) }

// LinkGrant answers the link-group question for a run over profileNames from
// the run's OWN granted set — ResolveBundleMCPServers over the same profiles
// the engine is launched with — so a fragment or skill linked to an MCP server
// is delivered exactly when that server is. It is keyed by server name AND
// owning bundle: the name arbiter can withhold one bundle's server while a
// same-named server from another survives, and the survivor must not stand in
// for the one the linked item actually depends on.
//
// The granted set is resolved ONCE, on the FIRST question, and never at
// construction. Both halves matter. The resolve reports a fail-loudly finding
// per unresolvable ref and per name contest; a grant asked many times in one
// assembly must not repeat them, so it is memoised. And a grant is BUILT by
// every pipeline the run constructs -- context assembly, skills, commands,
// curated exports -- most of which never meet a linked item: resolving eagerly
// re-records the run's own findings once per pipeline, and `ctxloom doctor`,
// which counts ClassRef findings around one AssembleContext call to report how
// many refs were skipped, then reports double. Deferring to the first question
// makes a run with no linked items resolve zero extra times.
func (c *Config) LinkGrant(profileNames []string) bundles.LinkGrant {
	var (
		once    sync.Once
		granted map[string]wire.MCPServer
	)
	return bundles.LinkGrantFunc(func(read bundles.BundleRead, server string) bool {
		once.Do(func() { granted = c.ResolveBundleMCPServers(profileNames) })
		srv, ok := granted[server]
		return ok && srv.SCM == bundleSCM(read.SourceRef())
	})
}

// LinkGrantFor is LinkGrant over an already resolved profile set.
func (c *Config) LinkGrantFor(set []profiles.ResolvedProfile) bundles.LinkGrant {
	var (
		once    sync.Once
		granted map[string]wire.MCPServer
	)
	return bundles.LinkGrantFunc(func(read bundles.BundleRead, server string) bool {
		once.Do(func() { granted = c.ResolveBundleMCPServersFor(set) })
		srv, ok := granted[server]
		return ok && srv.SCM == bundleSCM(read.SourceRef())
	})
}

// resolveProfileOrReport resolves profileName through the recursive resolver
// on behalf of the four bundle resolvers (MCP servers, hooks, commands,
// skills), REPORTING an unresolvable profile instead of skipping it.
//
// The four call sites used to `continue` on the error, so `ctxloom run -p
// <typo>` delivered zero MCP servers, zero hooks, zero commands and zero
// skills with no diagnostic from this layer and exit 0 — an empty result that
// looks exactly like "nothing was configured". It isn't: it is "we could not
// work out what to deliver". The inline-profile path
// (resolveProfileParents) already reports a fail-loudly finding for this, as does the
// sibling loadBundleProfileSeed; this brings the bundle resolvers into line.
//
// FailOnce, not Fail: one unresolvable profile is hit by all four resolvers
// (and each of those by several callers), so the per-message dedup keeps it to
// a single line per window while still recording the finding the startup choke
// aborts on in strict mode. --degraded downgrades it, as everywhere else.
func resolveProfileOrReport(rep report.Reporter, profileLoader *profiles.Loader, profileName string) (*profiles.ResolvedProfile, bool) {
	resolved, err := profileLoader.ResolveProfile(profileName, nil)
	if err != nil {
		rep.FailOncef(report.KindRef,
			"check the profile name (`ctxloom profile list`), or pass --degraded to continue without it",
			"profile %q could not be resolved; the bundles it references contribute no MCP servers, hooks, commands or skills: %v",
			profileName, err)
		return nil, false
	}
	return resolved, true
}

// loadMCPFromBundleRef loads MCP servers from a bundle reference (remote
// "remote/name" or local name). It resolves through loader.Load, which checks
// the seeded-bundle map first: remote bundles are no longer extracted to disk
// (they live only in the SeededBundleLoader seed), so resolving a remote ref by
// a computed filesystem path would silently find nothing and drop its servers.
func loadMCPFromBundleRef(rep report.Reporter, bundleRef string, cat bundles.Catalog, gate bundles.Authorizer) map[string]wire.MCPServer {
	read, err := cat.Read(bundleRef)
	if err != nil {
		reportBundleRefLoadFailure(rep, bundleRef, err)
		return nil
	}
	return extractMCPFromBundle(rep, read, read.SourceRef(), gate)
}

// reportBundleRefLoadFailure reports a bundle ref that could not be loaded on
// behalf of loadMCPFromBundleRef and loadHooksFromBundleRef.
//
// Both used to swallow the error and return an empty result, which is
// indistinguishable from a bundle that simply ships no MCP servers or no
// hooks: a ref the user configured contributed nothing, with no warning, no
// finding and exit 0. The sibling loadBundleProfileSeed (config.go) already
// reports exactly this fault with this fixit, so this brings the executable
// surfaces into line with the profile surface.
//
// Note the loader's own directory scan reports MALFORMED local bundle files
// itself; what reaches here is chiefly the not-found ref — a bundle named by a
// profile but never pulled, or misspelled.
func reportBundleRefLoadFailure(rep report.Reporter, bundleRef string, err error) {
	// A profile's `bundles:` list may carry ITEM-SCOPED refs
	// ("<bundle>#fragments/<name>") selecting one item out of a bundle. Those
	// name a fragment/command, not a bundle: loader.Load cannot resolve them
	// by design (it would look for a file literally named
	// "<bundle>#fragments/<name>.yaml"), and a selector that picked one
	// fragment SHOULD contribute no MCP servers and no hooks. That is the
	// legitimate empty case, so it stays silent — reporting it would turn
	// every fragment-scoped profile into a fatal startup finding.
	if strings.Contains(bundleRef, "#") {
		return
	}
	rep.FailOncef(report.KindBundle,
		"run `ctxloom deps pull` or fix the bundle ref, or pass --degraded",
		"failed to load bundle %q; the MCP servers and hooks it ships are not applied: %v", bundleRef, err)
}

// ResolveBundleHooks aggregates hooks shipped by every bundle referenced
// in the caller's selected profiles (or the configured defaults when none
// are passed), plus the always-on hooks shipped by every discovered
// COMPANION's loadout (ctxloom's own included — unconditional, e.g. ltk's
// pre-tool guard or taskloom's session-bind, but never exempt: see
// ResolveBundleMCPServers for why). Mirrors ResolveBundleMCPServers. Each
// emitted hook carries SCM source info so apply-hooks can identify
// ctxloom-managed entries when reconciling the backend's settings.json.
func (c *Config) ResolveBundleHooks(profileNames []string) wire.UnifiedHooks {
	return c.ResolveBundleHooksFor(c.ResolveProfileSet(profileNames))
}

// ResolveBundleHooksFor is ResolveBundleHooks over an already resolved
// profile set.
func (c *Config) ResolveBundleHooksFor(set []profiles.ResolvedProfile) wire.UnifiedHooks {
	var result wire.UnifiedHooks

	// One link grant for every arm: the granted set it answers from holds
	// companion and profile servers alike, so a hook linked to its
	// server is delivered exactly when that server is, whichever arm shipped
	// both. Lazy, so an assembly with no linked hook never resolves it.
	links := c.LinkGrantFor(set)

	bundleLoader := c.BundleLoader()

	// Companion loadout hooks (ctxloom's own included): same extraction+gate
	// path a profile-referenced bundle uses, keyed and signed by the
	// companion's OWN bundle. Sorted for a deterministic result across runs.
	cat := bundleLoader.Catalog()
	for _, ref := range companionRefs(cat) {
		result.Append(loadHooksFromBundleRef(c.rep, ref, cat, c.ExecutableTrustGate(), links))
	}

	eachBundleRef(set, func(bundleRef string) {
		result.Append(loadHooksFromBundleRef(c.rep, bundleRef, cat, c.ExecutableTrustGate(), links))
	})
	return result
}

// eachBundleRef calls fn with every bundle reference the resolved profiles
// carry, in profile order, then in the order each profile lists them. The
// set resolved RECURSIVELY (ResolveProfileSet), so a bundle inherited from a
// parent profile is visited like one the child names directly.
func eachBundleRef(set []profiles.ResolvedProfile, fn func(bundleRef string)) {
	for _, resolved := range set {
		for _, bundleRef := range resolved.Bundles {
			fn(bundleRef)
		}
	}
}

// companionRefs returns the loader's companion loadout refs
// (ctxloom:companion@<bin>) in deterministic sorted order.
//
// It asks the RESOLVED SET what was read rather than re-probing: the reads
// already carry which source each bundle came from, so "everything the
// companion reader contributed" is a fact on the record instead of a second
// discovery pass that could answer differently — and it does not exec anything
// a second time.
func companionRefs(cat bundles.Catalog) []string {
	reads := companionReads(cat)
	out := make([]string, 0, len(reads))
	for _, read := range reads {
		out = append(out, read.DisplayName())
	}
	return out
}

// companionReads returns the companion loadout reads in deterministic order,
// for the callers that need the bundles themselves.
func companionReads(cat bundles.Catalog) []bundles.BundleRead {
	return cat.Scoped(bundles.ProvenanceCompanion).Reads()
}

// resolveProfileScope returns the profile set a bundle-resolution call should
// use: the caller's explicit selection (e.g. `run -p`) when non-empty, else the
// configured defaults. This is the seam that makes mcp/commands/hooks follow the
// SELECTED profile (the same set AssembleContext scopes context to) instead of
// always the defaults, while preserving the default-scoped behavior for the
// `manage`/apply-hooks path that passes nothing.
func (c *Config) resolveProfileScope(profileNames []string) []string {
	if len(profileNames) > 0 {
		return profileNames
	}
	return c.DefaultAgentProfiles()
}

// ResolveBundleCommands aggregates the prompts (slash-command exports) shipped
// by every bundle referenced in the caller's selected profiles (or the
// configured defaults when none are passed), PLUS the commands shipped by
// every discovered COMPANION's loadout (S8 — unconditional whenever the
// companion binary is on PATH, e.g. ltk's task-runner command, but NEVER
// exempt: routed through bundleLoader.CommandsFromBundleRef,
// the identical extraction+gate path a profile-referenced bundle's commands
// use — see ResolveCompanionCommands). Deduped by prompt name; profile-sourced
// commands are resolved FIRST so an explicit profile curation of the same
// name wins over the companion's (ADDING companion commands to the set, never
// replacing curation). Mirrors ResolveBundleMCPServers / ResolveBundleHooks —
// the profile-scoped replacement for the global ListAllCommands sweep, so a
// session only carries the commands its profile pulls in (plus its
// companions'). Built-in embedded commands are added by the caller
// (LoadCommandExports), not here, since they are not bundle-shipped.
//
// Gating and form selection are this stage's calls, not the reader's: the
// executable trust gate comes off cfg (nil on management paths = no gating) and
// the configured form from ShouldUseDistilled, and both are handed to the
// process stage here rather than baked into how the reader was built. A
// withheld command is therefore not exported.
func (c *Config) ResolveBundleCommands(profileNames []string) []*bundles.LoadedContent {
	loader := c.BundleLoader()
	pipe := bundles.NewPipeline(loader, c.ExecutableTrustGate(), c.LinkGrant(profileNames), c.ShouldUseDistilled())

	seen := make(map[string]bool)
	var out []*bundles.LoadedContent
	add := func(prompt *bundles.LoadedContent) {
		if seen[prompt.Item] {
			return
		}
		seen[prompt.Item] = true
		out = append(out, prompt)
	}

	eachBundleRef(c.ResolveProfileSet(profileNames), func(bundleRef string) {
		for _, prompt := range pipe.CommandsFromBundleRef(bundleRef) {
			add(prompt)
		}
	})

	for _, command := range resolveCompanionCommandsWith(pipe, loader.Catalog()) {
		add(command)
	}
	return out
}

// ResolveBundleSkills aggregates the Agent Skill packages shipped by every
// bundle referenced in the caller's selected profiles (or the configured
// defaults when none are passed) — the skills analog of ResolveBundleCommands.
// Mirrors ONLY its UNCURATED path: every profile-referenced bundle's skills
// export by default (each still gated by its own per-engine enablement flag
// downstream, mirroring the mcp/hooks/commands resolvers). A profile's
// `skills:` CURATED list (opt-in, mirroring `commands:`) and companion-shipped
// skills are both Part B6 (skill-command-split.plan.md §3.2 notes companion
// skill emission explicitly out of the first slices) — not implemented here.
// Deduped by skill item name (first occurrence wins), matching
// ResolveBundleCommands' dedup key.
func (c *Config) ResolveBundleSkills(profileNames []string) []*bundles.LoadedSkill {
	pipe := bundles.NewPipeline(c.BundleLoader(), c.ExecutableTrustGate(), c.LinkGrant(profileNames), c.ShouldUseDistilled())

	seen := make(map[string]bool)
	var out []*bundles.LoadedSkill
	add := func(skill *bundles.LoadedSkill) {
		if seen[skill.Item] {
			return
		}
		seen[skill.Item] = true
		out = append(out, skill)
	}

	eachBundleRef(c.ResolveProfileSet(profileNames), func(bundleRef string) {
		for _, skill := range pipe.SkillsFromBundleRef(bundleRef) {
			add(skill)
		}
	})
	return out
}

// ResolveCompanionCommands returns the commands shipped by every discovered
// companion's loadout (S8 — companionBundleSeed / sortedCompanionRefs),
// unconditionally whenever the companion binary is on PATH, in deterministic
// (companion-ref-sorted, then name-sorted within a loadout) order. Routed
// through bundleLoader.CommandsFromBundleRef — the SAME extraction+gate path a
// profile-referenced bundle's commands use, keyed and signed by the
// companion's OWN bundle; it decides
// with the cfg-carried executable trust gate exactly like ResolveBundleCommands,
// so an unsigned/withheld companion loadout's commands do not export.
//
// This is the piece LoadCommandExports adds on BOTH its curated and uncurated
// paths (ResolveBundleCommands only covers the uncurated one, since a
// profile's commands: curation bypasses it entirely) — see prompts.go.
// profileNames scopes only the LINK grant: a companion's commands are
// unconditional, but one linked to a server the selected profiles veto is
// withheld with it.
func (c *Config) ResolveCompanionCommands(profileNames []string) []*bundles.LoadedContent {
	loader := c.BundleLoader()
	return resolveCompanionCommandsWith(
		bundles.NewPipeline(loader, c.ExecutableTrustGate(), c.LinkGrant(profileNames), c.ShouldUseDistilled()), loader.Catalog())
}

// resolveCompanionCommandsWith is the shared companion-command extraction
// loop, taking an already-built pipeline so ResolveBundleCommands (which needs
// one for the profile-scoped pass too) doesn't construct a second one. Gate and
// form travel with it, so both callers necessarily agree on both.
func resolveCompanionCommandsWith(pipe *bundles.Pipeline, cat bundles.Catalog) []*bundles.LoadedContent {
	var out []*bundles.LoadedContent
	for _, ref := range companionRefs(cat) {
		out = append(out, pipe.CommandsFromBundleRef(ref)...)
	}
	return out
}

// loadHooksFromBundleRef loads hooks from a bundle reference. Like
// loadMCPFromBundleRef it resolves via loader.Load (seed-aware) rather than a
// computed fs path, so remote bundles' hooks aren't silently dropped.
func loadHooksFromBundleRef(rep report.Reporter, bundleRef string, cat bundles.Catalog, gate bundles.Authorizer, links bundles.LinkGrant) wire.UnifiedHooks {
	read, err := cat.Read(bundleRef)
	if err != nil {
		reportBundleRefLoadFailure(rep, bundleRef, err)
		return wire.UnifiedHooks{}
	}
	return extractHooksFromBundle(rep, read, read.SourceRef(), gate, links)
}

// extractHooksFromBundle converts a bundle's hooks to wire.Hooks. Each
// hook's executable surface is
// hashed (BundleHook.ComputeContentHash) and run through the cascade keyed on
// the canonical bundle-reference grammar's item selector over source
// (bundles.ItemRefFor(src, trust.KindHook, "<event>/<index>")); a DENY omits the
// hook — a bundle hook is an arbitrary-command executable that must never be
// applied unevaluated (fail-closed). gate is the executable trust gate (TR5);
// a nil gate withholds every hook (bundles.Decide). The identity scheme is
// bundles.HookEntry.ID() ("<event>/<index>"), shared with the migration
// baseline so a baselined hook's ref matches.
//
// links is the run's link grant (bundles.LinkGrant): a hook linked to an MCP
// server the run was not granted is withheld here, after trust, exactly as the
// content pipeline withholds a linked fragment — hooks never pass through that
// pipeline, so this is where the group's atomicity is enforced for them. A nil
// grant withholds every linked hook; a surface that gates nothing says
// bundles.LinksUnchecked.
func extractHooksFromBundle(rep report.Reporter, read bundles.BundleRead, src trust.BundleRef, gate bundles.Authorizer, links bundles.LinkGrant) wire.UnifiedHooks {
	bundle := read.Bundle
	if !bundle.Hooks.HasAny() {
		return wire.UnifiedHooks{}
	}
	marker := "bundle:" + string(src.BundleIdentity())
	convert := func(event string, in []bundles.BundleHook) []wire.Hook {
		if len(in) == 0 {
			return nil
		}
		// A bundle's `order:` sequences ITS hooks within this event. Across
		// bundles nothing changes: UnifiedHooks.Append still concatenates, and
		// the merge sequence is still the bundles' order, not any hook's.
		//
		// This sorts a permutation of AUTHORED INDICES rather than the hooks
		// themselves, because the authored index is a hook's trust identity on
		// this path ("<bundle>#hooks/<event>/<index>", shared with the migration
		// baseline and every recorded grant). Resolving to a new position must
		// not renumber that ref, or every existing hook approval silently
		// detaches from the hook it was granted for.
		//
		// SliceStable over an already-ascending permutation is what makes ties —
		// and a bundle where nothing declares an order at all — resolve to
		// authored position, byte-for-byte as this path behaved before the field
		// existed. Hence the empty tie-break keys: stability IS the tie-break.
		order := make([]int, len(in))
		for i := range in {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool {
			return wire.HookOrderLess(in[order[a]].Order, "", in[order[b]].Order, "")
		})

		out := make([]wire.Hook, 0, len(in))
		for _, i := range order {
			h := in[i]
			id := bundles.HookEntry{Event: event, Index: i}.ID()
			// Key by the bundle's source ref (canonical for a remote/cloned
			// bundle, the local name for a project bundle) — NOT bundle.Name,
			// whose short form is ambiguous across local and cloned bundles.
			// This makes the cascade's IsLocal/RepoURL honest (local hooks
			// auto-trust; a cloned one is judged by WHO SIGNED it) and aligns
			// the gate key with the baseline/grant key (both source).
			ref, rerr := bundles.ItemRefFor(src, trust.KindHook, id)
			if rerr != nil {
				// Fail CLOSED and NAMED: a hook nothing can address is a
				// hook nothing can decide about, and one such hook costs
				// itself, never the bundle's other hooks.
				rep.Failf(report.KindBundle,
					"fix or re-pull the bundle, or pass --degraded",
					"bundle hook withheld: %v", rerr)
				continue
			}
			payload, perr := hookPreimage(h)
			if perr != nil {
				// Cannot build the preimage → cannot evaluate → withhold. Fail
				// CLOSED, but never SILENTLY: a hook the user configured would
				// otherwise vanish from the launched engine with no trace
				// (U049-F17). Name the ref and the fault.
				rep.Failf(report.KindBundle,
					"fix or re-pull the bundle, or pass --degraded",
					"bundle hook %q withheld: cannot build its trust preimage: %v", ref, perr)
				continue
			}
			if !bundles.Decide(rep, gate, read, ref, payload, bundles.FormRaw).Allow {
				continue // withheld by the trust gate
			}
			// Trust decided first, so a trust withhold is reported as one;
			// links are the second question, asked only of a hook trust
			// would deliver. Effective tags, as LinkGroups computes them.
			if linkID, server, withheld := bundles.LinkWithholds(links, read, slices.Concat(bundle.Tags, h.Tags)); withheld {
				bundles.WarnLinkWithheld(rep, read.DisplayName()+"#hooks/"+id, linkID, server)
				continue
			}
			out = append(out, wire.Hook{
				Matcher:         h.Matcher,
				Command:         h.Command,
				Type:            h.Type,
				Prompt:          h.Prompt,
				Timeout:         h.Timeout,
				Async:           h.Async,
				SCM:             marker,
				PreToolFallback: h.PreToolFallback,
			})
		}
		return out
	}
	return wire.UnifiedHooks{
		PreTool:      convert(bundles.HookEventPreTool, bundle.Hooks.PreTool),
		PostTool:     convert(bundles.HookEventPostTool, bundle.Hooks.PostTool),
		SessionStart: convert(bundles.HookEventSessionStart, bundle.Hooks.SessionStart),
		SessionEnd:   convert(bundles.HookEventSessionEnd, bundle.Hooks.SessionEnd),
		PreShell:     convert(bundles.HookEventPreShell, bundle.Hooks.PreShell),
		PostFileEdit: convert(bundles.HookEventPostFileEdit, bundle.Hooks.PostFileEdit),
		TurnEnd:      convert(bundles.HookEventTurnEnd, bundle.Hooks.TurnEnd),
	}
}

// extractMCPFromBundle extracts MCP servers from a loaded bundle. Each
// server's executable surface
// (Command+Args+Env+Installation) is hashed and run through the cascade keyed
// on the canonical bundle-reference grammar's item selector over source
// (bundles.ItemRefFor(src, trust.KindMCP, name)); a DENY omits the server entirely
// — an arbitrary-command executable must never reach settings unevaluated
// (fail-closed). gate is the executable trust gate (TR5); a nil gate
// withholds every server (bundles.Decide).
func extractMCPFromBundle(rep report.Reporter, read bundles.BundleRead, src trust.BundleRef, gate bundles.Authorizer) map[string]wire.MCPServer {
	bundle := read.Bundle
	result := make(map[string]wire.MCPServer)

	for name, mcp := range bundle.MCP {
		// Key by the source ref (canonical for a cloned bundle, local name for
		// a project bundle) so the cascade's IsLocal/RepoURL are honest and the
		// gate key matches the baseline/grant key. See extractHooksFromBundle.
		ref, rerr := bundles.ItemRefFor(src, trust.KindMCP, name)
		if rerr != nil {
			// See extractHooksFromBundle: fail closed, named, per item.
			rep.Failf(report.KindBundle,
				"fix or re-pull the bundle, or pass --degraded",
				"bundle MCP server withheld: %v", rerr)
			continue
		}
		payload, perr := mcpPreimage(mcp)
		if perr != nil {
			// Cannot build the preimage → cannot evaluate → withhold. Fail
			// CLOSED, but never SILENTLY: an MCP server the user configured
			// would otherwise vanish from the launched engine with no trace
			// (U049-F17). Name the ref and the fault.
			rep.Failf(report.KindBundle,
				"fix or re-pull the bundle, or pass --degraded",
				"bundle MCP server %q withheld: cannot build its trust preimage: %v", ref, perr)
			continue
		}
		if !bundles.Decide(rep, gate, read, ref, payload, bundles.FormRaw).Allow {
			continue // withheld by the trust gate
		}
		srv := mcp.AsWire()
		srv.Notes = mcp.Notes
		srv.Installation = mcp.Installation
		srv.SCM = bundleSCM(src)
		result[name] = srv
	}

	return result
}
