package cmd

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// newLLMDistiller builds an operations.Distiller backed by the fast-role LLM
// and distill prompt. It is the single construction point shared by every CLI
// frontend (bundle/fragment/prompt distill and item edits) so distillation
// wiring lives in one place. Returns nil when no LLM or distill prompt is
// available — the operations layer then stores raw content (fault-tolerant).
func newLLMDistiller(cfg *config.Config) operations.Distiller {
	if cfg == nil {
		return nil
	}
	return newLLMDistillerForLabel(cfg, cfg.FastLabel())
}

// newLLMDistillerForLabel is newLLMDistiller for an explicit config label
// (e.g. `bundle distill --llm <label>`). The label resolves to its backend +
// model via the registry; env comes from the same labeled entry. Returns nil
// when no backend/prompt resolves.
func newLLMDistillerForLabel(cfg *config.Config, label string) operations.Distiller {
	if cfg == nil || label == "" {
		return nil
	}
	prompt, err := loadDistillPrompt()
	if err != nil {
		return nil
	}
	backend, model := cfg.ResolveLLM(label)
	if backend == "" {
		return nil
	}
	return &mcpLLMDistillerSDK{
		llmName:  backend,
		llmLabel: label,
		llmEnv:   llmEnvFor(cfg, label),
		model:    model,
		prompt:   prompt,
	}
}

// mcpLLMDistillerSDK adapts the cmd distill helpers to the operations.Distiller
// interface. State is captured at construction so each Distill call is
// self-contained.
type mcpLLMDistillerSDK struct {
	llmName  string
	llmLabel string // resolved config label, so serve configures exactly this entry
	llmEnv   map[string]string
	model    string // cheap compression model (e.g. "haiku"); empty = backend default
	prompt   string
}

func (d *mcpLLMDistillerSDK) Distill(_ context.Context, req operations.DistillRequest) (operations.DistillResult, error) {
	var excludeName string
	switch req.Kind {
	case operations.DistillKindFragment:
		excludeName = "fragments/" + req.Name
	case operations.DistillKindPrompt:
		excludeName = "prompts/" + req.Name
	}
	var siblingCtx string
	if req.Bundle != nil {
		siblingCtx = buildSiblingContext(req.Bundle, excludeName)
	}
	distilled, modelID, err := distillWithModel(d.llmName, d.llmLabel, d.model, d.llmEnv, req.Name, req.Content, d.prompt, siblingCtx)
	if err != nil {
		return operations.DistillResult{}, err
	}
	return operations.DistillResult{Distilled: distilled, ModelID: modelID}, nil
}
