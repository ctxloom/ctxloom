//go:build acceptance

// J8: "Guardrails — when the assistant does not listen" (j8_guardrails.feature).
//
// ctxloom's own promise stops at DELIVERY: the right fragments, signed,
// trusted, reach the assembled context. What a fragment can never do is
// ENFORCE — an LLM agent that reaches for the raw tool out of habit, or
// writes a fourth copy of a helper that already exists, has not violated any
// mechanism ctxloom itself runs. That gap is exactly what ctxloom's
// companions (ltk, reprise) exist to close, and this journey is where their
// loadout delivery finally gets acceptance coverage —
// internal/config/companions.go's DiscoverCompanions/ProbeCompanionLoadouts
// is thoroughly unit-tested (internal/config/companion_loadout_test.go) but
// had ZERO acceptance coverage before this file, in either direction (a
// companion's content reaching the assembled surface, or its absence
// degrading gracefully).
//
// SCOPE, verified empirically before writing a line (per the worktree
// brief): ltk and reprise ARE real binaries on this dev machine's PATH, but
// neither is installed in the acceptance suite's CI job
// (.github/workflows/ci.yml's "Acceptance Tests (hermetic)" step adds only
// the freshly-built ctxloom binary to PATH; ltk/reprise are best-effort,
// warn-and-continue additions to the container-image build recipes, never to
// this job) — so every scenario here uses testenv.InstallFakeCompanion,
// exactly like steps_j4_onboarding.go already does for reprise, never a real
// companion subprocess. ltk's fake loadout carries its ACTUAL committed
// fragment prose (cmd/ltk/loadout.yaml, read at test time — not a hand-typed
// duplicate that could silently drift from what ltk really ships), so the
// "cooperative redirect, not a sandbox" assertion below is checking ltk's
// real words, not an invented paraphrase. reprise has no real `loadout`
// implementation yet (companions.go's own comment: "its loadout is a
// separate-repo follow-up"), so its fixture here is a marker fragment
// standing in for that future content — the same simulated-loadout technique
// steps_j4_onboarding.go's own reprise fixture (j4RepriseMarker) already
// established.
//
// NOT WRITTEN, and why (see the worktree brief's "BE HONEST" section): a
// scenario proving ltk actually intercepts a live tool call (a real
// PreToolUse hook firing mid-session) or reprise actually rejecting a `git
// commit` at pre-commit (lefthook.yml:29). Neither is observable from
// outside ctxloom's own process — tests/acceptance drives a ctxloom
// subprocess over MCP stdio and reads files ctxloom wrote; it has no live
// coding-agent turn that fires a real tool-use hook (even @live's mock/
// real-agent harness never drives a project's own hooks), and a git
// pre-commit hook fires from `git commit` entirely outside ctxloom's
// process, gated on the real reprise binary CI does not install. Both
// mechanisms are already proven by their OWN companion's test suite (ltk:
// internal/ltk/rules/eval_test.go et al.; reprise: its own repo). ctxloom's
// job — and this journey's job — stops at proving the guidance and the hook
// WIRING actually reach the assistant's real configuration, and that what
// reaches it is honest about what the companion does, not stronger than it
// is.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

const (
	// j8Target is the materialize destination for Alice's scenarios (1 & 2) —
	// mirrors "Alice starts a session"'s own hardcoded "out" (steps_j1_setup.go).
	j8Target = "out"

	// j8ReuseMarker stands in for reprise's reuse-before-you-write guidance.
	// reprise does not implement the `loadout` protocol yet (companions.go:
	// "reprise is listed for completeness ... even though it does not
	// implement loadout yet") — this marker simulates the content it WOULD
	// contribute once it does, exactly like steps_j4_onboarding.go's
	// j4RepriseMarker fixture.
	j8ReuseMarker = "J8-REPRISE-REUSE-BEFORE-YOU-WRITE-MARKER"

	j8LtkPrincipal       = "ltk@example.com"
	j8RepriseCoPrincipal = "reprise@example.com"

	// j8LtkHeading and j8LtkRedirectClaim are exact substrings of ltk's REAL,
	// committed fragment (cmd/ltk/loadout.yaml's fragments.ltk.content) — not
	// paraphrased. The redirect claim is the load-bearing one: it is the
	// sentence that stops ctxloom's delivery from overclaiming ltk as a
	// sandbox or a security boundary.
	j8LtkHeading       = "# llm-tool-killer (ltk)"
	j8LtkRedirectClaim = "ltk is a cooperative redirect, not a sandbox"
)

