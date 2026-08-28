package memory

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// defaultLLMPlugin is the plugin a distillation call falls back to when the
// caller named none, shared by CompactionConfig's defaulting and
// DistillConfig's so the two paths cannot drift onto different fallbacks.
const defaultLLMPlugin = "claude-code"

// DistillConfig configures one standalone distillation call: which LLM plugin
// runs it, with what model and environment. It is the subset of
// CompactionConfig a single call needs — Compactor.runDistill forwards its own
// config here, so every distillation request in the process is built by
// Distill and there is exactly one request shape.
type DistillConfig struct {
	// LLM is the plugin to run (default: claude-code).
	LLM string
	// Model selects the model within the plugin (e.g. "haiku", "sonnet").
	Model string
	// Env is the resolved LLM label's config-declared environment
	// (llm.configs.<label>.env). SkipSetup in Distill makes the request the
	// only channel that can carry it: Setup, which would otherwise deliver
	// configuration, is bypassed.
	Env map[string]string
	// ClientFactory creates the plugin client (default:
	// pb.DefaultClientFactory()). It is the stochastic boundary: a test
	// supplies pb.MockClientFactory and everything on this side stays
	// deterministic.
	ClientFactory pb.ClientFactory
}

// Distill executes one LLM call: a fresh plugin subprocess given systemPrompt
// as its instruction and payload as the material it works on, run one-shot in
// minimal mode. The session distillation, the per-result finding repair and
// the premise author all go through here so the request shape stays in one
// place.
//
// payload is the ALREADY-ENVELOPED material — the caller wraps it in whatever
// element names what it is (<session_log> for a transcript, <fragment> for a
// fragment body), because only the caller knows what the material is.
func Distill(ctx context.Context, cfg DistillConfig, systemPrompt, payload string) (string, error) {
	if cfg.LLM == "" {
		cfg.LLM = defaultLLMPlugin
	}
	if cfg.ClientFactory == nil {
		cfg.ClientFactory = pb.DefaultClientFactory()
	}
	client, err := cfg.ClientFactory(cfg.LLM, "", 0)
	if err != nil {
		return "", fmt.Errorf("start plugin: %w", err)
	}
	defer client.Kill()

	// SkipSetup=true keeps the call minimal (no hooks/commands/context), but
	// the server delivers req.Fragments to the backend only via Setup — which
	// SkipSetup bypasses. So the instructions must travel in the prompt itself,
	// ahead of the payload; sent as a Fragment they'd be silently dropped and
	// the model would just answer the payload conversationally.
	req := &pb.RunStart{
		Prompt: &pb.Fragment{
			Content: fmt.Sprintf("%s\n\n%s", systemPrompt, payload),
		},
		Options: &pb.RunOptions{
			PermissionMode: agent.PermissionBypass.String(),
			Mode:           pb.ExecutionMode_ONESHOT,
			Model:          cfg.Model,
			Env:            cfg.Env,
			SkipSetup:      true,
		},
	}

	var stdout, stderr bytes.Buffer
	exitCode, err := client.Run(ctx, req, nil, &stdout, &stderr, nil)
	if err != nil {
		return "", err
	}
	if exitCode != 0 {
		return "", fmt.Errorf("LLM exited with code %d: %s", exitCode, stderr.String())
	}

	// Exit 0 with nothing on stdout is a FAILED call, not an empty result.
	// Counted as success it lands at the caller as "", which for the compactor
	// meant an empty result written straight over a previously good essence.md.
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return "", fmt.Errorf("LLM exited 0 but produced no output: %s", strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
