package steps

import (
	"github.com/cucumber/godog"
)

// RegisterCommandSteps registers steps for running commands.
func RegisterCommandSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^I run scm "([^"]*)"$`, iRunSCM)
	ctx.Step(`^I run scm with args:$`, iRunSCMWithArgs)
}

func iRunSCM(args string) error {
	// Split args by space (simple splitting, doesn't handle quotes)
	parts := splitArgs(args)

	// Run the command but don't return the error - let assertion steps check results
	_ = TestEnv.RunSCM(parts...)
	return nil
}

func iRunSCMWithArgs(args *godog.DocString) error {
	parts := splitArgs(args.Content)
	// Run the command but don't return the error - let assertion steps check results
	_ = TestEnv.RunSCM(parts...)
	return nil
}

// splitArgs splits a command string into arguments.
// Simple implementation - doesn't handle quoted strings with spaces.
func splitArgs(s string) []string {
	var parts []string
	for _, p := range splitWhitespace(s) {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

func splitWhitespace(s string) []string {
	var parts []string
	var current string
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(r)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}
