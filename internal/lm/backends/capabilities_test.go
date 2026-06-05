package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCodex_History(t *testing.T) {
	backend := NewCodex()
	history := backend.History()
	assert.NotNil(t, history)
}

func TestMock_History(t *testing.T) {
	backend := NewMock()
	history := backend.History()
	// Mock returns a NilSessionHistory (stub that returns empty/nil for all methods)
	assert.NotNil(t, history)
}
