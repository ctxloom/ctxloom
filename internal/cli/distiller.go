package cli

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// newLLMDistiller builds an operations.Distiller backed by the fast-role LLM
// and distill prompt. It is the single construction point shared by every CLI
// frontend (bundle/fragment/prompt distill and item edits) so distillation
// wiring lives in one place. Returns a nil Distiller and a nil error when no
// LLM resolves — the operations layer then stores raw content (fault-tolerant),
// and the caller is told why on stderr.
//
// A NON-NIL ERROR IS A REFUSAL, not a fault: see newLLMDistillerForLabel.
func newLLMDistiller(cfg *config.Config) (operations.Distiller, error) {
	if cfg == nil {
		clidiag.Warn("ctxloom", "no config is available, so nothing can be distilled: content will be stored RAW (undistilled)")
		return nil, nil
	}
	return newLLMDistillerForLabel(cfg, cfg.FastLabel())
}

// newLLMDistillerForLabel is newLLMDistiller for an explicit config label
// (e.g. `bundle distill --llm <label>`). The label resolves to its backend +
// model via the registry; env comes from the same labeled entry.
//
// Returning nil means "this content will be stored RAW", which every caller
// treats as success — so the reason is warned rather than swallowed: a distill
// command that reports "distilled 4 items" while storing four raw ones is
// indistinguishable from working. There is exactly ONE reachable reason: no
// label resolves, i.e. neither llm.defaults.fast nor llm.defaults.primary is
// set and llm.configs does not hold exactly one entry (config.PrimaryLabel).
// The backend guard below is defensive only — config.ResolveLLM degrades a
// missing label and an empty type to the built-in default backend
// (LLMConfig.EffectiveType) and never yields "".
func newLLMDistillerForLabel(cfg *config.Config, label string) (operations.Distiller, error) {
	if cfg == nil {
		clidiag.Warn("ctxloom", "no config is available, so nothing can be distilled: content will be stored RAW (undistilled)")
		return nil, nil
	}
	if label == "" {
		clidiag.Warn("ctxloom", "no LLM label resolves for distillation (set llm.defaults.fast or llm.defaults.primary in config.yaml, or keep exactly one llm.configs entry): content will be stored RAW (undistilled)")
		return nil, nil
	}
	backend, model := cfg.ResolveLLM(label)
	if backend == "" {
		clidiag.Warn("ctxloom", "llm label %q resolves to no backend: content will be stored RAW (undistilled)", label)
		return nil, nil
	}
	// The ONE error this constructor has: the project configured a `distill`
	// prompt and the trust gate withheld it. Warning-and-continuing here would
	// be exactly the swallow being fixed — the run would proceed on ctxloom's
	// own default and report success. The error is returned so the command
	// refuses (refuseWithheldDistillPrompt), which is a decision, not a fault.
	prompt, err := loadDistillPrompt(cfg)
	if err != nil {
		return nil, err
	}
	return &llmDistiller{
		llmName:  backend,
		llmLabel: label,
		llmEnv:   operations.LLMEnvFor(cfg, label),
		model:    model,
		prompt:   prompt,
	}, nil
}

// llmDistiller adapts the cmd distill helpers to the operations.Distiller
// interface. State is captured at construction so each Distill call is
// self-contained.
type llmDistiller struct {
	llmName  string
	llmLabel string // resolved config label, so serve configures exactly this entry
	llmEnv   map[string]string
	model    string // cheap compression model (e.g. "haiku"); empty = backend default
	prompt   string
}

func (d *llmDistiller) Distill(ctx context.Context, req operations.DistillRequest) (operations.DistillResult, error) {
	var excludeName string
	switch req.Kind {
	case operations.DistillKindFragment:
		excludeName = itemRefPrefix(ItemTypeFragment) + req.Name
	case operations.DistillKindCommand:
		excludeName = itemRefPrefix(ItemTypeCommand) + req.Name
	}
	var siblingCtx string
	if req.Bundle != nil {
		siblingCtx = buildSiblingContext(req.Bundle, excludeName)
	}
	distilled, modelID, err := distillWithModel(ctx, d.llmName, d.llmLabel, d.model, d.llmEnv, req.Name, req.Content, d.prompt, siblingCtx)
	if err != nil {
		return operations.DistillResult{}, err
	}
	return operations.DistillResult{Distilled: distilled, ModelID: modelID}, nil
}
