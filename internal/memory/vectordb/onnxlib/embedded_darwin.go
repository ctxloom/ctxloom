//go:build memory && vectors && onnx && darwin

package onnxlib

import (
	_ "embed"
)

// onnxRuntimeLib contains the embedded ONNX runtime dylib for macOS.
// This file is populated during build by copying libonnxruntime.dylib to this directory.
//
//go:embed libonnxruntime.dylib
var onnxRuntimeLib []byte

// libraryName returns the platform-specific library filename.
func libraryName() string {
	return "libonnxruntime.dylib"
}
