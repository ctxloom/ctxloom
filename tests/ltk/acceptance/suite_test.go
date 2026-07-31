// Package acceptance runs the human-readable Gherkin features in features/
// against the real decision pipeline (app.Decide) via godog.
package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/ltk/app"
	"github.com/ctxloom/ctxloom/internal/ltk/engine"
	"github.com/ctxloom/ctxloom/internal/ltk/ir"
	"github.com/ctxloom/ctxloom/internal/ltk/rules"
	"github.com/ctxloom/ctxloom/internal/ltk/state"
)

// readmePath is the ltk README, relative to this package's directory. Its
// embedded ```gherkin blocks are the source of truth for these acceptance
// tests. It lives under docs/ltk/ in the consolidated ctxloom module (folded
// from the standalone ltk repo's root README).
const readmePath = "../../../docs/ltk/README.md"

// world is the per-scenario state: the configured tool and the last decision.
type world struct {
	cfg       *rules.Config
	app       *app.App
	resp      engine.Response
	fs        afero.Fs // in-memory override state, isolated per scenario
	stateFile string
}

// confirmWindow is the override window used in the acceptance scenarios.
const confirmWindow = 30 * time.Second

// projectRedirects builds a rule set from the Background table. Columns:
// "when the agent runs" | "it should instead" | "because". The redirects use
// mode: confirm so the confirm-by-repeating override is exercised below; a
// global window backs it.
func (w *world) projectRedirects(table *godog.Table) error {
	w.cfg = &rules.Config{Defaults: rules.Defaults{
		RepeatWindowSeconds: int(confirmWindow.Seconds()),
	}}
	for _, row := range table.Rows[1:] { // skip the header row
		command := strings.TrimSpace(row.Cells[0].Value)
		instead := strings.TrimSpace(row.Cells[1].Value)
		because := strings.TrimSpace(row.Cells[2].Value)
		w.cfg.Rules = append(w.cfg.Rules, rules.Rule{
			ID:      command,
			Match:   rules.Match{Command: rules.CommandPattern(strings.Fields(command))},
			Message: because,
			Suggest: instead,
			Mode:    rules.ModeConfirm,
		})
	}
	w.app = app.New(w.cfg, app.Shells{})
	return nil
}

// markInviolate adds a rule for command in mode: enable (the default) — a firm
// denial that repeating can never lift.
func (w *world) markInviolate(command string) error {
	if w.cfg == nil {
		return fmt.Errorf("no project configured (missing Background)")
	}
	w.cfg.Rules = append(w.cfg.Rules, rules.Rule{
		ID:      "inviolate: " + command,
		Match:   rules.Match{Command: rules.CommandPattern(strings.Fields(command))},
		Message: "this command is never allowed through ltk",
		Mode:    rules.ModeEnable,
	})
	w.app = app.New(w.cfg, app.Shells{})
	return nil
}

// setOnParseErrorDeny switches the project's on_parse_error policy from the
// default (allow) to deny: an operator opting into "block anything I could
// not verify". Rebuilds w.app, same as markInviolate above does after
// mutating w.cfg.
func (w *world) setOnParseErrorDeny() error {
	if w.cfg == nil {
		return fmt.Errorf("no project configured (missing Background)")
	}
	w.cfg.Defaults.OnParseError = rules.ActionDeny
	w.app = app.New(w.cfg, app.Shells{})
	return nil
}

func (w *world) theAgentRuns(command string) error {
	if w.app == nil {
		return fmt.Errorf("no project configured (missing Background)")
	}
	w.resp = w.app.Decide(context.Background(), engine.Request{
		ToolName: "Bash",
		Command:  command,
		Shell:    ir.ShellBash,
	})
	return nil
}

func (w *world) theCommandIsBlocked() error {
	if w.resp.Allow {
		return fmt.Errorf("expected the command to be blocked, but it was allowed")
	}
	return nil
}

func (w *world) theCommandIsAllowed() error {
	if !w.resp.Allow {
		return fmt.Errorf("expected the command to be allowed, but it was blocked: %s", w.resp.Message())
	}
	return nil
}

