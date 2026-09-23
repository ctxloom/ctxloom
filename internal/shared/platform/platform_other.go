//go:build !linux && !darwin && !windows

package platform

const (
	containersInVM      = false
	keychainCredentials = false
	loginShell          = true
	linuxHost           = false
	tempBase            = ""
)
