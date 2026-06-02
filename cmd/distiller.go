package cmd

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// newLLMDistiller builds an operations.Distiller backed by the configured LLM
// and distill prompt. It is the single construction point shared by every CLI
// frontend (bundle/fragment/prompt distill and item edits) so distillation
// wiring lives in one place. Returns nil when no LLM or distill prompt is
// available — the operations layer then stores raw content (fault-tolerant).
func newLLMDistiller(cfg *config.Config) operations.Distiller {
	if cfg == nil {
		return nil
	}
	return newLLMDistillerForLLM(cfg, cfg.GetDefaultLLM())
}

// newLLMDistillerForLLM is newLLMDistiller with an explicit LLM override
// (e.g. `bundle distill --llm`). Returns nil when no LLM/prompt resolves.
func newLLMDistillerForLLM(cfg *config.Config, llmName string) operations.Distiller {
	if cfg == nil || llmName == "" {
		return nil
	}
	prompt, err := loadDistillPrompt()
	if err != nil {
		return nil
	}
	return &mcpLLMDistillerSDK{
		llmName: llmName,
		llmEnv:  cfg.LM.Configs[llmName].Env,
		prompt:  prompt,
	}
}

// mcpLLMDistillerSDK adapts the cmd distill helpers to the operations.Distiller
// interface. State is captured at construction so each Distill call is
// self-contained.
type mcpLLMDistillerSDK struct {
	llmName string
	llmEnv  map[string]string
	prompt  string
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
	distilled, modelID, err := distillWithModel(d.llmName, d.llmEnv, req.Name, req.Content, d.prompt, siblingCtx)
	if err != nil {
		return operations.DistillResult{}, err
	}
	return operations.DistillResult{Distilled: distilled, ModelID: modelID}, nil
}
