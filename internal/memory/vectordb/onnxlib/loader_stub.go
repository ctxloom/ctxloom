//go:build !onnx || !vectors

// Package onnxlib provides embedded ONNX runtime library loading.
// This stub is used when ONNX support is not compiled in.
package onnxlib

import "errors"

var errNotCompiled = errors.New("onnx runtime not compiled in (build with -tags 'memory,vectors,onnx')")

// Init is a stub that returns an error when ONNX is not compiled in.
func Init() (string, error) {
	return "", errNotCompiled
}

// Path returns empty string when ONNX is not compiled in.
func Path() string {
	return ""
}

// Cleanup is a no-op when ONNX is not compiled in.
func Cleanup() {}

// IsEmbedded returns false when ONNX is not compiled in.
func IsEmbedded() bool {
	return false
}

// EmbeddedSize returns 0 when ONNX is not compiled in.
func EmbeddedSize() int {
	return 0
}
