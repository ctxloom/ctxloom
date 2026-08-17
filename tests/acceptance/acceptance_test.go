//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
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
	// Whether THIS run is the hermetic lane decides whether a runtime skip is
	// fatal below. ACCEPTANCE_PATHS deliberately does not affect it: narrowing
	// to one feature file is still the hermetic lane, so `just test-acceptance`
	// with ACCEPTANCE_PATHS set stays gated.
	hermetic := tags == ""
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
	skips := &skipLedger{}
	suite := godog.TestSuite{
		Name: "ctxloom-acceptance",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			InitializeScenario(ctx)
			skips.attach(ctx)
		},
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

	// THE SKIP GATE. godog files an all-skipped scenario under PASSED and
	// reports no scenario-level skipped count, so "571 passed" cannot be told
	// apart from "571, some of which quietly declined to run". Everything above
	// has already established the run was GREEN; this asks the second question.
	//
	// It is only reached on green, and that is what keeps it readable: once any
	// step returns non-nil, godog marks every REMAINING step in that scenario
	// Skipped too, so on a red run the ledger would be mostly wreckage from the
	// failure rather than steps that declined.
	if declined := skips.report(); declined != "" {
		if hermetic {
			t.Fatal("the hermetic suite passed, but steps declined to run — a skip in this lane is a defect, " +
				"because every scenario that may legitimately skip is excluded by tag before the run starts:\n" + declined)
		}
		// NOT fatal outside the hermetic lane, and this is a HOLD rather than a
		// designed exemption. @live/@container skips are per-Examples-ROW
		// (claude authenticated, codex not) while a tag is per-SCENARIO, so they
		// cannot be hoisted to a startup exclusion without splitting those
		// Examples tables — and making them fatal today would turn
		// `ACCEPTANCE_TAGS=@live` red on any box not authenticated for every
		// vendor. That is a user-visible call for a human, not a default to
		// drift into. Printing the inventory is what makes it decidable.
		fmt.Printf("\nSTEPS THAT DECLINED TO RUN (not fatal: this is not the hermetic lane)\n%s\n", declined)
	}
}

// skipLedger records the step that DECLINED in each scenario, so a skip can be
// named rather than merely counted.
//
// Only the FIRST skipped step of a scenario is kept. godog's after-step hook
// fires for every step including skipped ones, and a scenario that skips at
// step 3 reports steps 4..n as skipped as well; the first one is the only one
// that made a decision, and the rest are its shadow.
type skipLedger struct {
	mu      sync.Mutex
	seen    map[string]bool
	entries []string
}

// skipLedgerScenarioKey carries the running scenario into the after-step hook,
// which is handed the step but not the scenario it belongs to. A context value
// rather than a field because godog may run scenarios concurrently.
type skipLedgerScenarioKey struct{}

func (l *skipLedger) attach(ctx *godog.ScenarioContext) {
	ctx.Before(func(c context.Context, sc *godog.Scenario) (context.Context, error) {
		return context.WithValue(c, skipLedgerScenarioKey{}, sc), nil
	})
	ctx.StepContext().After(func(c context.Context, st *godog.Step, status godog.StepResultStatus, _ error) (context.Context, error) {
		if status != godog.StepSkipped {
			return c, nil
		}
		sc, ok := c.Value(skipLedgerScenarioKey{}).(*godog.Scenario)
		if !ok {
			return c, nil
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.seen == nil {
			l.seen = map[string]bool{}
		}
		if l.seen[sc.Id] {
			return c, nil
		}
		l.seen[sc.Id] = true
		l.entries = append(l.entries, fmt.Sprintf("  %s: %s\n    declined at step: %s", sc.Uri, sc.Name, st.Text))
		return c, nil
	})
}

func (l *skipLedger) report() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) == 0 {
		return ""
	}
	return strings.Join(l.entries, "\n")
}
