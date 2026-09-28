package isolation

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// wantPUIDArg is the PUID flag a run on this host carries.
func wantPUIDArg() string {
	uid, _ := runIdentity()
	return fmt.Sprintf("-e PUID=%d", uid)
}

// imageUserID is what the baked user layer spells out, so the fallback
// identity and the image cannot drift apart.
func TestImageUserID_IsTheBakedUser(t *testing.T) {
	id := strconv.Itoa(imageUserID)
	assert.Contains(t, overlayUserLayer, "useradd -o -m -u "+id+" -g "+id+" ")
	assert.Contains(t, overlayUserLayer, "groupadd -o -g "+id+" ")
}

// Whatever the host, the remap identity is a real uid: the entrypoint has
// nothing to drop to for a negative one.
func TestIdentityEnvArgs_NeverNegative(t *testing.T) {
	argv := strings.Join(identityEnvArgs(), " ")
	assert.NotContains(t, argv, "=-")
	assert.Contains(t, argv, strings.TrimPrefix(wantPUIDArg(), "-e "))
}
