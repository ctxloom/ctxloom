//go:build windows

package iox

import (
	"errors"
	"time"

	"github.com/spf13/afero"
	"golang.org/x/sys/windows"
)

// renameRetryBudget and renameRetryMaxDelay bound Rename's wait: the Go
// toolchain's own cmd/go/internal/robustio gives the same Windows condition
// the same two seconds.
const (
	renameRetryBudget   = 2 * time.Second
	renameRetryMaxDelay = 100 * time.Millisecond
)

// Rename is fs.Rename, retried while Windows refuses it as a sharing
// conflict. Replacing a file fails with ERROR_ACCESS_DENIED or
// ERROR_SHARING_VIOLATION while ANY process holds the destination open
// without FILE_SHARE_DELETE — an engine reading the file, a virus scanner or
// indexer opening what was just written — and that holder is not ours to
// wait on. A genuine permission failure is returned once the budget is spent.
func Rename(fs afero.Fs, oldpath, newpath string) error {
	deadline := time.Now().Add(renameRetryBudget)
	delay := time.Millisecond
	for {
		err := fs.Rename(oldpath, newpath)
		if !isSharingConflict(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(delay)
		delay = min(2*delay, renameRetryMaxDelay)
	}
}

func isSharingConflict(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
