package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCLIContextProvider_Provide(t *testing.T) {
	provider := &CLIContextProvider{}

	fragments := []*Fragment{
		{Content: "Fragment one"},
		{Content: "Fragment two"},
	}

	err := provider.Provide("/test", fragments)
	assert.NoError(t, err)
	assert.Contains(t, provider.assembledContext, "Fragment one")
	assert.Contains(t, provider.assembledContext, "Fragment two")
}

func TestCLIContextProvider_Clear(t *testing.T) {
	provider := &CLIContextProvider{
		assembledContext: "some context",
	}

	err := provider.Clear("/test")
	assert.NoError(t, err)
	assert.Empty(t, provider.assembledContext)
}

func TestCLIContextProvider_GetAssembled(t *testing.T) {
	provider := &CLIContextProvider{
		assembledContext: "assembled content",
	}

	result := provider.GetAssembled()
	assert.Equal(t, "assembled content", result)
}

func TestAssembleFragments(t *testing.T) {
	t.Run("empty slice", func(t *testing.T) {
		result := assembleFragments([]*Fragment{})
		assert.Empty(t, result)
	})

	t.Run("nil slice", func(t *testing.T) {
		result := assembleFragments(nil)
		assert.Empty(t, result)
	})

	t.Run("single fragment", func(t *testing.T) {
		result := assembleFragments([]*Fragment{
			{Content: "Single content"},
		})
		assert.Equal(t, "Single content", result)
	})

	t.Run("multiple fragments", func(t *testing.T) {
		result := assembleFragments([]*Fragment{
			{Content: "First"},
			{Content: "Second"},
			{Content: "Third"},
		})
		assert.Contains(t, result, "First")
		assert.Contains(t, result, "Second")
		assert.Contains(t, result, "Third")
		assert.Contains(t, result, "---")
	})

	t.Run("skips empty content", func(t *testing.T) {
		result := assembleFragments([]*Fragment{
			{Content: ""},
			{Content: "Actual content"},
		})
		assert.Equal(t, "Actual content", result)
	})

	t.Run("all empty returns empty", func(t *testing.T) {
		result := assembleFragments([]*Fragment{
			{Content: ""},
			{Content: ""},
		})
		assert.Empty(t, result)
	})
}
