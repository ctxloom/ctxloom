package operations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/cbroglie/mustache"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// Mustache tag classification uses cbroglie/mustache's OWN exported TagType
// constants (mustache.Variable / Section / InvertedSection). This package used
// to mirror that enum as three untyped ints of its own, and the mirror drifted:
// it was numbered one too high for every section-shaped tag, so a real Section
// was classified as a plain variable, hasChildTags never matched one, and
// checkTags never walked into a `{{#name}}...{{/name}}` body to find the
// undefined variables nested inside it. Naming the library's constants makes
// that class of drift unrepresentable.
//
// Note there is deliberately no "RawVariable" constant in the library:
// {{name}}, {{{name}}} and {{&name}} all report Type()==Variable — raw vs
// escaped is tracked internally (varElement.raw) and not exposed through the
// public Tag interface, so checkTags/undefinedPlainVariableLiterals cannot and
// do not distinguish them.

// ProfileLoader interface for resolving profiles from directory (allows mocking in tests).
type ProfileLoader interface {
	ResolveProfile(name string, visited map[string]bool) (*profiles.ResolvedProfile, error)
}

// AssembleContextRequest contains parameters for assembling context.
type AssembleContextRequest struct {
	Profile string `json:"profile"`
	// Profiles composes SEVERAL named profiles into one assembled context (union
	// of fragments, later-wins variables, first-non-empty llm) — the same merge
	// the configured-defaults path already runs, surfaced as an explicit ask so a
	// agent can bind multiple profiles. When non-empty it takes precedence over
	// Profile; an explicit set's resolution failures are hard errors (not the
	// fault-tolerant skip the defaults path uses).
	Profiles  []string `json:"profiles"`
	Fragments []string `json:"fragments"`
	Tags      []string `json:"tags"`

	// Consumer states WHAT these bytes are for — a live session (the zero
	// value) or a materialized surface for a named engine (MaterializedFor).
	// It is a subject, never a mode: whether premised fragments are withheld
	// and indexed or written into the context is resolved from it in ONE
	// place (ContextConsumer.static), so no caller picks static-vs-dynamic
	// for itself and two callers composing for the same surface cannot
	// disagree about what it holds.
	Consumer ContextConsumer `json:"-"`

	// Loader is an optional pre-configured loader (for testing).
	Pipeline *bundles.Pipeline `json:"-"`

	// ProfileLoaderFunc is an optional function to get the profile loader (for testing).
	ProfileLoaderFunc func() ProfileLoader `json:"-"`
}

// ContextConsumer is what an assembly's bytes are FOR: the subject that
// decides static-vs-dynamic delivery. The zero value is a LIVE session, one
// with ctxloom behind it (its MCP server, the CLI, a hook), which can pull a
// withheld fragment later — so premised fragments are withheld and indexed,
// and the index is the menu it pulls from. MaterializedFor names the other
// kind.
type ContextConsumer struct {
	materialized bool
	backend      string
}

// MaterializedFor names an OUT-OF-THE-LOOP surface: backend's native context
// file, written — or, for a comparison, composed as it would be written — for
// a launch with no ctxloom behind it. Nothing there can pull a withheld
// fragment later, so it is either handed over as a skill package, where the
// engine has a skills surface, or written into the context itself. The
// comparison side must state this too: composing for a live session and
// diffing against a materialized file reports a correct file stale forever.
func MaterializedFor(backend string) ContextConsumer {
	return ContextConsumer{materialized: true, backend: backend}
}

// static is THE resolution of static-vs-dynamic delivery, and the only one:
// a static assembly INCLUDES premised fragments and hands back an empty
// PremiseIndex, because nobody behind the surface can act on a menu. It is
// decided from the consumer alone. A materialized surface goes static exactly
// when its engine has no skills surface to re-deliver the withheld fragments
// through — read via backends.SupportsSkills, the same predicate
// SkillExportsFor gates on, so the mode and the skills delivery cannot
// disagree either. An engine nobody registered is refused rather than treated
// as skill-less: that would dump every premised fragment into a file for a
// launch that does not exist.
func (c ContextConsumer) static() (bool, error) {
	if !c.materialized {
		return false, nil
	}
	backend, err := registeredBackend(c.backend)
	if err != nil {
		return false, err
	}
	return !backends.SupportsSkills(backend), nil
}

// AssembleContextResult contains the assembled context.
// WithheldFragment is one premised fragment an assembly held back, with the
// body it did not deliver.
type WithheldFragment struct {
	Name    string
	Premise string
	Content string
}

