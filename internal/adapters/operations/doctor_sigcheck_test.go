package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
)

func TestDoctorCheckSigCheck_WarnsWhenTheInvocationWaivedIt(t *testing.T) {
	c := doctorCheckSigCheck(true, nil)
	assert.Equal(t, doctorSigCheckMarker, c.Marker)
	assert.Equal(t, DoctorWarn, c.Status, "a waived check is never reported as fine")
	assert.Equal(t, bundles.SigCheckDisabledNotice, c.Detail)
	assert.Contains(t, c.Remedy, "--"+bundles.SigCheckFlag)
	assert.Contains(t, c.Remedy, bundles.SigCheckEnv)
}

func TestDoctorCheckSigCheck_OKWhenEnforced(t *testing.T) {
	c := doctorCheckSigCheck(false, nil)
	assert.Equal(t, DoctorOK, c.Status)
	assert.Empty(t, c.Remedy)
}

// The owner accepted that the waiver also hides tampering of an installed
// signed tree, on the condition that doctor names every tree it hid.
func TestDoctorCheckSigCheck_NamesEditedSignedTreesTheWaiverAccepted(t *testing.T) {
	c := doctorCheckSigCheck(true, []string{"acme/a", "acme/b"})
	assert.Equal(t, DoctorWarn, c.Status)
	assert.Contains(t, c.Detail, bundles.SigCheckDisabledNotice)
	assert.Contains(t, c.Detail, bundles.EditedSignedTreeWords)
	assert.Contains(t, c.Detail, "acme/a, acme/b")
}
