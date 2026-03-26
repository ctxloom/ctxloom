//go:build memory && vectors && onnx && linux

package onnxlib

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// extractLibrary extracts the embedded ONNX runtime to a memfd.
// Returns the path as /proc/self/fd/N which can be used with dlopen.
func extractLibrary() (string, error) {
	if err := validateLibrary(); err != nil {
		return "", err
	}

	// Create an anonymous file in memory
	fd, err := unix.MemfdCreate(libraryName(), unix.MFD_CLOEXEC)
	if err != nil {
		// Fall back to temp file if memfd not available
		return extractToTempFile()
	}

	// Write the library to the memfd
	f := os.NewFile(uintptr(fd), libraryName())
	if f == nil {
		unix.Close(fd)
		return "", fmt.Errorf("failed to create file from memfd")
	}

	n, err := f.Write(onnxRuntimeLib)
	if err != nil {
		f.Close()
		return "", fmt.Errorf("failed to write library to memfd: %w", err)
	}
	if n != len(onnxRuntimeLib) {
		f.Close()
		return "", fmt.Errorf("incomplete write to memfd: %d of %d bytes", n, len(onnxRuntimeLib))
	}

	// Seal the memfd to prevent modification (optional security measure)
	// Note: we keep the fd open so dlopen can access it via /proc/self/fd/N

	// Return the proc path for dlopen
	path := fmt.Sprintf("/proc/self/fd/%d", fd)
	return path, nil
}

// extractToTempFile is a fallback when memfd is not available.
func extractToTempFile() (string, error) {
	return extractToTemp()
}

// cleanup is a no-op on Linux since memfd auto-cleans when fd is closed.
func cleanup() {
	// memfd is automatically cleaned up when the process exits
	// and the fd is closed
}
