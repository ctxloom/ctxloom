//go:build acceptance

package acceptance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/testsupport/containercell"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// The tags a scenario (or its Rule) carries to declare a container image it
// needs. A scenario that tests the build itself declares none: it must see
// the build happen, not find it already done.
const (
	imageMockAgent = "@image-mock-agent"
	imageCell      = "@image-cell"
)

// mockAgentImageBuildBound bounds building the mock agent image before the
// scenarios run. Derivation: 5x a cold `ctxloom container build mock` (its
// default --pull --no-cache) measured at 13.8s with the base image already
// local; a warm --keep-cache build, which is what the suite runs, took 1.3s.
// NOT covered: pulling the base image onto a machine that has never had it.
// If this fires there, measure that pull and fold it in rather than raising
// the bound by feel.
const mockAgentImageBuildBound = 70 * time.Second

// cellImageBuildBound bounds building the container-cell image before the
// scenarios run. The cell image is a FROM-scratch copy of a freshly compiled
// static ctxloom, so the compile is its cost. Derivation: 5x a cold-GOCACHE
// compile of ctxloom, measured at 39.9s. Re-measure the same way if it fires
// on a build that was not hung.
const cellImageBuildBound = 200 * time.Second

// suiteImage is an image some scenarios need, built ONCE before any scenario
// runs instead of inside whichever scenario reaches it first.
//
// Why before: a build inside a scenario makes that scenario's command time
// "the command, plus a cold build when the image is missing", and no single
// bound fits that — loose enough for the cold build is too loose to catch a
// hang, tight enough to catch a hang fails the cold build. With the build
// split out, the build gets a bound measured from build times and every
// scenario command gets testenv.CommandBound.
type suiteImage struct {
	tag   string
	what  string // names the image in every message
	bound time.Duration
	build func(ctx context.Context, rt containercell.Runtime, bound time.Duration) error
}

var suiteImages = []suiteImage{
	{tag: imageMockAgent, what: "the mock agent image", bound: mockAgentImageBuildBound, build: buildMockAgentImage},
	{tag: imageCell, what: "the container-cell image", bound: cellImageBuildBound, build: buildCellImage},
}

// suiteImageState is what preparing one image decided, replayed to every
// scenario that declared it.
type suiteImageState struct {
	decision dockergate.Decision
	msg      string
}

// preparedSuiteImages is written by prepareSuiteImages before the suite runs
// and only read once scenarios start.
var preparedSuiteImages = map[string]suiteImageState{}

// selectedScenarioTags returns every tag carried by a scenario the suite will
// run. The selection is godog's own — the suite's paths filtered by its tag
// expression — so it is exactly the set Run executes.
func selectedScenarioTags(suite godog.TestSuite) (map[string]bool, error) {
	features, err := suite.RetrieveFeatures()
	if err != nil {
		return nil, fmt.Errorf("list the selected scenarios: %w", err)
	}
	tags := map[string]bool{}
	for _, f := range features {
		for _, p := range f.Pickles {
			for _, t := range p.Tags {
				tags[t.Name] = true
			}
		}
	}
	return tags, nil
}

// prepareSuiteImages builds each image some selected scenario declared, under
// that image's bound, and records the outcome for requireSuiteImage. A build
// that fails or outlives its bound fails the scenarios that need the image —
// with the reason — rather than the whole run.
func prepareSuiteImages(ctx context.Context, declared map[string]bool) {
	for _, img := range suiteImages {
		if !declared[img.tag] {
			continue
		}
		rt, decision, msg := containercell.Select(ctx, img.what)
		if decision == dockergate.Proceed {
			start := time.Now()
			err := img.build(ctx, rt, img.bound)
			verdict := "built"
			if err != nil {
				decision, verdict = dockergate.Fail, "FAILED"
				msg = fmt.Sprintf("building %s before the scenarios ran failed: %v", img.what, err)
			}
			// Printed on every run: these lines are the build-time evidence a
			// re-measure of the bound starts from.
			fmt.Printf("suite image %s: %s on %s in %s (bound %s)\n", img.tag, verdict, rt.Name, time.Since(start).Round(100*time.Millisecond), img.bound)
		}
		preparedSuiteImages[img.tag] = suiteImageState{decision: decision, msg: msg}
	}
}

// requireSuiteImage is the check at the point of use: the step that needs an
// image calls it, and it fails a scenario that did not declare the image —
// which would otherwise get no image, or build one inside its own run — so a
// declaration cannot drift from the steps that rely on it. A declared image
// replays its preparation outcome through gateContainerRow.
func requireSuiteImage(w *World, tag, row string) error {
	if !scenarioHasTag(w.scenario, tag) {
		return fmt.Errorf("%s needs %s, which is built before the scenarios run only for scenarios tagged %s — tag this scenario (or its Rule) %s", row, tag, tag, tag)
	}
	st, ok := preparedSuiteImages[tag]
	if !ok {
		return fmt.Errorf("%s: the scenario is tagged %s but no image was prepared for it — suiteImages has no entry for that tag, or prepareSuiteImages did not run", row, tag)
	}
	return gateContainerRow(w, row, st.decision, st.msg)
}

func scenarioHasTag(sc *godog.Scenario, tag string) bool {
	if sc == nil {
		return false
	}
	for _, t := range sc.Tags {
		if t.Name == tag {
			return true
		}
	}
	return false
}

// buildCellImage builds the container-cell image. containercell memoizes the
// outcome per process, so the scenario's own Runtime.Run later finds it done.
func buildCellImage(ctx context.Context, rt containercell.Runtime, bound time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	err := rt.EnsureImage(ctx)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("did not finish within its %s bound: %w", bound, err)
	}
	return err
}
