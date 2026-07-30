package sessions

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/harp"
)

// TestGenerateUniqueHarp_DelegatesToSharedAllocator pins the session harp
// allocator to harp.UniqueFrom's contract: a collision it cannot resolve is a
// returned error, never one final unchecked name that may still collide. A
// local retry loop cannot honour that, because its signature has nowhere to
// say so.
func TestGenerateUniqueHarp_DelegatesToSharedAllocator(t *testing.T) {
	used := map[string]struct{}{}
	for range 200 {
		name, err := generateUniqueHarp(used)
		require.NoError(t, err)
		require.NoError(t, harp.Validate(name))
		_, dup := used[name]
		assert.False(t, dup, "generateUniqueHarp returned %q, already in the used set", name)
		used[name] = struct{}{}
	}
}
