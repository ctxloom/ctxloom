//go:build !schemagen

// This stub gives cmd/gen-schemas a buildable package for ordinary
// `go build ./...` runs; the real generator is build-tagged `schemagen` (see
// main.go) so its reflection dependency never enters the production toolchain.
package main

func main() {}
