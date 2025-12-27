package steps

import (
	"github.com/cucumber/godog"
)

// RegisterEnvironmentSteps registers steps for environment setup.
func RegisterEnvironmentSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^a new project directory$`, aNewProjectDirectory)
	ctx.Step(`^a git repository$`, aGitRepository)
	ctx.Step(`^a project with scm initialized$`, aProjectWithMlcmInitialized)
	ctx.Step(`^a home directory with scm config$`, aHomeDirectoryWithMlcmConfig)
}

func aNewProjectDirectory() error {
	// Project directory is already created by NewTestEnvironment
	return nil
}

func aGitRepository() error {
	return TestEnv.InitGitRepo()
}

func aProjectWithMlcmInitialized() error {
	if err := TestEnv.InitGitRepo(); err != nil {
		return err
	}
	return TestEnv.CreateProjectSCM()
}

func aHomeDirectoryWithMlcmConfig() error {
	// Home .scm is already created by NewTestEnvironment
	// Create a minimal config
	config := `lm:
  default_plugin: claude-code
defaults:
  use_distilled: false
personas: {}
`
	return TestEnv.WriteHomeFile(".scm/config.yaml", config)
}
