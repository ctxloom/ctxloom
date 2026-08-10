//go:build acceptance

package acceptance

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"
)

// Review steps (trust-simplify slice 2). A seeded file:// remote's canonical
// bundle refs embed the scenario's temp path, so a feature file cannot spell
// the full `ctxloom bundle trust <ref>` argument statically; these steps compose the
// ref from the seeded remote's bare-repo path and drive the scriptable
// plumbing under the review porcelain. Exit status flows through the shared
// runner state so "the command succeeds" keeps working.
func registerReviewSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^I accept the pending item "([^"]*)" from remote "([^"]*)"$`, func(c context.Context, item, name string) error {
		return runTrustPlumbing(c, "trust", item, name)
	})

	ctx.Step(`^I reject the pending item "([^"]*)" from remote "([^"]*)"$`, func(c context.Context, item, name string) error {
		return runTrustPlumbing(c, "untrust", item, name)
	})
}

// runTrustPlumbing runs `ctxloom bundle <verb> file://<bare>@bundles/<item>`
// for a seeded remote. item is "<bundle>#<kind>/<name>", e.g. "demo#fragments/x".
//
// This used to discard the run's error and return nil
// unconditionally, deferring to a separate "the command succeeds" step every
// current scenario happens to pair it with -- but neither of this function's
// two step texts ("I accept/reject the pending item…") reads as an action
// whose result a later step will check, so a feature author could omit that
// exit assertion and silently accept a failed trust write. Every other
// fixture step in the suite fails loudly (runOK); this now does too.
func runTrustPlumbing(c context.Context, verb, item, remoteName string) error {
	w := worldFrom(c)
	bare := w.remoteBare[remoteName]
	if bare == "" {
		return fmt.Errorf("remote %q was not seeded", remoteName)
	}
	ref := fmt.Sprintf("file://%s@bundles/%s", bare, item)
	return runOK(w, "bundle", verb, ref)
}
