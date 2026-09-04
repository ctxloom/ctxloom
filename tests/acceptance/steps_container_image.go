//go:build acceptance

// container.feature's shared_fs="ok" claim (the Rule "Check reports; it never
// builds, changes or blocks anything") can only be earned against a PRESENT
// agent image: isolation.Diagnose runs the definitive marker probe only when
// the image already exists, and otherwise falls back to an advisory heuristic
// ("unprobed: likely shared ..."). The image tag is content- AND
// version-keyed (imagebuild.go's composedImageTagFor), so any change to the
// base Containerfile OR the ctxloom version invalidates whatever tag happens
// to already sit on a developer's machine — dragging-neatness, ruled (a)
// 2026-09-03: build the image on demand rather than loosen the assertion.
//
// THE SUBJECT IS "mock", not a vendor engine. mockInstallFragment
// (internal/lm/isolation/enginespec.go) installs no vendor CLI at all, so
// building it touches the network zero times beyond the base image layer —
// unlike a claude-code/codex/opencode build, which pulls each engine's
// official installer and costs minutes. container.feature's own header rules
// out driving a real vendor build from this file; mock is exempt from the
// cost that rule exists to avoid.
//
// GATED ON dockergate (via containercell.Select, its acceptance-suite-facing
// wrapper — see steps_j002400_container.go for the same pattern): a machine
// with no reachable container runtime SKIPS, and CTXLOOM_REQUIRE_DOCKER=1
// (CI) promotes that to a hard failure rather than a silent skip.
//
// BUILT VIA THE CLI SUBPROCESS, NOT isolation.BuildAgentImage CALLED
// DIRECTLY. The image tag folds in isolation.SetBinaryVersion's stamp, which
// only internal/cli's root command sets, from the ldflags-injected version —
// a Go-level call from this test BINARY would build under an unstamped
// (omitted) version key and mint a DIFFERENT tag than the one the exec'd
// ./ctxloom binary looks for. Shelling out to the SAME built binary this
// whole suite already execs keeps the two tags identical by construction.
//
// BUILDS AT MOST ONCE FOR THE WHOLE SUITE. The tag is deterministic, so every
// scenario that needs it wants the exact same artifact; sync.Once makes sure
// only the FIRST scenario to reach this step pays the daemon round trip, and
// every later scenario (including ones outside this Rule that never call it)
// replays the cached decision for free.
package acceptance

import (
	"context"
	"fmt"
	"sync"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/testsupport/containercell"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// mockAgentImageWhat names this precondition in every dockergate/containercell
// message, so a skip or failure explains itself without cross-referencing
// this file.
const mockAgentImageWhat = `container.feature's shared_fs="ok" claim (dragging-neatness)`

var (
	mockAgentImageOnce     sync.Once
	mockAgentImageDecision dockergate.Decision
	mockAgentImageMsg      string
)

// ensureMockAgentImage runs the actual gate-and-build sequence exactly once
// per process, memoizing the outcome for every later call. runBuild executes
// `container build mock --keep-cache --runtime <rt>` against the SAME
// prebuilt binary the calling scenario already uses (see the package doc for
// why this cannot be a direct isolation.BuildAgentImage call), and returns its
// combined output plus any error.
func ensureMockAgentImage(runBuild func(runtimeCmd string) (output string, err error)) (dockergate.Decision, string) {
	mockAgentImageOnce.Do(func() {
		rt, decision, msg := containercell.Select(context.Background(), mockAgentImageWhat)
		if decision != dockergate.Proceed {
			mockAgentImageDecision, mockAgentImageMsg = decision, msg
			return
		}
		out, err := runBuild(rt.Command)
		if err != nil {
			mockAgentImageDecision = dockergate.Fail
			mockAgentImageMsg = fmt.Sprintf("building the mock agent image for %s failed: %v\n%s", mockAgentImageWhat, err, out)
			return
		}
		mockAgentImageDecision = dockergate.Proceed
	})
	return mockAgentImageDecision, mockAgentImageMsg
}

func registerContainerImageSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the mock agent image is available for the shared-filesystem probe$`, func(c context.Context) error {
		w := worldFrom(c)
		decision, msg := ensureMockAgentImage(func(runtimeCmd string) (string, error) {
			cmd := w.env.Command(nil, "container", "build", "mock", "--keep-cache", "--runtime", runtimeCmd)
			out, err := cmd.CombinedOutput()
			return string(out), err
		})
		switch decision {
		case dockergate.Skip:
			fmt.Printf("SKIPPED (%s): %s\n", mockAgentImageWhat, msg)
			return godog.ErrSkip
		case dockergate.Fail:
			return fmt.Errorf("%s", msg)
		default:
			return nil
		}
	})
}
