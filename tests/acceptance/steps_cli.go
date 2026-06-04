//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/cucumber/godog"
)

func registerCLISteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^I run "([^"]*)"$`, func(c context.Context, cmdline string) error {
		return runCLI(c, cmdline, "")
	})

	ctx.Step(`^I run "([^"]*)" with input:$`, func(c context.Context, cmdline string, doc *godog.DocString) error {
		return runCLI(c, cmdline, doc.Content)
	})

	ctx.Step(`^the command succeeds$`, func(c context.Context) error {
		w := worldFrom(c)
		if code := w.env.LastExitCode(); code != 0 {
			return fmt.Errorf("expected exit 0, got %d; output:\n%s", code, w.env.LastOutput())
		}
		return nil
	})

	ctx.Step(`^the command fails$`, func(c context.Context) error {
		w := worldFrom(c)
		if code := w.env.LastExitCode(); code == 0 {
			return fmt.Errorf("expected non-zero exit, got 0; output:\n%s", w.env.LastOutput())
		}
		return nil
	})

	ctx.Step(`^the output contains "([^"]*)"$`, func(c context.Context, want string) error {
		w := worldFrom(c)
		if !strings.Contains(w.env.LastOutput(), want) {
			return fmt.Errorf("output does not contain %q; output:\n%s", want, w.env.LastOutput())
		}
		return nil
	})

	ctx.Step(`^the output does not contain "([^"]*)"$`, func(c context.Context, unwant string) error {
		w := worldFrom(c)
		if strings.Contains(w.env.LastOutput(), unwant) {
			return fmt.Errorf("output unexpectedly contains %q; output:\n%s", unwant, w.env.LastOutput())
		}
		return nil
	})

	ctx.Step(`^the output matches "([^"]*)"$`, func(c context.Context, pattern string) error {
		w := worldFrom(c)
		re, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("invalid regexp %q: %w", pattern, err)
		}
		if !re.MatchString(w.env.LastOutput()) {
			return fmt.Errorf("output does not match /%s/; output:\n%s", pattern, w.env.LastOutput())
		}
		return nil
	})
}

func runCLI(c context.Context, cmdline, stdin string) error {
	w := worldFrom(c)
	// {task} expands to the harp_id captured from the most recent task tool call,
	// so CLI task mutations can target a task created over MCP.
	cmdline = strings.ReplaceAll(cmdline, "{task}", w.lastTaskHarp)
	args, err := ctxloomArgs(cmdline)
	if err != nil {
		return err
	}
	if stdin == "" {
		_ = w.env.Run(args...)
	} else {
		_ = w.env.RunWithStdin(stdin, args...)
	}
	return nil // exit status is asserted by a dedicated step
}

// ctxloomArgs strips the leading "ctxloom " from a feature command line and
// splits the remainder into argv. Whitespace splitting is sufficient for the
// suite's commands; arguments with spaces are passed as trailing positional text
// (cobra rejoins them) or via the stdin/table forms.
func ctxloomArgs(cmdline string) ([]string, error) {
	fields := strings.Fields(cmdline)
	if len(fields) == 0 || fields[0] != "ctxloom" {
		return nil, fmt.Errorf("command must start with \"ctxloom\": %q", cmdline)
	}
	return fields[1:], nil
}
