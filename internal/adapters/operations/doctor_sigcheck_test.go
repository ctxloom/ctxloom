package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
)

func TestDoctorCheckSigCheck_WarnsWhenTheInvocationWaivedIt(t *testing.T) {
	c := doctorCheckSigCheck(true, false, nil)
	assert.Equal(t, doctorSigCheckMarker, c.Marker)
	assert.Equal(t, DoctorWarn, c.Status, "a waived check is never reported as fine")
	assert.Equal(t, bundles.SigCheckDisabledNotice, c.Detail)
	assert.Contains(t, c.Remedy, "--"+bundles.SigCheckFlag)
	assert.Contains(t, c.Remedy, bundles.SigCheckEnv)
}

func TestDoctorCheckSigCheck_OKWhenEnforced(t *testing.T) {
	c := doctorCheckSigCheck(false, false, nil)
	assert.Equal(t, DoctorOK, c.Status)
	assert.Empty(t, c.Remedy)
}

// The owner accepted that the waiver also hides tampering of an installed
// signed tree, on the condition that doctor names every tree it hid.
func TestDoctorCheckSigCheck_NamesEditedSignedTreesTheWaiverAccepted(t *testing.T) {
	c := doctorCheckSigCheck(true, false, []string{"acme/a", "acme/b"})
	assert.Equal(t, DoctorWarn, c.Status)
	assert.Contains(t, c.Detail, bundles.SigCheckDisabledNotice)
	assert.Contains(t, c.Detail, bundles.EditedSignedTreeWords)
	assert.Contains(t, c.Detail, "acme/a, acme/b")
}

// Run from inside a waived session — its shell, a delegated agent's shell —
// doctor is a new invocation and verifies, but the session it diagnoses does
// not: the row says so rather than reporting "enforced" as if that were the
// whole story.
func TestDoctorCheckSigCheck_NamesTheWaiverOfTheSessionItRunsIn(t *testing.T) {
	c := doctorCheckSigCheck(false, true, nil)
	assert.Equal(t, DoctorWarn, c.Status, "a waived session is never reported as fine")
	assert.Equal(t, bundles.SessionSigCheckNotice, c.Detail)
}