type AssembleContextResult struct {
	Profiles        []string `json:"profiles"`
	FragmentsLoaded []string `json:"fragments_loaded"`
	// MissingFragments lists the EXPLICITLY requested fragments (Fragments in
	// the request) that did not resolve/load. Assembly is fault-tolerant (a
	// missing ask warns and is skipped), but callers like `run -f` treat an
	// all-missing explicit ask as a hard error — and the always-on builtin
	// companion fragments mean a non-empty FragmentsLoaded can't signal it.
	MissingFragments []string `json:"missing_fragments,omitempty"`
	// MissingTags names every requested tag (Tags in the request) when the
	// WHOLE tag selection contributed zero fragments (previously
	// AssembleContext returned Context: "" with a nil error and no warning
	// at all — `ctxloom run -t <tag-that-matches-nothing>` exited 0 having
	// delivered no context). ListByTags is a union query (any listed tag
	// matches), so a zero-fragment result cannot be attributed to one tag
	// over another — all requested tags are named together.
	MissingTags []string `json:"missing_tags,omitempty"`
	Context     string   `json:"context"`

	// ProfileLLM is the LLM the resolved profile(s) declared (first non-empty
	// across the resolved set). Empty means no profile preference; callers fall
	// back to the configured primary role. Overridable by -l/--llm at the call
	// site.
	ProfileLLM string `json:"profile_llm,omitempty"`

	// WithheldFragments carries each withheld fragment's already-gated BODY,
	// for an in-process caller that must deliver it some other way — a STATIC
	// assembly turning them into native skill packages for an engine ctxloom
	// will not be present to serve.
	//
	// It is safe on the RESULT only because the MCP surface no longer returns
	// this struct: internal/adapters/mcp projects a DTO that omits this field, so a body
	// cannot reach the wire by anyone adding to the domain type. Before that
	// port existed this had to travel as a request-side callback, which was a
	// workaround for the missing boundary rather than a design.
	WithheldFragments []WithheldFragment `json:"-"`

	// PremiseIndex is IN-PROCESS ONLY. internal/adapters/mcp's DTO does not project it,
	// and the json tag below is vestigial rather than a wire contract: the menu
	// an MCP consumer reads is the ctxloom://fragments resource, which carries
	// each fragment's premise and qualified ref. Do not restore it to the tool
	// response — that is where it lived only because the listing used to drop
	// the premise, and putting a menu on an assembly is what dragged this whole
	// struct onto the wire.
	//
	// PremiseIndex names the fragments this assembly WITHHELD because they
	// carry a premise, together with that premise. It is the agent's menu: it
	// evaluates each premise against what it is about to do and asks for the
	// ones that apply BY NAME, through Fragments on a second call. Empty when
	// nothing was withheld, which is every assembly over a corpus that
	// authors no premises.
	PremiseIndex []PremiseIndexEntry `json:"premise_index,omitempty"`
}

// AssembleContext assembles context from a profile, fragments, and/or tags.
// Fragments are sorted using bookend strategy based on priority:
// highest priority at start, second-highest at end, rest in middle.
func AssembleContext(ctx context.Context, cfg *config.Config, req AssembleContextRequest) (*AssembleContextResult, error) {
	// Resolved first: a consumer that cannot be resolved is a bad request,
	// and there is nothing to assemble for it.
	static, err := req.Consumer.static()
	if err != nil {
		return nil, fmt.Errorf("resolve who consumes the assembled context: %w", err)
	}
	profileNames := resolveContextProfileNames(cfg, req)

	pipe := req.Pipeline
	// gate is the trust gate behind pipe, when this call built its own (nil
	// for an injected test pipeline — see warnWithheld). Kept so the withheld
	// advisory below can name WHY each item was withheld, not just that it was.
	var gate bundles.Authorizer
	if pipe == nil {
		// Exposure surface: gate fragment/prompt content (trust rework, TR5). The
		// gate runs the baseline first (idempotent) so existing content stays
		// exposed, then withholds anything the cascade denies. Link groups are
		// judged against THIS run's granted MCP set — the same profiles the
		// engine is launched with — so a linked fragment is assembled exactly
		// when its server is.
		pipe, gate = exposurePipelineGated(cfg, cfg.LinkGrant(profileNames))
	}
	loader := pipe.Loader()

	// Profiles picked up from configured defaults (rather than an explicit
	// --profile / Profiles ask) degrade per fault-tolerance: a default that fails
	// to resolve is warned about and skipped, never blocking startup. An explicit
	// single Profile or a multi-profile Profiles set is the user's ask, so its
	// failures stay hard errors.
	fromDefaults := req.Profile == "" && len(req.Profiles) == 0

	allFragments, profileVars, profileLLM, declaredByProfile, err := collectProfileFragments(cfg, loader, profileNames, req.ProfileLoaderFunc, fromDefaults)
	if err != nil {
		return nil, err
	}

	// Add request fragments (priority 0) and request-tag fragments. Bare asks
	// resolve to their qualified pipeline name at intake (deterministic pick +
	// warning when the bare name is ambiguous across bundles), so downstream
	// dedup and ordering operate on exact identities only. The resolved asks
	// are remembered so the result can report which EXPLICIT requests went
	// missing.
	requested := make([]string, 0, len(req.Fragments))
	for _, f := range req.Fragments {
		resolved := loader.ResolveFragmentAsk(f)
		requested = append(requested, resolved)
		allFragments = append(allFragments, config.FragmentRef{Name: resolved, Priority: 0})
	}
	reqTagFragments, err := fragmentsFromTags(loader.Catalog(), req.Tags)
	if err != nil {
		return nil, fmt.Errorf("failed to list fragments by tags: %w", err)
	}
	allFragments = append(allFragments, reqTagFragments...)

	// An explicit tag selection that matches nothing must not be silently
	// indistinguishable from "no tags were asked for" — see MissingTags' doc.
	var missingTags []string
	if len(req.Tags) > 0 && len(reqTagFragments) == 0 {
		missingTags = append([]string{}, req.Tags...)
	}

	// Deduplicate (highest priority wins), then bookend-sort for the
	// "lost in the middle" optimization.
	orderedRefs := sortFragmentsByPriority(dedupeFragmentRefs(allFragments))

	// ONE ingest accumulator for BOTH routes into this context — loader-resolved
	// below, injected builtins further down. It is what makes ingest idempotent:
	// the same content arriving by two routes is assembled once (see
	// contextIngest for the identity rule and the order/silence decisions).
	ingest := newContextIngest()

	// Fragments carrying a premise are withheld and indexed instead, EXCEPT
	// the ones this request named: an explicit ask is the selection callback
	// closing the loop, so it always loads.
	filter := newPremiseFilter(requested)
	if static {
		filter = newStaticPremiseFilter()
	}
	var withheld []WithheldFragment
	filter.onWithheld = func(name, premise, content string) {
		withheld = append(withheld, WithheldFragment{Name: name, Premise: premise, Content: content})
	}

	loaderNames, err := ingestFragmentRefs(ingest, pipe, orderedRefs, profileVars, filter)
	if err != nil {
		return nil, err
	}

	// Which explicit asks failed to load, judged against the LOADER-sourced
	// names alone. loaderNames is never reassigned, so this answer does not
	// depend on where the call sits relative to the builtin append below —
	// folding builtin names in would let an always-on fragment mask an explicit
	// request the user made and did not get.
	missingRequested := missingFrom(requested, loaderNames)

	// Built-in bundles inject their fragments unconditionally — the always-on
	// counterpart to their hooks/MCP (ResolveBundleHooks/ResolveBundleMCPServers)
	// — independent of profile selection, and skipped when their companion
	// binary is absent. Appended after profile/request content. Gated through
	// the SAME content gate as loader-resolved fragments (pipe.Authorizer(), nil
	// for an injected gate-free pipeline) so a rejected builtin fragment is
	// withheld exactly like a rejected builtin MCP server/hook.
	loadedNames := ingestBuiltinFragments(ingest, cfg.ResolveBuiltinBundleFragments(pipe.Authorizer()), loaderNames, filter)

	// The assembled bytes, in ingest order, duplicates already collapsed. The
	// index of what was withheld is reported as STRUCTURED data on the result
	// (PremiseIndex), never spliced into these bytes: what a caller does with
	// the menu — render it, ignore it, ask again — is the caller's decision,
	// and a corpus authoring no premises withholds nothing and assembles the
	// exact bytes it did before this mechanism existed.
	contextContent := ingest.join()

	// Surface (content-free) any items the trust gate withheld during this
	// assembly so the user knows content was hidden, WHY, and how to review it.
	warnWithheld(gate)
	// ...and name any SELECTED profile the gate emptied out completely: the
	// per-item advisory above says WHICH items were withheld, never which
	// profile they cost.
	warnGuttedProfiles(declaredByProfile, loadedNames, gate)

	return &AssembleContextResult{
		Profiles:          profileNames,
		FragmentsLoaded:   loadedNames,
		MissingFragments:  missingRequested,
		MissingTags:       missingTags,
		Context:           contextContent,
		ProfileLLM:        profileLLM,
		PremiseIndex:      filter.entries(),
		WithheldFragments: withheld,
	}, nil
}

