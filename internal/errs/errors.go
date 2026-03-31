// Package errs provides sentinel errors for common failure cases.
// These errors enable programmatic error handling with errors.Is().
package errs

import "errors"

// NotFound errors indicate that a requested resource doesn't exist.
var (
	// ErrBundleNotFound indicates a bundle could not be located.
	ErrBundleNotFound = errors.New("bundle not found")

	// ErrFragmentNotFound indicates a fragment could not be located.
	ErrFragmentNotFound = errors.New("fragment not found")

	// ErrPromptNotFound indicates a prompt could not be located.
	ErrPromptNotFound = errors.New("prompt not found")

	// ErrProfileNotFound indicates a profile could not be located.
	ErrProfileNotFound = errors.New("profile not found")

	// ErrRemoteNotFound indicates a remote could not be located.
	ErrRemoteNotFound = errors.New("remote not found")
)

// Configuration errors indicate problems with configuration.
var (
	// ErrCircularInheritance indicates a cycle in profile inheritance.
	ErrCircularInheritance = errors.New("circular profile inheritance detected")

	// ErrInvalidReference indicates a malformed bundle/profile reference.
	ErrInvalidReference = errors.New("invalid reference")
)
