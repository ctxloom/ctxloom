package config

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/ctxloom/ctxloom/internal/shared/errs"
	"github.com/ctxloom/ctxloom/internal/shared/report"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// bindTrustRoot and bindCatalog attach the generation's signer trust root and
// resolved Catalog to the Config the Owner is about to publish, so a consumer
// reaching this generation through its *Config sees exactly what the Snapshot
// carries. The root is bound first because the readers the catalog resolves
// verify against it. Called once per generation, before publication; never on
// a published value.
func (c *Config) bindTrustRoot(root trust.TrustRoot, sigCheckDisabled bool) {
	c.trustRoot, c.sigCheckDisabled = root, sigCheckDisabled
}

func (c *Config) bindCatalog(catalog func() bundles.Catalog) { c.catalog = catalog }

// Catalog returns the generation's bundle catalog. Every Config an Owner
// published had one bound before publication (bindCatalog) and returns
// that same resolved set for its life. A Config no Owner published — a
// fixture — has no generation to pin: it resolves the one reader core
// itself can build, the project's authored bundles, on every call, and never
// sees remote or companion content, which only the composition root's
// Sources supply. It verifies against its TrustRoot, which for a fixture
// nobody bound trusts no signer (trust.NoSigners): a signed bundle reads as
// untrusted rather than as whatever the machine's signer files say.
func (c *Config) Catalog() bundles.Catalog {
	if c.catalog != nil {
		return c.catalog()
	}
	root := c.TrustRoot()
	return bundles.Resolve(context.Background(), c.rep.Sink,
		bundles.NewProjectReader(c.getFS(), c.BundleReaderDirs(), bundles.WithTrustRoot(root), bundles.WithReaderReporter(c.rep.Sink)))
}

// TrustRoot is the generation's signer trust root (bindTrustRoot). A Config
// no Owner published trusts no signer (trust.NoSigners).
func (c *Config) TrustRoot() trust.TrustRoot {
	if c == nil || c.trustRoot == nil {
		return trust.NoSigners{}
	}
	return c.trustRoot
}

// SignatureCheckDisabled reports whether this generation was built with
// signature verification waived (WithoutSignatureCheck).
func (c *Config) SignatureCheckDisabled() bool { return c != nil && c.sigCheckDisabled }

// BindTrustRootForTesting binds root and the signature-check posture to this
// Config exactly as the Owner does before publishing a Snapshot.
func (c *Config) BindTrustRootForTesting(root trust.TrustRoot, sigCheckDisabled bool) {
	c.bindTrustRoot(root, sigCheckDisabled)
}

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
	for _, ref := range cat.CompanionRefs() {
		addServers(ref, loadMCPFromBundleRef(c.rep, ref, cat))
	}

	// Finally the profile-referenced bundles. A bundle listed by two profiles
	// in scope is the SAME ref reached twice and dedupes silently; a bundle
	// that declares a name a companion or another profile bundle already
	// claimed is a contest between two different refs, and mcpNameClaims
	// withholds it loudly.
	eachBundleRef(set, func(bundleRef string) {
		addServers(bundleRef, loadMCPFromBundleRef(c.rep, bundleRef, cat))
	})

	return result
}

