package textblocks

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJoin_DropsEmptiesAndSeparatesWithABlankLine(t *testing.T) {
	assert.Equal(t, "a\n\nb", Join("a", "", "b"))
	assert.Equal(t, "b", Join("", "b"))
	assert.Equal(t, "a", Join("a", ""))
	assert.Empty(t, Join("", ""))
	assert.Empty(t, Join())
	assert.Equal(t, "solo", Join("solo"))
}
