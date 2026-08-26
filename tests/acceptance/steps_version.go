//go:build acceptance

// cli/version.feature: the one question a build must always be able to
// answer, now tabled by format. `ctxloom version`'s bare spelling used to be
// asserted as plain text unconditionally; off a terminal (which this harness
// always is) the derived default now resolves to JSON, so the bare row and
// the `--format json` row must report the SAME thing, and only an explicit
// `--format text` still gets prose. `ctxloom --version` is a distinct code
// path — cobra's own version templater, set once from rootCmd.Version — and
// never goes through cliemit.Resolve, so it is the fixed point every row
// cross-checks against rather than a fourth row of its own.
package acceptance

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
)

func registerVersionFormatSteps(ctx *godog.ScenarioContext) {
	// The claim is AGREEMENT, not a fixed value: the stamped version is
	// unknown to this test process. Each row runs `ctxloom <flags> version`,
	// decodes it in whichever format formatAskedFor resolves for those flags
	// (the same resolution every other tabled scenario uses), and checks the
	// decoded version both against versionStampRE (steps_cli.go) and against
	// the flag path, which a rendering bug in either surface cannot satisfy
	// by accident.
	ctx.Step(`^the version reported for "([^"]*)" agrees with "ctxloom --version"$`, func(c context.Context, flags string) error {
		w := worldFrom(c)
		cmdline := "ctxloom version"
		if flags != "" {
			cmdline = "ctxloom " + flags + " version"
		}
		if err := runCLI(c, cmdline, ""); err != nil {
			return err
		}
		format := formatAskedFor(w)
		var text string
		if format.Structured() {
			obj, err := lastOutputJSON(w)
			if err != nil {
				return fmt.Errorf("%q: %w", cmdline, err)
			}
			if got := fmt.Sprintf("%v", obj["name"]); got != "ctxloom" {
				return fmt.Errorf("%q: json name = %q, want %q", cmdline, got, "ctxloom")
			}
			text = fmt.Sprintf("%v", obj["version"])
		} else {
			text = strings.TrimSpace(w.env.LastStdout())
		}
		if !versionStampRE.MatchString(text) {
			return fmt.Errorf("%q reported %q, which is not a version string", cmdline, text)
		}

		if err := runCLI(c, "ctxloom --version", ""); err != nil {
			return err
		}
		flag := strings.TrimSpace(w.env.LastStdout())
		if want := "ctxloom version " + text; flag != want {
			return fmt.Errorf("`ctxloom --version` printed %q, want %q", flag, want)
		}
		return nil
	})
}
