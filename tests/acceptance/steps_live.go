//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

// The live-engine registry itself (liveAgent, liveAgents, liveAgentOrder,
// engineAvailable/probeEngine, the availability report, and the
// CTXLOOM_LIVE_REQUIRE floor) lives in live_engine_registry.go, deliberately
// untagged so `just test` gates on its decision logic directly. This file
// only wires that registry into godog steps.

func registerLiveSteps(ctx *godog.ScenarioContext) {
	// The gate-and-skip: every @live scenario starts here, so the hermetic run
	// (which excludes @live) never touches credentials, and an unavailable
	// engine skips (rather than fails) UNLESS CTXLOOM_LIVE_REQUIRE names it —
	// see checkRequiredEngines, enforced once up front in TestAcceptance. The
	// agent token comes from the Scenario Outline Examples table ("Claude",
	// "Antigravity", "Kiro").
	ctx.Step(`^a real (\S+) agent is available$`, func(c context.Context, name string) error {
		key := strings.ToLower(name)
		a, ok := liveAgents[key]
		if !ok {
			return fmt.Errorf("unknown live agent %q", name)
		}
		status := probeEngine(key, a, realHomeDir, resolveOptIn())
		w := worldFrom(c)
		// Set-and-consume evidence (steps_doc_capture.go): this engine's actual
		// resolved availability, in the SAME one-line shape as the suite-wide
		// report, so the published J000400 living-docs page carries the real,
		// per-row honesty rather than a bare pass with no reason attached — a
		// reader cannot mistake a skip for a fourth proven row.
		w.docStepMaterialized = formatLiveEngineReport([]engineStatus{status})
		if !status.available {
			return godog.ErrSkip
		}
		if err := w.env.InitGitRepo(); err != nil {
			return err
		}
		if err := w.env.WriteFile(".ctxloom/config.yaml", a.config); err != nil {
			return err
		}
		// Subscription path: MAP the engine at its real credential directory
		// (erased-collar) so a provider-side token rotation lands on the host
		// instead of dying with this scenario's temp HOME. The API-key path
		// needs nothing — it flows through the env.
		return seedLiveCredentials(key, a, realHomeDir, w.env.HomeDir, w.env.SetChildEnv)
	})

	// A long, information-dense fragment gives distillation something real to
	// compress and concrete topics to preserve.
	ctx.Step(`^a bundle "([^"]*)" with a long fragment "([^"]*)"$`,
		func(c context.Context, bundle, fragment string) error {
			w := worldFrom(c)
			body := liveBundleYAML(fragment)
			rel := filepath.Join(".ctxloom", "content", "bundles", bundle+".yaml")
			return w.env.WriteFile(rel, body)
		})

	// A fragment with exact, caller-chosen content — the payload-delivery
	// counterpart to the fixed-topic fragment above. Used to prove a specific
	// string ctxloom injects into the assembled context is the SAME string a
	// real agent reports back, end to end.
	ctx.Step(`^a bundle "([^"]*)" with a fragment "([^"]*)" containing "([^"]*)"$`,
		func(c context.Context, bundle, fragment, content string) error {
			w := worldFrom(c)
			body := fmt.Sprintf("version: 1.0.0\ndescription: live payload fixture\nfragments:\n  %s:\n    tags: [live]\n    content: |\n      %s\n",
				fragment, content)
			rel := filepath.Join(".ctxloom", "cache", "bundles", bundle+".yaml")
			return w.env.WriteFile(rel, body)
		})

	ctx.Step(`^the distilled fragment "([^"]*)" in bundle "([^"]*)" is a real compression$`,
		func(c context.Context, fragment, bundle string) error {
			content, distilled, err := readBundleFragment(worldFrom(c), bundle, fragment)
			if err != nil {
				return err
			}
			if strings.TrimSpace(distilled) == "" {
				return fmt.Errorf("fragment %q was not distilled (empty distilled field)", fragment)
			}
			// Passthrough detection: a real backend must transform, not copy.
			if strings.TrimSpace(distilled) == strings.TrimSpace(content) {
				return fmt.Errorf("distilled content is identical to the original (passthrough — backend did not run)")
			}
			// Compression band: shorter, but not a near-empty stub.
			ratio := float64(len(distilled)) / float64(len(content))
			if ratio >= 0.95 {
				return fmt.Errorf("no real compression: distilled is %.0f%% of original", ratio*100)
			}
			if ratio <= 0.05 {
				return fmt.Errorf("over-compression: distilled is only %.0f%% of original (likely a stub)", ratio*100)
			}
			return nil
		})

	// Fidelity, loosely: a faithful summary of this technical content keeps most
	// of the domain vocabulary even while dropping specific numbers and rewording
	// freely. Requiring a threshold of distinctive terms (not any single one)
	// tolerates phrasing but catches semantic collapse, an empty result, or an
	// off-topic stub.
	ctx.Step(`^the distilled fragment "([^"]*)" in bundle "([^"]*)" preserves the domain$`,
		func(c context.Context, fragment, bundle string) error {
			_, distilled, err := readBundleFragment(worldFrom(c), bundle, fragment)
			if err != nil {
				return err
			}
			lower := strings.ToLower(distilled)
			kept := 0
			var missing []string
			for _, term := range liveDomainTerms {
				if strings.Contains(lower, term) {
					kept++
				} else {
					missing = append(missing, term)
				}
			}
			const need = 4
			if kept < need {
				return fmt.Errorf("distilled content kept only %d/%d domain terms (need >=%d); missing %v; distilled:\n%s",
					kept, len(liveDomainTerms), need, missing, distilled)
			}
			return nil
		})

	// A looser check for prompt/bundle distill: the manifest now carries a
	// distilled rendering. The strong compression/fidelity invariants are
	// asserted on the fragment above.
	ctx.Step(`^the bundle "([^"]*)" records a distillation$`, func(c context.Context, bundle string) error {
		w := worldFrom(c)
		rel := filepath.Join(".ctxloom", "content", "bundles", bundle+".yaml")
		body, err := w.env.ReadFile(rel)
		if err != nil {
			return err
		}
		if !strings.Contains(body, "distilled:") {
			return fmt.Errorf("bundle %q records no distillation; manifest:\n%s", bundle, body)
		}
		return nil
	})
}

