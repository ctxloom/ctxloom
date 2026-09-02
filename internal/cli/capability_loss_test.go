package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// This file covers the OTHER two surfaces of the silent-capability-loss
// finding (trusting-ambiguity): `ctxloom doctor` and `ctxloom manage check`.
//
// `profile materialize` and `agent show` already name what the chosen engine
// cannot carry. doctor is the command whose entire job is telling a user what
// is wrong with their setup, and it said nothing — a green gate measuring
// nothing. These drive the REAL commands, because the finding was never that
// the data was unreachable; it was that nobody was TOLD.
//
// Every assertion here names the SPECIFICS of the loss (which hook event was
// requested, and the engine's stated reason it is not available). Asserting
// only that the word "capability" appears, or that the command exited 0, would
// pass against a doctor that prints a static heading over an empty list.

// capabilityLossFixtureProfile is a project's own default profile carrying a
// team guardrail on two unified events. session_start is the shape a backend
// with NO hook mechanism drops wholesale; session_end is the per-EVENT shape a
// backend that has hooks generally still has no native event for. No
// currently-registered backend declares the per-event shape, so only the
// whole-mechanism arm is exercised today.
const capabilityLossFixtureProfile = `description: "capability-loss fixture: a team guardrail on two unified events"
hooks:
  unified:
    session_start:
      - type: command
        command: echo team-guardrail
    session_end:
      - type: command
        command: echo team-teardown
`

// setupCapabilityLossProject scaffolds a real project whose sole agent
// ("default") binds the sole profile ("default") to engine, then overwrites
// that profile with one that declares hooks — so the agent's resolved engine
// binding really is being asked for something it may not be able to give.
func setupCapabilityLossProject(t *testing.T, engine string) (string, *config.Config) {
	t.Helper()
	root, cfg := setupProject(t, engine)
	path := filepath.Join(root, ".ctxloom", "profiles", "default.yaml")
	require.NoError(t, os.WriteFile(path, []byte(capabilityLossFixtureProfile), 0o644))
	return root, cfg
}

// requireFixtureLosesSomething is the fixture's OWN precondition: it asserts
// the project really does produce a capability loss before any test claims a
// surface failed to report one. Without it, a red assertion below could mean
// "the reporting is missing" or "the fixture never lost anything", and those
// are not the same finding.
func requireFixtureLosesSomething(t *testing.T, cfg *config.Config, wantDetail, wantReason string) {
	t.Helper()
	resolved, err := operations.ResolveAgent(context.Background(), cfg, "default", "")
	require.NoError(t, err, "precondition: the fixture's agent must resolve")
	losses := operations.CapabilityLoss(cfg, resolved.Backend, resolved.Profiles)
	require.NotEmpty(t, losses, "precondition: the fixture must actually lose a hook on %s", resolved.Backend)
	var joined string
	for _, l := range losses {
		joined += l.String() + "\n"
	}
	require.Contains(t, joined, wantDetail, "precondition: the fixture's loss must name the requested hook")
	require.Contains(t, joined, wantReason, "precondition: the fixture's loss must carry the engine's reason")
}

// linesContaining is lineContaining's plural sibling, for a report that
// legitimately carries several matching lines — one per lost hook kind. It
// asserts NOTHING about the count so the caller can state the number it
// expects, which is the part worth pinning.
func linesContaining(t *testing.T, out, needle string) []string {
	t.Helper()
	var found []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, needle) {
			found = append(found, line)
		}
	}
	return found
}

// requireFixtureLosesNothing is requireFixtureLosesSomething's twin for the
// false-alarm guards: it proves the fixture's engine CAN carry what it was
// given, so a report that stays silent is silent for the right reason.
func requireFixtureLosesNothing(t *testing.T, cfg *config.Config) {
	t.Helper()
	resolved, err := operations.ResolveAgent(context.Background(), cfg, "default", "")
	require.NoError(t, err, "precondition: the fixture's agent must resolve")
	require.Empty(t, operations.CapabilityLoss(cfg, resolved.Backend, resolved.Profiles),
		"precondition: %s must carry this fixture's hooks, or the silence below proves nothing", resolved.Backend)
}

// --- DOCTOR-CHECK-CAPABILITY-LOSS-u1 ---

// TestDoctorCmd_CapabilityLoss_NamesTheHooksAnEngineCannotCarry is the
// terminal-facing proof for the whole-mechanism shape. Pre-fix `doctor` ran
// twenty-one checks and not one of them mentioned that this project's
// guardrail will never fire.
// mockLossyHookReason is the reason the deliberately-lossy double declares,
// stated once here rather than retyped per assertion. It must match the
// descriptor's unsupportedHookKinds entry — a drifted copy would assert a
// message the product never emits, and the test would fail for the wrong
// reason.
const mockLossyHookReason = config.BackendMockLossy + " has no native session_start event"

