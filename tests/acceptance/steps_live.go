//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

// realHomeDir is the user's actual home, captured in TestMain before any
// scenario overrides HOME. Used to locate ~/.claude and ~/.gemini for the
// subscription-auth path.
var realHomeDir string

// liveAgent describes a real backend the @live suite can drive. The same
// distillation scenarios run against each entry via a Scenario Outline, so the
// behavioral assertions stay backend-agnostic while auth and config differ.
type liveAgent struct {
	// apiKeyEnvs are the env vars whose presence enables the unattended API-key
	// path. They flow to the CLI through the inherited subprocess env, so nothing
	// is copied for this path.
	apiKeyEnvs []string
	// credDir is the per-agent credential directory under HOME (e.g. ".claude").
	credDir string
	// config is the ctxloom config.yaml that points primary+fast at this backend.
	config string
	// copyCreds copies just the auth files from the real HOME into the isolated
	// one for the subscription path.
	copyCreds func(realHome, fakeHome string)
}

// liveAgents maps the lowercased scenario token ("claude", "gemini") to its
// backend wiring. Each uses a cheap model so the paid calls stay inexpensive.
var liveAgents = map[string]liveAgent{
	"claude": {
		apiKeyEnvs: []string{"ANTHROPIC_API_KEY"},
		credDir:    ".claude",
		config: `version: 3
llm:
  configs:
    claude:
      type: claude-code
      model: claude-haiku-4-5-20251001
  defaults:
    primary: claude
    fast: claude
profiles:
  defaults: []
`,
		copyCreds: copyClaudeCredentials,
	},
	"gemini": {
		apiKeyEnvs: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		credDir:    ".gemini",
		config: `version: 3
llm:
  configs:
    gemini:
      type: gemini
      model: gemini-2.5-flash
  defaults:
    primary: gemini
    fast: gemini
profiles:
  defaults: []
`,
		copyCreds: copyGeminiCredentials,
	},
}

// envSet reports whether any of the named env vars is non-empty.
func envSet(names []string) bool {
	for _, n := range names {
		if os.Getenv(n) != "" {
			return true
		}
	}
	return false
}

// liveAgentAvailable reports whether the named real agent can be reached. The
// API-key path (which flows through the subprocess env) triggers automatically.
// The subscription path copies local credentials and makes paid calls, so it is
// gated behind an explicit opt-in rather than the mere presence of the credential
// directory on a developer machine.
func liveAgentAvailable(a liveAgent) bool {
	if envSet(a.apiKeyEnvs) {
		return true
	}
	if os.Getenv("CTXLOOM_ACCEPTANCE_LIVE") == "1" && realHomeDir != "" {
		if _, err := os.Stat(filepath.Join(realHomeDir, a.credDir)); err == nil {
			return true
		}
	}
	return false
}

func registerLiveSteps(ctx *godog.ScenarioContext) {
	// The gate-and-skip: every @live scenario starts here, so the hermetic run
	// (which excludes @live) never touches credentials, and a credential-less
	// live run skips rather than fails. The agent token comes from the Scenario
	// Outline Examples table ("Claude", "Gemini").
	ctx.Step(`^a real (\S+) agent is available$`, func(c context.Context, name string) error {
		a, ok := liveAgents[strings.ToLower(name)]
		if !ok {
			return fmt.Errorf("unknown live agent %q", name)
		}
		if !liveAgentAvailable(a) {
			return godog.ErrSkip
		}
		w := worldFrom(c)
		if err := w.env.InitGitRepo(); err != nil {
			return err
		}
		if err := w.env.WriteFile(".ctxloom/config.yaml", a.config); err != nil {
			return err
		}
		// Subscription path: copy just the credential files into the isolated HOME
		// so the backend authenticates without dragging the whole credential tree.
		// The API-key path needs nothing — it flows through the env.
		if !envSet(a.apiKeyEnvs) && realHomeDir != "" {
			a.copyCreds(realHomeDir, w.env.HomeDir)
		}
		return nil
	})

	// A long, information-dense fragment gives distillation something real to
	// compress and concrete topics to preserve.
	ctx.Step(`^a bundle "([^"]*)" with a long fragment "([^"]*)"$`,
		func(c context.Context, bundle, fragment string) error {
			w := worldFrom(c)
			body := liveBundleYAML(fragment)
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
		rel := filepath.Join(".ctxloom", "cache", "bundles", bundle+".yaml")
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
// fragment and prompt for distillation.
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
	b.WriteString("prompts:\n")
	b.WriteString("  guidance:\n")
	b.WriteString("    description: live distillation prompt fixture\n")
	b.WriteString("    content: |\n")
	b.WriteString("      Operational guidance for reviewers:\n")
	for _, f := range facts {
		fmt.Fprintf(&b, "      - %s\n", f)
	}
	return b.String()
}

// copyClaudeCredentials copies just the auth-relevant files from the real
// ~/.claude into the isolated home, best effort — never the whole tree (which
// holds caches, history, and backups).
func copyClaudeCredentials(realHome, fakeHome string) {
	srcDir := filepath.Join(realHome, ".claude")
	dstDir := filepath.Join(fakeHome, ".claude")
	_ = os.MkdirAll(dstDir, 0o755)
	for _, name := range []string{".credentials.json", "settings.json", "config.json"} {
		data, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			continue
		}
		_ = os.WriteFile(filepath.Join(dstDir, name), data, 0o600)
	}
	// ~/.claude.json holds onboarding state; copying it stops the CLI from
	// dropping into an interactive first-run flow under the isolated HOME.
	if data, err := os.ReadFile(filepath.Join(realHome, ".claude.json")); err == nil {
		_ = os.WriteFile(filepath.Join(fakeHome, ".claude.json"), data, 0o600)
	}
}

// copyGeminiCredentials copies just the auth-relevant files from the real
// ~/.gemini into the isolated home, best effort — never the whole tree (which
// holds the tmp transcript cache and command exports). oauth_creds.json and
// google_accounts.json carry the subscription login; installation_id and
// settings.json keep the CLI out of its interactive first-run flow.
func copyGeminiCredentials(realHome, fakeHome string) {
	srcDir := filepath.Join(realHome, ".gemini")
	dstDir := filepath.Join(fakeHome, ".gemini")
	_ = os.MkdirAll(dstDir, 0o755)
	for _, name := range []string{"oauth_creds.json", "google_accounts.json", "settings.json", "installation_id", "user_id"} {
		data, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			continue
		}
		_ = os.WriteFile(filepath.Join(dstDir, name), data, 0o600)
	}
}
