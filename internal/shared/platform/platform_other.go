//go:build !linux && !darwin && !windows

package platform

const (
	containersInVM = false
	loginShell     = true
	linuxHost      = false
)
