// Package cobrafmt is the json-tags rule's stand-in for clifmt's cobra
// adapter; the command is a local type so the fixture needs no cobra.
package cobrafmt

import "github.com/ctxloom/ctxloom/pkg/clifmt"

// Command stands in for *cobra.Command.
type Command struct{}

// Emit is the adapter's sink.
func Emit(cmd *Command, data any, opts ...clifmt.Option) error { return nil }
