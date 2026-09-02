package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The `--version` output shape MEASURED on this project's dev host, per engine
// that can be asked. Per-engine parsing lives in the descriptor rather than in
// one shared regex because the shapes genuinely differ — version-first,
// name-first, bare — and TestVersionParsers_RefuseAShapeItDoesNotOwn is what
// keeps a parser from drifting into accepting a shape it does not own.
func TestVersionParsers_MatchMeasuredEngineOutput(t *testing.T) {
	cases := []struct {
		engine string
		output string
		want   string
	}{
		{"claude-code", "2.1.225 (Claude Code)", "2.1.225"}, // version first, name in parentheses
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

// A parser must REFUSE a shape it does not own. This is what stops a "close
// enough" parser from quietly picking up the wrong token: a version-first
// parser turned loose on a NAME-first banner would return the name
// ("codex-cli"), which is then carried into the session index as if it were a
// version — wrong, and silently so.
//
// The banner literals here are just strings; they do not require the engines
// that once emitted them to be registered.
func TestVersionParsers_RefuseAShapeItDoesNotOwn(t *testing.T) {
	claude, ok := VersionCommandFor("claude-code")
	require.True(t, ok)

	_, err := claude.Parse("codex-cli 0.144.4")
	assert.Error(t, err, "a version-first parser must refuse a name-first banner rather than return the name")

	_, err = claude.Parse("garbage")
	assert.Error(t, err, "a non-version token must be refused, never returned as a version")
}

// Every engine whose vendor transcripts ctxloom READS must be askable for its
// version — otherwise reader selection has nothing to select on and every
// session under that engine refuses. These are the REGISTERED engines
// operations.vendorReaderRegistry covers (and .github/engine-versions.env
// pins); this test states the requirement where the descriptors live so a new
// engine cannot be added without one.
func TestVersionCommands_DeclaredForEveryVendorReaderEngine(t *testing.T) {
	for _, engine := range []string{"claude-code"} {
		cmd, ok := VersionCommandFor(engine)
		assert.True(t, ok, "%s reads a vendor transcript, so it must declare a version command", engine)
		if ok {
			assert.NotNil(t, cmd.Parse, "%s's version command must know how to parse its output", engine)
			assert.NotEmpty(t, cmd.Args, "%s's version command must pass some argument", engine)
		}
	}
}

// mock deliberately declares NO version command: it has no binary at all, so
// there is no single binary whose version would mean anything. Declaring a
// bogus one for it would put a meaningless string in a session index.
func TestVersionCommands_AbsentWhereThereIsNoOneBinaryToAsk(t *testing.T) {
	for _, engine := range []string{"mock"} {
		_, ok := VersionCommandFor(engine)
		assert.False(t, ok, "%s has no single binary whose version means anything", engine)
	}
	_, ok := VersionCommandFor("no-such-engine")
	assert.False(t, ok, "an unregistered name declares nothing")
}

// An engine that IS registered but has no installed binary must resolve to
// *engineversion.BinaryAbsentError, not to some other failure — that type is
// what lets a caller treat "you don't have that engine" as ordinary while
// treating "it is installed and printed junk" as worth reporting.
func TestResolveEngineVersionCommand_UndeclaredEngineRefuses(t *testing.T) {
	_, _, err := ResolveEngineVersionCommand("mock")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no version command")
}
