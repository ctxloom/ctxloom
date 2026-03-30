//go:build vectors && onnx && windows

package onnxlib

import (
	_ "embed"
)

// onnxRuntimeLib contains the embedded ONNX runtime DLL for Windows.
// This file is populated during build by copying onnxruntime.dll to this directory.
//
//go:embed onnxruntime.dll
var onnxRuntimeLib []byte

// libraryName returns the platform-specific library filename.
func libraryName() string {
	return "onnxruntime.dll"
}
