package coord

import (
	"context"
	"io"
	"time"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// The runner half of this suite's in-process harness (the Home, the engine
// host and the runner link) lives in adapters/runner, which imports this
// package — so an in-package test cannot import it back. The suite holds the
// runner half through the interfaces below and constructs it through
// RunnerHooks, which the external test package's bridge (runnerbridge_test.go)
// registers at init; both halves link into the one test binary. Nothing
// here is compiled into the package proper.

// TestHome is the runner's Home as this suite drives it.
type TestHome interface {
	Attached() bool
	BindIdentity(id Identity)
	Harp() string
	Depth() int
	RunID() string
	Capabilities() []string
	Done() <-chan struct{}
	EmittedSeq() uint64
	Redial()
	SetTerminalNudge(fn func())
	RecvParked() bool
	BufferedMailCount() int
	SetTurnSink(sink func(*agentcoordpb.PeerMessage) bool)
	AwaitMailAcked(ctx context.Context, ids []string) error
	ReportRunExited(exitCode int, harnessSessionID string)
	Request(ctx context.Context, req *agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error)
	Recv(ctx context.Context, wait time.Duration) ([]*agentcoordpb.PeerMessage, error)
	Report(ctx context.Context, summary *agentcoordpb.Summary, artifacts []*agentcoordpb.ArtifactProduced) error
	Crash()
	Close(exitCode int, harnessSessionID string)
	SetSpoolDoorbellHandler(fn SpoolDoorbellHandler)
	SpoolDoorbellStats() SpoolDoorbellStats
	SpoolDeliveryStats() SpoolDeliveryStats
	UploadArtifact(ctx context.Context, artifactID, name, mediaType string, sha256Sum [32]byte, size int64, r io.Reader) (*agentcoordpb.ArtifactReceipt, error)
	DownloadArtifact(ctx context.Context, agentID, artifactID, destPath string) (shaHex string, size int64, err error)
	SweepSpoolIn()
	ReportTurnResult(text, inReplyTo string) error
	RingSpool(ref spool.Ref) error
}

// TestEngineHost is the runner's engine host as this suite drives it.
type TestEngineHost interface {
	Close()
	BindHome(h TestHome)
	Handle(req *agentcoordpb.RunnerRequest) *agentcoordpb.RunnerResponse
}

// TestRunnerLink is the runner's RunnerChannel link as this suite drives it.
type TestRunnerLink interface {
	Done() <-chan struct{}
	Shutdown(exitCode int, harnessSessionID string)
	Abort()
}

// TestRunnerRequestHandler answers one coordinator-initiated RunnerRequest —
// the runner's engine-control seam, as a Home is configured with it.
type TestRunnerRequestHandler func(*agentcoordpb.RunnerRequest) *agentcoordpb.RunnerResponse

// TestHomeConfig is the runner's HomeConfig as this suite spells it.
type TestHomeConfig struct {
	URL                string
	Token              string
	RunID              string
	Harness            string
	Version            string
	Engine             TestRunnerRequestHandler
	Capabilities       []string
	Harp               string
	Mapper             spool.PathMapper
	SpoolSweepInterval time.Duration
	Reporter           report.Sink
}

// TestRunnerHooks is what the bridge registers: the runner half's
// constructors and its frame vocabulary.
type TestRunnerHooks struct {
	// Serve stands the coordinator's wire up (the adapter's Serve): the
	// listeners the runner half dials.
	Serve         func(c *Coordinator) error
	NewHome       func(ctx context.Context, cfg TestHomeConfig) (TestHome, error)
	NewEngineHost func(ctx context.Context, rep report.Sink, harness, runID string) TestEngineHost
	// BindTestRunner binds the suite's runner tail to an engine host: the
	// tail decodes the StartRun frame's launch, opens the package and
	// drives inst's driver; refuse, when set and answering true, refuses the
	// launch the way a runner whose endpoint cannot be bound does.
	BindTestRunner func(eh TestEngineHost, inst engine.Instance, refuse func() bool)
	DialRunner     func(ctx context.Context, rep report.Sink, coordURL, token, runID, harness, version string, handler TestRunnerRequestHandler) (TestRunnerLink, error)
	// FrameCoordinatorDelivery renders one coordinator-delivered message the
	// way the engine host frames it for the engine; CoordinatorFrameOpen is
	// that frame's opening literal.
	FrameCoordinatorDelivery func(from, kind, body string) string
	FrameCoordinatorMessage  func(pm *agentcoordpb.PeerMessage) string
	CoordinatorFrameOpen     string
	// ErrCoordinatorUnreachable is the Home's refusal of a request that
	// never got through.
	ErrCoordinatorUnreachable error
	// HomeRedialBackoff is the Home's reconnect pace.
	HomeRedialBackoff time.Duration
}

var runnerHooks TestRunnerHooks

// SetRunnerHooks is called once by the bridge before any test runs.
func SetRunnerHooks(h TestRunnerHooks) { runnerHooks = h }
