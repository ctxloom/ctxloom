package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// removePointers takes out exactly the named pointers, in the order given, and
// leaves every other byte of the user's file where it was.
func TestRemovePointersKeepsEveryOtherByte(t *testing.T) {
	src := "{\n    \"keep\":   [1, 2, 3],\n  \"drop\": {\"x\": true},\n\t\"tail\" : \"t\"\n}\n"

	out, err := removePointers("settings.json", []byte(src), []string{"/keep/2", "/keep/1", "/drop"})
	require.NoError(t, err)

	assert.Equal(t, "{\n    \"keep\":   [1],\n\t\"tail\" : \"t\"\n}\n", string(out))
}

func TestRemovePointersRefusesAnUnknownFormat(t *testing.T) {
	_, err := removePointers("settings.unknownext", []byte("{}"), []string{"/a"})
	require.Error(t, err)
}

func TestRemovePointersWithNothingToRemoveIsIdentity(t *testing.T) {
	src := []byte("{\"a\": 1}\n")
	out, err := removePointers("s.json", src, nil)
	require.NoError(t, err)
	assert.Equal(t, string(src), string(out))
}
