// Package logboot stands at a path archrules.StdStreamsAllowed names, but no
// longer reaches a standard stream there.
package logboot // want `archrules.StdStreamsAllowed names "internal/shared/logboot/logboot.go#Install"`

// Install reaches no standard stream.
func Install() {}
