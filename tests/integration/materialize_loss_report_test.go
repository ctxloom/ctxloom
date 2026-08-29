//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// This file covers the USER-FACING half of the silent-hook-drop finding:
// `profile materialize` telling someone, at the terminal, that the engine
// they picked cannot carry part of what they just materialized.
//
// The unit tests (internal/operations) prove the loss reaches the result; these
// drive the real binary, because the finding was never that the data was
// missing — it was that nobody was TOLD. A structured field no CLI prints is
// the same silence with extra steps.

// lossFixtureConfig is a project whose config-level session_start hook is a
// team guardrail: the shape where the hook silently fails to follow the
// profile onto an engine with no hook mechanism.
const lossFixtureConfig = `version: 6
config:
  use_distilled: true
hooks:
  unified:
    session_start:
      - type: command
        command: echo team-guardrail
`

// lossFixtureBundle ships one fragment so the assembled context is non-empty —
// materialize refuses an empty payload outright, which would hide the report
// this test is about behind a different (correct) failure.
const lossFixtureBundle = `name: teamrules
version: "1.0"
fragments:
  rules:
    content: "TEAM RULES BODY"
`

// lossFixtureProfile selects that fragment.
const lossFixtureProfile = `name: team
bundles:
  - teamrules#fragments/rules
`

// setupLossFixture lays out a project carrying the guardrail hook and a
// resolvable `team` profile.
func setupLossFixture(t *testing.T) *testenv.TestEnvironment {
	t.Helper()
	e := setupTestEnv(t)
	require.NoError(t, e.WriteFile(".ctxloom/config.yaml", lossFixtureConfig))
	require.NoError(t, e.WriteFile(".ctxloom/content/bundles/teamrules.yaml", lossFixtureBundle))
	writeProfile(t, e, "team", lossFixtureProfile)
	return e
}

// TestMaterializeReport_NamesTheHooksOpencodeCannotCarry is the terminal-facing
// proof. Pre-fix the report was four true "wrote" lines with the dropped
// guardrail nowhere in them; a reader could not tell an engine with no hooks
// from a profile with no hooks.
func TestMaterializeReport_NamesTheHooksOpencodeCannotCarry(t *testing.T) {
	env := setupLossFixture(t)

	// `--format text` is asked for outright. Off a terminal the default is now
	// machine-readable, and the loss AS DATA is already pinned by name in
	// TestMaterializeReport_JSONCarriesTheLoss — so re-pointing this at the
	// JSON would delete the prose half rather than move it. The prose half is
	// this file's whole subject: a structured field no CLI prints is the same
	// silence with extra steps.
	require.NoError(t, env.Run("profile", "materialize", "team", "--target", "out-opencode", "--backend", "opencode", "--format", "text"),
		"a structural capability gap is reported, not fatal — the rest of the tree is still worth having")

	out := env.LastOutput()
	assert.Contains(t, out, "wrote context", "precondition: the surfaces that DID land are still reported")
	assert.Contains(t, strings.ToLower(out), "hook",
		"the report must mention the hook it could not deliver; without this a team ships a guardrail that silently never runs:\n"+out)
	assert.Contains(t, out, "session_start",
		"naming WHICH hook is what makes the line actionable rather than ominous:\n"+out)
	assert.Contains(t, out, "opencode has no hook mechanism",
		"the line must say WHY, so a reader can tell a capability gap from a ctxloom bug:\n"+out)
}

// TestMaterializeReport_JSONCarriesTheLoss pins the machine-readable half: a
// frontend or CI consumer reading --format json must see the loss as DATA, not
// only as prose it would have to scrape.
func TestMaterializeReport_JSONCarriesTheLoss(t *testing.T) {
	env := setupLossFixture(t)

	require.NoError(t, env.Run("profile", "materialize", "team", "--target", "out-json", "--backend", "opencode", "--format", "json"))

	var res struct {
		Backend    string   `json:"backend"`
		Wrote      []string `json:"wrote"`
		NotCarried []struct {
			Surface string `json:"surface"`
			Detail  string `json:"detail"`
			Reason  string `json:"reason"`
		} `json:"not_carried"`
	}
	// LastOutput is stdout+stderr combined, and ctxloom's warnings are their own
	// JSON lines on stderr — so decode the FIRST document rather than requiring
	// the whole stream to be one value.
	out := env.LastOutput()
	require.NoError(t, json.NewDecoder(strings.NewReader(out)).Decode(&res),
		"materialize --format json must emit the result as a JSON document:\n"+out)

	assert.Equal(t, "opencode", res.Backend)
	assert.NotEmpty(t, res.Wrote, "precondition: something was written")
	require.Len(t, res.NotCarried, 1, "the loss must be a structured field, not prose:\n"+out)
	assert.Equal(t, "hooks", res.NotCarried[0].Surface)
	assert.Contains(t, res.NotCarried[0].Detail, "session_start",
		"the detail must name the hook kinds by the config key the user wrote")
	assert.Equal(t, "opencode has no hook mechanism", res.NotCarried[0].Reason)
}

// TestMaterializeReport_SaysNothingWhenNothingIsLost is the false-alarm guard:
// claude-code carries the same hook, so its report must carry no loss line at
// all. A report that warns about a surface it DID deliver is worse than one
// that says nothing — it teaches readers to skip the line that matters.
func TestMaterializeReport_SaysNothingWhenNothingIsLost(t *testing.T) {
	env := setupLossFixture(t)

	// Explicit text for the same reason as its twin above: the silence being
	// proven is a silence in the PROSE report.
	require.NoError(t, env.Run("profile", "materialize", "team", "--target", "out-claude", "--backend", "claude-code", "--format", "text"))

	out := env.LastOutput()
	assert.Contains(t, out, "wrote settings", "precondition: claude-code's settings surface (which carries the hook) landed")
	assert.NotContains(t, out, "NOT carried",
		"claude-code carries hooks, so there is nothing to report as lost:\n"+out)
}
