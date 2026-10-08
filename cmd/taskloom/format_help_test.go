package main

import (
	"strings"
	"testing"
)

// TestFormatFlagHelp_StatesTheDerivedDefault: with --format unset the output
// is text on a terminal and json otherwise (cobrafmt.Resolve). The help must
// say that, not advertise a fixed "text" default a piped run contradicts.
func TestFormatFlagHelp_StatesTheDerivedDefault(t *testing.T) {
	usage := rootCmd.PersistentFlags().FlagUsages()
	if strings.Contains(usage, `(default "text")`) {
		t.Errorf("--format help advertises a fixed default:\n%s", usage)
	}
	if !strings.Contains(usage, formatFlagUsage) {
		t.Errorf("--format help does not state the derived default:\n%s", usage)
	}
}
