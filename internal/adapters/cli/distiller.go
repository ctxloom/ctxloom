package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/projectroot"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// errDistillFailed fails a command whose requested distillation did not run
// or was rejected. The command's other work stands (an edit's content is
// saved raw; a distill leaves any previous distillation in place); what must
// not stand is an exit status of success over content nobody distilled. What
// was kept differs per command, so the call site says it, not this sentinel.
var errDistillFailed = errors.New("distillation failed")

// newLLMDistiller builds the distiller for one config label and the distill
// prompt. It is the single construction point shared by every CLI frontend
// (bundle/fragment/prompt distill and item edits), and the ONE place that
// decides which label distills: an explicit label (`bundle distill --llm
// <label>`) is used as named; "" selects the fast (compression) role,
// cfg.FastLabel. The label resolves to its engine through the one launch
// resolver, so a bare engine name that is not a configured entry (`--llm
// mock`) reaches that engine here exactly as it does on `run`, and a label
// that names nothing refuses rather than silently running the default.
//
// Returning nil means "this content will be stored RAW", which every caller
// treats as success — so the reason is warned rather than swallowed: a distill
// command that reports "distilled 4 items" while storing four raw ones is
// indistinguishable from working. There is exactly ONE reachable reason: no
// label resolves, i.e. neither llm.defaults.fast nor llm.defaults.primary is
// set and llm.configs does not hold exactly one entry (config.PrimaryLabel).
//
// A NON-NIL ERROR IS A REFUSAL, not a fault: see the prompt load below.
func newLLMDistiller(cfg *config.Config, label string) (*llmDistiller, error) {
	if cfg == nil {
		clidiag.Warn("ctxloom", "no config is available, so nothing can be distilled: content will be stored RAW (undistilled)")
		return nil, nil
	}
	if label == "" {
		label = cfg.FastLabel()
	}
	if label == "" {
		clidiag.Warn("ctxloom", "no LLM label resolves for distillation (set llm.defaults.fast or llm.defaults.primary in config.yaml, or keep exactly one llm.configs entry): content will be stored RAW (undistilled)")
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
	return &llmDistiller{cfg: cfg, label: label, prompt: prompt}, nil
}

// llmDistiller adapts the cmd distill helpers to the operations.Distiller
// interface. The distiller is ONE internal one-shot session on the label —
// its own harp, started on the first item and ended by Close — whose turns
// are the items. A nil *llmDistiller is a Distiller that stores raw.
type llmDistiller struct {
	cfg    *config.Config
	label  string
	prompt string
	// session is the one-shot, started lazily so a command that distils
	// nothing mints no session.
	session *operations.OneShot
}

// Close ends the distiller's session. Nil-safe; idempotent.
func (d *llmDistiller) Close() {
	if d == nil || d.session == nil {
		return
	}
	d.session.End()
	d.session = nil
}

// turn drives one distill turn on the session, starting it on first use.
func (d *llmDistiller) turn(ctx context.Context, prompt string) (answer, model string, err error) {
	if d.session == nil {
		s, err := operations.StartInternalOneShot(ctx, App().LaunchFacts(), internalRunHosts(), d.cfg, d.label, "", projectroot.WorkDir(), "", 0)
		if err != nil {
			return "", "", fmt.Errorf("no reachable engine for distillation (label %q): %w — content saved raw, undistilled", d.label, err)
		}
		d.session = s
	}
	return d.session.TurnWithModel(ctx, prompt)
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
	distilled, modelID, err := distillWithModel(ctx, d.turn, req.Name, req.Content, d.prompt, siblingCtx)
	if err != nil {
		return operations.DistillResult{}, err
	}
	return operations.DistillResult{Distilled: distilled, ModelID: modelID}, nil
}