// missingFrom returns the requested names absent from loaded, in request
// order. nil when nothing was requested or everything was found.
func missingFrom(requested, loaded []string) []string {
	if len(requested) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(loaded))
	for _, n := range loaded {
		seen[n] = true
	}
	var missing []string
	for _, r := range requested {
		if !seen[r] {
			missing = append(missing, r)
		}
	}
	return missing
}

// ingestBuiltinFragments ingests the always-on built-in bundle fragments,
// AFTER the loader-resolved ones, so a builtin that was also selected by ref
// collapses into the selection rather than the other way round (contextIngest
// keeps the first occurrence). gate is the content gate builtins are routed
// through (see ResolveBuiltinBundleFragments).
//
// Every builtin's name is reported in the returned loaded list whether or not
// its bytes were ingested a second time: a collapsed duplicate's content IS in
// the assembled context, via the occurrence that survived, so reporting it as
// missing would be a lie — and warnGuttedProfiles would then accuse a profile
// of contributing nothing when its fragment is right there.
// It takes the RESOLVED fragments rather than a *config.Config so the ingest
// decision is testable on its own: resolution needs a config, a catalog and a
// live companion probe, and requiring all three to assert "a premised builtin is
// withheld" is what left this path with no unit coverage while it silently
// ignored premises.
func ingestBuiltinFragments(ingest *contextIngest, builtins []config.BuiltinFragment, loaded []string, filter *premiseFilter) []string {
	for _, f := range builtins {
		// A builtin or companion-loadout fragment reaches the SAME filter every
		// other fragment does. "Always-on" describes not being profile-selected;
		// it never meant immunity from a premise, and a fragment carrying none
		// is still loaded unconditionally by the filter's own rule — so this is
		// additive for every existing builtin.
		//
		// Without it an authored premise was inert in the worst way: the index
		// offered the fragment while the context already carried it.
		if filter.withhold(f.Name, f.Premise, func() string { return strings.TrimSpace(f.Content) }) {
			continue
		}
		ingest.add(ingestedFragment{Ref: f.Name, Name: f.Name, Content: strings.TrimSpace(f.Content)})
		loaded = append(loaded, f.Name)
	}
	return loaded
}

// resolveContextProfileNames picks the profiles to assemble from: the explicit
// request profile, else (when nothing at all is selected) the default agent's
// composed profiles (Config.DefaultAgentProfiles — profiles.defaults was
// retired). When no default agent is configured, assembly degrades to empty
// context. No synthetic profile is ever created.
func resolveContextProfileNames(cfg *config.Config, req AssembleContextRequest) []string {
	// A multi-profile compose ask wins over the single-profile field: collected
	// in order and merged downstream by collectProfileFragments (the same loop
	// the default-agent path uses), so the constituent profiles of an agent fold
	// into one context.
	if len(req.Profiles) > 0 {
		return req.Profiles
	}
	if req.Profile != "" {
		return []string{req.Profile}
	}
	if len(req.Fragments) == 0 && len(req.Tags) == 0 {
		return cfg.DefaultAgentProfiles()
	}
	return nil
}