func TestDoctorCmd_CapabilityLoss_NamesTheHooksAnEngineCannotCarry(t *testing.T) {
	root, cfg := setupCapabilityLossProject(t, config.BackendMockLossy)
	requireFixtureLosesSomething(t, cfg, "session_start", mockLossyHookReason)

	out, err := runDoctor(t, root)
	require.NoError(t, err, "doctor stays diagnostic-only: a capability gap is reported, never fatal")

	check := doctorCheckNamed(t, out, "DOCTOR-CHECK-CAPABILITY-LOSS-u1")
	assert.Equal(t, doctorWarn, check.Status,
		"a configured agent whose engine drops a hook it was given is a WARN — doctor's fail-loud signal:\n"+out)
	assert.Contains(t, check.Detail, "default",
		"the detail must name WHICH agent loses it, or a multi-agent roster is unactionable:\n"+out)
	assert.Contains(t, check.Detail, "session_start",
		"naming the hook event the user actually wrote is what makes the detail actionable rather than ominous:\n"+out)
	assert.Contains(t, check.Detail, mockLossyHookReason,
		"the detail must say WHY the engine cannot give it, so a reader can tell a capability gap from a ctxloom bug:\n"+out)
}

// TestDoctorCmd_CapabilityLoss_StaysQuietWhenNothingIsLost is the false-alarm
// guard, and it is the reason the check line itself is asserted present: a
// doctor that shouts about capability loss on a healthy project is the
// opposite bug and just as bad, while a doctor that dropped the check
// entirely would also "stay quiet". Both are excluded here.
func TestDoctorCmd_CapabilityLoss_StaysQuietWhenNothingIsLost(t *testing.T) {
	root, cfg := setupCapabilityLossProject(t, "claude-code")
	requireFixtureLosesNothing(t, cfg)

	out, err := runDoctor(t, root)
	require.NoError(t, err)

	check := doctorCheckNamed(t, out, "DOCTOR-CHECK-CAPABILITY-LOSS-u1")
	assert.Equal(t, doctorOK, check.Status,
		"the check must RUN and say so — silence from a check that was never wired is not the same as silence from a clean project:\n"+out)
	assert.NotContains(t, out, "NOT carried",
		"claude-code carries both hooks, so there is nothing to report as lost:\n"+out)
	assert.NotContains(t, check.Detail, "session_start",
		"a clean project's detail must not name a hook as lost:\n"+out)
	assert.NotContains(t, out, "no hook mechanism", out)
}

// --- manage check ---

// execManageCheck drives the REAL `ctxloom manage check` RunE in root and
// returns everything it wrote. Built the same way execDoctor is: a cobra
// stand-in carrying the persistent flags emit() reads, since this command has
// no parent here to inherit them from.
//
// The format is asked for EXPLICITLY, which is not a way around the
// machine-readable default a non-terminal resolves to — an explicit flag
// winning is the rule, so each arm can be driven deliberately. Both surfaces
// now carry the capability loss: runManageCheck stores it on
// HarnessStatusResult.CapabilityLoss before emitting, and the text closure
// renders that same value.
func execManageCheck(t *testing.T, root string) (string, error) {
	t.Helper()
	return execManageCheckAs(t, root, formatText)
}

// execManageCheckAs drives manage check in a named format.
func execManageCheckAs(t *testing.T, root, format string) (string, error) {
	t.Helper()
	t.Chdir(root)
	buf := &bytes.Buffer{}
	c := &cobra.Command{Use: "check", RunE: manageCheckCmd.RunE, SilenceErrors: true, SilenceUsage: true}
	addFlagOnce := func(name string, declare func()) {
		if c.Flags().Lookup(name) == nil {
			declare()
		}
	}
	addFlagOnce("format", func() { c.Flags().String("format", formatText, "") })
	addFlagOnce("degraded", func() { c.Flags().Bool("degraded", false, "") })
	addFlagOnce("no-companions", func() { c.Flags().Bool("no-companions", false, "") })
	c.SetOut(buf)
	c.SetErr(buf)
	c.SetContext(context.Background())
	c.SetArgs([]string{"--format", format})
	err := c.Execute()
	return buf.String(), err
}

