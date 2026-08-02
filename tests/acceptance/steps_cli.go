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
// splits the remainder into argv via shellSplit, so a feature file can
// express a quoted argument, an argument containing spaces, or a flag whose
// value is a sentence — exactly the argv shapes where the live
// variadic-flag defect class lives. Before this, bare
// strings.Fields whitespace-splitting made those shapes structurally
// inexpressible.
func ctxloomArgs(cmdline string) ([]string, error) {
	fields, err := shellSplit(cmdline)
	if err != nil {
		return nil, fmt.Errorf("parse command line %q: %w", cmdline, err)
	}
	if len(fields) == 0 || fields[0] != "ctxloom" {
		return nil, fmt.Errorf("command must start with \"ctxloom\": %q", cmdline)
	}
	return fields[1:], nil
}

// shellSplit tokenizes cmdline the way a POSIX shell would for the subset
// this suite needs: unquoted whitespace-separated words, single-quoted
// literals (no escapes inside, matching sh), and double-quoted strings
// (backslash escapes \" and \\ only).
func shellSplit(cmdline string) ([]string, error) {
	var fields []string
	var cur strings.Builder
	haveCur := false
	runes := []rune(cmdline)
	for i := 0; i < len(runes); {
		c := runes[i]
		switch {
		case c == ' ' || c == '\t':
			if haveCur {
				fields = append(fields, cur.String())
				cur.Reset()
				haveCur = false
			}
			i++
		case c == '\'':
			haveCur = true
			i++
			start := i
			for i < len(runes) && runes[i] != '\'' {
				i++
			}
			if i >= len(runes) {
				return nil, fmt.Errorf("unterminated single quote")
			}
			cur.WriteString(string(runes[start:i]))
			i++
		case c == '"':
			haveCur = true
			i++
			for i < len(runes) && runes[i] != '"' {
				if runes[i] == '\\' && i+1 < len(runes) && (runes[i+1] == '"' || runes[i+1] == '\\') {
					cur.WriteRune(runes[i+1])
					i += 2
					continue
				}
				cur.WriteRune(runes[i])
				i++
			}
			if i >= len(runes) {
				return nil, fmt.Errorf("unterminated double quote")
			}
			i++
		default:
			haveCur = true
			cur.WriteRune(c)
			i++
		}
	}
	if haveCur {
		fields = append(fields, cur.String())
	}
	return fields, nil
}