// fragmentsFromTags resolves tag-matched fragments to priority-0 refs. An empty
// tag list yields no refs. Names are emitted fully qualified
// ("<canonical-bundle>#fragments/<name>") — the origin bundle is known here,
// and discarding it would make same-named fragments from different bundles
// indistinguishable downstream (exclusion matching, dedup).
func fragmentsFromTags(cat bundles.Catalog, tags []string) ([]config.FragmentRef, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	taggedInfos, err := cat.ByTags(tags)
	if err != nil {
		return nil, err
	}
	refs := make([]config.FragmentRef, 0, len(taggedInfos))
	for _, info := range taggedInfos {
		bundle, err := remote.CanonicalBundleRef(info.Bundle)
		if err != nil {
			// One unaddressable bundle costs its own fragments, never the tag
			// query: the other bundles matching this tag are addressable and
			// their content is what the caller asked for.
			clidiag.Warn("ctxloom", "tag selection skips fragment %q: %v", info.Name, err)
			continue
		}
		refs = append(refs, config.FragmentRef{
			Name:     bundle + remote.FragmentSelector + info.Name,
			Priority: 0,
		})
	}
	return refs, nil
}

// collectProfileFragments resolves each profile (with inheritance) and gathers
// its tag-matched + explicit fragments and variables. It also reports the
// effective declared LLM: the first non-empty profile.LLM across the resolved
// set, warning to stderr if a later profile disagrees.
//
// declared maps each profile to the fragment names it pushed, so a caller can
// tell which PROFILE a withheld item cost (warnGuttedProfiles) —
// the flat ref list alone cannot attribute a gap to the profile that opened it.
func collectProfileFragments(cfg *config.Config, loader *bundles.Loader, profileNames []string, profileLoaderFunc func() ProfileLoader, fromDefaults bool) (refs []config.FragmentRef, vars map[string]string, llm string, declared map[string][]string, err error) {
	var allFragments []config.FragmentRef
	profileVars := make(map[string]string)
	declaredByProfile := make(map[string][]string, len(profileNames))
	effectiveLLM := ""

	for _, pName := range profileNames {
		profile, err := resolveProfile(cfg, pName, loader, profileLoaderFunc)
		if err != nil {
			// An explicitly requested profile failing is the user's signal to
			// fix the ask — a hard error. A configured default failing is
			// fatal-class in strict mode (the default IS an explicit ask, just
			// a persisted one): skip it so degraded mode still assembles what's
			// left, and let the startup choke owner abort on the finding.
			if fromDefaults {
				strictness.Fail(strictness.ClassRef, "fix the default agent's profiles in .ctxloom/config.yaml (agents.<name>.profiles), or install the missing content (ctxloom deps pull)",
					"skipping default profile %s: %v", pName, err)
				continue
			}
			return nil, nil, "", nil, fmt.Errorf("failed to resolve profile %s: %w", pName, err)
		}

		if profile.LLM != "" {
			if effectiveLLM == "" {
				effectiveLLM = profile.LLM
			} else if profile.LLM != effectiveLLM {
				clidiag.Warn("ctxloom",
					"profile %q declares llm %q but %q is already in effect; keeping %q",
					pName, profile.LLM, effectiveLLM, effectiveLLM)
			}
		}

		for k, v := range profile.Variables {
			profileVars[k] = v
		}

		tagFragments, err := fragmentsFromTags(loader.Catalog(), profile.SelectTags)
		if err != nil {
			return nil, nil, "", nil, fmt.Errorf("failed to list fragments by profile tags: %w", err)
		}
		// Exclusions always win (see profiles.md): a tag-matched fragment the
		// profile excludes is dropped, matching the bundle-expansion filter in
		// resolveProfile. Request-level tags/fragments are never filtered —
		// they are explicit user asks, not profile-pushed content.
		excluded := config.NewExclusionSet(profile.ExcludeFragments)
		for _, ref := range tagFragments {
			if config.IsExcludedFragment(ref.Name, excluded) {
				continue
			}
			allFragments = append(allFragments, ref)
			declaredByProfile[pName] = append(declaredByProfile[pName], ref.Name)
		}
		// Profile fragment refs may pin a content version ("@<commit>"); split it
		// into FragmentRef.Version (canonicalizing the version-agnostic Name) so
		// dedup/ordering stay version-agnostic while the load step honors the pin.
		// Bundle-expanded refs (resolveProfile) already carry Version and a
		// canonical Name, so normalization is a no-op for them.
		for _, ref := range profile.Fragments {
			norm, err := normalizeFragmentRef(ref)
			if err != nil {
				// Withheld, not fatal: one unaddressable ref must not cost the
				// profile every other fragment it declares. The load step is
				// what reports the gap, and the ref reaches it as authored.
				clidiag.Warn("ctxloom", "profile %q: fragment %q is not addressable and will be withheld: %v",
					pName, ref.Name, err)
				norm = ref
			}
			allFragments = append(allFragments, norm)
			declaredByProfile[pName] = append(declaredByProfile[pName], norm.Name)
		}
	}

	return allFragments, profileVars, effectiveLLM, declaredByProfile, nil
}

