package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
)

func hookOrderP(v int) *int { return &v }

// TestExtractHooksFromBundle_OrderFieldSequencesWithinAnEvent is what stops
// BundleHook.Order from being a field nobody reads — the exact failure that got
// its first draft retracted, and this project's characteristic bug: a key that
// looks honoured, encodes fine, and is silently ignored at the only moment it
// matters.
//
// Twelve hooks, because ten is where any width or lexical-sort bug shows.
func TestExtractHooksFromBundle_OrderFieldSequencesWithinAnEvent(t *testing.T) {
	// Declared in one order, ordered into the REVERSE of it.
	var in []bundles.BundleHook
	for i := 0; i < 12; i++ {
		in = append(in, bundles.BundleHook{
			Type:    "command",
			Command: string(rune('a' + i)),
			Order:   hookOrderP((12 - i) * 100),
		})
	}
	got := extractHooksFromBundle(bundles.ProjectAuthoredRead("fixture", &bundles.Bundle{Hooks: bundles.BundleHooks{PreTool: in}}), "src", bundles.AdmitAll())

	require.Len(t, got.PreTool, 12)
	var cmds []string
	for _, h := range got.PreTool {
		cmds = append(cmds, h.Command)
	}
	assert.Equal(t, []string{"l", "k", "j", "i", "h", "g", "f", "e", "d", "c", "b", "a"}, cmds,
		"the order field must sequence hooks within an event; declaration position must not")
}

// A bundle where NOTHING declares an order must resolve exactly as it does today:
// authored position, unchanged. This is the whole compatibility story for every
// bundle written before the field existed, and it is why absent sorts last rather
// than as zero — absent hooks stay in a stable authored run.
func TestExtractHooksFromBundle_NoDeclaredOrderKeepsAuthoredPosition(t *testing.T) {
	in := []bundles.BundleHook{
		{Type: "command", Command: "zulu"},
		{Type: "command", Command: "alpha"},
		{Type: "command", Command: "mike"},
	}
	got := extractHooksFromBundle(bundles.ProjectAuthoredRead("fixture", &bundles.Bundle{Hooks: bundles.BundleHooks{PreTool: in}}), "src", bundles.AdmitAll())

	var cmds []string
	for _, h := range got.PreTool {
		cmds = append(cmds, h.Command)
	}
	assert.Equal(t, []string{"zulu", "alpha", "mike"}, cmds,
		"with no order declared anywhere, resolution must be authored position — byte-for-byte today's behaviour")
}

// A hook that declares an order runs before every hook that declares none, in the
// SAME event, whatever the authored positions. Sorting an undeclared hook first
// would let a bundle's legacy hooks silently overtake the ones its author had
// just sequenced.
func TestExtractHooksFromBundle_DeclaredOrderBeatsUndeclared(t *testing.T) {
	in := []bundles.BundleHook{
		{Type: "command", Command: "legacy-first"},
		{Type: "command", Command: "legacy-second"},
		{Type: "command", Command: "sequenced", Order: hookOrderP(900000)},
	}
	got := extractHooksFromBundle(bundles.ProjectAuthoredRead("fixture", &bundles.Bundle{Hooks: bundles.BundleHooks{PreTool: in}}), "src", bundles.AdmitAll())

	var cmds []string
	for _, h := range got.PreTool {
		cmds = append(cmds, h.Command)
	}
	assert.Equal(t, []string{"sequenced", "legacy-first", "legacy-second"}, cmds)
}

// TestExtractHooksFromBundle_GateRefsStayAuthoredIndex is the trust-side
// invariant. A hook's trust identity on this path is "<bundle>#hooks/<event>/
// <authored-index>", shared with the migration baseline and every recorded grant.
// If reordering renumbered the gate refs, every existing hook approval would
// silently stop matching the hook it was granted for — a trust decision quietly
// reattached to different bytes.
func TestExtractHooksFromBundle_GateRefsStayAuthoredIndex(t *testing.T) {
	seen := map[string]string{}
	in := []bundles.BundleHook{
		{Type: "command", Command: "runs-last", Order: hookOrderP(900)},
		{Type: "command", Command: "runs-first", Order: hookOrderP(100)},
	}
	got := extractHooksFromBundle(
		bundles.ProjectAuthoredRead("fixture", &bundles.Bundle{Hooks: bundles.BundleHooks{PreTool: in}}),
		"remote/tools", recordingGate(seen))

	require.Len(t, got.PreTool, 2)
	assert.Equal(t, "runs-first", got.PreTool[0].Command, "resolution still honours order")

	// "runs-last" is authored at index 0 and must be gated as index 0.
	authored0 := bundles.BundleHook{Type: "command", Command: "runs-last", Order: hookOrderP(900)}
	payload, err := authored0.ContentPayload()
	require.NoError(t, err)
	assert.Equal(t, bundles.HashPayload(payload), seen["remote/tools#hooks/pre_tool/0"],
		"the gate ref must key on AUTHORED index, not resolved position, or every recorded hook grant detaches")
}

// A gate DENIAL must not shift anyone's identity either: the survivors keep their
// declared sequence, and the denied hook simply is not there.
func TestExtractHooksFromBundle_DenialDoesNotDisturbRemainingOrder(t *testing.T) {
	in := []bundles.BundleHook{
		{Type: "command", Command: "third", Order: hookOrderP(300)},
		{Type: "command", Command: "denied", Order: hookOrderP(200)},
		{Type: "command", Command: "first", Order: hookOrderP(100)},
	}
	got := extractHooksFromBundle(
		bundles.ProjectAuthoredRead("fixture", &bundles.Bundle{Hooks: bundles.BundleHooks{PreTool: in}}),
		"remote/tools", recordingGate(nil, "#hooks/pre_tool/1"))

	var cmds []string
	for _, h := range got.PreTool {
		cmds = append(cmds, h.Command)
	}
	assert.Equal(t, []string{"first", "third"}, cmds)
}

// Order is ctxloom's scheduling bookkeeping, not part of what the hook DOES, and
// it is CONSUMED here: extractHooksFromBundle resolves the sequence and the
// resolved slice IS the answer. It must not travel any further.
//
// That matters because wire.Hook is serialized straight into a backend's settings
// file (.claude/settings.json and friends). A key there is our bookkeeping leaking
// into a foreign schema whose owner did not agree to it and may reject it — and
// the obvious "just carry the field through" refactor is exactly what this pins
// against.
func TestExtractHooksFromBundle_OrderIsConsumedAndNeverSerialized(t *testing.T) {
	in := []bundles.BundleHook{{Type: "command", Command: "x", Order: hookOrderP(4242)}}
	got := extractHooksFromBundle(bundles.ProjectAuthoredRead("fixture", &bundles.Bundle{Hooks: bundles.BundleHooks{PreTool: in}}), "src", bundles.AdmitAll())
	require.Len(t, got.PreTool, 1)

	encoded, err := json.Marshal(got.PreTool[0])
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "order",
		"the order field leaked into a hook's serialized form:\n%s", encoded)
	assert.NotContains(t, string(encoded), "4242",
		"the order VALUE leaked into a hook's serialized form:\n%s", encoded)

	yamlEncoded, err := yaml.Marshal(got.PreTool[0])
	require.NoError(t, err)
	assert.NotContains(t, string(yamlEncoded), "4242",
		"the order VALUE leaked into a hook's YAML form:\n%s", yamlEncoded)
}
