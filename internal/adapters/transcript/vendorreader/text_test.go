package vendorreader

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