// normalizeFragmentRef splits a "@<commit>" content version off a profile
// fragment ref into FragmentRef.Version and canonicalizes the version-agnostic
// Name. A ref that already carries a Version (e.g. emitted by ExpandBundleRefs
// with a version-agnostic Name) is returned untouched so re-normalization never
// clobbers it.
func normalizeFragmentRef(ref config.FragmentRef) (config.FragmentRef, error) {
	if ref.Version != "" {
		return ref, nil
	}
	name, version, err := remote.SplitFragmentVersion(ref.Name)
	if err != nil {
		return ref, err
	}
	ref.Name, ref.Version = name, version
	return ref, nil
}

// ingestFragmentRefs loads the ordered fragments (honoring per-ref content
// versions), applies variable substitution PER FRAGMENT, and ingests each one —
// it does not join, because the builtin injection route ingests into the same
// accumulator afterwards and only the accumulator may decide what is a
// duplicate. Returns the names of every fragment that LOADED, which is
// deliberately not the same as the ones ingested: a fragment collapsed as a
// duplicate loaded fine and its content is in the context.
// A fragment that fails to load — not found, gate-withheld, or a pinned
// version that fails to fetch — is skipped so the rest still assemble in
// degraded mode; in strict mode the skip records a fatal finding (see
// warnFragmentLoadFailure) so a profile-pushed fragment can never silently
// vanish from the session.
//
// Substituting per-fragment (rather than joining first, as this used to)
// buys two things: warnSubstitutionFor can name the offending fragment in
// the warning, and a fragment that opens a Mustache Set Delimiter escape
// block and forgets to close it (docs/guides/templating.md's documented
// footgun) can no longer swallow a NEIGHBORING fragment's real variables —
// the escape state is scoped to one substituteVariables call, hence one
// fragment, same as hooks.go's regenerateContext already did.
func ingestFragmentRefs(ingest *contextIngest, pipe *bundles.Pipeline, ordered []config.FragmentRef, profileVars map[string]string, filter *premiseFilter) ([]string, error) {
	if len(ordered) == 0 {
		return nil, nil
	}
	var loadedNames []string
	for _, ref := range ordered {
		lc, err := loadFragmentRef(pipe, ref)
		if err != nil {
			warnFragmentLoadFailure(ref, err)
			continue
		}
		// A withheld fragment is NOT a load failure and must not warn like
		// one: it loaded fine and is being offered to the agent instead. It
		// is also absent from loadedNames on purpose, so a caller reading
		// FragmentsLoaded is never told it received content it did not.
		if filter.withhold(ref.Name, lc.Premise, func() string {
			return substituteVariables(strings.TrimSpace(lc.Content), profileVars, warnSubstitutionFor(ref.Name))
		}) {
			continue
		}
		substituted := substituteVariables(strings.TrimSpace(lc.Content), profileVars, warnSubstitutionFor(ref.Name))
		ingest.add(ingestedFragment{Ref: ref.Name, Name: ref.Name, Content: substituted})
		loadedNames = append(loadedNames, ref.Name)
	}
	return loadedNames, nil
}

// warnSubstitutionFor returns a substituteVariables warnFunc that names
// fragmentName in the finding it surfaces — an undefined variable, a
// template parse failure, or a template render failure — via the standard
// clidiag warning line ("ctxloom: warning: ..."), exactly as
// docs/concepts/fragments.md and docs/guides/templating.md promise for an
// undefined variable. Both substituteVariables call sites (this file's
// loadAssembledContext and hooks.go's regenerateContext) build their warnFunc
// through this one helper so the wording and dedup behavior can't drift
// between the two.
//
// The fragment name is APPENDED, not prepended, so the message keeps
// starting with "undefined variable: {{name}}" / "failed to parse template:
// ..." exactly as documented and already asserted by
// TestAssembleContext_UndefinedVariableWarns and its siblings — this is
// additive attribution, not a reformat. Without it, a warning from a
// multi-fragment assembly names the undefined variable but not WHICH of the
// assembled fragments to open and fix.
//
// Deliberately NOT a strictness.Fail/FailOnce finding: an undefined variable
// renders empty and is recoverable (making it fatal-by-default would break
// existing profiles that never bothered to bind every optional variable),
// and no existing strictness.Class cleanly fits "a fragment's mustache
// template is malformed" without extending the strictness model. This stays
// a plain, non-fatal warning in both strict and degraded mode, the same
// posture as warnWithheld's content-free trust-gate summary.
//
// Deduped per process via clidiag.WarnOnce: AssembleContext/regenerateContext
// can run repeatedly in one process (once per conversation turn, once per
// SessionStart), so an unchanged fragment's undefined variable would
// otherwise re-warn on every call. WarnOnce is the same dedup
// strictness.FailOnce already layers over for chokes that "re-fire per
// subsystem" — this is that shape without a recorded finding. The dedup key
// is the full formatted line (fragment name included), so the SAME variable
// left undefined in TWO different fragments still warns once per fragment,
// not once total.
func warnSubstitutionFor(fragmentName string) func(string) {
	return func(msg string) {
		clidiag.WarnOnce("ctxloom", "%s (fragment %q)", msg, fragmentName)
	}
}

