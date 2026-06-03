package bundles

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestBundleWarner_DedupesPerRef verifies a ref warns once even across repeated
// calls (startup assembles context more than once via independent loaders).
func TestBundleWarner_DedupesPerRef(t *testing.T) {
	var buf strings.Builder
	w := newBundleWarner(&buf)
	err := errors.New("bundle not found")

	w.unresolved("personal/core-practices", err)
	w.unresolved("personal/core-practices", err)
	w.unresolved("personal/developer-mindset", err)

	out := buf.String()
	assert.Equal(t, 1, strings.Count(out, `bundle "personal/core-practices"`), "repeated ref must warn once")
	assert.Equal(t, 1, strings.Count(out, `bundle "personal/developer-mindset"`))
	assert.Equal(t, 2, strings.Count(out, "skipping unresolved bundle"))
}
