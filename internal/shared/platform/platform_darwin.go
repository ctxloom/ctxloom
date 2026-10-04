package platform

import "github.com/ctxloom/ctxloom/internal/shared/platform/darwin"

const (
	containersInVM = true
	loginShell     = true
	linuxHost      = false
	name           = "macOS"
)

var current Host = darwin.OS{}
