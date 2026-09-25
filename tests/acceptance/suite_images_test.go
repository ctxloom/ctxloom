//go:build acceptance

package acceptance

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
	"testing/fstest"
	"time"

	"github.com/cucumber/godog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// A command that outlives its bound fails THAT scenario, naming the command
// and the bound — even though the step running it discards the error, as the
// CLI step does by design — and the scenario after it still runs and passes.
// Before the bound, the command ran until go test's own timeout panicked the
// whole binary with no scenario named at all.
func TestScenarioCommandPastItsBoundFailsOnlyThatScenario(t *testing.T) {
	const bound = 200 * time.Millisecond
	feature := `Feature: bounded commands
  Scenario: a command hangs
    When a command outlives its bound

  Scenario: the next scenario still runs
    When a command finishes inside its bound
`
	var out bytes.Buffer
	suite := godog.TestSuite{
		Name: "bounded-commands",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) {
			InitializeScenario(ctx)
			ctx.Step(`^a command outlives its bound$`, func(c context.Context) error {
				w := worldFrom(c)
				w.env.SetCommandBound(bound)
				_ = w.env.Exec(exec.Command("sleep", "30"))
				return nil // discarded, exactly as runCLI discards it
			})
			ctx.Step(`^a command finishes inside its bound$`, func(c context.Context) error {
				return worldFrom(c).env.Exec(exec.Command("true"))
			})
		},
		Options: &godog.Options{
			Format:          "pretty",
			Output:          &out,
			Strict:          true,
			FeatureContents: []godog.Feature{{Name: "bounded.feature", Contents: []byte(feature)}},
			// An empty FS: with no Paths, godog otherwise ALSO loads ./features
			// — the whole suite — next to the contents above.
			FS: fstest.MapFS{},
		},
	}

	done := make(chan int, 1)
	go func() { done <- suite.Run() }()
	select {
	case code := <-done:
		assert.NotEqual(t, 0, code, "the suite passed with a hung command in it:\n%s", out.String())
	case <-time.After(15 * time.Second):
		t.Fatalf("the suite did not finish: a command past its %s bound was waited on", bound)
	}
	report := out.String()
	assert.Contains(t, report, "command `sleep 30` did not exit within its 200ms bound", "the failure must name the command and the bound")
	assert.Contains(t, report, "1 failed", report)
	assert.Contains(t, report, "1 passed", report)
}

// The selection is godog's own: a Rule's tag reaches every scenario under it,
// and a scenario the run's tag filter excludes declares nothing — so a lane
// that does not run the image's scenarios does not pay for the image.
func TestSelectedScenarioTags_FollowsTheRunsOwnSelection(t *testing.T) {
	selected := func(tags string, paths ...string) map[string]bool {
		t.Helper()
		got, err := selectedScenarioTags(godog.TestSuite{Options: &godog.Options{Paths: paths, Tags: tags}})
		require.NoError(t, err)
		return got
	}

	assert.True(t, selected("", "features/cli/container.feature")[imageMockAgent],
		"container.feature's Rule carries %s, so the scenarios under it must declare it", imageMockAgent)
	assert.False(t, selected("~@container", "features/journeys/j002400_container.feature")[imageMockAgent],
		"j002400's only image scenario is @container; a run excluding @container must not declare its image")
	assert.True(t, selected("@container", "features/journeys/j002400_container.feature")[imageMockAgent])
	assert.True(t, selected("@container", "features/journeys/j001400_bundle_distribution.feature")[imageCell])
}

// A step that needs an image refuses to run in a scenario that did not
// declare it: that scenario would get no image, or build one inside its own
// command's bound — the coupling this whole split exists to break.
func TestRequireSuiteImage_AnUndeclaredScenarioIsRefused(t *testing.T) {
	features, err := godog.TestSuite{Options: &godog.Options{Paths: []string{"features/cli/container.feature"}}}.RetrieveFeatures()
	require.NoError(t, err)
	var declared, undeclared *godog.Scenario
	for _, f := range features {
		for _, p := range f.Pickles {
			if scenarioHasTag(p, imageMockAgent) {
				declared = p
			} else {
				undeclared = p
			}
		}
	}
	require.NotNil(t, declared, "container.feature has no scenario declaring %s", imageMockAgent)
	require.NotNil(t, undeclared, "container.feature has no scenario without %s", imageMockAgent)

	preparedSuiteImages[imageMockAgent] = suiteImageState{decision: dockergate.Proceed}
	t.Cleanup(func() { delete(preparedSuiteImages, imageMockAgent) })

	err = requireSuiteImage(&World{scenario: undeclared}, imageMockAgent, "the row")
	require.Error(t, err, "a scenario that did not declare the image must be refused it")
	assert.Contains(t, err.Error(), imageMockAgent, "the refusal must name the tag to add")
	assert.NoError(t, requireSuiteImage(&World{scenario: declared}, imageMockAgent, "the row"))
}
