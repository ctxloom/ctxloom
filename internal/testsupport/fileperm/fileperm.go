// Package fileperm asserts a file's permission bits as far as the host OS
// stores them, so a test of a portable writer can pin its mode everywhere,
// and asserts a path is owner-only in the host OS's own terms.
//
// OwnerOnly reads the platform's access control directly rather than through
// the production owner-only seam, so a test using it is an independent check
// of that seam, not the seam grading itself.
package fileperm
