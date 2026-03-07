package acceptance

import (
	"context"
	"github.com/benjaminabbitt/scm/tests/acceptance/steps"
	"github.com/benjaminabbitt/scm/tests/acceptance/steps/support"
	"testing"

	"github.com/cucumber/godog"
)

// TestFeatures runs all cucumber feature tests.
func TestFeatures(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: InitializeScenario,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"features"},
			TestingT: t,
		},
	}

	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run feature tests")
	}
}

// InitializeScenario sets up step definitions and hooks for each scenario.
func InitializeScenario(ctx *godog.ScenarioContext) {
	// Before each scenario: create fresh test environment
	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		var err error
		steps.TestEnv, err = support.NewTestEnvironment()
		if err != nil {
			return ctx, err
		}

		if err := steps.TestEnv.Setup(); err != nil {
			_ = steps.TestEnv.Cleanup()
			return ctx, err
		}

		// Reset MockLM for each scenario
		steps.MockLM = nil

		return ctx, nil
	})

	// After each scenario: cleanup test environment
	ctx.After(func(ctx context.Context, sc *godog.Scenario, err error) (context.Context, error) {
		if steps.TestEnv != nil {
			cleanupErr := steps.TestEnv.Cleanup()
			steps.TestEnv = nil
			if err == nil {
				return ctx, cleanupErr
			}
		}
		return ctx, err
	})

	// Register step definitions from separate files
	steps.RegisterEnvironmentSteps(ctx)
	steps.RegisterFragmentSteps(ctx)
	steps.RegisterCommandSteps(ctx)
	steps.RegisterAssertionSteps(ctx)
	steps.RegisterMockLMSteps(ctx)
	steps.RegisterMCPSteps(ctx)
}
