package interaction

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// An endpoint that names no timeout gets IdleSessionTimeout, never the
// go-sdk zero value, which closes no idle session at all.
func TestSessionTimeout_ZeroIsTheIdleSessionTimeout(t *testing.T) {
	assert.Equal(t, IdleSessionTimeout, Endpoint{}.sessionTimeout())
	assert.Equal(t, time.Second, Endpoint{SessionTimeout: time.Second}.sessionTimeout())
}