// j8LtkLoadoutYAML reads ltk's REAL, committed loadout bundle
// (cmd/ltk/loadout.yaml — "the single source of truth for what ltk tells
// ctxloom about itself", per cmd/ltk/loadout.go's own doc comment) relative
// to THIS source file via runtime.Caller, so the fake companion's content is
// never a hand-typed duplicate that could silently drift from what the real
// ltk binary ships.
func j8LtkLoadoutYAML() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("resolve this source file's own path")
	}
	// tests/acceptance/steps_j8_guardrails.go -> repo root -> cmd/ltk/loadout.yaml
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "cmd", "ltk", "loadout.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read ltk's real loadout.yaml (%s): %w", path, err)
	}
	return string(data), nil
}

// j8InstallFakeCompanion signs bundleYAML with a fresh throwaway test signer,
// trusts it project-scoped (so it flows through the real trust gate exactly
// like a remote bundle — companions.go: "flows through the UNCHANGED trust
// gate"), and installs bin as a fake companion emitting that envelope for
// `loadout --format json` and versionJSON for `version --format json` —
// mirrors steps_j4_onboarding.go's reprise fixture, generalized to any bin.
func j8InstallFakeCompanion(w *World, bin, principal, bundleYAML, versionJSON string) error {
	signer, err := testenv.GenerateTestSigner()
	if err != nil {
		return fmt.Errorf("generate %s signer: %w", bin, err)
	}
	sig, err := signing.Sign([]byte(bundleYAML), signer.Signer, signing.NamespacePublish)
	if err != nil {
		return fmt.Errorf("sign %s loadout: %w", bin, err)
	}
	envelope, err := signing.EncodeLoadoutEnvelope([]byte(bundleYAML), sig, principal)
	if err != nil {
		return fmt.Errorf("encode %s loadout envelope: %w", bin, err)
	}
	if err := w.env.TrustSigner(signer, principal, true); err != nil {
		return fmt.Errorf("trust %s signer: %w", bin, err)
	}
	return w.env.InstallFakeCompanion(bin, versionJSON, string(envelope))
}

// j8ScrubFromPath removes any PATH entry that would satisfy a lookup for a
// binary literally named bin — shared by J4 (steps_j4_onboarding.go, "the
// reprise companion is not installed") and J8 (below, ltk), consolidated
// from two byte-identical copies once the worktree-era
// file-ownership boundary that justified the duplication no longer applied.
// Without it, a host that happens to have a real ltk/reprise on PATH (this
// repo dogfoods both — see .ltk/config.yaml, lefthook.yml) would make the
// "not installed" arm host-dependent instead of deterministic.
func j8ScrubFromPath(w *World, bin string) error {
	current := os.Getenv("PATH")
	var kept []string
	for _, dir := range filepath.SplitList(current) {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, bin)); err == nil {
			continue // this directory would satisfy lookPath(bin) — drop it
		}
		kept = append(kept, dir)
	}
	w.env.SetEnv("PATH", strings.Join(kept, string(os.PathListSeparator)))
	return nil
}

// j8LtkGuardedTools is the tool classes the scenario sentence "run ltk on
// every shell command AND FILE EDIT" actually promises, written out HERE
// rather than derived from ltk's loadout — and that is the point.
//
// This assertion used to check only that some matcher CONTAINED "Bash", so
// the file-edit half of its own sentence went unasserted: narrowing ltk's
// shipped matcher from "Bash|PowerShell|Edit|Write|MultiEdit|NotebookEdit"
// to just "Bash" — silently dropping the guardrail from every file-editing
// tool — left all three j8 scenarios green. Deriving the expectation from
// loadout.yaml would not have caught it either, since the mutation is IN
// loadout.yaml: both sides would have moved together. An independent,
// hand-written list is the only thing that fails when the shipped matcher
// shrinks.
//
// Ordered shell-first, then the file-editing tools, mirroring ltk's own
// installer (cmd/ltk/loadout.yaml's hooks.pre_tool comment). Membership is a
// SUPERSET check: ltk gaining a new tool class is fine, losing one is not.
var j8LtkGuardedTools = []string{"Bash", "PowerShell", "Edit", "Write", "MultiEdit", "NotebookEdit"}

