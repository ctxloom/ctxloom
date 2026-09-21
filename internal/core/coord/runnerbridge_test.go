package coord_test

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/textblocks"
)

// The bridge: this external test package MAY import adapters/runner (the
// import cycle rule binds in-package tests only), so it is where the
// in-package suite's runner half is built. It registers the constructors
// through coord.SetRunnerHooks before any test runs.
func init() {
	coord.SetRunnerHooks(coord.TestRunnerHooks{
		NewHome: func(ctx context.Context, cfg coord.TestHomeConfig) (coord.TestHome, error) {
			h, err := runner.NewHome(ctx, runner.HomeConfig{
				URL:                cfg.URL,
				Token:              cfg.Token,
				RunID:              cfg.RunID,
				Harness:            cfg.Harness,
				Version:            cfg.Version,
				Engine:             runner.RunnerRequestHandler(cfg.Engine),
				Capabilities:       cfg.Capabilities,
				Harp:               cfg.Harp,
				Mapper:             cfg.Mapper,
				SpoolSweepInterval: cfg.SpoolSweepInterval,
				Reporter:           cfg.Reporter,
			})
			if h == nil {
				return nil, err
			}
			return h, err
		},
		NewEngineHost: func(ctx context.Context, rep report.Sink, backend agent.StructuredChat, harness, runID string) coord.TestEngineHost {
			return &engineHost{EngineHost: runner.NewEngineHost(ctx, rep, backend, harness, runID)}
		},
		BindTestRunner: func(eh coord.TestEngineHost, refuse func() bool) {
			host := eh.(*engineHost).EngineHost
			host.BindRunner(testRunner{eh: host, refuse: refuse})
		},
		DialRunner: func(ctx context.Context, rep report.Sink, coordURL, token, runID, harness, version string, handler coord.TestRunnerRequestHandler) (coord.TestRunnerLink, error) {
			link, err := runner.DialRunner(ctx, rep, coordURL, token, runID, harness, version, runner.RunnerRequestHandler(handler))
			if link == nil {
				return nil, err // a nil *RunnerLink must not become a non-nil interface
			}
			return link, err
		},
		FrameCoordinatorDelivery:  runner.FrameCoordinatorDelivery,
		FrameCoordinatorMessage:   runner.FrameCoordinatorMessage,
		CoordinatorFrameOpen:      runner.CoordinatorFrameOpen,
		ErrCoordinatorUnreachable: runner.ErrCoordinatorUnreachable,
		HomeRedialBackoff:         runner.HomeRedialBackoff,
	})
}

// engineHost adapts the runner's engine host to the suite's interface: the
// one seam that differs is BindHome, whose parameter is the runner's own
// home port.
type engineHost struct{ *runner.EngineHost }

func (e *engineHost) BindHome(h coord.TestHome) { e.EngineHost.BindHome(h.(*runner.Home)) }

// testRunner is the suite's runner tail: it executes the launch the StartRun
// frame carries the way runner.Execute does — decode, open the package,
// drive — delivering nothing, so the tests observe the drive.
type testRunner struct {
	eh *runner.EngineHost
	// refuse, when set and true, refuses the launch the way a runner whose
	// endpoint cannot be bound does.
	refuse func() bool
}

func (r testRunner) Execute(ctx context.Context, wire *agentcoordpb.Launch) error {
	l, err := coordgrpc.DecodeLaunch(wire)
	if err != nil {
		return err
	}
	if r.refuse != nil && r.refuse() {
		return delivery.ErrEndpointUnavailable
	}
	pkg, err := composite.Open(ctx, composite.Inline{}, composite.ClaimCheck{Store: launchtest.MemStore{}}, l.Package)
	if err != nil {
		return err
	}
	prompt := l.Prompt
	if l.Resume.NativeKey == "" {
		prompt = textblocks.Join(pkg.Context.Text, l.Prompt)
	}
	return r.eh.Drive(ctx, runner.Turn{
		Launch: l,
		Chat:   agent.ChatRequest{WorkDir: l.Cell.Workspace, Model: l.Label.Model, Permissions: l.Permission, ResumeSessionID: l.Resume.NativeKey},
		Prompt: prompt,
	})
}
