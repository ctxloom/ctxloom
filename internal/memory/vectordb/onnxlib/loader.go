//go:build memory && vectors && onnx

// Package onnxlib provides embedded ONNX runtime library loading.
// The library is embedded in the binary and extracted to memory (Linux)
// or a temp file (other platforms) at runtime.
package onnxlib

import (
	"fmt"
	"sync"
)

var (
	initOnce  sync.Once
	initError error
	libPath   string
)

// Init extracts the embedded ONNX runtime library and returns the path.
// This must be called before using onnxruntime_go.
// On Linux, uses memfd_create for in-memory loading.
// On other platforms, extracts to a temp file.
func Init() (string, error) {
	initOnce.Do(func() {
		libPath, initError = extractLibrary()
	})
	return libPath, initError
}

// Path returns the path to the extracted library.
// Returns empty string if Init hasn't been called or failed.
func Path() string {
	return libPath
}

// Cleanup removes any temp files created during extraction.
// Safe to call multiple times. No-op on Linux (memfd auto-cleans).
func Cleanup() {
	cleanup()
}

// IsEmbedded returns true if the ONNX runtime is embedded in this build.
func IsEmbedded() bool {
	return len(onnxRuntimeLib) > 0
}

// EmbeddedSize returns the size of the embedded library in bytes.
func EmbeddedSize() int {
	return len(onnxRuntimeLib)
}

// validateLibrary does a basic sanity check on the embedded library.
func validateLibrary() error {
	if len(onnxRuntimeLib) == 0 {
		return fmt.Errorf("onnx runtime library not embedded in binary")
	}
	if len(onnxRuntimeLib) < 4 {
		return fmt.Errorf("embedded library too small")
	}

	// Check magic numbers for different platforms
	// ELF (Linux): 0x7f 'E' 'L' 'F'
	// PE (Windows): 'M' 'Z'
	// Mach-O (macOS): 0xfe 0xed 0xfa 0xce (32-bit) or 0xfe 0xed 0xfa 0xcf (64-bit)
	//                 or 0xcf 0xfa 0xed 0xfe (64-bit little-endian)
	isELF := onnxRuntimeLib[0] == 0x7f && onnxRuntimeLib[1] == 'E' &&
		onnxRuntimeLib[2] == 'L' && onnxRuntimeLib[3] == 'F'
	isPE := onnxRuntimeLib[0] == 'M' && onnxRuntimeLib[1] == 'Z'
	isMachO := (onnxRuntimeLib[0] == 0xfe && onnxRuntimeLib[1] == 0xed) ||
		(onnxRuntimeLib[0] == 0xcf && onnxRuntimeLib[1] == 0xfa) ||
		(onnxRuntimeLib[0] == 0xca && onnxRuntimeLib[1] == 0xfe) // universal binary

	if !isELF && !isPE && !isMachO {
		return fmt.Errorf("embedded library has invalid magic number (not ELF, PE, or Mach-O)")
	}
	return nil
}