// LinkGrantFor answers the link-group question for a run over an already
// resolved profile set from the run's OWN granted set —
// ResolveBundleMCPServersFor over the same profiles the engine is launched
// with — through bundles.ServerGrant, so a fragment, skill or hook linked to an
// MCP server is delivered exactly when that server, as shipped by its own
// bundle, is.
//
// The servers are resolved ONCE, on the FIRST question, and never at
// construction. Both halves matter: the resolve reports a fail-loudly finding
// per unloadable bundle ref and per name contest, so memoising keeps a grant
// asked many times in one assembly from repeating them, and deferring keeps a
// grant built by a surface that never meets a linked item from recording them
// at all.
func (c *Config) LinkGrantFor(set []profiles.ResolvedProfile) bundles.LinkGrant {
	var (
		once  sync.Once
		grant bundles.LinkGrant
	)
	return bundles.LinkGrantFunc(func(read bundles.BundleRead, server string) bool {
		once.Do(func() { grant = bundles.ServerGrant(c.ResolveBundleMCPServersFor(set)) })
		return grant.Granted(read, server)
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
func loadMCPFromBundleRef(rep report.Reporter, bundleRef string, cat bundles.Catalog) map[string]wire.MCPServer {
	read, err := cat.Read(bundleRef)
	if err != nil {
		reportBundleRefLoadFailure(rep, bundleRef, err)
		return nil
	}
	return extractMCPFromBundle(rep, read, read.SourceRef())
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
	// A bundle its reader found but could not produce was reported by that
	// reader, with the remedy for its real cause; reporting the ref too would
	// make N broken bundles read as 2N findings.
	if errors.Is(err, errs.ErrBundleUnreadable) {
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
	for _, ref := range cat.CompanionRefs() {
		result.Append(loadHooksFromBundleRef(c.rep, ref, cat, links))
	}

	eachBundleRef(set, func(bundleRef string) {
		result.Append(loadHooksFromBundleRef(c.rep, bundleRef, cat, links))
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

// resolveProfileScope returns the profile set a bundle-resolution call should
// use: the caller's explicit selection (e.g. `run -p`) when non-empty, else the
// configured defaults. This is the seam that makes mcp/hooks follow the
// SELECTED profile (the same set AssembleContext scopes context to) instead of
// always the defaults, while preserving the default-scoped behavior for the
// `manage`/apply-hooks path that passes nothing.
func (c *Config) resolveProfileScope(profileNames []string) []string {
	if len(profileNames) > 0 {
		return profileNames
	}
	return c.DefaultAgentProfiles()
}

// loadHooksFromBundleRef loads hooks from a bundle reference. Like
// loadMCPFromBundleRef it resolves via loader.Load (seed-aware) rather than a
// computed fs path, so remote bundles' hooks aren't silently dropped.
func loadHooksFromBundleRef(rep report.Reporter, bundleRef string, cat bundles.Catalog, links bundles.LinkGrant) wire.UnifiedHooks {
	read, err := cat.Read(bundleRef)
	if err != nil {
		reportBundleRefLoadFailure(rep, bundleRef, err)
		return wire.UnifiedHooks{}
	}
	return extractHooksFromBundle(rep, read, read.SourceRef(), links)
}

// extractHooksFromBundle converts a bundle's hooks to wire.Hooks. Each hook is
// addressed by the canonical bundle-reference grammar's item selector over
// source (bundles.ItemRefFor(src, trust.KindHook, "<event>/<index>")); a hook
// nothing can address is a named load error and costs only itself. The
// identity scheme is bundles.HookEntry.ID() ("<event>/<index>").
//
// links is the run's link grant (bundles.LinkGrant): a hook linked to an MCP
// server the run was not granted is withheld here, exactly as the content
// pipeline withholds a linked fragment — hooks never pass through that
// pipeline, so this is where the group's atomicity is enforced for them. A nil
// grant withholds every linked hook; a surface that checks no links says
// bundles.LinksUnchecked.
func extractHooksFromBundle(rep report.Reporter, read bundles.BundleRead, src trust.BundleRef, links bundles.LinkGrant) wire.UnifiedHooks {
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
		// themselves, because the authored index is a hook's identity on this
		// path ("<bundle>#hooks/<event>/<index>", shared with the migration
		// baseline). Resolving to a new position must not renumber that ref.
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
			if _, rerr := bundles.ItemRefFor(src, trust.KindHook, id); rerr != nil {
				// A hook nothing can address is a named load error, and one
				// such hook costs itself, never the bundle's other hooks.
				rep.Failf(report.KindBundle,
					"fix or re-pull the bundle, or pass --degraded",
					"bundle hook withheld: %v", rerr)
				continue
			}
			// Effective tags, as LinkGroups computes them.
			if linkID, server, withheld := bundles.LinkWithholds(links, read, slices.Concat(bundle.Tags, h.Tags)); withheld {
				bundles.WarnLinkWithheld(rep, read.DisplayName()+"#hooks/"+id, linkID, server)
				continue
			}
			out = append(out, wire.Hook{
				Matcher:         h.Matcher,
				Command:         h.Command,
				Args:            h.Args,
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

// extractMCPFromBundle extracts MCP servers from a loaded bundle. Each server
// is addressed by the canonical bundle-reference grammar's item selector over
// source (bundles.ItemRefFor(src, trust.KindMCP, name)); a server nothing can
// address is a named load error and costs only itself.
func extractMCPFromBundle(rep report.Reporter, read bundles.BundleRead, src trust.BundleRef) map[string]wire.MCPServer {
	bundle := read.Bundle
	result := make(map[string]wire.MCPServer)

	for name, mcp := range bundle.MCP {
		// Key by the source ref (canonical for a cloned bundle, local name for
		// a project bundle). See extractHooksFromBundle.
		if _, rerr := bundles.ItemRefFor(src, trust.KindMCP, name); rerr != nil {
			rep.Failf(report.KindBundle,
				"fix or re-pull the bundle, or pass --degraded",
				"bundle MCP server withheld: %v", rerr)
			continue
		}
		srv := mcp.AsWire()
		srv.Notes = mcp.Notes
		srv.Installation = mcp.Installation
		srv.SCM = bundles.BundleSCM(src)
		result[name] = srv
	}

	return result
}
