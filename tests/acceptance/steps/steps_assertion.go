package steps

import (
	"fmt"
	"regexp"

	"github.com/cucumber/godog"
)

// RegisterAssertionSteps registers steps for assertions.
func RegisterAssertionSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the exit code should be (\d+)$`, theExitCodeShouldBe)
	ctx.Step(`^the output should contain "([^"]*)"$`, theOutputShouldContain)
	ctx.Step(`^the output should not contain "([^"]*)"$`, theOutputShouldNotContain)
	ctx.Step(`^the output should match "([^"]*)"$`, theOutputShouldMatch)
	ctx.Step(`^the file "([^"]*)" should exist$`, theFileShouldExist)
	ctx.Step(`^the file "([^"]*)" should not exist$`, theFileShouldNotExist)
	ctx.Step(`^the file "([^"]*)" should contain "([^"]*)"$`, theFileShouldContain)
	ctx.Step(`^the file "([^"]*)" should not contain "([^"]*)"$`, theFileShouldNotContain)
	ctx.Step(`^the home file "([^"]*)" should exist$`, theHomeFileShouldExist)
	ctx.Step(`^the home file "([^"]*)" should not exist$`, theHomeFileShouldNotExist)
	ctx.Step(`^the home file "([^"]*)" should contain "([^"]*)"$`, theHomeFileShouldContain)
	ctx.Step(`^the home file "([^"]*)" should not contain "([^"]*)"$`, theHomeFileShouldNotContain)
}

func theExitCodeShouldBe(expected int) error {
	actual := TestEnv.LastExitCode()
	if actual != expected {
		return fmt.Errorf("expected exit code %d, got %d\nOutput: %s", expected, actual, TestEnv.LastOutput())
	}
	return nil
}

func theOutputShouldContain(expected string) error {
	output := TestEnv.LastOutput()
	if !contains(output, expected) {
		return fmt.Errorf("expected output to contain %q\nActual output:\n%s", expected, output)
	}
	return nil
}

func theOutputShouldNotContain(unexpected string) error {
	output := TestEnv.LastOutput()
	if contains(output, unexpected) {
		return fmt.Errorf("expected output NOT to contain %q\nActual output:\n%s", unexpected, output)
	}
	return nil
}

func theOutputShouldMatch(pattern string) error {
	output := TestEnv.LastOutput()
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("invalid regex pattern %q: %w", pattern, err)
	}
	if !re.MatchString(output) {
		return fmt.Errorf("expected output to match pattern %q\nActual output:\n%s", pattern, output)
	}
	return nil
}

func theFileShouldExist(path string) error {
	if !TestEnv.FileExists(path) {
		return fmt.Errorf("expected file %q to exist", path)
	}
	return nil
}

func theFileShouldNotExist(path string) error {
	if TestEnv.FileExists(path) {
		return fmt.Errorf("expected file %q NOT to exist", path)
	}
	return nil
}

func theFileShouldContain(path, expected string) error {
	content, err := TestEnv.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read file %q: %w", path, err)
	}
	if !contains(content, expected) {
		return fmt.Errorf("expected file %q to contain %q\nActual content:\n%s", path, expected, content)
	}
	return nil
}

func theFileShouldNotContain(path, unexpected string) error {
	content, err := TestEnv.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read file %q: %w", path, err)
	}
	if contains(content, unexpected) {
		return fmt.Errorf("expected file %q NOT to contain %q\nActual content:\n%s", path, unexpected, content)
	}
	return nil
}

func theHomeFileShouldExist(path string) error {
	if !TestEnv.HomeFileExists(path) {
		return fmt.Errorf("expected home file %q to exist", path)
	}
	return nil
}

func theHomeFileShouldNotExist(path string) error {
	if TestEnv.HomeFileExists(path) {
		return fmt.Errorf("expected home file %q NOT to exist", path)
	}
	return nil
}

func theHomeFileShouldContain(path, expected string) error {
	content, err := TestEnv.ReadHomeFile(path)
	if err != nil {
		return fmt.Errorf("failed to read home file %q: %w", path, err)
	}
	if !contains(content, expected) {
		return fmt.Errorf("expected home file %q to contain %q\nActual content:\n%s", path, expected, content)
	}
	return nil
}

func theHomeFileShouldNotContain(path, unexpected string) error {
	content, err := TestEnv.ReadHomeFile(path)
	if err != nil {
		return fmt.Errorf("failed to read home file %q: %w", path, err)
	}
	if contains(content, unexpected) {
		return fmt.Errorf("expected home file %q NOT to contain %q\nActual content:\n%s", path, unexpected, content)
	}
	return nil
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
