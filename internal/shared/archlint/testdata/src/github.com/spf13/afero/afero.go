// Package afero is a stub of github.com/spf13/afero carrying only what the
// write-discipline fixtures call.
package afero

// File stands in for afero.File.
type File interface{}

// Fs stands in for afero.Fs.
type Fs interface {
	Create(name string) (File, error)
	Rename(oldname, newname string) error
}

// MemMapFs stands in for afero.MemMapFs.
type MemMapFs struct{}

// Create stands in for MemMapFs.Create.
func (*MemMapFs) Create(string) (File, error) { return nil, nil }

// Rename stands in for MemMapFs.Rename.
func (*MemMapFs) Rename(string, string) error { return nil }

// NewMemMapFs stands in for afero.NewMemMapFs.
func NewMemMapFs() Fs { return &MemMapFs{} }

// WriteFile stands in for afero.WriteFile.
func WriteFile(Fs, string, []byte, uint32) error { return nil }
