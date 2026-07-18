package clifmt

import "io"

// Renderer is the escape hatch for values the reflective default can't
// express as a human view. Render checks for it before falling back to the
// reflective/marshal path (json/yaml/toml still marshal generically, text/
// markdown still walk struct fields) for every format.
//
// RenderCLI returns handled=false to defer a given format to the default
// path, so a type can override just the formats where reflection falls
// short (typically text/markdown) while json/yaml/toml keep marshaling the
// struct generically.
type Renderer interface {
	RenderCLI(w io.Writer, f Format) (handled bool, err error)
}
