package platform

import "github.com/ctxloom/ctxloom/internal/shared/platform/windows"

const (
	containersInVM = true
	loginShell     = false
	linuxHost      = false
	name           = "Windows"
)

var current Host = windows.OS{}
