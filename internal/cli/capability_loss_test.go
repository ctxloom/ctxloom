package cli

import (
	"bytes"
	"context"
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
// with NO hook mechanism drops wholesale (opencode); session_end is the
// per-EVENT shape a backend that has hooks generally still has no native event
// for (codex). One fixture exercises both.
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

// requireFixtureLosesNothing is requireFixtureLosesSomething's twin for the
// quiet case: the SAME hook-declaring profile on an engine that carries it, so
// a "stays quiet" assertion is proving silence about a real non-loss rather
// than about a fixture that declared no hooks in the first place.
func requireFixtureLosesNothing(t *testing.T, cfg *config.Config) {
	t.Helper()
	resolved, err := operations.ResolveAgent(context.Background(), cfg, "default", "")
	require.NoError(t, err, "precondition: the fixture's agent must resolve")
	require.Empty(t, operations.CapabilityLoss(cfg, resolved.Backend, resolved.Profiles),
		"precondition: %s must carry this fixture's hooks, or the silence below proves nothing", resolved.Backend)
}

// lineContaining returns the one output line carrying needle, failing when no
// line does or when several do. Every specific below is asserted against THAT
// line rather than against the whole report: doctor prints "default" in its
// roster check and "hooks" in its wiring check, so a whole-output Contains
// would stay green against a capability-loss line that named nothing at all.
func lineContaining(t *testing.T, out, needle string) string {
	t.Helper()
	var found []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, needle) {
			found = append(found, line)
		}
	}
	require.Lenf(t, found, 1, "expected exactly one line containing %q in:\n%s", needle, out)
	return found[0]
}

// --- DOCTOR-CHECK-CAPABILITY-LOSS-u1 ---

// TestDoctorCmd_CapabilityLoss_NamesTheHooksOpencodeCannotCarry is the
// terminal-facing proof for the whole-mechanism shape. Pre-fix `doctor` ran
// twenty-one checks and not one of them mentioned that this project's
// guardrail will never fire.
func TestDoctorCmd_CapabilityLoss_NamesTheHooksOpencodeCannotCarry(t *testing.T) {
	root, cfg := setupCapabilityLossProject(t, "opencode")
	requireFixtureLosesSomething(t, cfg, "session_start", "opencode has no hook mechanism")

	out, err := runDoctor(t, root)
	require.NoError(t, err, "doctor stays diagnostic-only: a capability gap is reported, never fatal")

	line := lineContaining(t, out, "DOCTOR-CHECK-CAPABILITY-LOSS-u1")
	assert.Contains(t, line, "[warn]",
		"a configured agent whose engine drops a hook it was given is a WARN — doctor's fail-loud signal:\n"+out)
	assert.Contains(t, line, "default",
		"the line must name WHICH agent loses it, or a multi-agent roster is unactionable:\n"+out)
	assert.Contains(t, line, "session_start",
		"naming the hook event the user actually wrote is what makes the line actionable rather than ominous:\n"+out)
	assert.Contains(t, line, "opencode has no hook mechanism",
		"the line must say WHY the engine cannot give it, so a reader can tell a capability gap from a ctxloom bug:\n"+out)
}

// TestDoctorCmd_CapabilityLoss_NamesThePerEventGapCodexHas covers the OTHER
// shape, the one a whole-mechanism check structurally cannot see: codex has
// hooks, but no native session-end event. A check that only knew "does this
// engine have hooks at all" would report this project clean.
func TestDoctorCmd_CapabilityLoss_NamesThePerEventGapCodexHas(t *testing.T) {
	root, cfg := setupCapabilityLossProject(t, "codex")
	requireFixtureLosesSomething(t, cfg, "session_end", "codex has no session-end event")

	out, err := runDoctor(t, root)
	require.NoError(t, err)

	line := lineContaining(t, out, "DOCTOR-CHECK-CAPABILITY-LOSS-u1")
	assert.Contains(t, line, "[warn]", out)
	assert.Contains(t, line, "session_end",
		"the per-EVENT gap must name the event, not just the surface:\n"+out)
	assert.Contains(t, line, "codex has no session-end event", out)
	assert.NotContains(t, line, "session_start",
		"codex DOES carry session_start here; reporting it as lost would be the false alarm that teaches readers to skip the line:\n"+out)
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

	line := lineContaining(t, out, "DOCTOR-CHECK-CAPABILITY-LOSS-u1")
	assert.Contains(t, line, "[ok]",
		"the check must RUN and say so — silence from a check that was never wired is not the same as silence from a clean project:\n"+out)
	assert.NotContains(t, out, "NOT carried",
		"claude-code carries both hooks, so there is nothing to report as lost:\n"+out)
	assert.NotContains(t, line, "session_start",
		"a clean project's line must not name a hook as lost:\n"+out)
	assert.NotContains(t, out, "no hook mechanism", out)
}

// --- manage check ---

// execManageCheck drives the REAL `ctxloom manage check` RunE in root and
// returns everything it wrote. Built the same way execDoctor is: a cobra
// stand-in carrying the persistent flags emit() reads, since this command has
// no parent here to inherit them from.
func execManageCheck(t *testing.T, root string) (string, error) {
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
	c.SetArgs(nil)
	err := c.Execute()
	return buf.String(), err
}

// TestManageCheck_CapabilityLoss_NamesTheHooksOpencodeCannotCarry pins the
// wiring report's half. `manage check` answers "what has ctxloom wired in",
// and every line of it was true while the guardrail it could not wire went
// unmentioned — the same silence the delivery report had.
func TestManageCheck_CapabilityLoss_NamesTheHooksOpencodeCannotCarry(t *testing.T) {
	root, cfg := setupCapabilityLossProject(t, "opencode")
	requireFixtureLosesSomething(t, cfg, "session_start", "opencode has no hook mechanism")

	out, err := execManageCheck(t, root)
	require.NoError(t, err)

	assert.Contains(t, out, "Project:", "precondition: the wiring report itself still rendered")
	line := lineContaining(t, out, "NOT carried")
	assert.Contains(t, line, "default", "the line must name which agent loses it:\n"+out)
	assert.Contains(t, line, "session_start", "the line must name the hook event that was requested:\n"+out)
	assert.Contains(t, line, "opencode has no hook mechanism", "the line must say why it is not available:\n"+out)
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
