package agents

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDrivingMode(t *testing.T) {
	cases := []struct {
		in     string
		want   DrivingMode
		wantOk bool
	}{
		{"", DrivingConversational, true},
		{"conversational", DrivingConversational, true},
		{"oneshot", DrivingOneshot, true},
		{"bogus", "", false},
		{"Conversational", "", false}, // NOT lenient on case, unlike ParsePermissionMode
	}
	for _, tc := range cases {
		got, ok := parseDrivingMode(tc.in)
		assert.Equal(t, tc.wantOk, ok, "in=%q", tc.in)
		if tc.wantOk {
			assert.Equal(t, tc.want, got, "in=%q", tc.in)
		}
	}
}

// TestValidateDriving_NamesTheVocabulary pins that the refusal is actionable:
// a rejected value that does not say what IS accepted moves the cost onto the
// reader rather than paying it.
func TestValidateDriving_NamesTheVocabulary(t *testing.T) {
	require.NoError(t, ValidateDriving(""))
	require.NoError(t, ValidateDriving(DrivingOneshot))

	err := ValidateDriving("warm")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "warm")
	for _, name := range DrivingModeNames() {
		assert.Contains(t, err.Error(), name)
	}
}

// TestAgentValueVocabulary_IsClosed pins that the two closed enums this package
// owns stay enumerable — flag help, shell completion and every error message
// read them, so a value added to one and not to its Names() would be settable
// but undiscoverable.
func TestAgentValueVocabulary_IsClosed(t *testing.T) {
	assert.Equal(t, []string{"conversational", "oneshot"}, DrivingModeNames())
	assert.Equal(t, []string{ConfigHomeProject, ConfigHomeHost}, ConfigHomeNames())
}
