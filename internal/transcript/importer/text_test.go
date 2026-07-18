package importer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJoinNonEmpty(t *testing.T) {
	assert.Equal(t, "a\n\nb", JoinNonEmpty([]string{"a", "", "b"}))
	assert.Equal(t, "", JoinNonEmpty(nil))
	assert.Equal(t, "", JoinNonEmpty([]string{"", ""}))
	assert.Equal(t, "solo", JoinNonEmpty([]string{"solo"}))
}

func TestJoinNonEmptyFunc(t *testing.T) {
	type block struct{ text string }
	blocks := []block{{"a"}, {""}, {"b"}}
	got := JoinNonEmptyFunc(blocks, func(b block) string { return b.text })
	assert.Equal(t, "a\n\nb", got)

	assert.Equal(t, "", JoinNonEmptyFunc([]block(nil), func(b block) string { return b.text }))
}
