// Package jsonsinkplant hands an untagged struct to every structured-output
// sink, and to one function that is not a sink.
package jsonsinkplant

import (
	"io"

	"github.com/ctxloom/ctxloom/pkg/clifmt"
	"github.com/ctxloom/ctxloom/pkg/clifmt/cobrafmt"
)

type row struct {
	Name string
}

// Plant calls each sink.
func Plant(w io.Writer, cmd *cobrafmt.Command, p *clifmt.Printer) {
	_ = cobrafmt.Emit(cmd, row{})       // want `jsonsinkplant.row is rendered as structured CLI output but these exported fields have no json tag: .*jsonsinkplant.row.Name `
	_ = clifmt.Render(w, row{}, "json") // want `jsonsinkplant.row is rendered as structured CLI output`
	_ = p.Render(w, row{}, "json")      // want `jsonsinkplant.row is rendered as structured CLI output`
	_ = clifmt.Unrelated(w, row{})
}
