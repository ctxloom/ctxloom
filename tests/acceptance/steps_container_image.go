//go:build acceptance

// container.feature's shared_fs="ok" claim (the Rule "Check reports; it never
// builds, changes or blocks anything") can only be earned against a PRESENT
// agent image: isolation.Diagnose runs the definitive marker probe only when
// the image already exists, and otherwise falls back to an advisory heuristic
// ("unprobed: likely shared ..."). The image tag is content- AND
// version-keyed (imagebuild.go's composedImageTagFor), so any change to the
// base Containerfile OR the ctxloom version invalidates whatever tag happens
// to already sit on a developer's machine — dragging-neatness, ruled (a)
// 2026-09-03: build the image rather than loosen the assertion. It is built
// once, before any scenario runs, for the scenarios tagged imageMockAgent
// (suite_images.go) — the Rule carries the tag.
//
// THE SUBJECT IS "mock", not a vendor engine. mockInstallFragment
// (internal/adapters/isolation/enginespec.go) installs no vendor CLI at all, so
// building it touches the network zero times beyond the base image layer —
// unlike a claude-code/codex/opencode build, which pulls each engine's
// official installer and costs minutes. container.feature's own header rules
// out driving a real vendor build from this file; mock is exempt from the
// cost that rule exists to avoid.
//
// GATED ON dockergate (via containercell.Select, its acceptance-suite-facing
// wrapper): a machine with no reachable container runtime SKIPS, and
// CTXLOOM_REQUIRE_DOCKER=1 (CI) promotes that to a hard failure rather than a
// silent skip.
//
// BUILT VIA THE CLI SUBPROCESS, NOT isolation.BuildAgentImage CALLED
// DIRECTLY. The image tag folds in isolation.SetBinaryVersion's stamp, which
// only internal/adapters/cli's root command sets, from the ldflags-injected version —
// a Go-level call from this test BINARY would build under an unstamped
// (omitted) version key and mint a DIFFERENT tag than the one the exec'd
// ./ctxloom binary looks for. Shelling out to the SAME built binary this
// whole suite already execs keeps the two tags identical by construction.
package acceptance

import (
	"context"
	"fmt"
	"time"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/testsupport/containercell"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// mockAgentImageWhat names this precondition's row in skip and failure
// messages, so they explain themselves without cross-referencing this file.
const mockAgentImageWhat = `container.feature's shared_fs="ok" claim (dragging-neatness)`

// buildMockAgentImage runs `container build mock --keep-cache --runtime <rt>`
// through the SAME prebuilt binary every scenario execs (see the file comment
// for why it cannot be a direct isolation.BuildAgentImage call), from a
// throwaway environment shaped like a scenario's, under bound.
func buildMockAgentImage(_ context.Context, rt containercell.Runtime, bound time.Duration) error {
	env, err := testenv.NewTestEnvironment()
	if err != nil {
		return err
	}
	defer func() { _ = env.Cleanup() }()
	if err := env.Setup(); err != nil {
		return err
	}
	env.SetCommandBound(bound)
	if err := env.Run("container", "build", "mock", "--keep-cache", "--runtime", rt.Command); err != nil {
		return fmt.Errorf("%w\n%s", err, env.LastOutput())
	}
	return nil
}

func registerContainerImageSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the mock agent image is available for the shared-filesystem probe$`, func(c context.Context) error {
		return requireSuiteImage(worldFrom(c), imageMockAgent, mockAgentImageWhat)
	})
}
