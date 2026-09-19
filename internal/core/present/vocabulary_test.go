package present

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The surface vocabulary: Kind is the closed set of surface categories, Traits
// the declared facts about one approach, RootKind the roots an approach can
// write under. Pinned here so the engine base, delivery and launch read one
// declaration instead of each keeping a spelling.

func TestKind_ClosedSet_RendersStableLabels(t *testing.T) {
	want := map[Kind]string{
		Context:  "context",
		MCP:      "mcp",
		Settings: "settings",
		Commands: "commands",
		Skills:   "skills",
	}
	for k, label := range want {
		assert.Equal(t, label, k.String())
	}
	assert.Equal(t, "unknown", Kind(99).String(), "an out-of-range kind renders as unknown, never as a member")
}

func TestRootKind_ThreeRoots_AreDistinct(t *testing.T) {
	roots := []RootKind{RootSessionHome, RootProjectRoot, RootWorkDir}
	seen := map[RootKind]bool{}
	for _, r := range roots {
		assert.NotZero(t, r, "the zero RootKind means 'no root named'")
		assert.False(t, seen[r], "%v declared twice", r)
		seen[r] = true
	}
}

func TestTraits_Offers_ReadsTheDeclaredRoots(t *testing.T) {
	tr := Traits{Roots: []RootKind{RootSessionHome, RootProjectRoot}, Channel: ChannelFile}
	assert.True(t, tr.Offers(RootSessionHome))
	assert.True(t, tr.Offers(RootProjectRoot))
	assert.False(t, tr.Offers(RootWorkDir))
	assert.False(t, Traits{}.Offers(RootSessionHome), "no roots declared offers nothing")
}

func TestChannel_ThreeWaysToTellTheEngine(t *testing.T) {
	chans := []Channel{ChannelFile, ChannelArgv, ChannelEnv}
	seen := map[Channel]bool{}
	for _, c := range chans {
		assert.NotZero(t, c)
		assert.False(t, seen[c])
		seen[c] = true
	}
}
