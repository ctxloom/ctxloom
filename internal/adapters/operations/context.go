package operations

import (
	"context"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
)

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

	// Pipeline is an optional pre-configured process stage (for testing).
	Pipeline *bundles.Pipeline `json:"-"`

	// ProfileLoaderFunc is an optional function to get the profile loader (for testing).
	ProfileLoaderFunc func() ProfileLoader `json:"-"`
}

// packageRequest is the request's package-level shape: the profile set as
// one list (Profiles wins over Profile).
func (req AssembleContextRequest) packageRequest() PackageRequest {
	profileSet := req.Profiles
	if len(profileSet) == 0 && req.Profile != "" {
		profileSet = []string{req.Profile}
	}
	return PackageRequest{
		Profiles:          profileSet,
		Fragments:         req.Fragments,
		Tags:              req.Tags,
		Consumer:          req.Consumer,
		Pipeline:          req.Pipeline,
		ProfileLoaderFunc: req.ProfileLoaderFunc,
	}
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
	engines      engine.Registry
}

// MaterializedFor names an OUT-OF-THE-LOOP surface: backend's native context
// file, written — or, for a comparison, composed as it would be written — for
// a launch with no ctxloom behind it. Nothing there can pull a withheld
// fragment later, so it is either handed over as a skill package, where the
// engine has a skills surface, or written into the context itself. The
// comparison side must state this too: composing for a live session and
// diffing against a materialized file reports a correct file stale forever.
func MaterializedFor(reg engine.Registry, backend string) ContextConsumer {
	return ContextConsumer{materialized: true, backend: backend, engines: reg}
}

// static is THE resolution of static-vs-dynamic delivery, and the only one:
// a static assembly INCLUDES premised fragments and hands back an empty
// PremiseIndex, because nobody behind the surface can act on a menu. It is
// decided from the consumer alone. A materialized surface goes static exactly
// when its engine exports no skill package to re-deliver the withheld
// fragments through — asked of the engine's own Exports, the same decision
// the skills delivery is made by, so the mode and the delivery cannot
// disagree. An engine nobody registered is refused rather than treated as
// skill-less: that would dump every premised fragment into a file for a
// launch that does not exist.
func (c ContextConsumer) static() (bool, error) {
	if !c.materialized {
		return false, nil
	}
	eng, ok := c.engines.Lookup(engine.Name(c.backend))
	if !ok {
		return false, fmt.Errorf("unknown backend %q", c.backend)
	}
	return !exportsSkills(eng), nil
}

// exportsSkills asks the engine whether it exports a skill package at all:
// handed one package with no block of its own, does it offer it? A double
// that declares no skill export answers no; every engine with a skills
// surface answers yes.
func exportsSkills(eng engine.Engine) bool {
	ex, err := eng.Exports(engine.Items{Skills: []engine.SkillItem{{Ref: "ctxloom+local:probe#skills/probe", Name: "probe"}}})
	return err == nil && len(ex.Skills) == 1 && ex.Skills[0].Enabled
}

// WithheldFragment is one premised fragment an assembly held back, with the
// body it did not deliver.
type WithheldFragment struct {
	Name    string
	Premise string
	Content string
}

// AssembleContextResult contains the assembled context.
type AssembleContextResult struct {
	Profiles        []string `json:"profiles"`
	FragmentsLoaded []string `json:"fragments_loaded"`
	// MissingFragments lists the EXPLICITLY requested fragments (Fragments in
	// the request) that did not resolve/load. Assembly is fault-tolerant (a
	// missing ask warns and is skipped), but callers like `run -f` treat an
	// all-missing explicit ask as a hard error — and the always-on companion
	// fragments mean a non-empty FragmentsLoaded can't signal it.
	MissingFragments []string `json:"missing_fragments,omitempty"`
	// MissingTags names every requested tag (Tags in the request) when the
	// WHOLE tag selection contributed zero fragments. A tag query is a union
	// (any listed tag matches), so a zero-fragment result cannot be
	// attributed to one tag over another — all requested tags are named.
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
	// It is safe on the RESULT only because the MCP surface does not return
	// this struct: internal/adapters/mcp projects a DTO that omits this
	// field, so a body cannot reach the wire by anyone adding to the domain
	// type.
	WithheldFragments []WithheldFragment `json:"-"`

	// PremiseIndex is IN-PROCESS ONLY. internal/adapters/mcp's DTO does not
	// project it, and the json tag below is vestigial rather than a wire
	// contract: the menu an MCP consumer reads is the ctxloom://fragments
	// resource, which carries each fragment's premise and qualified ref.
	//
	// PremiseIndex names the fragments this assembly WITHHELD because they
	// carry a premise, together with that premise. It is the agent's menu: it
	// evaluates each premise against what it is about to do and asks for the
	// ones that apply BY NAME, through Fragments on a second call. Empty when
	// nothing was withheld, which is every assembly over a corpus that
	// authors no premises.
	PremiseIndex []PremiseIndexEntry `json:"premise_index,omitempty"`
}

// AssembleContext assembles context from a profile set, fragments, and/or
// tags: the ONE package's context half, projected. See AssemblePackage for
// what is assembled and composite.Assemble for how.
func AssembleContext(ctx context.Context, cfg *config.Config, req AssembleContextRequest) (*AssembleContextResult, error) {
	pkg, err := AssemblePackage(ctx, cfg, req.packageRequest())
	if err != nil {
		return nil, err
	}
	return contextResultOf(pkg), nil
}

// contextResultOf projects the package's context half onto the result.
func contextResultOf(pkg composite.Package) *AssembleContextResult {
	res := &AssembleContextResult{
		Profiles:         pkg.Selection.Profiles,
		FragmentsLoaded:  pkg.Loaded,
		MissingFragments: missingFrom(pkg.Selection.Explicit, pkg.Loaded),
		MissingTags:      pkg.Selection.MissingTags,
		Context:          pkg.Context.Text,
		ProfileLLM:       pkg.Selection.LLM,
	}
	for _, p := range pkg.Premised {
		res.PremiseIndex = append(res.PremiseIndex, PremiseIndexEntry{Name: p.Value.Name, Premise: p.Value.Premise})
		res.WithheldFragments = append(res.WithheldFragments, WithheldFragment{Name: p.Value.Name, Premise: p.Value.Premise, Content: p.Value.Body})
	}
	return res
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

// refuseEmptySelection refuses an explicit selection that matched nothing:
// the caller asked for fragments by name or by tag and would get none.
// Checked via the misses — the always-on companion fragments mean the
// loaded list is never empty, so a bare count cannot see the miss.
func refuseEmptySelection(req PackageRequest, res *AssembleContextResult) error {
	if len(req.Fragments) > 0 && len(res.MissingFragments) == len(req.Fragments) {
		return fmt.Errorf("%w: requested fragments not found: %s", errNoFragments, strings.Join(res.MissingFragments, ", "))
	}
	if len(req.Tags) > 0 && len(res.MissingTags) == len(req.Tags) {
		return fmt.Errorf("%w: no fragment matches tag(s): %s", errNoFragments, strings.Join(res.MissingTags, ", "))
	}
	return nil
}
