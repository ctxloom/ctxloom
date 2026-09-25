package cli

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// A setup launch refused with its own remedy (a delivery refusal naming the
// binding change) must keep that remedy; only a failure that names none
// gets init's generic one.
func TestReportSetupLaunchFailure_KeepsTheLaunchErrorsRemedy(t *testing.T) {
	restore := clidiag.SetSink(new(strings.Builder))
	t.Cleanup(func() { restore(); clidiag.ResetWarnOnce(); strictness.Reset() })

	const fix = "select a root the cell provides"
	err := reportSetupLaunchFailure(fmt.Errorf("launch: %w", delivery.Unrootable{Approach: "file", Fix: fix}))
	require.Error(t, err)
	got, _ := clifmt.RemedyOf(err)
	assert.Equal(t, fix, got)

	err = reportSetupLaunchFailure(errors.New("engine exited"))
	require.Error(t, err)
	got, _ = clifmt.RemedyOf(err)
	assert.Equal(t, setupLaunchRemedy, got)
}
