//go:build memory && vectors && onnx && !linux

package onnxlib

// extractLibrary extracts the embedded ONNX runtime to a temp file.
func extractLibrary() (string, error) {
	return extractToTemp()
}

// cleanup removes the temp file.
func cleanup() {
	cleanupTemp()
}
