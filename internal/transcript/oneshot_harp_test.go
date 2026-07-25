package transcript

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// U144-F15: RecordOneshot returned nil when harp == "", so a run that failed
// to mint a harp reported full capture success with ZERO bytes recorded —
// callers check only the error.
func TestRecordOneshot_NoHarpWithContentIsAnError(t *testing.T) {
	err := RecordOneshot("", "claude", "the prompt", "the output")
	assert.Error(t, err, "content that cannot be filed anywhere must not report as captured")
}

// Nothing to capture is still legitimately nothing to do.
func TestRecordOneshot_NoHarpNoContentIsFine(t *testing.T) {
	assert.NoError(t, RecordOneshot("", "claude", "   ", ""))
}
