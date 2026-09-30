package termui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPresentPolicy_DefaultsAndFloors(t *testing.T) {
	assert.Equal(t, PresentPolicy{QuietFor: 1500 * time.Millisecond, ArmFor: 750 * time.Millisecond},
		PresentPolicy{}.normalized(), "zero takes the ruled defaults")
	assert.Equal(t, PresentPolicy{QuietFor: 500 * time.Millisecond, ArmFor: 300 * time.Millisecond},
		PresentPolicy{QuietFor: time.Millisecond, ArmFor: time.Millisecond}.normalized(),
		"a duration under the floor is raised to it, never honoured")
	assert.Equal(t, PresentPolicy{QuietFor: 2 * time.Second, ArmFor: time.Second},
		PresentPolicy{QuietFor: 2 * time.Second, ArmFor: time.Second}.normalized())
}
