package report_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// The structural binding between the raise side (report) and the one
// renderer (clifmt): clifmt imports nothing of ours, so it can only read a
// fix through its Remedier method set. These assertions are what turn that
// unchecked coupling into a checked one.
var (
	_ clifmt.Remedier   = report.Error{}
	_ clifmt.Remedier   = &report.Error{}
	_ report.Remediable = report.Error{}
)

const fix = "run ctxloom deps pull"

var errSentinel = errors.New("sentinel")

func TestError_RemedyOfThroughWrap(t *testing.T) {
	err := fmt.Errorf("x: %w", report.Error{Fix: fix})
	if got, ok := clifmt.RemedyOf(err); !ok || got != fix {
		t.Fatalf("clifmt.RemedyOf = %q, %v; want %q", got, ok, fix)
	}
}

func TestErrorf_KeepsWrappedTargetAndFix(t *testing.T) {
	err := report.Errorf(fix, "load: %w", errSentinel)
	if !errors.Is(err, errSentinel) {
		t.Fatal("errors.Is lost the %w target through report.Errorf")
	}
	if got, _ := clifmt.RemedyOf(err); got != fix {
		t.Fatalf("RemedyOf = %q; want %q", got, fix)
	}
	if err.Error() != "load: sentinel" {
		t.Fatalf("Error() = %q", err.Error())
	}
}

func TestError_Text(t *testing.T) {
	cases := []struct {
		e    report.Error
		want string
	}{
		{report.Error{Msg: "m"}, "m"},
		{report.Error{Err: errSentinel}, "sentinel"},
		{report.Error{Msg: "m", Err: errSentinel}, "m: sentinel"},
	}
	for _, c := range cases {
		if got := c.e.Error(); got != c.want {
			t.Errorf("%+v.Error() = %q; want %q", c.e, got, c.want)
		}
	}
}
