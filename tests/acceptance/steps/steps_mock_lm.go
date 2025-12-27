package steps

import (
	"fmt"

	"github.com/cucumber/godog"
)

// RegisterMockLMSteps registers steps for mock LM operations.
func RegisterMockLMSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^a mock LM is configured$`, aMockLMIsConfigured)
	ctx.Step(`^the mock LM will respond with:$`, theMockLMWillRespondWith)
	ctx.Step(`^the mock LM will exit with code (\d+)$`, theMockLMWillExitWithCode)
	ctx.Step(`^the LM should have received context containing "([^"]*)"$`, theLMShouldHaveReceivedContextContaining)
	ctx.Step(`^the LM should have received context not containing "([^"]*)"$`, theLMShouldHaveReceivedContextNotContaining)
	ctx.Step(`^the LM input should contain "([^"]*)"$`, theLMInputShouldContain)
}

func aMockLMIsConfigured() error {
	var err error
	MockLM, err = TestEnv.SetupMockLM()
	return err
}

func theMockLMWillRespondWith(response *godog.DocString) error {
	if MockLM == nil {
		return fmt.Errorf("mock LM not configured; use 'Given a mock LM is configured' first")
	}
	return MockLM.SetResponse(response.Content)
}

func theMockLMWillExitWithCode(code int) error {
	if MockLM == nil {
		return fmt.Errorf("mock LM not configured; use 'Given a mock LM is configured' first")
	}
	return MockLM.SetExitCode(code)
}

func theLMShouldHaveReceivedContextContaining(expected string) error {
	if MockLM == nil {
		return fmt.Errorf("mock LM not configured")
	}
	input, err := MockLM.GetRecordedInput()
	if err != nil {
		return fmt.Errorf("failed to get recorded input: %w", err)
	}
	if !contains(input, expected) {
		return fmt.Errorf("expected LM input to contain %q\nActual input:\n%s", expected, input)
	}
	return nil
}

func theLMShouldHaveReceivedContextNotContaining(unexpected string) error {
	if MockLM == nil {
		return fmt.Errorf("mock LM not configured")
	}
	input, err := MockLM.GetRecordedInput()
	if err != nil {
		return fmt.Errorf("failed to get recorded input: %w", err)
	}
	if contains(input, unexpected) {
		return fmt.Errorf("expected LM input NOT to contain %q\nActual input:\n%s", unexpected, input)
	}
	return nil
}

func theLMInputShouldContain(expected string) error {
	return theLMShouldHaveReceivedContextContaining(expected)
}
