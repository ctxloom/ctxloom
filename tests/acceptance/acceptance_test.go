//go:build acceptance

package acceptance

import (
	"os"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"github.com/cucumber/godog/colors"

	"github.com/ctxloom/ctxloom/internal/testsupport"
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
	os.Exit(m.Run())
}

// TestAcceptance runs the full-stack godog suite. The hermetic suite is the
// default; @live scenarios (real Claude agent) are opt-in via ACCEPTANCE_TAGS
// and self-skip when no credentials are present.
func TestAcceptance(t *testing.T) {
	tags := os.Getenv("ACCEPTANCE_TAGS")
	if tags == "" {
		// Default suite is hermetic: @live needs the real agent, @network reaches
		// out (real init clones the default remote). Both are opt-in.
		tags = "~@live && ~@network"
	}
	paths := []string{"features"}
	// ACCEPTANCE_PATHS narrows the run to specific feature files for fast local
	// iteration (comma-separated, e.g. "features/j1_setup.feature"); unset runs
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
