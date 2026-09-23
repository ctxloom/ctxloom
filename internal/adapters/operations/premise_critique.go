package operations

import (
	"context"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/memory"
)

// Pass 2 of premise authoring: ATTACK a drafted premise rather than polish it.
// The critic sees the full body, the draft and the sibling premises it could
// collide with, and returns findings for the author to weigh. Like the draft,
// it is a proposal surface and writes nothing.
//
// The critic must run on a session that never saw the draft being written:
// a critique sharing the drafter's history is self-review. That separation is
// the caller's (it owns the sessions), which is why this takes its own
// PremiseAuthorConfig rather than a draft's.

// PremiseFindingKind names what a finding says is wrong with a draft. The set
// is closed: parsePremiseCritique refuses a kind outside it.
type PremiseFindingKind string

const (
	FindingTooBroad     PremiseFindingKind = "too-broad"
	FindingTooNarrow    PremiseFindingKind = "too-narrow"
	FindingNamesMode    PremiseFindingKind = "names-a-mode"
	FindingUnobservable PremiseFindingKind = "unobservable"
	FindingOverlaps     PremiseFindingKind = "overlaps"
	FindingLeaksBody    PremiseFindingKind = "leaks-body"
	FindingSplitMissed  PremiseFindingKind = "split-missed"
	FindingShouldBeNone PremiseFindingKind = "should-be-none"
)

// premiseFindingKinds is the closed vocabulary parsePremiseCritique accepts.
var premiseFindingKinds = map[PremiseFindingKind]bool{
	FindingTooBroad: true, FindingTooNarrow: true, FindingNamesMode: true, FindingUnobservable: true,
	FindingOverlaps: true, FindingLeaksBody: true, FindingSplitMissed: true, FindingShouldBeNone: true,
}

// PremiseFinding is one objection to a draft.
type PremiseFinding struct {
	Kind PremiseFindingKind `json:"kind"`
	// Evidence is why the critic believes the finding.
	Evidence string `json:"evidence,omitempty"`
	// Counterexample is a concrete moment the draft gets wrong.
	Counterexample string `json:"counterexample,omitempty"`
	// OverlapsWith is the qualified ref of the sibling an overlaps finding
	// collides with.
	OverlapsWith string `json:"overlaps_with,omitempty"`
	// Patch is a proposed replacement premise, when the critic offers one.
	Patch string `json:"patch,omitempty"`
}

// PremiseCritique is pass 2's verdict on one draft. No findings is a clean
// critique, not a failure.
type PremiseCritique struct {
	Fragment string           `json:"fragment"`
	Findings []PremiseFinding `json:"findings"`
}

// CritiquePremise attacks draft, the proposed premise for the fragment name
// with the given body, against siblings — the premises it could collide with,
// which the caller supplies WITHOUT this fragment's own entry. One LLM call
// through the shared distillation path (memory.Distill).
func CritiquePremise(ctx context.Context, cfg PremiseAuthorConfig, name, body string, draft *PremiseDraft, siblings []PremiseIndexEntry) (*PremiseCritique, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("critique premise: fragment name is empty")
	}
	if strings.TrimSpace(body) == "" {
		return nil, fmt.Errorf("critique premise for %q: fragment body is empty; there is nothing to judge the premise against", name)
	}
	if draft == nil {
		return nil, fmt.Errorf("critique premise for %q: no draft to critique", name)
	}
	prompt, err := premisePrompt(cfg.PromptDir, premiseCritiquePromptName)
	if err != nil {
		return nil, err
	}
	payload, err := critiquePayload(name, body, draft, siblings)
	if err != nil {
		return nil, fmt.Errorf("critique premise for %q: %w", name, err)
	}
	out, err := memory.Distill(ctx, cfg.Run, prompt, payload)
	if err != nil {
		return nil, fmt.Errorf("critique premise for %q: %w", name, err)
	}
	critique, err := parsePremiseCritique(name, out)
	if err != nil {
		return nil, fmt.Errorf("critique premise for %q: %w", name, err)
	}
	return critique, nil
}

// critiquePayload renders the critic's input. The draft goes back in the same
// YAML shape the drafter emits, NONE included, so an always-load verdict
// reaches the critic as that verdict rather than as a missing premise.
func critiquePayload(name, body string, draft *PremiseDraft, siblings []PremiseIndexEntry) (string, error) {
	premise := draft.Premise
	if premise == "" {
		premise = premiseNone
	}
	draftDoc, err := yaml.Marshal(premiseDraftDoc{
		Premise: &premise, Moments: draft.Moments, NotFor: draft.NotFor, Split: draft.SplitHint,
	})
	if err != nil {
		return "", fmt.Errorf("render draft: %w", err)
	}
	type sibling struct {
		Name    string `yaml:"name"`
		Premise string `yaml:"premise"`
	}
	rows := make([]sibling, 0, len(siblings))
	for _, s := range siblings {
		rows = append(rows, sibling{Name: s.Name, Premise: s.Premise})
	}
	sibDoc, err := yaml.Marshal(rows)
	if err != nil {
		return "", fmt.Errorf("render siblings: %w", err)
	}
	return fmt.Sprintf("<fragment name=%q>\n%s\n</fragment>\n<draft>\n%s</draft>\n<siblings>\n%s</siblings>",
		name, body, draftDoc, sibDoc), nil
}

// premiseCritiqueDoc is the YAML document the critique prompt instructs.
// Findings is a pointer so a document with no findings key is distinguishable
// from an explicitly empty list.
type premiseCritiqueDoc struct {
	Findings *[]struct {
		Kind           string `yaml:"kind"`
		Evidence       string `yaml:"evidence"`
		Counterexample string `yaml:"counterexample"`
		OverlapsWith   string `yaml:"overlaps_with"`
		Patch          string `yaml:"patch"`
	} `yaml:"findings"`
}

// parsePremiseCritique parses the critic's output strictly: anything that is
// not the instructed shape, and any finding kind outside the closed set, is an
// error — a guessed critique would go in front of the author as if the model
// had said it.
func parsePremiseCritique(name, out string) (*PremiseCritique, error) {
	var parsed premiseCritiqueDoc
	if err := yaml.Unmarshal([]byte(stripCodeFence(out)), &parsed); err != nil {
		return nil, fmt.Errorf("output is not the instructed YAML shape: %w", err)
	}
	if parsed.Findings == nil {
		return nil, fmt.Errorf("output has no findings field")
	}
	critique := &PremiseCritique{Fragment: name, Findings: []PremiseFinding{}}
	for _, f := range *parsed.Findings {
		kind := PremiseFindingKind(strings.TrimSpace(f.Kind))
		if !premiseFindingKinds[kind] {
			return nil, fmt.Errorf("unknown finding kind %q", kind)
		}
		critique.Findings = append(critique.Findings, PremiseFinding{
			Kind:           kind,
			Evidence:       strings.TrimSpace(f.Evidence),
			Counterexample: strings.TrimSpace(f.Counterexample),
			OverlapsWith:   strings.TrimSpace(f.OverlapsWith),
			Patch:          strings.TrimSpace(f.Patch),
		})
	}
	return critique, nil
}
