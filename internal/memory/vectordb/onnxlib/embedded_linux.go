//go:build memory && vectors && onnx && linux

package onnxlib

import (
	_ "embed"
)

// onnxRuntimeLib contains the embedded ONNX runtime shared library for Linux.
// This file is populated during build by copying libonnxruntime.so to this directory.
//
//go:embed libonnxruntime.so
var onnxRuntimeLib []byte

// libraryName returns the platform-specific library filename.
func libraryName() string {
	return "libonnxruntime.so"
}
