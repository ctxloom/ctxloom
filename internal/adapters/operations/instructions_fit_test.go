package operations

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/harp"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// worstCaseHarp is the longest harp a session can carry: harp.MaxNameLen, the
// rename limit (a minted name is shorter). This test is what holds that limit
// to the instructions budget.
var worstCaseHarp = strings.Repeat("a", harp.MaxNameLen)

// worstCaseHome is the longest ordinary Linux home: /home/<user> at useradd's
// 32-character username limit. The plan dir and its worked example both embed
// it, so it is paid twice.
const worstCaseHome = "/home/abcdefghijklmnopqrstuvwxyz012345"

// TestSessionInstructions_PartsLeadWithWhatCostsMostToLose pins the order a
// truncating client depends on: session line, catalog pointer, static text.
func TestSessionInstructions_PartsLeadWithWhatCostsMostToLose(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "brisk-teal-otter"
	got := SessionInstructions(harp)

	session := strings.Index(got, "`"+harp+"`")
	catalog := strings.Index(got, PremiseSelectionInstruction()[:40])
	static := strings.Index(got, strings.TrimRight(mcpServerInstructions, "\n"))
	require.True(t, session >= 0 && catalog >= 0 && static >= 0, "every part must be present")
	assert.True(t, strings.HasPrefix(got, "Your session is named `"+harp+"`"), "the session line must lead")
	assert.Less(t, session, catalog, "the session line precedes the catalog pointer")
	assert.Less(t, catalog, static, "the catalog pointer precedes the static text")
}

// TestSessionInstructions_FitTheClientCap pins the WORST-CASE instructions
// under InstructionsCharCap: past it, claude drops the tail without telling
// the agent, and what is dropped is whatever was written last.
func TestSessionInstructions_FitTheClientCap(t *testing.T) {
	testsupport.Isolate(t)
	t.Setenv("HOME", worstCaseHome)
	t.Setenv(sessions.EnvHarp, worstCaseHarp)
	t.Setenv("CTXLOOM_RESUMED_FROM", worstCaseHarp)
	t.Setenv("CTXLOOM_RESUMED_PARTS", "session,tasks")

	got := SessionInstructions(worstCaseHarp)
	require.Contains(t, got, "Resumed from", "the worst case must carry resume provenance")
	require.Contains(t, got, worstCaseHome, "the worst case must carry the plan dir")

	n := utf8.RuneCountInString(got)
	t.Logf("worst case: %d chars (session line %d, catalog %d [selection %d], static %d)", n,
		utf8.RuneCountInString(sessionLine(worstCaseHarp)),
		utf8.RuneCountInString(strings.TrimRight(premiseCatalogInstruction(), "\n")),
		utf8.RuneCountInString(PremiseSelectionInstruction()),
		utf8.RuneCountInString(strings.TrimRight(mcpServerInstructions, "\n")))

	// What the order guarantees today: whatever claude keeps, it keeps the
	// session line and the whole catalog pointer.
	catalogEnd := strings.Index(got, strings.TrimRight(premiseCatalogInstruction(), "\n")) +
		len(strings.TrimRight(premiseCatalogInstruction(), "\n"))
	assert.LessOrEqual(t, utf8.RuneCountInString(got[:catalogEnd]), InstructionsCharCap,
		"the session line and the catalog pointer must survive the client cap")

	assert.LessOrEqual(t, n, InstructionsCharCap,
		"the worst-case instructions exceed the client cap; claude would drop the tail")
}
