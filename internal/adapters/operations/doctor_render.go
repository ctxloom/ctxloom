package operations

import (
	"io"

	"github.com/ctxloom/ctxloom/internal/core/displaysafe"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// WriteDoctorReport writes the human-readable check list, one
// "DOCTOR-CHECK-* [status] detail" line per check, in the fixed order the
// checks were run. It is `ctxloom doctor`'s rendering and the startup
// findings' (WithStartupFindings), so an agent reads the surface a human does.
func WriteDoctorReport(out io.Writer, report DoctorReport) error {
	w := errwriter.New(out)
	w.Println("ctxloom doctor")
	for _, c := range report.Checks {
		WriteDoctorRow(w, c)
	}
	return w.Err()
}

// WriteDoctorRow writes one "DOCTOR-CHECK-* [status] detail" line and its fix
// line. A detail is ctxloom's sentence with publisher values (bundle refs,
// remote errors) spliced in, so detail and remedy are rendered inert
// (displaysafe.Text) and may span lines.
func WriteDoctorRow(w *errwriter.Writer, c DoctorCheck) {
	w.Printf("  %s [%s] %s%s\n", c.Marker, c.Status, displaysafe.Text(c.Detail, true),
		clifmt.FixLine("    ", displaysafe.Text(c.Remedy, true)))
}
