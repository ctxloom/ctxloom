// Remote type tests.
package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Note: TestItemType_DirName and TestItemType_Plural are in browse_test.go

func TestItemType_DirName_CustomType(t *testing.T) {
	// Custom types should pluralize for directory naming consistency
	assert.Equal(t, "customs", ItemType("custom").DirName())
}
