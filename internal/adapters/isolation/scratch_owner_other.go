//go:build !unix

package isolation

import "io/fs"

// ownedByCurrentUser has no uid to compare against here; the per-user temp
// dir is what keeps another account's scratch out of reach instead.
func ownedByCurrentUser(fs.FileInfo) bool { return true }
