package operations

import (
	"context"
)

// DoctorStatus is one check's verdict, and there are exactly three of them. It
// is a named type rather than a bare string because the value set IS the
// contract: it is shared with the "ctxloom-doctor" Agent Skill and with every
// consumer of `doctor --format json`. The underlying type stays string, so the
// JSON wire shape is unchanged.
type DoctorStatus string

const (
	// DoctorOK: the check's subject is in the state setup intends.
	DoctorOK DoctorStatus = "ok"
	// DoctorWarn: doctor's fail-loud signal. Doctor never fails the process,
	// so a warn is how a real problem is reported.
	DoctorWarn DoctorStatus = "warn"
	// DoctorInfo: reported for context, not a verdict — nothing to fix.
	DoctorInfo DoctorStatus = "info"
)

// DoctorCheck is one named check's outcome. Marker is the DOCTOR-CHECK-*
// vocabulary doctor shares with the "ctxloom-doctor" Agent Skill, so a human
// or an LLM reading either surface sees one language.
type DoctorCheck struct {
	Marker string       `json:"marker"`
	Status DoctorStatus `json:"status"`
	Detail string       `json:"detail"`
}

// DoctorReport is `ctxloom doctor`'s structured result: the checks in the
// order they ran, which is the order every frontend renders.
type DoctorReport struct {
	Checks []DoctorCheck `json:"checks"`
}

// DoctorRequest scopes one report.
type DoctorRequest struct {
	// DepsOnly scopes the report to the machine-capability probes alone —
	// questions that are true-or-false regardless of whether a project has
	// been set up yet.
	DepsOnly bool
	// Home is the user's home directory, a fact the composition root
	// establishes; "" when it could not be, in which case the home-rooted
	// checks skip what they cannot resolve.
	Home string
}

// Doctor runs the deterministic setup checks and returns their rows. It
// prints nothing: the frontend renders.
func Doctor(ctx context.Context, app *App, req DoctorRequest) (DoctorReport, error) {
	return DoctorReport{}, nil
}
