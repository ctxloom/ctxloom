package procsec_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/procsec"
)

// TestHardenAgainstSameUIDInspection_BypassIsReportedNotSilent pins the two
// halves of the bypass contract that hold on every platform: the env var wins
// over the mechanism, and the outcome it produces is one that Diagnostic
// refuses to stay silent about.
func TestHardenAgainstSameUIDInspection_BypassIsReportedNotSilent(t *testing.T) {
	t.Setenv(procsec.EnvAllowProcessInspection, "1")

	applied, reason, err := procsec.HardenAgainstSameUIDInspection()

	require.NoError(t, err, "a deliberate bypass is not an error")
	require.False(t, applied)
	require.Equal(t, procsec.ReasonBypassed, reason)
	require.NotEmpty(t, procsec.Diagnostic(reason, err),
		"a bypass that prints nothing is indistinguishable from hardening that silently failed")
}

// TestHardenAgainstSameUIDInspection_OnlyOneIsATrueBypass pins that values
// other than "1" leave the hardening in force — an env var that happens to be
// present must not be a bypass.
func TestHardenAgainstSameUIDInspection_OnlyOneIsATrueBypass(t *testing.T) {
	for _, value := range []string{"", "0", "true", "yes", "2"} {
		t.Run("value="+value, func(t *testing.T) {
			t.Setenv(procsec.EnvAllowProcessInspection, value)

			_, reason, err := procsec.HardenAgainstSameUIDInspection()

			require.NoError(t, err)
			require.NotEqual(t, procsec.ReasonBypassed, reason)
		})
	}
}

// TestDiagnostic pins WHICH outcomes speak and WHAT they say. The silence
// cases matter as much as the loud ones: a warning that fires on every run of
// every platform is one nobody reads, so the two outcomes that leave no /proc
// exposure must produce no line at all.
func TestDiagnostic(t *testing.T) {
	tests := []struct {
		name         string
		reason       string
		err          error
		wantSilence  bool
		wantContains []string
	}{
		{
			name:        "hardening applied says nothing",
			reason:      procsec.ReasonHardened,
			wantSilence: true,
		},
		{
			name:        "platform without proc environ says nothing",
			reason:      procsec.ReasonNoProcExposure,
			wantSilence: true,
		},
		{
			name:   "bypass names the switch, the file and the secret",
			reason: procsec.ReasonBypassed,
			wantContains: []string{
				procsec.EnvAllowProcessInspection + "=1",
				"/environ",
				"CTXLOOM_COORD_CRED",
				"this uid",
			},
		},
		{
			name:   "mechanism failure names the cause and stays a warning",
			reason: procsec.ReasonMechanismFailed,
			err:    errors.New("prctl(PR_SET_DUMPABLE, 0): operation not permitted"),
			wantContains: []string{
				"PR_SET_DUMPABLE",
				"operation not permitted",
				"/environ",
				"CTXLOOM_COORD_CRED",
			},
		},
		{
			name:        "an unknown reason says nothing rather than guessing",
			reason:      "some-future-reason",
			wantSilence: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := procsec.Diagnostic(tc.reason, tc.err)

			if tc.wantSilence {
				require.Empty(t, got)
				return
			}
			require.NotEmpty(t, got)
			for _, want := range tc.wantContains {
				require.Contains(t, got, want)
			}
			require.False(t, strings.HasSuffix(got, "\n"),
				"Diagnostic returns a message body; the reporting channel owns the line ending")
		})
	}
}
