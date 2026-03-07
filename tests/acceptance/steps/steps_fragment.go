package steps

import (
	"fmt"
	"strings"

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"
)

// testBundle represents a bundle for testing purposes.
type testBundle struct {
	Version   string                  `yaml:"version"`
	Fragments map[string]testFragment `yaml:"fragments,omitempty"`
	Prompts   map[string]testPrompt   `yaml:"prompts,omitempty"`
}

type testFragment struct {
	Tags      []string `yaml:"tags,omitempty"`
	Content   string   `yaml:"content"`
	NoDistill bool     `yaml:"no_distill,omitempty"`
}

type testPrompt struct {
	Content string `yaml:"content"`
}

// inputFragment represents the YAML structure passed from feature files.
type inputFragment struct {
	Tags      []string `yaml:"tags,omitempty"`
	Content   string   `yaml:"content"`
	NoDistill bool     `yaml:"no_distill,omitempty"`
}

// inputPrompt represents the YAML structure passed from feature files.
type inputPrompt struct {
	Content string `yaml:"content"`
}

// RegisterFragmentSteps registers steps for fragment operations.
func RegisterFragmentSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^a fragment "([^"]*)" with content:$`, aFragmentWithContent)
	ctx.Step(`^a fragment "([^"]*)" in the project with content:$`, aFragmentInProjectWithContent)
	ctx.Step(`^a fragment "([^"]*)" in home with content:$`, aFragmentInHomeWithContent)
	ctx.Step(`^a prompt "([^"]*)" in the project with content:$`, aPromptInProjectWithContent)
	ctx.Step(`^a prompt "([^"]*)" in home with content:$`, aPromptInHomeWithContent)
	ctx.Step(`^a config file with:$`, aConfigFileWith)
	ctx.Step(`^a home config file with:$`, aHomeConfigFileWith)
	ctx.Step(`^a profile file "([^"]*)" with:$`, aProfileFileWith)
}

func aFragmentWithContent(name string, content *godog.DocString) error {
	return aFragmentInProjectWithContent(name, content)
}

func aFragmentInProjectWithContent(name string, content *godog.DocString) error {
	return addFragmentToBundle(".scm/bundles/local.yaml", name, content.Content, false)
}

func aFragmentInHomeWithContent(name string, content *godog.DocString) error {
	return addFragmentToBundle(".scm/bundles/local.yaml", name, content.Content, true)
}

func aPromptInProjectWithContent(name string, content *godog.DocString) error {
	return addPromptToBundle(".scm/bundles/local.yaml", name, content.Content, false)
}

func aPromptInHomeWithContent(name string, content *godog.DocString) error {
	return addPromptToBundle(".scm/bundles/local.yaml", name, content.Content, true)
}

// addFragmentToBundle adds a fragment to a bundle file, creating or updating as needed.
// The rawContent is expected to be YAML with tags and content fields.
func addFragmentToBundle(bundlePath, name, rawContent string, isHome bool) error {
	// Parse the input YAML to extract tags and content
	var input inputFragment
	if err := yaml.Unmarshal([]byte(rawContent), &input); err != nil {
		return fmt.Errorf("failed to parse fragment YAML: %w", err)
	}

	var existing string
	var err error
	if isHome {
		existing, err = TestEnv.ReadHomeFile(bundlePath)
	} else {
		existing, err = TestEnv.ReadFile(bundlePath)
	}

	var bundle testBundle
	if err == nil && existing != "" {
		if err := yaml.Unmarshal([]byte(existing), &bundle); err != nil {
			return fmt.Errorf("failed to parse existing bundle: %w", err)
		}
	}

	if bundle.Version == "" {
		bundle.Version = "1.0"
	}
	if bundle.Fragments == nil {
		bundle.Fragments = make(map[string]testFragment)
	}

	bundle.Fragments[name] = testFragment(input)

	data, err := yaml.Marshal(bundle)
	if err != nil {
		return fmt.Errorf("failed to marshal bundle: %w", err)
	}

	if isHome {
		return TestEnv.WriteHomeFile(bundlePath, string(data))
	}
	return TestEnv.WriteFile(bundlePath, string(data))
}

// addPromptToBundle adds a prompt to a bundle file, creating or updating as needed.
// The rawContent is expected to be YAML with a content field.
func addPromptToBundle(bundlePath, name, rawContent string, isHome bool) error {
	// Parse the input YAML to extract content
	var input inputPrompt
	if err := yaml.Unmarshal([]byte(rawContent), &input); err != nil {
		return fmt.Errorf("failed to parse prompt YAML: %w", err)
	}

	var existing string
	var err error
	if isHome {
		existing, err = TestEnv.ReadHomeFile(bundlePath)
	} else {
		existing, err = TestEnv.ReadFile(bundlePath)
	}

	var bundle testBundle
	if err == nil && existing != "" {
		if err := yaml.Unmarshal([]byte(existing), &bundle); err != nil {
			return fmt.Errorf("failed to parse existing bundle: %w", err)
		}
	}

	if bundle.Version == "" {
		bundle.Version = "1.0"
	}
	if bundle.Prompts == nil {
		bundle.Prompts = make(map[string]testPrompt)
	}

	bundle.Prompts[name] = testPrompt(input)

	data, err := yaml.Marshal(bundle)
	if err != nil {
		return fmt.Errorf("failed to marshal bundle: %w", err)
	}

	if isHome {
		return TestEnv.WriteHomeFile(bundlePath, string(data))
	}
	return TestEnv.WriteFile(bundlePath, string(data))
}

func aConfigFileWith(content *godog.DocString) error {
	configContent := content.Content
	// Replace mock LM path placeholder if mock LM is configured
	if MockLM != nil {
		configContent = strings.ReplaceAll(configContent, "{{MOCK_LM_PATH}}", MockLM.BinaryPath)
	}
	if err := TestEnv.WriteFile(".scm/config.yaml", configContent); err != nil {
		return err
	}
	// If mock LM is configured, re-apply mock settings while preserving profiles
	if MockLM != nil {
		if err := MockLM.WriteConfig(); err != nil {
			return err
		}
		// Debug: show the final config
		// fmt.Println("DEBUG: Config written to", filepath.Join(MockLM.ProjectDir, ".scm", "config.yaml"))
	}
	return nil
}

func aHomeConfigFileWith(content *godog.DocString) error {
	return TestEnv.WriteHomeFile(".scm/config.yaml", content.Content)
}

func aProfileFileWith(name string, content *godog.DocString) error {
	path := ".scm/profiles/" + name + ".yaml"
	return TestEnv.WriteFile(path, content.Content)
}
