//go:build acceptance

package acceptance

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"github.com/cucumber/godog/colors"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// TestMain scrubs the ambient session variables from the test process before any
// scenario runs. Without this, the CLI runner (which inherits the process
// environment with only HOME overridden) would resolve the host session's
// project id, while the spawned MCP server scrubs it — the two would disagree on
// which home-rooted store to use. Scrubbing once here keeps both axes isolated.
func TestMain(m *testing.M) {
	// Capture the real home before any scenario overrides HOME — @live uses it to
	// locate ~/.claude for the subscription-auth path.
	realHomeDir = os.Getenv("HOME")
	for _, k := range testsupport.EnvKeys {
		_ = os.Unsetenv(k)
	}
	code := m.Run()
	// The taskloom binary testenv builds for the j002500/j002600/trigger steps lives
	// behind a sync.Once and is shared by every scenario, so its ~30MB
	// directory can only be dropped here, after the whole suite has finished —
	// not in a per-scenario Cleanup. os.Exit skips defers, so this is an
	// explicit statement rather than `defer`.
	testenv.RemoveTaskloomBinary()
	os.Exit(code)
}

// TestAcceptance runs the full-stack godog suite. The hermetic suite is the
// default; @live scenarios (real engine agents) are opt-in via ACCEPTANCE_TAGS
// and self-skip when no credentials are present.
func TestAcceptance(t *testing.T) {
	// The availability report prints on EVERY run — hermetic or live — so a
	// credential expiry (or a binary going missing) shows up as a loud line
	// instead of silently dropping live coverage to zero while the suite
	// still reports green. See live_engine_registry.go.
	report := computeLiveEngineReport(realHomeDir, resolveOptIn())
	fmt.Println(formatLiveEngineReport(report))

	// THE FLOOR: CTXLOOM_LIVE_REQUIRE names engines that MUST be available —
	// unset (a dev box), a missing engine just skips; set (CI, or a dev who
	// wants the guarantee), a missing/unauthenticated engine is a hard
	// failure, named explicitly, checked before the (possibly long) suite
	// run rather than after.
	if required := parseRequiredEngines(os.Getenv("CTXLOOM_LIVE_REQUIRE")); len(required) > 0 {
		if err := checkRequiredEngines(report, required); err != nil {
			t.Fatal(err)
		}
	}

	tags := os.Getenv("ACCEPTANCE_TAGS")
	if tags == "" {
		// out (real init clones the default remote). Both are opt-in.
		// @future: behavior this suite deliberately does not implement yet (see
		//   j000700_team_authoring.feature's own-active-session scenario) — excluded
		//   rather than left undefined, which Strict mode would fail on.
		// @wip: a scenario that cannot be honestly greened yet because of a real
		//   product gap, not a harness gap (see j001500_corporate_signed.feature's
		//   retraction scenario and the filed task) — excluded from the default run.
		// @container: needs a reachable docker/podman daemon AND builds an agent
		//   image on first use (measured at minutes, against a suite that already
		//   brushes go test's 10m default). It has its own gate,
		//   `just test-acceptance-container`, which really performs the launch.
		tags = "~@live && ~@network && ~@future && ~@wip && ~@container"
	}
	paths := []string{"features"}
	// ACCEPTANCE_PATHS narrows the run to specific feature files for fast local
	// iteration (comma-separated, e.g. "features/j000200_setup.feature"); unset runs
	// the whole suite, exactly as before this existed.
	if p := os.Getenv("ACCEPTANCE_PATHS"); p != "" {
		paths = strings.Split(p, ",")
	}
	suite := godog.TestSuite{
		Name:                "ctxloom-acceptance",
		ScenarioInitializer: InitializeScenario,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    paths,
			Tags:     tags,
			Strict:   true,
			Output:   colors.Colored(os.Stdout),
			TestingT: t,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("acceptance suite failed")
	}
}
