//go:build !linux && !darwin

package claude

// hostSandbox: claude has no command sandbox on this OS (native Windows).
const hostSandbox = false
