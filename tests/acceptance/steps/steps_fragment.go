package steps

import (
	"strings"

	"github.com/cucumber/godog"
)

// RegisterFragmentSteps registers steps for fragment operations.
func RegisterFragmentSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^a fragment "([^"]*)" with content:$`, aFragmentWithContent)
	ctx.Step(`^a fragment "([^"]*)" in the project with content:$`, aFragmentInProjectWithContent)
	ctx.Step(`^a fragment "([^"]*)" in home with content:$`, aFragmentInHomeWithContent)
	ctx.Step(`^a prompt "([^"]*)" in the project with content:$`, aPromptInProjectWithContent)
	ctx.Step(`^a prompt "([^"]*)" in home with content:$`, aPromptInHomeWithContent)
	ctx.Step(`^a config file with:$`, aConfigFileWith)
	ctx.Step(`^a home config file with:$`, aHomeConfigFileWith)
}

func aFragmentWithContent(name string, content *godog.DocString) error {
	return aFragmentInProjectWithContent(name, content)
}

func aFragmentInProjectWithContent(name string, content *godog.DocString) error {
	path := ".mlcm/context-fragments/" + name + ".yaml"
	return TestEnv.WriteFile(path, content.Content)
}

func aFragmentInHomeWithContent(name string, content *godog.DocString) error {
	path := ".mlcm/context-fragments/" + name + ".yaml"
	return TestEnv.WriteHomeFile(path, content.Content)
}

func aPromptInProjectWithContent(name string, content *godog.DocString) error {
	path := ".mlcm/prompts/" + name + ".yaml"
	return TestEnv.WriteFile(path, content.Content)
}

func aPromptInHomeWithContent(name string, content *godog.DocString) error {
	path := ".mlcm/prompts/" + name + ".yaml"
	return TestEnv.WriteHomeFile(path, content.Content)
}

func aConfigFileWith(content *godog.DocString) error {
	configContent := content.Content
	// Replace mock LM path placeholder if mock LM is configured
	if MockLM != nil {
		configContent = strings.ReplaceAll(configContent, "{{MOCK_LM_PATH}}", MockLM.BinaryPath)
	}
	return TestEnv.WriteFile(".mlcm/config.yaml", configContent)
}

func aHomeConfigFileWith(content *godog.DocString) error {
	return TestEnv.WriteHomeFile(".mlcm/config.yaml", content.Content)
}
