package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
)

func TestDoctorCheckSigCheck_WarnsWhenTheInvocationWaivedIt(t *testing.T) {
	c := doctorCheckSigCheck(true)
	assert.Equal(t, doctorSigCheckMarker, c.Marker)
	assert.Equal(t, DoctorWarn, c.Status, "a waived check is never reported as fine")
	assert.Equal(t, bundles.SigCheckDisabledNotice, c.Detail)
	assert.Contains(t, c.Remedy, "--"+bundles.SigCheckFlag)
	assert.Contains(t, c.Remedy, bundles.SigCheckEnv)
}

func TestDoctorCheckSigCheck_OKWhenEnforced(t *testing.T) {
	c := doctorCheckSigCheck(false)
	assert.Equal(t, DoctorOK, c.Status)
	assert.Empty(t, c.Remedy)
}
