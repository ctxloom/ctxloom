// Package cliversion is the cross-binary version contract for the ctxloom
// family. Every binary (ctxloom, ltk, taskloom, …) exposes
// `<binary> version --format json` as {"name","version"}; ctxloom probes its
// companions at boot by parsing exactly this shape, so the struct lives here as
// the single source of truth rather than being re-declared per binary.
package cliversion

import (
	"encoding/json"
	"fmt"
	"io"
)

// Info is the machine-readable shape of `<binary> version --format json`.
type Info struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Render writes info to w in the given format: "text" (or "") prints the bare
// version line; "json" prints indented {name,version}. Any other format is an
// error naming the supported set.
func Render(w io.Writer, info Info, format string) error {
	switch format {
	case "", "text":
		_, err := fmt.Fprintln(w, info.Version)
		return err
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	default:
		return fmt.Errorf("unknown format %q (supported: text, json)", format)
	}
}