// loadFragmentRef resolves one fragment ref, honoring a pinned content version.
// An unversioned ref takes the lockfile-pinned default path (GetFragment,
// untouched); a "@<commit>"-pinned ref resolves that exact historical version
// (GetFragmentAtVersion), gated by ITS OWN effective-content hash. A version
// fetch/resolve failure fails closed (withholds the item) via the returned
// error.
func loadFragmentRef(pipe *bundles.Pipeline, ref config.FragmentRef) (*bundles.LoadedContent, error) {
	if ref.Version == "" {
		return pipe.GetFragment(ref.Name)
	}
	return pipe.GetFragmentAtVersion(ref.Name, ref.Version)
}

// warnFragmentLoadFailure surfaces a profile-pushed fragment that failed to
// load during assembly. Unresolvable refs are fatal-class in strict mode: the
// warning streams either way and the startup choke owner aborts on the
// finding. This also fixes the historical silent skip — an UNVERSIONED miss
// used to say nothing at all; it now warns in degraded mode too. A gate
// withhold is exempt: it is already surfaced content-free by warnWithheld and
// stays a warning in both modes (trust withholding is never a startup fault).
func warnFragmentLoadFailure(ref config.FragmentRef, err error) {
	if errors.Is(err, errs.ErrFragmentWithheld) {
		return
	}
	if ref.Version != "" {
		strictness.Fail(strictness.ClassRef, "fix the pinned version in the referencing profile, or ctxloom deps pull",
			"withholding %s@%s: %v", ref.Name, ref.Version, err)
		return
	}
	strictness.Fail(strictness.ClassRef, "fix the fragment ref in the referencing profile, or install its bundle (ctxloom deps pull)",
		"fragment %s failed to load (%v); skipping", ref.Name, err)
}

// dedupeFragmentRefs removes duplicates, keeping the highest priority for each
// fragment. Dedup identity is the version-agnostic Name (so a versioned and an
// unversioned spelling of one item collapse); among the collapsed entries an
// explicit "@<commit>" version wins over the default (one version per item).
func dedupeFragmentRefs(fragments []config.FragmentRef) []config.FragmentRef {
	priorities := make(map[string]int)
	versions := make(map[string]string)
	order := make(map[string]int) // Track first occurrence order

	for i, f := range fragments {
		if _, ok := priorities[f.Name]; ok {
			if f.Priority > priorities[f.Name] {
				priorities[f.Name] = f.Priority
			}
			// Explicit @commit wins over a default-version entry; never carry
			// two versions of one item into the assembly.
			if versions[f.Name] == "" && f.Version != "" {
				versions[f.Name] = f.Version
			}
		} else {
			priorities[f.Name] = f.Priority
			versions[f.Name] = f.Version
			order[f.Name] = i
		}
	}

	// Build result maintaining original order for same priority
	result := make([]config.FragmentRef, 0, len(priorities))
	for name, priority := range priorities {
		result = append(result, config.FragmentRef{Name: name, Priority: priority, Version: versions[name]})
	}

	// Sort by original order (for stable output when priorities are equal)
	slices.SortFunc(result, func(a, b config.FragmentRef) int {
		return order[a.Name] - order[b.Name]
	})

	return result
}

// sortFragmentsByPriority arranges fragments using bookend strategy:
// Highest priority at start, second-highest at end, rest fill middle (descending).
// This addresses the "lost in the middle" problem where Configs poorly attend to
// middle content. The returned refs preserve each FragmentRef.Version so the
// load step can honor a pinned content version.
func sortFragmentsByPriority(fragments []config.FragmentRef) []config.FragmentRef {
	if len(fragments) == 0 {
		return nil
	}

	// Sort by priority descending
	sorted := slices.Clone(fragments)
	slices.SortStableFunc(sorted, func(a, b config.FragmentRef) int {
		return b.Priority - a.Priority // Descending
	})

	// For 1-2 fragments, just return in priority order
	if len(sorted) <= 2 {
		return sorted
	}

	// Bookend placement: [highest, middle..., second-highest]
	result := make([]config.FragmentRef, len(sorted))
	result[0] = sorted[0]             // Highest priority at start
	result[len(result)-1] = sorted[1] // Second-highest at end

	// Fill middle with remaining (already sorted descending)
	for i := 2; i < len(sorted); i++ {
		result[i-1] = sorted[i]
	}

	return result
}

