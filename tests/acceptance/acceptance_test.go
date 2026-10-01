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
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
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
	launchCredentials = captureLaunchCredentials()
	for _, k := range testsupport.EnvKeys {
		_ = os.Unsetenv(k)
	}
	enginefixture.MustComposeShipped()
	code := m.Run()
	// The taskloom binary testenv builds for the j002500/j002600/trigger steps lives
	// behind a sync.Once and is shared by every scenario, so its ~30MB
	// directory can only be dropped here, after the whole suite has finished —
	// not in a per-scenario Cleanup. os.Exit skips defers, so this is an
	// explicit statement rather than `defer`.
	testenv.RemoveTaskloomBinary()
	os.Exit(code)
}

// TestHermeticChildSeesNoCredential pins the other half of the launch
// capture: capturing the credential for @live cells must not hand it to
// anything else. TestMain's scrub has removed every engine credential from this
// process, and a ctxloom child a hermetic scenario spawns carries none — even
// with a token in the capture and one re-exported ambiently, which is the
// state a refactor that "simplified" the capture into an inherited env would
// produce. Values are fake; nothing is executed.
func TestHermeticChildSeesNoCredential(t *testing.T) {
	var keys []string
	for _, a := range liveAgents {
		keys = append(keys, a.apiKeyEnvs...)
	}
	for _, k := range keys {
		if _, set := os.LookupEnv(k); set {
			t.Errorf("%s is set in the test process: TestMain must scrub every engine credential before any scenario runs", k)
		}
	}

	saved := launchCredentials
	t.Cleanup(func() { launchCredentials = saved })
	launchCredentials = map[string]string{}
	for _, k := range keys {
		launchCredentials[k] = "fake-captured"
		t.Setenv(k, "fake-ambient")
	}

	env, err := testenv.NewTestEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Cleanup() })
	for _, kv := range env.Command(nil, "version").Env {
		for _, k := range keys {
			if strings.HasPrefix(kv, k+"=") {
				t.Errorf("a hermetic child's environment carries %s: only an @live cell may be handed a credential", k)
			}
		}
	}
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
		// @container: needs a reachable docker/podman daemon AND an agent image,
		//   built before the scenarios run (prepareSuiteImages). It has its own
		//   gate, `just test-acceptance-container`, which really performs the
		//   launch. ACCEPTANCE_INCLUDE_CONTAINER=1 keeps them IN this lane: a
		//   caller that must judge the hermetic AND container scenarios of the
		//   same features in one run (the mutation harness's acceptanceJudge)
		//   adds them here rather than hand-copying the exclusions above into an
		//   ACCEPTANCE_TAGS of its own, which would drift from this one — and
		//   staying hermetic keeps a declined container step fatal.
		tags = "~@live && ~@network && ~@future && ~@wip"
		if os.Getenv("ACCEPTANCE_INCLUDE_CONTAINER") != "1" {
			tags += " && ~@container"
		}
	}
	paths := []string{"features"}
	// ACCEPTANCE_PATHS narrows the run to specific feature files for fast local
	// iteration (comma-separated, e.g. "features/journeys/j000200_setup.feature"); unset runs
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
	declared, err := selectedScenarioTags(suite)
	if err != nil {
		t.Fatal(err)
	}
	prepareSuiteImages(context.Background(), declared)
	excludeSkippedSuiteImagesInLane(t, &suite, hermetic)
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
		// Outside the hermetic lane a declining step is RECORDED, not fatal.
		// The gate above holds only because every legitimate skip in that lane
		// is excluded by tag before the run starts; the opt-in lanes have no
		// such guarantee. Their skips are decided per Examples ROW — one
		// engine authenticated, another not — while a tag applies to a whole
		// SCENARIO, so the condition is not addressable by exclusion and a
		// skip there is not by itself a defect. Naming the steps keeps that
		// difference visible rather than silent.
		fmt.Printf("\nSTEPS THAT DECLINED TO RUN (not fatal: this is not the hermetic lane)\n%s\n", declined)
	}
}

// excludeSkippedSuiteImagesInLane applies excludeSkippedSuiteImages to the
// hermetic lane only, and prints what it left out. The opt-in lanes keep the
// image's scenarios selected: there a declined row is recorded, not fatal.
func excludeSkippedSuiteImagesInLane(t *testing.T, suite *godog.TestSuite, hermetic bool) {
	t.Helper()
	if !hermetic {
		return
	}
	excluded, err := excludeSkippedSuiteImages(suite)
	if err != nil {
		t.Fatal(err)
	}
	if excluded != "" {
		fmt.Print("\n" + excluded + "\n")
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
