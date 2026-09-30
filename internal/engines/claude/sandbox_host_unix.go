//go:build linux || darwin

package claude

// hostSandbox: claude sandboxes its commands on this OS (bubblewrap on
// Linux, Seatbelt on macOS).
const hostSandbox = true