// resolveProfile resolves a profile from config or directory and expands its
// bundle references into fragment references.
//
// Profile.Bundles entries (whole bundles like "remote/bundle") and
// Profile.BundleItems entries (cherry-picked items like
// "remote/bundle:fragments/name") are expanded via the bundle loader and
// appended to Fragments so a single downstream pipeline (GetFragment per
// FragmentRef.Name) can load everything regardless of how the profile
// referenced it.
//
// loader may be nil, in which case bundle expansion is skipped — callers
// that don't have a bundle loader handy still get the inline Tags/Fragments
// behavior, which is enough for the lightweight callers that only inspect
// metadata.
func resolveProfile(cfg *config.Config, name string, loader *bundles.Loader, profileLoaderFunc func() ProfileLoader) (*config.Profile, error) {
	// Directory-resolve the profile (.ctxloom/profiles/<name>.yaml, or a
	// bundle-shipped profile reached through the same loader). config.Profile
	// is the ASSEMBLY pipeline's shape, not a config block any more: the inline
	// `profiles:` map is retired, so this conversion is the only way in.
	var pLoader ProfileLoader
	if profileLoaderFunc != nil {
		pLoader = profileLoaderFunc()
	} else {
		pLoader = cfg.GetProfileLoader()
	}
	resolved, rerr := pLoader.ResolveProfile(name, nil)
	if rerr != nil {
		return nil, fmt.Errorf("profile %s: %w", name, rerr)
	}
	profile := &config.Profile{
		Tags:       resolved.Tags,
		SelectTags: resolved.SelectTags,
		Bundles:    resolved.Bundles,
		// BundleItems are expanded via ExpandBundleRefs below, honoring any
		// "@<commit>" pin, exactly like Bundles.
		BundleItems: resolved.BundleItems,
		Commands:    resolved.Commands,
		Skills:      resolved.Skills,
		// Direct fragments carry into the Fragments pipeline
		// (collectProfileFragments -> normalizeFragmentRef honors "@<commit>"),
		// filtered by exclude_fragments here.
		Fragments: convertProfileFragments(resolved.Fragments, resolved.ExcludeFragments),
		// Directly-declared hooks are executable surfaces and reach the
		// managed-hooks resolution + executable trust gate via
		// backends.AssembleManagedHooks.
		Hooks:            resolved.Hooks,
		Variables:        resolved.Variables,
		LLM:              resolved.LLM,
		ExcludeFragments: resolved.ExcludeFragments,
		ExcludeMCP:       resolved.ExcludeMCP,
		DenyTools:        resolved.DenyTools,
	}

	// Expand Bundles and BundleItems into FragmentRefs via the bundle loader.
	// Without this, profiles that only list bundles (the common case for
	// directory profiles) would resolve to zero fragments.
	//
	// The profile's exclude_fragments filter applies here as well as in
	// convertProfileFragments above: bundle-expanded fragments never pass
	// through that call, so without this seam an exclusion naming one would
	// silently no-op.
	if loader != nil {
		excluded := config.NewExclusionSet(profile.ExcludeFragments)
		refs := make([]string, 0, len(profile.Bundles)+len(profile.BundleItems))
		refs = append(refs, profile.Bundles...)
		refs = append(refs, profile.BundleItems...)
		for _, er := range loader.ExpandBundleRefs(refs) {
			if config.IsExcludedFragment(er.Name, excluded) {
				continue
			}
			profile.Fragments = append(profile.Fragments, config.FragmentRef{Name: er.Name, Priority: 0, Version: er.Version})
		}
	}

	return profile, nil
}

// convertProfileFragments maps directory-profile fragment refs to
// config.FragmentRef for the shared assembly pipeline, preserving Name (any
// "@<commit>" pin rides along to be split transiently by normalizeFragmentRef)
// and Priority, and dropping any the profile's exclude_fragments removes — the
// directory-profile mirror of the inline toProfile exclusion filter.
func convertProfileFragments(frags []profiles.FragmentRef, exclude []string) []config.FragmentRef {
	if len(frags) == 0 {
		return nil
	}
	excluded := config.NewExclusionSet(exclude)
	out := make([]config.FragmentRef, 0, len(frags))
	for _, f := range frags {
		if config.IsExcludedFragment(f.Name, excluded) {
			continue
		}
		out = append(out, config.FragmentRef{Name: f.Name, Priority: f.Priority})
	}
	return out
}

// substituteVariables applies mustache variable substitution to content.
//
// DECISION: an unresolved PLAIN variable tag renders
// VERBATIM — the literal source text the author wrote (`{{ARGS}}` stays
// `{{ARGS}}`) — instead of vanishing. checkTags' warning is unchanged and
// still fires; verbatim rendering makes the mistake visible in the output
// itself, the warning tells the author which fragment to fix. See
// undefinedPlainVariableLiterals for the mechanism and the section/inverted-
// section idiom it deliberately leaves untouched.
func substituteVariables(content string, vars map[string]string, warnFunc func(string)) string {
	// Parse the template using the mustache library (handles delimiter changes correctly)
	tmpl, err := mustache.ParseString(content)
	if err != nil {
		warnFunc(fmt.Sprintf("failed to parse template: %v", err))
		return content
	}

	// Check for undefined variables by walking the parsed tags
	seen := collections.NewSet[string]()
	checkTags(tmpl.Tags(), vars, seen, warnFunc)

	data := make(map[string]interface{})
	for k, v := range vars {
		data[k] = v
	}
	// Pre-seed the data map with the literal source text for any name found
	// undefined AND used only as a plain variable tag — never for a name used
	// as a section/inverted-section anywhere in the template, which must stay
	// governed by key-absence so the presence-toggle idiom is untouched.
	for name, literal := range undefinedPlainVariableLiterals(tmpl.Tags(), vars) {
		data[name] = literal
	}

	rendered, err := tmpl.Render(data)
	if err != nil {
		warnFunc(fmt.Sprintf("failed to render template: %v", err))
		return content
	}

	return rendered
}