// encodedOutput renders the last decision through a real engine adapter. Every
// step above stops at app.Decide, which is one layer short of what the operator
// actually experiences: the decision still has to be encoded onto the hook's
// wire (stdout document, stderr diagnostic, exit code) before anyone sees it.
// U067-F03 lived precisely in that gap — Output.Stderr was consumed by
// cmd/ltk/evaluate.go and produced by nothing — so a scenario about what the
// operator is TOLD has to cross this boundary rather than stop at the verdict.
func (w *world) encodedOutput() (engine.Output, error) {
	return engine.ClaudeCode{}.Encode(w.resp)
}

// theOperatorIsWarnedItCouldNotBeAnalyzed asserts the failure-path payload: an
// allow that ltk reached WITHOUT being able to parse the command must not be
// byte-identical to a clean allow. The command still runs — this is a
// cooperative redirect, and on_parse_error: allow is the shipped default — but
// staying silent would leave an operator believing their rules were consulted
// when nothing was checked at all.
func (w *world) theOperatorIsWarnedItCouldNotBeAnalyzed() error {
	if !w.resp.Allow {
		return fmt.Errorf("this scenario is about an ALLOW that could not be analyzed, but the command was turned away: %s", w.resp.Message())
	}
	out, err := w.encodedOutput()
	if err != nil {
		return fmt.Errorf("encode the decision for the hook: %w", err)
	}
	if len(out.Stderr) == 0 {
		return fmt.Errorf("expected the operator to be warned the command could not be fully analyzed, " +
			"but the encoded output was silent — byte-identical to a clean allow")
	}
	if !strings.Contains(string(out.Stderr), "could not fully analyze") {
		return fmt.Errorf("expected the warning to say the command could not be fully analyzed, got %q", out.Stderr)
	}
	// The command is still permitted: no deny document, no non-zero exit.
	if len(out.Stdout) != 0 || out.ExitCode != 0 {
		return fmt.Errorf("an allow must not turn into a block on the wire, got stdout=%q exit=%d", out.Stdout, out.ExitCode)
	}
	return nil
}

// theOperatorIsNotWarned is the paired negative: an ordinary, fully-understood
// allow must stay silent. Without it, the warning assertion above could be
// satisfied by warning on EVERY command, which would bury the signal it exists
// to carry under one line per tool call.
func (w *world) theOperatorIsNotWarned() error {
	out, err := w.encodedOutput()
	if err != nil {
		return fmt.Errorf("encode the decision for the hook: %w", err)
	}
	if len(out.Stderr) != 0 {
		return fmt.Errorf("a fully-analyzed allow must stay silent, but the operator was warned: %q", out.Stderr)
	}
	return nil
}

func (w *world) theAgentIsTold(text string) error {
	if !strings.Contains(w.resp.Message(), text) {
		return fmt.Errorf("expected the agent to be told %q, got %q", text, w.resp.Message())
	}
	return nil
}

func (w *world) theAgentIsPointedAt(suggestion string) error {
	if w.resp.Suggest != suggestion {
		return fmt.Errorf("expected the agent to be pointed at %q, got %q", suggestion, w.resp.Suggest)
	}
	return nil
}

// applyOverride mirrors the CLI gate: the confirm-by-repeating override runs
// only for a denial the fired rule marks confirmable. now lets the caller order
// the two attempts within the window.
func (w *world) applyOverride(command string, now time.Time) (engine.Response, bool) {
	if w.resp.Allow || !w.resp.Confirmable || w.resp.ConfirmWindowSeconds <= 0 {
		return w.resp, false
	}
	resp, overridden, _ := state.ConfirmByRepeat(w.fs, w.resp, command, w.stateFile, now,
		time.Duration(w.resp.ConfirmDelaySeconds)*time.Second,
		time.Duration(w.resp.ConfirmWindowSeconds)*time.Second)
	return resp, overridden
}

// theAgentRunsTurnedAwayPending runs command as the *first* attempt: it must
// remain turned away, with a hint to repeat, and arm the override for next time.
func (w *world) theAgentRunsTurnedAwayPending(command string) error {
	if err := w.theAgentRuns(command); err != nil {
		return err
	}
	resp, overridden := w.applyOverride(command, time.Unix(1_000_000, 0))
	if overridden {
		return fmt.Errorf("the first attempt should arm the override, not satisfy it")
	}
	if resp.Allow {
		return fmt.Errorf("expected the command to be turned away pending confirmation, but it was allowed")
	}
	if !strings.Contains(resp.Message(), "again") {
		return fmt.Errorf("expected a confirm-by-repeating hint, got %q", resp.Message())
	}
	w.resp = resp
	return nil
}

