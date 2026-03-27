//go:build !memory

package cmd

// loadSessionMemoryForHook is a stub when memory feature is disabled.
func loadSessionMemoryForHook(_ string) string {
	return ""
}