// liveDomainTerms are distinctive topics from the fixture content. A faithful
// compression keeps a threshold of them; an empty or off-topic result does not.
var liveDomainTerms = []string{"tenant", "cache", "token", "webhook", "retry", "migration", "rate", "vault"}

// liveDistinctFacts are varied, information-dense lines (not repetitive filler)
// so a faithful compression lands in a realistic band rather than collapsing
// redundancy to near-zero. Each carries a distinct topic from liveDomainTerms.
func liveDistinctFacts() []string {
	return []string{
		"The ingestion service accepts JSON and Protobuf, rejecting any payload above four megabytes.",
		"Authentication uses short-lived bearer tokens minted by the identity broker, never long-lived API keys.",
		"The canonical retry limit is seven; a request that fails that many times is sent to the dead-letter queue.",
		"Each tenant has an isolated schema, and cross-tenant joins are forbidden at the query planner.",
		"Background jobs run on a separate worker pool sized to twice the number of CPU cores minus one.",
		"Idempotency keys are stored for twenty-four hours so retried writes never duplicate a record.",
		"The cache is write-through for reference data and write-behind for high-volume event counters.",
		"Schema migrations are forward-only; rollbacks happen by deploying the previous release, not reversing DDL.",
		"Outbound webhooks are signed with HMAC-SHA256 and include a timestamp to defeat replay attacks.",
		"Rate limiting is per-tenant and per-endpoint, enforced with a token bucket refilled every second.",
		"Connection pools cap at five hundred sockets per node to keep the database from thrashing.",
		"Tracing context propagates through every hop using W3C traceparent headers for end-to-end spans.",
		"Secrets are fetched at boot from the vault and held only in memory, never written to disk or logs.",
		"The scheduler favors the least-loaded region but pins stateful workloads to their data's home region.",
		"Soft deletes mark rows with a tombstone; a nightly compaction purges anything older than thirty days.",
		"Feature flags are evaluated server-side so a rollback never requires shipping new client code.",
		"The API gateway strips internal headers before responses leave the trust boundary.",
		"Health checks distinguish liveness from readiness so a warming node is not sent traffic prematurely.",
	}
}

// liveBundleYAML builds a bundle manifest with a substantial, information-dense
// fragment and command for distillation.
func liveBundleYAML(fragment string) string {
	facts := liveDistinctFacts()
	var b strings.Builder
	b.WriteString("version: 1.0.0\n")
	b.WriteString("description: live distillation fixture\n")
	b.WriteString("fragments:\n")
	b.WriteString("  " + fragment + ":\n")
	b.WriteString("    tags: [live]\n")
	b.WriteString("    content: |\n")
	for _, f := range facts {
		fmt.Fprintf(&b, "      %s\n", f)
	}
	b.WriteString("commands:\n")
	b.WriteString("  guidance:\n")
	b.WriteString("    description: live distillation prompt fixture\n")
	b.WriteString("    content: |\n")
	b.WriteString("      Operational guidance for reviewers:\n")
	for _, f := range facts {
		fmt.Fprintf(&b, "      - %s\n", f)
	}
	return b.String()
}