// theAgentRunsASecondTime runs command again within the window; for a
// confirmable denial the override consumes the armed entry and allows it.
func (w *world) theAgentRunsASecondTime(command string) error {
	if err := w.theAgentRuns(command); err != nil {
		return err
	}
	resp, _ := w.applyOverride(command, time.Unix(1_000_001, 0))
	w.resp = resp
	return nil
}

// theAgentRepeatsButStaysBlocked runs command twice; an inviolate rule must keep
// it turned away both times, and the override must never even be offered.
func (w *world) theAgentRepeatsButStaysBlocked(command string) error {
	for i, now := range []time.Time{time.Unix(2_000_000, 0), time.Unix(2_000_001, 0)} {
		if err := w.theAgentRuns(command); err != nil {
			return err
		}
		if w.resp.Confirmable {
			return fmt.Errorf("attempt %d: an inviolate command must not be confirmable", i+1)
		}
		resp, overridden := w.applyOverride(command, now)
		if overridden || resp.Allow {
			return fmt.Errorf("attempt %d: repeating must not let an inviolate command through", i+1)
		}
		w.resp = resp
	}
	return nil
}

// InitializeScenario binds steps to a fresh world for each scenario.
func InitializeScenario(sc *godog.ScenarioContext) {
	w := &world{fs: afero.NewMemMapFs(), stateFile: filepath.Join(".ltk", "state.json")}
	sc.Step(`^the project asks agents to use:$`, w.projectRedirects)
	sc.Step(`^"([^"]*)" is inviolate$`, w.markInviolate)
	sc.Step(`^defaults\.on_parse_error is deny$`, w.setOnParseErrorDeny)
	sc.Step(`^the agent runs "([^"]*)"$`, w.theAgentRuns)
	sc.Step(`^the agent runs "([^"]*)" and is turned away pending confirmation$`, w.theAgentRunsTurnedAwayPending)
	sc.Step(`^the agent runs "([^"]*)" a second time$`, w.theAgentRunsASecondTime)
	sc.Step(`^the agent runs "([^"]*)" twice and is turned away both times$`, w.theAgentRepeatsButStaysBlocked)
	sc.Step(`^the command is turned away$`, w.theCommandIsBlocked)
	sc.Step(`^the command is allowed$`, w.theCommandIsAllowed)
	sc.Step(`^the operator is warned it could not be fully analyzed$`, w.theOperatorIsWarnedItCouldNotBeAnalyzed)
	sc.Step(`^the operator is not warned$`, w.theOperatorIsNotWarned)
	sc.Step(`^the agent is told "([^"]*)"$`, w.theAgentIsTold)
	sc.Step(`^the agent is pointed at "([^"]*)"$`, w.theAgentIsPointedAt)
}

func TestFeatures(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: InitializeScenario,
		Options: &godog.Options{
			Format:          "pretty",
			FeatureContents: featuresFromREADME(t),
			TestingT:        t,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("acceptance scenarios (extracted from the README) failed")
	}
}

// featuresFromREADME reads the README and returns each embedded ```gherkin block
// as a godog feature, so the documented behavior is exactly what we test.
func featuresFromREADME(t *testing.T) []godog.Feature {
	t.Helper()
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read %s: %v", readmePath, err)
	}
	blocks := extractGherkinBlocks(string(data))
	if len(blocks) == 0 {
		t.Fatalf("no ```gherkin blocks found in %s", readmePath)
	}
	feats := make([]godog.Feature, len(blocks))
	for i, b := range blocks {
		feats[i] = godog.Feature{
			Name:     fmt.Sprintf("%s (gherkin block %d)", readmePath, i+1),
			Contents: []byte(b),
		}
	}
	return feats
}

// extractGherkinBlocks returns the contents of every fenced ```gherkin block in
// the markdown, preserving inner indentation.
func extractGherkinBlocks(md string) []string {
	var blocks []string
	var cur []string
	inBlock := false
	for _, line := range strings.Split(md, "\n") {
		fence := strings.TrimSpace(line)
		switch {
		case !inBlock && fence == "```gherkin":
			inBlock, cur = true, nil
		case inBlock && fence == "```":
			blocks = append(blocks, strings.Join(cur, "\n"))
			inBlock = false
		case inBlock:
			cur = append(cur, line)
		}
	}
	return blocks
}
