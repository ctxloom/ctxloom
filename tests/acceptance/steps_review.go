//go:build acceptance

package acceptance

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"
)

// Review steps (trust-simplify slice 2). A seeded file:// remote's canonical
// bundle refs embed the scenario's temp path, so a feature file cannot spell
// the full `ctxloom trust <ref>` argument statically; these steps compose the
// ref from the seeded remote's bare-repo path and drive the scriptable
// plumbing under the review porcelain. Exit status flows through the shared
// runner state so "the command succeeds" keeps working.
func registerReviewSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^I accept the pending item "([^"]*)" from remote "([^"]*)"$`, func(c context.Context, item, name string) error {
		return runTrustPlumbing(c, "trust", item, name)
	})

	ctx.Step(`^I reject the pending item "([^"]*)" from remote "([^"]*)"$`, func(c context.Context, item, name string) error {
		return runTrustPlumbing(c, "blacklist", item, name)
	})
}

// runTrustPlumbing runs `ctxloom <verb> file://<bare>@bundles/<item>` for a
// seeded remote. item is "<bundle>#<kind>/<name>", e.g. "demo#fragments/x".
func runTrustPlumbing(c context.Context, verb, item, remoteName string) error {
	w := worldFrom(c)
	bare := w.remoteBare[remoteName]
	if bare == "" {
		return fmt.Errorf("remote %q was not seeded", remoteName)
	}
	ref := fmt.Sprintf("file://%s@bundles/%s", bare, item)
	_ = w.env.Run(verb, ref)
	return nil // exit status is asserted by a dedicated step
}
