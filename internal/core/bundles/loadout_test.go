package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// TestParseLoadout_RunAndInitAreTyped pins the loadout document's shape: ONE
// signed document carrying TWO loadouts with different lifecycles. `run:` is
// the bundle a session consumes every time (fragments, commands, hooks, MCP
// servers); `init:` is consumed once at setup, and its fields are TYPED —
// setup guidance, tooling, where the companion's pre-existing context lives,
// and the questions init should put to the human — not commands with a
// well-known name that silently no-op on a typo.
func TestParseLoadout_RunAndInitAreTyped(t *testing.T) {
	doc := []byte(`run:
  version: 1.2.0
  fragments:
    ltk:
      content: RUN-FRAGMENT
init:
  setup_guidance: SETUP-GUIDANCE
  tooling: TOOLING-TEXT
  legacy_context:
    - .ltk/config.yaml
  questions:
    - id: task-runner
      prompt: Which task runner does this project use?
`)
	lo, err := ParseLoadout(doc)
	require.NoError(t, err)

	require.NotNil(t, lo.Run, "the RUN loadout is the bundle")
	assert.Equal(t, "1.2.0", lo.Run.Version)
	assert.Equal(t, "RUN-FRAGMENT", lo.Run.Fragments["ltk"].Content)

	assert.Equal(t, "SETUP-GUIDANCE", lo.Init.SetupGuidance)
	assert.Equal(t, "TOOLING-TEXT", lo.Init.Tooling)
	assert.Equal(t, []string{".ltk/config.yaml"}, lo.Init.LegacyContext)
	require.Len(t, lo.Init.Questions, 1)
	assert.Equal(t, InitQuestion{ID: "task-runner", Prompt: "Which task runner does this project use?"}, lo.Init.Questions[0])
}

// TestParseLoadout_RunOnlyHasEmptyInit: a companion contributing nothing at
// setup omits `init:` entirely; its INIT loadout is the zero value, and
// IsZero says so, so a consumer can skip it without inspecting every field.
func TestParseLoadout_RunOnlyHasEmptyInit(t *testing.T) {
	lo, err := ParseLoadout([]byte("run:\n  version: 1.0.0\n"))
	require.NoError(t, err)
	assert.True(t, lo.Init.IsZero())
	assert.Equal(t, "1.0.0", lo.Run.Version)
}

// TestParseLoadout_InitOnlyHasEmptyRun: the other direction — a companion
// that only speaks at setup still parses, to an empty RUN bundle rather than
// a nil one, so a reader can seed it without a nil check at every use.
func TestParseLoadout_InitOnlyHasEmptyRun(t *testing.T) {
	lo, err := ParseLoadout([]byte("init:\n  setup_guidance: ONLY-AT-SETUP\n"))
	require.NoError(t, err)
	require.NotNil(t, lo.Run)
	assert.Empty(t, lo.Run.Fragments)
	assert.Equal(t, "ONLY-AT-SETUP", lo.Init.SetupGuidance)
}

// TestParseLoadout_RefusesUnknownKeys is the whole reason the INIT loadout is
// typed: a misspelled field must fail loud at parse, at every level of the
// document, never load clean and contribute nothing.
func TestParseLoadout_RefusesUnknownKeys(t *testing.T) {
	for name, doc := range map[string]string{
		"top-level":       "run:\n  version: 1.0.0\nsetup_guidance: STRAY\n",
		"inside-init":     "init:\n  setup_guidence: TYPO\n",
		"inside-run":      "run:\n  version: 1.0.0\n  fragmentz: {}\n",
		"inside-question": "init:\n  questions:\n    - id: q\n      promt: TYPO\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseLoadout([]byte(doc))
			require.Error(t, err)
		})
	}
}

// TestParseLoadout_RefusesBareBundle: a v1-shaped payload (a bundle document
// at the top level, no run:/init: split) is NOT a loadout document. Reading
// it as one would silently deliver an empty RUN loadout — the clean break
// must be loud at the parser too, not only at the envelope's contract string.
func TestParseLoadout_RefusesBareBundle(t *testing.T) {
	_, err := ParseLoadout([]byte("version: 1.0.0\nfragments:\n  ltk:\n    content: hello\n"))
	require.Error(t, err)
}

// TestParseLoadout_RefusesEmptyDocument mirrors ParseBundle's floor: a
// document that decodes to nothing contributed nothing and must not look
// like a successfully-parsed loadout.
func TestParseLoadout_RefusesEmptyDocument(t *testing.T) {
	for _, doc := range [][]byte{nil, []byte(""), []byte("# only a comment\n")} {
		_, err := ParseLoadout(doc)
		require.Error(t, err)
	}
}

// TestParseLoadout_MCPServedByTheSessionEndpoint pins the companion's
// DYNAMIC declaration: an `mcp:` entry may say it is served by the running
// session's endpoint instead of naming a command or a URL. The declaration
// carries nothing executable — no command, no args — and the one-of rule
// wire.MCPServer.Validate enforces refuses an entry that declares both.
func TestParseLoadout_MCPServedByTheSessionEndpoint(t *testing.T) {
	lo, err := ParseLoadout([]byte(`run:
  version: 1.0.0
  mcp:
    ctxloom:
      served_by: session-endpoint
      notes: served by the session
`))
	require.NoError(t, err)
	entry := lo.Run.MCP["ctxloom"]
	assert.Equal(t, wire.ServedBySessionEndpoint, entry.ServedBy)
	assert.Empty(t, entry.Command)
	assert.Empty(t, entry.Args)
	assert.True(t, entry.AsWire().IsSessionEndpoint())

	_, err = ParseLoadout([]byte(`run:
  version: 1.0.0
  mcp:
    ctxloom:
      served_by: session-endpoint
      command: ctxloom
      args: [mcp, serve]
`))
	require.ErrorIs(t, err, wire.ErrMCPServerTwoTargets, "a dynamic entry names no command")
}