// undefinedPlainVariableLiterals walks the parsed tag tree and returns, for
// every name that is undefined in vars and used as a plain VARIABLE tag
// (never as a SECTION or INVERTED_SECTION anywhere in the tree), the literal
// source text to substitute verbatim: "{{name}}". This covers {{name}},
// {{{name}}}, and {{&name}} alike — cbroglie/mustache's Tag.Type() does not
// distinguish raw output from escaped output, both report Variable, so a
// literal reconstructed as "{{{name}}}" for an undefined raw tag is not
// obtainable from this API; "{{name}}" is used uniformly. In the (expected
// to be rare) case a fragment writes both {{name}} and {{{name}}} for one
// undefined name, both occurrences render as "{{name}}" rather than
// preserving each one's original brace count — a minor, documented
// approximation, not data loss.
//
// The section exclusion is deliberately tree-wide and by name, not by
// occurrence: a name that appears as a section/inverted-section tag
// ANYWHERE — even if it ALSO appears as a plain variable elsewhere in the
// same fragment — is dropped from the result entirely. Seeding a non-empty
// literal string for that name would make mustache treat it as truthy,
// silently flipping the polarity of every `{{#name}}...{{/name}}` /
// `{{^name}}...{{/name}}` block using it — the one behavior this task must
// never change, since it's a documented, sanctioned idiom (e.g.
// `{{#DEBUG}}...{{/DEBUG}}`) used across existing profiles. The tradeoff in
// that rare collision case: the plain-variable occurrence renders empty (the
// historical default) rather than verbatim — see
// TestSubstituteVariables_NameUsedAsBothSectionAndPlainVariableStaysFalsy.
func undefinedPlainVariableLiterals(tags []mustache.Tag, vars map[string]string) map[string]string {
	sectionNames := collections.NewSet[string]()
	literals := make(map[string]string)

	var walk func([]mustache.Tag)
	walk = func(tags []mustache.Tag) {
		for _, tag := range tags {
			name := tag.Name()
			switch tag.Type() {
			case mustache.Section, mustache.InvertedSection:
				sectionNames.Add(name)
			case mustache.Variable:
				if _, ok := vars[name]; !ok {
					if _, already := literals[name]; !already {
						literals[name] = "{{" + name + "}}"
					}
				}
			}
			if hasChildTags(tag.Type()) {
				if children := tag.Tags(); len(children) > 0 {
					walk(children)
				}
			}
		}
	}
	walk(tags)

	for name := range sectionNames {
		delete(literals, name)
	}
	return literals
}

// hasChildTags reports whether a tag type can contain nested tags (sections).
func hasChildTags(t mustache.TagType) bool {
	return t == mustache.Section || t == mustache.InvertedSection
}

// checkTags recursively walks mustache tags to find undefined variables.
func checkTags(tags []mustache.Tag, vars map[string]string, seen collections.Set[string], warnFunc func(string)) {
	for _, tag := range tags {
		name := tag.Name()
		tagType := tag.Type()

		// A tag references a variable name when it is a plain variable (which
		// covers both escaped {{name}} and raw {{{name}}}/{{&name}} —
		// indistinguishable via Tag.Type()) or a section tag, which keys off
		// a variable.
		referencesVariable := tagType == mustache.Variable || tagType == mustache.Section || tagType == mustache.InvertedSection
		if referencesVariable && !seen.Has(name) {
			seen.Add(name)
			if _, exists := vars[name]; !exists {
				warnFunc(fmt.Sprintf("undefined variable: {{%s}}", name))
			}
		}

		// Recursively check nested tags (only sections have children).
		if hasChildTags(tagType) {
			if children := tag.Tags(); len(children) > 0 {
				checkTags(children, vars, seen, warnFunc)
			}
		}
	}
}

// guttedProfiles names the selected profiles that declared fragments but
// contributed NONE of them to the assembled context, in the order the profiles
// were selected. A profile that declared nothing was never going to contribute
// and is not "gutted"; a partially-loaded one still carries some of its role.
func guttedProfiles(declared map[string][]string, loaded []string) []string {
	if len(declared) == 0 {
		return nil
	}
	loadedSet := make(map[string]bool, len(loaded))
	for _, n := range loaded {
		loadedSet[n] = true
	}
	names := make([]string, 0, len(declared))
	for p := range declared {
		names = append(names, p)
	}
	sort.Strings(names)

	var out []string
	for _, p := range names {
		refs := declared[p]
		if len(refs) == 0 {
			continue
		}
		contributed := false
		for _, r := range refs {
			if loadedSet[r] {
				contributed = true
				break
			}
		}
		if !contributed {
			out = append(out, p)
		}
	}
	return out
}

// warnGuttedProfiles surfaces the failure: a profile the user
// explicitly SELECTED whose content the trust gate withheld in full. Assembly
// then produces a stub and exits 0 — the agent still answers, with its whole
// role missing, and the only signal was a generic "N item(s) awaiting review"
// tally that named neither the profile nor what it cost. Silent context
// degradation is the worst outcome for a trust gate, so the profile is named.
//
// It stays a WARNING rather than a startup fault, deliberately: assembly's
// existing posture is that "trust withholding is never a startup fault" (see
// warnFragmentLoadFailure, which exempts ErrFragmentWithheld from the
// strictness classes on purpose). This closes the naming gap without
// relitigating that decision.
func warnGuttedProfiles(declared map[string][]string, loaded []string, gate bundles.Authorizer) {
	warnGuttedProfilesTo(os.Stderr, declared, loaded, gate)
}

// warnGuttedProfilesTo is warnGuttedProfiles with the sink injected.
func warnGuttedProfilesTo(w io.Writer, declared map[string][]string, loaded []string, gate bundles.Authorizer) {
	var withheld []string
	for _, it := range composite.WithheldBy(gate) {
		withheld = append(withheld, it.Ref)
	}
	if len(withheld) == 0 {
		// The profile may be empty for reasons that are not the gate's doing
		// (an empty bundle, an exclusion filter). Only speak to withholding.
		return
	}
	for _, p := range guttedProfiles(declared, loaded) {
		clidiag.Fwarn(w, "ctxloom",
			"profile %q contributed NO content to this context: every fragment it declares was withheld. "+
				"Withheld item(s): %s — run 'ctxloom review' to see and accept them",
			p, strings.Join(withheld, ", "))
	}
}
