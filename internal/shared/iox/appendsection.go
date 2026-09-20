package iox

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
)

// AppendSection writes text after the file's current bytes, a blank line
// between, creating the file when there is none; a trailing newline ends
// it. What stood in the file is left byte for byte ahead of the section —
// the writer that appends owns only what it appended, and whoever keeps
// the record of that write is what takes it back out.
func AppendSection(fs afero.Fs, path string, text []byte, perm os.FileMode) error {
	current, err := afero.ReadFile(fs, path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	body := bytes.TrimRight(current, "\n")
	if len(body) > 0 {
		body = append(body, '\n', '\n')
	}
	body = append(body, bytes.TrimRight(text, "\n")...)
	body = append(body, '\n')
	if err := fs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return WriteFileAtomicFs(fs, path, body, perm)
}
