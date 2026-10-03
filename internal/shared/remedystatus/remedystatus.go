// Package remedystatus is the ONE encoding of a refusal's remedy on a gRPC
// status: the fix an error names rides as an errdetails.Help detail, because
// a Status's message is a single string and report.Error leaves its Fix out
// of Error(). It sits in the shared ring because both ends of the wire live
// in adapters that may not import each other (archrules
// "adapters-import-core-not-each-other"): the coordinator codec
// (adapters/coordgrpc) and the session endpoint (adapters/runner/interaction).
package remedystatus

import (
	"errors"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// Refusal is err as a refusal under code: err's text is the message, and the
// fix err names (clifmt.RemedyOf: a report.Remediable anywhere in its chain)
// rides as an errdetails.Help detail, decoded by Err.
func Refusal(code codes.Code, err error) *rpcstatus.Status {
	st := &rpcstatus.Status{Code: int32(code), Message: err.Error()}
	if fix, ok := clifmt.RemedyOf(err); ok {
		// anypb.New fails only on a message that cannot marshal; Help is a
		// generated message of strings.
		if help, aerr := anypb.New(&errdetails.Help{Links: []*errdetails.Help_Link{{Description: fix}}}); aerr == nil {
			st.Details = append(st.Details, help)
		}
	}
	return st
}

// Err is a wire status as the error it reports: nil for OK (or no status); a
// report.Error carrying the remedy when Refusal attached one; otherwise a
// plain error of the message. A detail of any other type is not this
// codec's to read and is skipped.
func Err(st *rpcstatus.Status) error {
	if st.GetCode() == int32(codes.OK) {
		return nil
	}
	for _, d := range st.GetDetails() {
		var help errdetails.Help
		if !d.MessageIs(&help) || d.UnmarshalTo(&help) != nil {
			continue
		}
		for _, l := range help.GetLinks() {
			if fix := l.GetDescription(); fix != "" {
				return report.Error{Msg: st.GetMessage(), Fix: fix}
			}
		}
	}
	return errors.New(st.GetMessage())
}
