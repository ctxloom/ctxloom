//go:build vectors && onnx

package onnxlib

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var (
	tempFile  string
	tempMutex sync.Mutex
)

// extractToTemp extracts the embedded library to a temp file.
// Used as fallback on Linux and primary method on other platforms.
func extractToTemp() (string, error) {
	tempMutex.Lock()
	defer tempMutex.Unlock()

	if err := validateLibrary(); err != nil {
		return "", err
	}

	// Create temp directory in user cache if possible
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}

	libDir := filepath.Join(cacheDir, "scm", "lib")
	if err := os.MkdirAll(libDir, 0755); err != nil {
		// Fall back to system temp
		libDir = os.TempDir()
	}

	// Create the library file (platform-specific name)
	libPath := filepath.Join(libDir, libraryName())

	// Check if it already exists and is valid
	if info, err := os.Stat(libPath); err == nil {
		if info.Size() == int64(len(onnxRuntimeLib)) {
			// File exists and is the right size, assume it's valid
			tempFile = libPath
			return libPath, nil
		}
		// Wrong size, remove it
		os.Remove(libPath)
	}

	// Write the library
	f, err := os.OpenFile(libPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return "", fmt.Errorf("failed to create library file: %w", err)
	}
	defer f.Close()

	n, err := f.Write(onnxRuntimeLib)
	if err != nil {
		os.Remove(libPath)
		return "", fmt.Errorf("failed to write library: %w", err)
	}
	if n != len(onnxRuntimeLib) {
		os.Remove(libPath)
		return "", fmt.Errorf("incomplete write: %d of %d bytes", n, len(onnxRuntimeLib))
	}

	tempFile = libPath
	return libPath, nil
}

// cleanupTemp removes the temp file if it exists.
func cleanupTemp() {
	tempMutex.Lock()
	defer tempMutex.Unlock()

	if tempFile != "" {
		os.Remove(tempFile)
		tempFile = ""
	}
}
