// Package safefs is a stub of the write library, carrying only what the
// write-discipline fixtures call: its name ends in "fs", which is the
// spelling a syntax-only rule would mistake for an afero.Fs receiver.
package safefs

import "github.com/spf13/afero"

// Create stands in for safefs.Create.
func Create(fs afero.Fs, name string) (afero.File, error) { return fs.Create(name) }

// WriteFile stands in for safefs.WriteFile.
func WriteFile(afero.Fs, string, []byte, uint32) error { return nil }
