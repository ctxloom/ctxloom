package operations

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// RunOneshotRequest specifies a single profile-agent oneshot run: assemble a
// profile's context, launch its backend once, and capture stdout. It is the
// backbone both `ctxloom run --print` and the parallel `map`/`weave` orchestration
// build on, so a member agent is just one of these.
type RunOneshotRequest struct {
	Profile   string // profile whose context specializes this agent (may be empty)
	Task      string // the prompt/task sent to the agent
	LLM       string // optional label/backend override; wins over the profile's llm
	WorkDir   string // working directory for the run
	Verbosity int

	// Loader is an optional pre-configured bundle loader (test seam).
	Loader *bundles.Loader
	// Factory builds the plugin client; nil self-invokes the compiled-in
	// backend carrying the resolved config label
	// (pb.DefaultClientFactoryForLabel). The seam lets map/weave and tests
	// inject a client without spawning real backends.
	Factory pb.ClientFactory
}

// RunOneshotResult is the captured output of one oneshot agent run, plus the
// resolved transport metadata.
type RunOneshotResult struct {
	Profile  string `json:"profile,omitempty"`
	Output   string `json:"output"`
	Label    string `json:"label"`
	Backend  string `json:"backend"`
	Model    string `json:"model,omitempty"`
	ExitCode int32  `json:"exit_code"`
}

// RunOneshot assembles the profile's context, resolves the LLM (override → the
// profile's declared llm → primary role), and runs the backend once in ONESHOT
// mode with stdout captured. It mirrors memory/compactor's distillation run: the
// client factory abstracts backend construction, the model rides in RunOptions,
// and SkipSetup keeps startup minimal (no hooks/MCP/statusline) — the profile's
// assembled context is the only specialization.
func RunOneshot(ctx context.Context, cfg *config.Config, req RunOneshotRequest) (*RunOneshotResult, error) {
	ctxResult, err := AssembleContext(ctx, cfg, AssembleContextRequest{
		Profile: req.Profile,
		Loader:  req.Loader,
	})
	if err != nil {
		return nil, fmt.Errorf("assemble context: %w", err)
	}

	label := resolveOneshotLabel(cfg, req.LLM, ctxResult.ProfileLLM)
	backendName, model := ResolveBackend(cfg, label)

	var fragments []*pb.Fragment
	if ctxResult.Context != "" {
		fragments = append(fragments, &pb.Fragment{Content: ctxResult.Context})
	}
	runReq := &pb.RunStart{
		Fragments: fragments,
		Prompt:    &pb.Fragment{Content: req.Task},
		Options: &pb.RunOptions{
			WorkDir:     req.WorkDir,
			AutoApprove: true,
			Mode:        pb.ExecutionMode_ONESHOT,
			Model:       model,
			Verbosity:   uint32(req.Verbosity * 16),
			SkipSetup:   true,
		},
	}

	factory := req.Factory
	if factory == nil {
		factory = pb.DefaultClientFactory()
	}
	client, err := factory(backendName, label, req.Verbosity)
	if err != nil {
		return nil, fmt.Errorf("start plugin: %w", err)
	}
	defer client.Kill()

	var stdout, stderr bytes.Buffer
	exitCode, err := client.Run(ctx, runReq, nil, &stdout, &stderr, nil)
	if err != nil {
		return nil, fmt.Errorf("agent run: %w", err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("agent exited with code %d: %s", exitCode, strings.TrimSpace(stderr.String()))
	}

	return &RunOneshotResult{
		Profile:  req.Profile,
		Output:   strings.TrimSpace(stdout.String()),
		Label:    label,
		Backend:  backendName,
		Model:    model,
		ExitCode: exitCode,
	}, nil
}

// ResolveBackend maps a config label to its backend type and model. A label that
// is not a configured entry but names a registered backend type resolves to that
// backend directly (the ad-hoc `--llm <type>` convenience); otherwise
// cfg.ResolveLLM's lookup/default applies. Shared by `run` and the
// oneshot/map/weave path so backend resolution is identical everywhere.
func ResolveBackend(cfg *config.Config, label string) (backend, model string) {
	backend, model = cfg.ResolveLLM(label)
	if _, configured := cfg.LM.Configs[label]; !configured && backends.Exists(label) {
		return label, ""
	}
	return backend, model
}

// resolveOneshotLabel picks the config label for a oneshot run: an explicit
// override wins, then the profile's declared llm, then the primary role. Unknown
// labels degrade through cfg.ResolveLLM (→ default backend), so a stale profile
// llm never blocks the run (CLAUDE.md fault tolerance).
func resolveOneshotLabel(cfg *config.Config, override, profileLLM string) string {
	if override != "" {
		return override
	}
	if profileLLM != "" {
		return profileLLM
	}
	return cfg.PrimaryLabel()
}