// TestManageCheck_CapabilityLoss_NamesTheHooksAnEngineCannotCarry pins the
// wiring report's half. `manage check` answers "what has ctxloom wired in",
// and every line of it was true while the guardrail it could not wire went
// unmentioned — the same silence the delivery report had.
func TestManageCheck_CapabilityLoss_NamesTheHooksAnEngineCannotCarry(t *testing.T) {
	root, cfg := setupCapabilityLossProject(t, config.BackendMockLossy)
	requireFixtureLosesSomething(t, cfg, "session_start", mockLossyHookReason)

	out, err := execManageCheck(t, root)
	require.NoError(t, err)

	assert.Contains(t, out, "Project:", "precondition: the wiring report itself still rendered")

	// ONE LINE PER LOST KIND. The double declares two unsupported events, and
	// the report must not collapse them: a reader told only that "hooks" were
	// lost cannot tell which of their guardrails is missing, and each kind
	// carries its own reason.
	lines := linesContaining(t, out, "NOT carried")
	require.Len(t, lines, 2, "expected one NOT-carried line per declared unsupported kind in:\n"+out)

	joined := strings.Join(lines, "\n")
	assert.Contains(t, joined, "default", "a line must name which agent loses it:\n"+out)
	assert.Contains(t, joined, "session_start", "the requested session_start hook must be named:\n"+out)
	assert.Contains(t, joined, "session_end", "the requested session_end hook must be named:\n"+out)
	assert.Contains(t, joined, mockLossyHookReason, "a line must say why it is not available:\n"+out)
}

// TestManageCheck_CapabilityLoss_StaysQuietWhenNothingIsLost is the
// false-alarm twin, with the same "the command really ran" precondition.
func TestManageCheck_CapabilityLoss_StaysQuietWhenNothingIsLost(t *testing.T) {
	root, cfg := setupCapabilityLossProject(t, "claude-code")
	requireFixtureLosesNothing(t, cfg)

	out, err := execManageCheck(t, root)
	require.NoError(t, err)

	assert.Contains(t, out, "Project:", "precondition: the wiring report itself rendered, so the silence below is about the loss section")
	assert.NotContains(t, out, "Capability loss",
		"not even the HEADING may appear: a labelled section over an empty list is the shape that teaches readers to skip the line that matters:\n"+out)
	assert.NotContains(t, out, "NOT carried",
		"claude-code carries this fixture's hooks; a loss section here would be a false alarm:\n"+out)
	assert.NotContains(t, out, "no hook mechanism", out)
}

// TestManageCheck_CapabilityLoss_JSONCarriesTheLoss pins the machine-readable
// half. It matters more than the text half: off a terminal the resolved format
// is json, so this is what every script, CI job and agent receives by default.
// A report that named the loss only in prose would state it to a human and
// withhold it from every machine consumer.
func TestManageCheck_CapabilityLoss_JSONCarriesTheLoss(t *testing.T) {
	root, cfg := setupCapabilityLossProject(t, config.BackendMockLossy)
	requireFixtureLosesSomething(t, cfg, "session_start", mockLossyHookReason)

	out, err := execManageCheckAs(t, root, "json")
	require.NoError(t, err)

	var got operations.HarnessStatusResult
	require.NoError(t, json.Unmarshal([]byte(out), &got), "manage check --format json must emit parseable JSON:\n"+out)

	require.Len(t, got.CapabilityLoss, 1, "exactly the one configured agent loses something:\n"+out)
	entry := got.CapabilityLoss[0]
	assert.Equal(t, "default", entry.Agent, "the payload must name WHICH agent loses it")
	assert.Equal(t, config.BackendMockLossy, entry.Backend, "the payload must name the engine that cannot carry it")
	// ONE LOSS PER DECLARED KIND, each carrying its own detail and reason. A
	// payload that merged them would tell a consumer that "hooks" were lost
	// without saying which events, and would attribute one kind's absence to
	// the other's cause.
	require.Len(t, entry.Losses, 2, "an agent listed as losing something must say what, per kind")
	var details []string
	for _, l := range entry.Losses {
		assert.Equal(t, "hooks", l.Surface, "the surface is named in the user's own vocabulary")
		assert.NotEmpty(t, l.Reason, "each loss must carry its own reason, not share one")
		details = append(details, l.Detail)
	}
	assert.Contains(t, strings.Join(details, " "), "session_start", "the detail must name the hook events actually requested")
	assert.Contains(t, strings.Join(details, " "), "session_end", "the detail must name the hook events actually requested")
}

// TestManageCheck_CapabilityLoss_JSONOmitsTheKeyWhenNothingIsLost is the
// false-alarm twin: omitempty means a healthy project carries no key at all,
// not an empty array a consumer must special-case.
func TestManageCheck_CapabilityLoss_JSONOmitsTheKeyWhenNothingIsLost(t *testing.T) {
	root, cfg := setupCapabilityLossProject(t, "claude-code")
	requireFixtureLosesNothing(t, cfg)

	out, err := execManageCheckAs(t, root, "json")
	require.NoError(t, err)

	assert.NotContains(t, out, "capability_loss", "claude-code carries this fixture's hooks; the key must be absent entirely:\n"+out)
}
