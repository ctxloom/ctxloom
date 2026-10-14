package operations

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// PremiseWithhold is one fragment kept out of the assembled context by its
// premise, and the surface that carried it instead.
//
// Reported as STRUCTURED DATA and not as a warning, ruled by the human
// 2026-09-09. The accepted cost, recorded so it reads later as a choice: a
// person running materialize in a terminal sees nothing. Note the asymmetry
// this leaves and do not "fix" it by adding a warning — a TRUST-withheld
// fragment does warn, so the two withhold reasons report through different
// surfaces by decision.
type PremiseWithhold struct {
	// Name is the fragment's qualified ref, the same one the premise index
	// hands out, so a reader can ask for it by name.
	Name string `json:"name"`
	// Premise is the condition that did not match. Carried so the report says
	// WHY it was withheld rather than only that it was.
	Premise string `json:"premise"`
	// Delivered names the surface that carried it instead ("skills"), or is
	// EMPTY when nothing carried it. Empty is the case worth looking at: it
	// means the content reached the agent by no route at all.
	Delivered string `json:"delivered,omitempty"`
}

// withheldFragmentSkillItems packages the assembly's withheld fragments as
// skill items for the package.
func withheldFragmentSkillItems(asm *AssembleContextResult) ([]composite.Item[composite.Skill], error) {
	withheld := make([]PremisedFragment, 0, len(asm.WithheldFragments))
	for _, w := range asm.WithheldFragments {
		withheld = append(withheld, PremisedFragment{Ref: w.Name, Premise: w.Premise, Content: w.Content})
	}
	fragmentSkills, err := PremisedFragmentSkills(withheld)
	if err != nil {
		return nil, err
	}
	items := make([]composite.Item[composite.Skill], 0, len(fragmentSkills))
	for _, sk := range fragmentSkills {
		skill := composite.Skill{Name: sk.Name, Description: sk.Description}
		for _, f := range sk.Files {
			skill.Files = append(skill.Files, engine.SkillFile{Path: f.RelPath, Bytes: f.Content, Size: int64(len(f.Content)), Mode: uint32(f.Mode.Perm())})
		}
		items = append(items, composite.Item[composite.Skill]{Ref: "materialize#skill/" + sk.Name, Value: skill})
	}
	return items, nil
}

// describePremiseWithholds turns the assembly's premise index into the result's
// withhold report. asSkills says whether the withheld bodies were actually
// re-delivered as skill packages.
//
// Returns nil for an empty index, and every caller relies on that: a
// materialization over a corpus authoring no premises must report no withholds
// rather than an empty list, so the JSON omitempty tag can keep the payload
// byte-identical to what it was before this field existed.
func describePremiseWithholds(index []PremiseIndexEntry, asSkills bool) []PremiseWithhold {
	if len(index) == 0 {
		return nil
	}
	delivered := ""
	if asSkills {
		delivered = "skills"
	}
	out := make([]PremiseWithhold, 0, len(index))
	for _, e := range index {
		out = append(out, PremiseWithhold{Name: e.Name, Premise: e.Premise, Delivered: delivered})
	}
	return out
}

// notCarried is the LOSS half of the materialize report, read from the same
// plan the delivery was built from: a kind the engine does not carry at all
// (the plan's accepted losses), and a configured hook event the engine
// declares it cannot carry (Definition.HookLosses — the one statement of
// that, the same read CapabilityLoss makes for `agent show` and `doctor`).
// An event carried on a matcher-narrowed native one (post_file_edit on
// claude's PostToolUse) is carried, not lost. res.Wrote can only ever list
// what landed, so a loss is invisible in it by construction; this names it.
func notCarried(backend string, kind engine.Engine, pkg composite.Package, plan delivery.Plan) []agent.SurfaceLoss {
	var out []agent.SurfaceLoss
	for _, loss := range plan.Losses {
		out = append(out, agent.SurfaceLoss{
			Surface: loss.Kind.String(),
			Detail:  fmt.Sprintf("%s has no %s surface; nothing was written for it", backend, loss.Kind),
			Reason:  fmt.Sprintf("%s declares no approach for %s", backend, loss.Kind),
		})
	}
	return append(out, unsupportedHookKindLosses(kind.Root().HookLosses, pkg.Hooks)...)
}