// j8LtkShippedPreToolMatcher returns the pre_tool matcher from ltk's real
// committed loadout, so the generated settings can be held to carrying the
// companion's own value through verbatim. It is the SECOND half of the
// assertion, never the whole of it (see j8LtkGuardedTools): a matcher that
// merely agrees with a mutated loadout.yaml proves only that delivery is
// faithful, not that what was delivered still guards anything.
func j8LtkShippedPreToolMatcher() (string, error) {
	raw, err := j8LtkLoadoutYAML()
	if err != nil {
		return "", err
	}
	var doc struct {
		Hooks struct {
			PreTool []struct {
				Command string `yaml:"command"`
				Matcher string `yaml:"matcher"`
			} `yaml:"pre_tool"`
		} `yaml:"hooks"`
	}
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		return "", fmt.Errorf("parse ltk's loadout.yaml: %w", err)
	}
	for _, h := range doc.Hooks.PreTool {
		if h.Command == "ltk evaluate" {
			return h.Matcher, nil
		}
	}
	return "", fmt.Errorf("ltk's committed loadout.yaml declares no pre_tool hook running \"ltk evaluate\"")
}

// j8ClaudeSettings is the minimal shape this journey needs to parse out of
// the generated .claude/settings.json — matches internal/claude/claude.go's
// claudeCodeSettings/claudeCodeHookMatcher/claudeCodeHook exactly (only the
// fields this journey asserts on), so scenario 1's hook-wiring assertion
// PARSES the generated file rather than a bare substring/exists check.
type j8ClaudeSettings struct {
	Hooks struct {
		PreToolUse []struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"PreToolUse"`
	} `json:"hooks"`
}

func registerJ8Steps(ctx *godog.ScenarioContext) {
	// --- Scenarios 1 & 2 share this Given: Alice, both companions installed --

	ctx.Step(`^ltk and reprise are installed on Alice's machine$`, func(c context.Context) error {
		w := worldFrom(c)
		// Scrub any REAL ltk/reprise this dev machine happens to have on PATH
		// first (see j8ScrubFromPath's doc) so the fake always wins deterministically.
		if err := j8ScrubFromPath(w, "ltk"); err != nil {
			return err
		}
		if err := j8ScrubFromPath(w, "reprise"); err != nil {
			return err
		}
		ltkYAML, err := j8LtkLoadoutYAML()
		if err != nil {
			return err
		}
		if err := j8InstallFakeCompanion(w, "ltk", j8LtkPrincipal, ltkYAML, `{"name":"ltk","version":"v0.0.0-j8-fake"}`); err != nil {
			return err
		}
		repriseYAML := fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  reuse-discipline:\n    content: %q\n", j8ReuseMarker)
		return j8InstallFakeCompanion(w, "reprise", j8RepriseCoPrincipal, repriseYAML, `{"name":"reprise","version":"v0.0.0-j8-fake"}`)
	})

	// "Alice's project exists" (ensureProjectWithEngine "claude-code") is
	// already registered by steps_j7.go; "Alice starts a session" (materialize
	// "default" into "out") is already registered by steps_j1_setup.go — both
	// reused verbatim below, no new registration (godog rejects an ambiguous
	// second match for the same step text).

	ctx.Step(`^her assistant receives ltk's task-runner guidance$`, func(c context.Context) error {
		w := worldFrom(c)
		body, err := materializeDefault(w, j8Target)
		if err != nil {
			return err
		}
		w.docStepMaterialized = filepath.Join(j8Target, "CLAUDE.md") + ":\n" + j5Excerpt(body, j8LtkHeading, 4)
		if !strings.Contains(body, j8LtkHeading) {
			return fmt.Errorf("assembled context does not contain ltk's real fragment (looked for %q); content:\n%s", j8LtkHeading, body)
		}
		return nil
	})

	ctx.Step(`^her assistant receives reprise's reuse-before-you-write guidance$`, func(c context.Context) error {
		w := worldFrom(c)
		body, err := materializeDefault(w, j8Target)
		if err != nil {
			return err
		}
		w.docStepMaterialized = filepath.Join(j8Target, "CLAUDE.md") + ":\n" + j5Excerpt(body, j8ReuseMarker, 2)
		if !strings.Contains(body, j8ReuseMarker) {
			return fmt.Errorf("assembled context does not contain reprise's reuse-discipline marker %q; content:\n%s", j8ReuseMarker, body)
		}
		return nil
	})

	ctx.Step(`^her assistant's engine is configured to run ltk on every shell command and file edit$`, func(c context.Context) error {
		w := worldFrom(c)
		if _, err := materializeDefault(w, j8Target); err != nil {
			return err
		}
		rel := filepath.Join(j8Target, ".claude", "settings.json")
		raw, err := w.env.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read generated %s: %w", rel, err)
		}
		w.docStepMaterialized = rel + ":\n" + raw
		var settings j8ClaudeSettings
		if err := json.Unmarshal([]byte(raw), &settings); err != nil {
			return fmt.Errorf("parse generated %s: %w", rel, err)
		}
		matcher := ""
		found := false
		for _, entry := range settings.Hooks.PreToolUse {
			for _, h := range entry.Hooks {
				if h.Command == "ltk evaluate" {
					matcher, found = entry.Matcher, true
				}
			}
		}
		if !found {
			return fmt.Errorf("generated %s has no PreToolUse hook running \"ltk evaluate\" at all; content:\n%s", rel, raw)
		}
		covered := map[string]bool{}
		for _, tool := range strings.Split(matcher, "|") {
			covered[strings.TrimSpace(tool)] = true
		}
		var missing []string
		for _, tool := range j8LtkGuardedTools {
			if !covered[tool] {
				missing = append(missing, tool)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("the ltk PreToolUse matcher %q does not cover %v — this scenario promises the guardrail runs on every shell command AND FILE EDIT, so every one of %v must be matched; generated %s:\n%s", matcher, missing, j8LtkGuardedTools, rel, raw)
		}
		shipped, err := j8LtkShippedPreToolMatcher()
		if err != nil {
			return err
		}
		if matcher != shipped {
			return fmt.Errorf("ctxloom generated the ltk PreToolUse matcher as %q, but ltk's committed loadout ships %q — delivery must carry the companion's own matcher through verbatim, neither widening nor narrowing it", matcher, shipped)
		}
		w.docStepMaterialized = fmt.Sprintf("%s:\nPreToolUse matcher %q -> \"ltk evaluate\"\n(covers %v; identical to ltk's own cmd/ltk/loadout.yaml)", rel, matcher, j8LtkGuardedTools)
		return nil
	})

	ctx.Step(`^her assistant is told ltk is a cooperative redirect, not a sandbox$`, func(c context.Context) error {
		w := worldFrom(c)
		body, err := materializeDefault(w, j8Target)
		if err != nil {
			return err
		}
		w.docStepMaterialized = filepath.Join(j8Target, "CLAUDE.md") + ":\n" + j5Excerpt(body, j8LtkRedirectClaim, 4)
		if !strings.Contains(body, j8LtkRedirectClaim) {
			return fmt.Errorf("assembled context does not carry ltk's own honest self-description (%q) — ctxloom's delivery must never claim more than the companion itself does; content:\n%s", j8LtkRedirectClaim, body)
		}
		return nil
	})

	// --- Scenario 3: graceful degradation, Bob has neither companion --------

	ctx.Step(`^the "ltk" companion is not installed on Bob's machine$`, func(c context.Context) error {
		// Only the "not installed" arm is implemented: no J8 scenario needs
		// ltk "installed" on Bob's machine (scenario 1 already proves the
		// present-companion contribution, on Alice) — mirrors J4's own "not
		// installed" behavior for reprise (scrub only, no fake install).
		return j8ScrubFromPath(worldFrom(c), "ltk")
	})

	// "the team's project carries the context Carol has standardized on",
	// "Bob clones the project", "the \"reprise\" companion is (installed|not
	// installed) on Bob's machine" (its "not installed" arm needs none of
	// J4's own reprise-envelope Given), "Bob starts a session", "his
	// assistant receives the team's standardized context", and "nothing
	// fails because of the companion's presence or absence" are ALL already
	// registered by steps_j4_onboarding.go — reused verbatim in
	// j8_guardrails.feature, no new registration here (godog rejects an
	// ambiguous second match for the same step text).
}
