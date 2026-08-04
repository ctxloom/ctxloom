package config

import (
	"context"
	"os/exec"
	"sort"
	"strings"
	"sync"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// lookPath is the PATH-resolution seam for tests.
var lookPath = exec.LookPath

// SetExecutableTrustGate injects the trust gate consulted when resolving the
// bundle executable surfaces — bundle MCP servers (ResolveBundleMCPServers),
// bundle hooks (ResolveBundleHooks), and prompt command-file exports
// (backends.LoadCommandExports). The operations/run consumers set it before
// writing backend settings (trust rework, TR5) so an untrusted bundle's
// executables are omitted; management/listing paths leave it nil (no gating).
// Builtin bundles are always exempt regardless of the gate.
func (c *Config) SetExecutableTrustGate(gate bundles.ContentGate) {
	c.execGate = gate
}

// ExecutableTrustGate returns the injected executable trust gate (nil when none
// was set — no gating).
func (c *Config) ExecutableTrustGate() bundles.ContentGate {
	return c.execGate
}

// SetLookPathForTesting overrides the companion-binary PATH-resolution seam and
// returns a restore function. Tests in other packages use it to make companion
// detection (built-in bundle hooks/MCP/fragments) deterministic regardless of
// what is installed on the developer's machine.
func SetLookPathForTesting(fn func(string) (string, error)) func() {
	prev := lookPath
	lookPath = fn
	return func() { lookPath = prev }
}

// missingWarned tracks binaries already warned about, so a missing companion
// produces one hint per process, not one per resolve pass.
var (
	missingWarnedMu sync.Mutex
	missingWarned   = map[string]bool{}
)

// missingCompanion reports whether the command's executable is a companion
// binary absent from PATH. Commands run by ctxloom itself (`ctxloom hook ...`)
// always pass — gating exists for foreign binaries shipped separately
// (taskloom, ltk), whose builtin-bundle entries must degrade to absent rather
// than register a broken server or hook.
func missingCompanion(command string) (string, bool) {
	bin := companionBin(command)
	if bin == "" {
		return "", false
	}
	if _, err := lookPath(bin); err != nil {
		return bin, true
	}
	return "", false
}

// companionBin extracts the companion executable a command invokes: its first
// field, unless the command is empty or runs ctxloom itself.
func companionBin(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 || fields[0] == "ctxloom" {
		return ""
	}
	return fields[0]
}

// warnMissingCompanion emits the one-shot install hint for an absent binary.
func warnMissingCompanion(bin, hint string) {
	missingWarnedMu.Lock()
	defer missingWarnedMu.Unlock()
	if missingWarned[bin] {
		return
	}
	missingWarned[bin] = true
	msg := bin + " not found on PATH; its tools are disabled for this session"
	if hint != "" {
		msg += " (" + strings.TrimSpace(hint) + ")"
	}
	clidiag.Warn("ctxloom", "%s", msg)
}

// ResolveBundleMCPServers loads MCP servers from bundles referenced in the
// caller's selected profiles (or the configured defaults when none are passed),
// plus servers shipped by built-in bundles embedded in the binary
// (resources/builtin_bundles) and by every discovered COMPANION's loadout
// (S8 — a companion's MCP registration is unconditional, matching the old
// embedded-bundle behavior for taskloom, but NEVER exempt like a builtin: it
// is routed through the identical extraction+gate path a profile-referenced
// bundle uses, so trusted-signer/approved/pending applies). Mirrors
// ResolveBundleHooks: any future built-in that ships an MCP server is picked
// up automatically, tagged with SCM="bundle:builtin:<name>" so reconciliation
// can identify it.
func (c *Config) ResolveBundleMCPServers(profileNames []string) map[string]wire.MCPServer {
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
	var scopedProfiles []*profiles.ResolvedProfile
	excluded := make(map[string]bool)
	if len(c.appPaths) > 0 {
		profileLoader := c.GetProfileLoader()
		for _, profileName := range c.resolveProfileScope(profileNames) {
			// Resolve through the recursive resolver so bundles inherited from
			// parent profiles are included — a flat Load would only see this
			// profile's direct Bundles, silently dropping MCP servers shipped by
			// an inherited bundle (while the fragment path, which resolves
			// recursively, still picks them up). See ResolveBundleHooks for the
			// matching pattern.
			resolved, ok := resolveProfileOrReport(profileLoader, profileName)
			if !ok {
				continue
			}
			scopedProfiles = append(scopedProfiles, resolved)
			for _, name := range resolved.ExcludeMCP {
				excluded[name] = true
			}
		}
	}

	// addServers is the ONLY way a server reaches result, so no source can
	// bypass the exclusion filter by construction.
	addServers := func(servers map[string]wire.MCPServer) {
		for name, server := range servers {
			if excluded[name] {
				continue
			}
			result[name] = server
		}
	}

	// Built-in bundles are unconditional — they ship core ctxloom
	// functionality and aren't gated on profile membership. Run them
	// first so profile-sourced servers can intentionally override. They ARE
	// routed through c.execGate (nil on management/listing paths, matching the
	// no-gating convention there) so a builtin item can still be REJECTED —
	// see resolveBuiltinBundleMCPServers.
	addServers(resolveBuiltinBundleMCPServers(c.execGate))

	// BundleLoader includes remote bundles from the active lockfile AND every
	// discovered companion's loadout, read under its ctxloom:companion@<bin>
	// ref; without them, MCP servers shipped in remote bundles (or a companion
	// loadout) silently disappear (see docs/bundle-review-plan.md Phase 1.2).
	bundleLoader := c.BundleLoader()

	// Companion loadouts are resolved through loadMCPFromBundleRef — the SAME
	// path a profile-referenced bundle uses (Load -> extractMCPFromBundle) —
	// so a companion's server is judged by ITS bundle's own source ref and
	// verified Signer(), never the builtin exemption. Sorted for a
	// deterministic result across runs.
	for _, ref := range companionRefs(bundleLoader) {
		addServers(loadMCPFromBundleRef(ref, bundleLoader, c.execGate))
	}

	// Finally the profile-referenced bundles, so profile-sourced servers still
	// override builtin/companion ones of the same name.
	for _, resolved := range scopedProfiles {
		for _, bundleRef := range resolved.Bundles {
			addServers(loadMCPFromBundleRef(bundleRef, bundleLoader, c.execGate))
		}
	}

	return result
}

// resolveBuiltinBundleMCPServers parses every YAML under
// resources/builtin_bundles/ (embedded at build time) and returns the
// merged MCP-server map. Mirrors resolveBuiltinBundleHooks. Each server
// is tagged with SCM="bundle:builtin:<name>" so apply-* reconciliation can
// identify built-in entries. Failures on individual bundles are logged
// to stderr and skipped — built-in bundles must never block startup.
//
// gate is run through extractMCPFromBundle exactly like a remote/local bundle's
// servers (the "builtin:<name>" source ref parses as trust.Ref{IsBuiltin: true}
// — see operations.parseTrustItemRef): the decision function still ALLOWS a
// builtin server by default (no new review friction — trust-model.md's builtin
// exemption), but a REJECTED builtin item is now withheld, because rejection is
// evaluated before the builtin exemption. A nil gate (management/listing paths,
// matching every other resolver here) is fully ungated, unchanged from before.
func resolveBuiltinBundleMCPServers(gate bundles.ContentGate) map[string]wire.MCPServer {
	out := make(map[string]wire.MCPServer)
	eachBuiltinBundle(func(name string, b *bundles.Bundle) {
		for serverName, server := range extractMCPFromBundle(b, "builtin:"+name, gate) {
			// Builtin bundles wire in standalone companion binaries; a
			// missing one degrades to no entry (and one install hint)
			// rather than a broken server in every backend.
			if bin, missing := missingCompanion(server.Command); missing {
				warnMissingCompanion(bin, server.Installation)
				continue
			}
			out[serverName] = server
		}
	})
	return out
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
// (resolveProfileParents) already calls strictness.Fail for this, as does the
// sibling loadBundleProfileSeed; this brings the bundle resolvers into line.
//
// FailOnce, not Fail: one unresolvable profile is hit by all four resolvers
// (and each of those by several callers), so the per-message dedup keeps it to
// a single line per window while still recording the finding the startup choke
// aborts on in strict mode. --degraded downgrades it, as everywhere else.
func resolveProfileOrReport(profileLoader *profiles.Loader, profileName string) (*profiles.ResolvedProfile, bool) {
	resolved, err := profileLoader.ResolveProfile(profileName, nil)
	if err != nil {
		strictness.FailOnce(strictness.ClassRef,
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
func loadMCPFromBundleRef(bundleRef string, loader *bundles.Loader, gate bundles.ContentGate) map[string]wire.MCPServer {
	bundle, err := loader.Load(bundleRef)
	if err != nil {
		reportBundleRefLoadFailure(bundleRef, err)
		return nil
	}
	return extractMCPFromBundle(bundle, bundleRef, gate)
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
func reportBundleRefLoadFailure(bundleRef string, err error) {
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
	strictness.FailOnce(strictness.ClassBundle,
		"run `ctxloom remote pull` or fix the bundle ref, or pass --degraded",
		"failed to load bundle %q; the MCP servers and hooks it ships are not applied: %v", bundleRef, err)
}

// ResolveBundleHooks aggregates hooks shipped by every bundle referenced
// in the caller's selected profiles (or the configured defaults when none
// are passed), plus the always-on hooks shipped by built-in bundles embedded
// in the binary (resources/builtin_bundles) and by every discovered
// COMPANION's loadout (S8 — unconditional like a builtin hook, e.g. ltk's
// pre-tool guard or taskloom's session-bind, but NEVER exempt like one: see
// ResolveBundleMCPServers for why). Mirrors ResolveBundleMCPServers. Each
// emitted hook carries SCM source info so apply-hooks can identify
// ctxloom-managed entries when reconciling the backend's settings.json.
func (c *Config) ResolveBundleHooks(profileNames []string) wire.UnifiedHooks {
	var result wire.UnifiedHooks

	// Built-in bundles are unconditional — they ship core ctxloom
	// functionality (session bind, plan-stamping). No profile
	// gating, no remote pull. Routed through c.execGate (see
	// resolveBuiltinBundleMCPServers for why: allowed by default, but now
	// reachable by a rejection).
	result.Append(resolveBuiltinBundleHooks(c.execGate))

	bundleLoader := c.BundleLoader()

	// Companion loadout hooks: same extraction+gate path a profile-referenced
	// bundle uses, keyed and signed by the companion's OWN bundle — never the
	// builtin exemption. Sorted for a deterministic result across runs.
	for _, ref := range companionRefs(bundleLoader) {
		result.Append(loadHooksFromBundleRef(ref, bundleLoader, c.execGate))
	}

	profiles := c.resolveProfileScope(profileNames)
	if len(profiles) == 0 || len(c.appPaths) == 0 {
		return result
	}
	profileLoader := c.GetProfileLoader()

	for _, profileName := range profiles {
		// Resolve recursively so hooks shipped by bundles inherited from
		// parent profiles are included (matches ResolveBundleMCPServers and
		// the fragment resolution path); a flat Load would drop them.
		resolved, ok := resolveProfileOrReport(profileLoader, profileName)
		if !ok {
			continue
		}
		for _, bundleRef := range resolved.Bundles {
			hooks := loadHooksFromBundleRef(bundleRef, bundleLoader, c.execGate)
			result.Append(hooks)
		}
	}
	return result
}

// companionRefs returns the loader's companion loadout refs
// (ctxloom:companion@<bin>) in deterministic sorted order.
//
// It asks the LOADER what it read rather than re-probing: the reads already
// carry which source each bundle came from, so "everything the companion reader
// contributed" is a fact on the record instead of a second discovery pass that
// could answer differently — and it does not exec anything a second time.
func companionRefs(loader *bundles.Loader) []string {
	var out []string
	for _, read := range loader.Reads() {
		if read.Provenance == bundles.ProvenanceCompanion {
			out = append(out, read.Ref())
		}
	}
	sort.Strings(out)
	return out
}

// companionReads returns the loader's companion loadout reads in the same
// deterministic order, for the one caller that needs the bundles themselves.
func companionReads(loader *bundles.Loader) []bundles.BundleRead {
	var out []bundles.BundleRead
	for _, read := range loader.Reads() {
		if read.Provenance == bundles.ProvenanceCompanion {
			out = append(out, read)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref() < out[j].Ref() })
	return out
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
// exempt like a builtin: routed through bundleLoader.CommandsFromBundleRef,
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
func (c *Config) ResolveBundleCommands(profileNames []string, opts ...BundleLoaderOption) []*bundles.LoadedContent {
	loader := c.BundleLoader(opts...)
	pipe := bundles.NewPipeline(loader, c.ExecutableTrustGate(), c.ShouldUseDistilled())

	seen := make(map[string]bool)
	var out []*bundles.LoadedContent
	add := func(prompt *bundles.LoadedContent) {
		if seen[prompt.Item] {
			return
		}
		seen[prompt.Item] = true
		out = append(out, prompt)
	}

	profiles := c.resolveProfileScope(profileNames)
	if len(profiles) > 0 && len(c.appPaths) > 0 {
		profileLoader := c.GetProfileLoader()
		for _, profileName := range profiles {
			resolved, ok := resolveProfileOrReport(profileLoader, profileName)
			if !ok {
				continue
			}
			for _, bundleRef := range resolved.Bundles {
				for _, prompt := range pipe.CommandsFromBundleRef(bundleRef) {
					add(prompt)
				}
			}
		}
	}

	for _, command := range resolveCompanionCommandsWith(pipe, loader) {
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
func (c *Config) ResolveBundleSkills(profileNames []string, opts ...BundleLoaderOption) []*bundles.LoadedSkill {
	pipe := bundles.NewPipeline(c.BundleLoader(opts...), c.ExecutableTrustGate(), c.ShouldUseDistilled())

	seen := make(map[string]bool)
	var out []*bundles.LoadedSkill
	add := func(skill *bundles.LoadedSkill) {
		if seen[skill.Item] {
			return
		}
		seen[skill.Item] = true
		out = append(out, skill)
	}

	profiles := c.resolveProfileScope(profileNames)
	if len(profiles) > 0 && len(c.appPaths) > 0 {
		profileLoader := c.GetProfileLoader()
		for _, profileName := range profiles {
			resolved, ok := resolveProfileOrReport(profileLoader, profileName)
			if !ok {
				continue
			}
			for _, bundleRef := range resolved.Bundles {
				for _, skill := range pipe.SkillsFromBundleRef(bundleRef) {
					add(skill)
				}
			}
		}
	}
	return out
}

// ResolveCompanionCommands returns the commands shipped by every discovered
// companion's loadout (S8 — companionBundleSeed / sortedCompanionRefs),
// unconditionally whenever the companion binary is on PATH, in deterministic
// (companion-ref-sorted, then name-sorted within a loadout) order. Routed
// through bundleLoader.CommandsFromBundleRef — the SAME extraction+gate path a
// profile-referenced bundle's commands use, keyed and signed by the
// companion's OWN bundle — never the builtin nil-gate exemption; it decides
// with the cfg-carried executable trust gate exactly like ResolveBundleCommands,
// so an unsigned/withheld companion loadout's commands do not export.
//
// This is the piece LoadCommandExports adds on BOTH its curated and uncurated
// paths (ResolveBundleCommands only covers the uncurated one, since a
// profile's commands: curation bypasses it entirely) — see prompts.go.
func (c *Config) ResolveCompanionCommands(opts ...BundleLoaderOption) []*bundles.LoadedContent {
	loader := c.BundleLoader(opts...)
	return resolveCompanionCommandsWith(
		bundles.NewPipeline(loader, c.ExecutableTrustGate(), c.ShouldUseDistilled()), loader)
}

// resolveCompanionCommandsWith is the shared companion-command extraction
// loop, taking an already-built pipeline so ResolveBundleCommands (which needs
// one for the profile-scoped pass too) doesn't construct a second one. Gate and
// form travel with it, so both callers necessarily agree on both.
func resolveCompanionCommandsWith(pipe *bundles.Pipeline, loader *bundles.Loader) []*bundles.LoadedContent {
	var out []*bundles.LoadedContent
	for _, ref := range companionRefs(loader) {
		out = append(out, pipe.CommandsFromBundleRef(ref)...)
	}
	return out
}

// resolveBuiltinBundleHooks parses every YAML under
// resources/builtin_bundles/ (embedded at build time) and returns the
// merged hook set. Each hook is tagged with SCM="bundle:builtin:<name>" so the
// apply-hooks reconciliation can identify built-in entries. Failures on
// individual bundles are logged to stderr and skipped — built-in bundles
// must never block startup.
// gate is threaded through exactly like resolveBuiltinBundleMCPServers: still
// allowed by default (no new review friction), but now reachable by a
// rejection (rejection is evaluated before the builtin exemption). A nil gate
// (management/listing paths) stays fully ungated.
func resolveBuiltinBundleHooks(gate bundles.ContentGate) wire.UnifiedHooks {
	var out wire.UnifiedHooks
	eachBuiltinBundle(func(name string, b *bundles.Bundle) {
		out.Append(filterMissingCompanionHooks(extractHooksFromBundle(b, "builtin:"+name, gate)))
	})
	return out
}

// filterMissingCompanionHooks drops hooks whose executable is a companion
// binary absent from PATH (one install hint per binary). ctxloom's own hooks
// always pass; see missingCompanion.
func filterMissingCompanionHooks(in wire.UnifiedHooks) wire.UnifiedHooks {
	keep := func(hooks []wire.Hook) []wire.Hook {
		var out []wire.Hook
		for _, h := range hooks {
			if bin, missing := missingCompanion(h.Command); missing {
				warnMissingCompanion(bin, "")
				continue
			}
			out = append(out, h)
		}
		return out
	}
	return wire.UnifiedHooks{
		PreTool:      keep(in.PreTool),
		PostTool:     keep(in.PostTool),
		SessionStart: keep(in.SessionStart),
		SessionEnd:   keep(in.SessionEnd),
		PreShell:     keep(in.PreShell),
		PostFileEdit: keep(in.PostFileEdit),
	}
}

// BuiltinFragment is one always-on fragment shipped by a built-in bundle,
// ready for unconditional injection into assembled context.
type BuiltinFragment struct {
	Name         string // reporting identity ("builtin:<bundle>#fragments/<name>")
	Content      string
	Installation string
}

// ResolveBuiltinBundleFragments returns the fragments shipped by built-in
// bundles embedded in the binary (resources/builtin_bundles), for unconditional
// injection into assembled context — the always-on counterpart to
// ResolveBundleHooks / ResolveBundleMCPServers, which wire in those same
// bundles' hooks and MCP servers. A bundle whose companion binary (the
// executable its hooks/MCP invoke, e.g. ltk, taskloom) is absent from PATH is
// skipped: with the tool not installed, briefing the agent about it is noise,
// and its hooks/MCP are skipped for the same reason. ctxloom-only built-in
// bundles have no companion and always inject. Order is deterministic (bundle
// name, then fragment name) for a stable context hash. The missing-companion
// install hint is emitted by the hooks/MCP path; this resolver stays silent.
//
// gate mirrors ResolveBundleHooks/ResolveBundleMCPServers's builtin routing: a
// builtin fragment is still exposed by default (no new review friction), but a
// REJECTED builtin fragment is now withheld — rejection is evaluated before the
// builtin exemption. Callers on an exposure surface pass the SAME gate their
// content loader is using (Loader.Gate()) so builtin fragments gate through the
// identical decision as the loader-resolved ones, sharing its trust-store
// open and its withheld-ref tally. nil (management/listing callers) is fully
// ungated, matching every other resolver here.
func (c *Config) ResolveBuiltinBundleFragments(gate bundles.ContentGate) []BuiltinFragment {
	preferDistilled := c.ShouldUseDistilled()
	var out []BuiltinFragment

	eachBuiltinBundle(func(name string, b *bundles.Bundle) {
		if _, missing := builtinBundleCompanionMissing(b); missing {
			return
		}
		// A builtin carries NO signer: it is not signed and must not be
		// (signing bytes embedded in the binary that verifies them is
		// circular — spec §4.5). The "builtin:" ref prefix is what routes it
		// to the decision function's builtin step, which sits BELOW
		// rejection so a user can still reject a builtin.
		out = fragmentsFromBundle(out, "builtin:"+name, b, "", preferDistilled, gate)
	})

	// Companion loadouts (S8): unconditional like a builtin fragment (the
	// old embedded-bundle behavior for ltk/taskloom), but NEVER exempt like
	// one — ref carries the companion's own ctxloom:companion@<bin> source
	// and signer carries its VERIFIED publisher identity, so it reaches
	// EffectiveTrust's trusted-signer/approved/pending steps like any other
	// seeded bundle. Do NOT route this through the builtin ref prefix above:
	// that is precisely the nil-gate/exemption bypass the trust rework
	// forbids for third-party content.
	for _, read := range companionReads(c.BundleLoader()) {
		b := read.Bundle
		out = fragmentsFromBundle(out, read.Ref(), b, b.Signer(), preferDistilled, gate)
	}
	return out
}

// fragmentsFromBundle extracts every fragment in b as BuiltinFragment
// entries in a stable (sorted-by-name) order, applying gate per item
// (ref = "<source>#fragments/<name>", using the caller-supplied signer) and
// skipping empty content. Shared by the embedded-builtin loop above (signer
// "", ref "builtin:<name>") and the companion-loadout loop (signer
// b.Signer(), ref "ctxloom:companion@<bin>") — the only two callers, which
// differ solely in source/signer.
func fragmentsFromBundle(out []BuiltinFragment, source string, b *bundles.Bundle, signer string, preferDistilled bool, gate bundles.ContentGate) []BuiltinFragment {
	fragNames := make([]string, 0, len(b.Fragments))
	for fragName := range b.Fragments {
		fragNames = append(fragNames, fragName)
	}
	sort.Strings(fragNames)
	for _, fragName := range fragNames {
		frag := b.Fragments[fragName]
		content := frag.EffectiveContent(preferDistilled)
		if strings.TrimSpace(content) == "" {
			continue
		}
		ref := source + "#fragments/" + fragName
		if gate != nil {
			payload, form := frag.ContentPayload(preferDistilled)
			if !gate(ref, payload, string(form), signer) {
				continue // withheld by the trust gate (e.g. rejected, or pending)
			}
		}
		out = append(out, BuiltinFragment{
			Name:         ref,
			Content:      content,
			Installation: frag.Installation,
		})
	}
	return out
}

// builtinBundleCompanionMissing reports the companion binary a built-in bundle
// depends on (the first executable its hooks or MCP servers invoke that isn't
// ctxloom itself) and whether it is absent from PATH. A bundle with no companion
// returns ("", false). Mirrors the gating applied to the bundle's hooks/MCP.
func builtinBundleCompanionMissing(b *bundles.Bundle) (string, bool) {
	for _, hs := range [][]bundles.BundleHook{
		b.Hooks.PreTool, b.Hooks.PostTool, b.Hooks.SessionStart,
		b.Hooks.SessionEnd, b.Hooks.PreShell, b.Hooks.PostFileEdit,
	} {
		for _, h := range hs {
			if bin, missing := missingCompanion(h.Command); missing {
				return bin, true
			}
		}
	}
	for _, m := range b.MCP {
		if bin, missing := missingCompanion(m.Command); missing {
			return bin, true
		}
	}
	return "", false
}

// loadHooksFromBundleRef loads hooks from a bundle reference. Like
// loadMCPFromBundleRef it resolves via loader.Load (seed-aware) rather than a
// computed fs path, so remote bundles' hooks aren't silently dropped.
func loadHooksFromBundleRef(bundleRef string, loader *bundles.Loader, gate bundles.ContentGate) wire.UnifiedHooks {
	bundle, err := loader.Load(bundleRef)
	if err != nil {
		reportBundleRefLoadFailure(bundleRef, err)
		return wire.UnifiedHooks{}
	}
	return extractHooksFromBundle(bundle, bundleRef, gate)
}

// extractHooksFromBundle converts a bundle's hooks to wire.Hooks. When gate is
// non-nil (the executable trust gate, TR5), each hook's executable surface is
// hashed (BundleHook.ComputeContentHash) and run through the cascade keyed
// "<bundle>#hooks/<event>/<index>"; a DENY omits the hook — a bundle hook is an
// arbitrary-command executable that must never be applied unevaluated
// (fail-closed). Builtin callers pass nil (in-binary, exempt). The identity
// scheme is bundles.HookEntry, shared with the migration baseline so a baselined
// hook's ref matches.
func extractHooksFromBundle(bundle *bundles.Bundle, source string, gate bundles.ContentGate) wire.UnifiedHooks {
	if !bundle.Hooks.HasAny() {
		return wire.UnifiedHooks{}
	}
	marker := "bundle:" + source
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
			if gate != nil {
				// Key by the bundle's source ref (canonical for a remote/cloned
				// bundle, the local name for a project bundle) — NOT bundle.Name,
				// whose short form is ambiguous across local and cloned bundles.
				// This makes the cascade's IsLocal/RepoURL honest (local hooks
				// auto-trust; a cloned one is judged by WHO SIGNED it) and aligns
				// the gate key with the baseline/grant key (both source).
				ref := source + "#hooks/" + bundles.HookEntry{Event: event, Index: i}.ID()
				payload, perr := h.ContentPayload()
				if perr != nil {
					continue // cannot build the preimage → cannot evaluate → withhold
				}
				if !gate(ref, payload, string(bundles.FormRaw), bundle.Signer()) {
					continue // withheld by the trust gate
				}
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
	}
}

// extractMCPFromBundle extracts MCP servers from a loaded bundle. When gate is
// non-nil (the executable trust gate, TR5), each server's executable surface
// (Command+Args+Env+Installation) is hashed and run through the cascade keyed
// "<bundle>#mcp/<name>"; a DENY omits the server entirely — an arbitrary-command
// executable must never reach settings unevaluated (fail-closed). Builtin
// callers pass nil (in-binary, exempt).
func extractMCPFromBundle(bundle *bundles.Bundle, source string, gate bundles.ContentGate) map[string]wire.MCPServer {
	result := make(map[string]wire.MCPServer)

	for name, mcp := range bundle.MCP {
		if gate != nil {
			// Key by the source ref (canonical for a cloned bundle, local name for
			// a project bundle) so the cascade's IsLocal/RepoURL are honest and the
			// gate key matches the baseline/grant key. See extractHooksFromBundle.
			ref := source + "#mcp/" + name
			payload, perr := mcp.ContentPayload()
			if perr != nil {
				continue // cannot build the preimage → cannot evaluate → withhold
			}
			if !gate(ref, payload, string(bundles.FormRaw), bundle.Signer()) {
				continue // withheld by the trust gate
			}
		}
		result[name] = wire.MCPServer{
			Command:      mcp.Command,
			Args:         mcp.Args,
			Env:          mcp.Env,
			Notes:        mcp.Notes,
			Installation: mcp.Installation,
			SCM:          "bundle:" + source, // Mark as coming from a bundle
		}
	}

	return result
}

// eachBuiltinBundle parses every bundle embedded in the binary through the
// CANONICAL bundle parser and hands each one to fn. A bundle that cannot be
// listed, read or parsed is a warning and is skipped — one broken builtin never
// sinks the others.
//
// It exists so the four surfaces that read builtin bundles (MCP servers, hooks,
// fragments, companion bins) cannot disagree about what a builtin bundle SAYS.
// They previously each ran their own `yaml.Unmarshal`, which bypasses
// bundles.ParseBundle's schema-upgrade pipeline: a migration that renames a key
// would have applied on every on-disk and remote bundle and silently not here,
// so the same bytes would mean different things depending on which surface
// asked. Routing all four through the builtin READER — the same localfs
// implementation that reads the project's own bundles, pointed at the embedded
// filesystem — is what makes "a builtin bundle" one definition rather than four,
// and what keeps a builtin's trust facts established the same way everything
// else's are.
func eachBuiltinBundle(fn func(name string, b *bundles.Bundle)) {
	reads, err := bundles.NewBuiltinReader().Read(context.Background())
	if err != nil {
		clidiag.Warn("ctxloom", "read builtin bundles: %v", err)
		return
	}
	for _, read := range reads {
		fn(read.Ref(), read.Bundle)
	}
}
