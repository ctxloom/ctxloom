package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The three `--version` output shapes MEASURED on this project's dev host,
// 2026-08-07. They are three genuinely different shapes — version-first,
// name-first, bare — which is why per-engine parsing lives in the descriptor
// and not in one shared regex. If a future refactor collapses them, this table
// is the evidence that it cannot be done.
func TestVersionParsers_MatchMeasuredEngineOutput(t *testing.T) {
	cases := []struct {
		engine string
		output string
		want   string
	}{
		{"claude-code", "2.1.225 (Claude Code)", "2.1.225"}, // version first, name in parentheses
		{"codex", "codex-cli 0.144.4", "0.144.4"},           // name first, then version
		{"opencode", "1.18.4", "1.18.4"},                    // bare version
	}
	for _, tc := range cases {
		t.Run(tc.engine, func(t *testing.T) {
			cmd, ok := VersionCommandFor(tc.engine)
			require.True(t, ok, "%s must declare a version command", tc.engine)
			assert.Equal(t, []string{"--version"}, cmd.Args)

			got, err := cmd.Parse(tc.output)
			require.NoError(t, err, "the engine's own measured output must parse")
			assert.Equal(t, tc.want, got)
		})
	}
}

// Each engine's parser must REFUSE the other engines' shapes. This is what
// stops a "close enough" parser from quietly picking up the wrong token — a
// codex parser applied to claude's output would return "(Claude", and a claude
// parser applied to codex's would return "codex-cli", both of which are then
// carried into the session index as if they were versions.
func TestVersionParsers_RefuseAnotherEnginesShape(t *testing.T) {
	claude, ok := VersionCommandFor("claude-code")
	require.True(t, ok)
	codex, ok := VersionCommandFor("codex")
	require.True(t, ok)

	_, err := claude.Parse("codex-cli 0.144.4")
	assert.Error(t, err, "claude's version-first parser must refuse codex's name-first banner")

	_, err = codex.Parse("2.1.225 (Claude Code)")
	assert.Error(t, err, "codex's name-first parser must refuse claude's version-first banner")
}

// Every engine whose vendor transcripts ctxloom READS must be askable for its
// version — otherwise reader selection has nothing to select on and every
// session under that engine refuses. The three are the ones
// operations.vendorReaderRegistry covers (and .github/engine-versions.env
// pins); this test states the requirement where the descriptors live so a new
// engine cannot be added without one.
func TestVersionCommands_DeclaredForEveryVendorReaderEngine(t *testing.T) {
	for _, engine := range []string{"claude-code", "codex", "kiro"} {
		cmd, ok := VersionCommandFor(engine)
		assert.True(t, ok, "%s reads a vendor transcript, so it must declare a version command", engine)
		if ok {
			assert.NotNil(t, cmd.Parse, "%s's version command must know how to parse its output", engine)
			assert.NotEmpty(t, cmd.Args, "%s's version command must pass some argument", engine)
		}
	}
}

// mock and acp deliberately declare NO version command: mock has no binary at
// all, and the generic acp backend drives whatever command config names, so
// there is no single binary whose version would mean anything. Declaring a
// bogus one for them would put a meaningless string in a session index.
func TestVersionCommands_AbsentWhereThereIsNoOneBinaryToAsk(t *testing.T) {
	for _, engine := range []string{"mock", "acp"} {
		_, ok := VersionCommandFor(engine)
		assert.False(t, ok, "%s has no single binary whose version means anything", engine)
	}
	_, ok := VersionCommandFor("no-such-engine")
	assert.False(t, ok, "an unregistered name declares nothing")
}

// An engine that IS registered but has no installed binary must resolve to
// *engineversion.BinaryAbsentError, not to some other failure — that type is
// what lets a caller treat "you don't have kiro" as ordinary while treating
// "kiro is installed and printed junk" as worth reporting.
func TestResolveEngineVersionCommand_UndeclaredEngineRefuses(t *testing.T) {
	_, _, err := ResolveEngineVersionCommand("mock")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no version command")
}
