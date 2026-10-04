package platform

import "github.com/ctxloom/ctxloom/internal/shared/platform/linux"

const (
	containersInVM = false
	loginShell     = true
	linuxHost      = true
	name           = "linux"
)

var current Host = linux.OS{}
