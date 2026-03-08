package steps

import (
	"github.com/SophisticatedContextManager/scm/tests/acceptance/steps/support"
)

// TestEnv holds the current test environment for step definitions
var TestEnv *support.TestEnvironment

// MockLM holds the mock language model for the current scenario
var MockLM *support.MockLM
