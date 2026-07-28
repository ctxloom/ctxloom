// Package cliversion is the cross-binary version contract for the ctxloom
// family. Every binary (ctxloom, ltk, taskloom, …) exposes
// `<binary> version --format json` as {"name","version"}; ctxloom probes its
// companions at boot by parsing exactly this shape, so the struct lives here as
// the single source of truth rather than being re-declared per binary.
package cliversion

// Info is the machine-readable shape of `<binary> version --format json`.
type Info struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
