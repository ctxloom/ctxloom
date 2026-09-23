//go:build acceptance

package acceptance

import "testing"

// An EMPTY section is followed directly by the next marker line, with no
// content line between them. The parser must stop there and return "" —
// returning the next marker as content made every "no credential file"
// assertion read a copied credential that was never there.
func TestIsoParseSpySection_EmptySectionIsEmpty(t *testing.T) {
	body := "===ENV===\nA=1\n===CLAUDE_CONFIG_DIR_CREDS===\n===CODEX_HOME_CREDS===\n===CONFIG_HOME_LISTING===\nDIR /x\n"
	cases := map[string]string{
		"===ENV===":                     "A=1",
		"===CLAUDE_CONFIG_DIR_CREDS===": "",
		"===CODEX_HOME_CREDS===":        "",
		"===CONFIG_HOME_LISTING===":     "DIR /x",
		"===ABSENT===":                  "",
	}
	for marker, want := range cases {
		if got := isoParseSpySection(body, marker); got != want {
			t.Errorf("isoParseSpySection(%s) = %q, want %q", marker, got, want)
		}
	}
}
