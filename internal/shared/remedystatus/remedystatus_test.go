package remedystatus

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

const remedy = "declare `auth: token` on a container agent, or run it with `runtime: host`"

// overTheWire marshals st and unmarshals it into a fresh value: what the far
// side of the connection actually holds, not the encoder's in-memory value.
func overTheWire(t *testing.T, st *rpcstatus.Status) *rpcstatus.Status {
	t.Helper()
	raw, err := proto.Marshal(st)
	require.NoError(t, err)
	var got rpcstatus.Status
	require.NoError(t, proto.Unmarshal(raw, &got))
	return &got
}

// TestRefusal_RemedyRoundTrips: a refusal naming its fix decodes, across a
// real marshal, to an error with the same text and the same fix, and the
// code the caller chose is untouched.
func TestRefusal_RemedyRoundTrips(t *testing.T) {
	cause := report.Errorf(remedy, "container: no credential to forward")
	st := overTheWire(t, Refusal(codes.FailedPrecondition, cause))
	assert.EqualValues(t, codes.FailedPrecondition, st.GetCode())
	err := Err(st)
	require.Error(t, err)
	assert.Equal(t, cause.Error(), err.Error())
	fix, ok := clifmt.RemedyOf(err)
	assert.True(t, ok)
	assert.Equal(t, remedy, fix)
}

// TestRefusal_NoRemedyNoDetail: an error that names no fix — plain, or a
// report.Error whose Fix is empty — attaches no detail at all.
func TestRefusal_NoRemedyNoDetail(t *testing.T) {
	for _, err := range []error{errors.New("plain"), report.Error{Msg: "listing"}} {
		st := Refusal(codes.Internal, err)
		assert.Empty(t, st.GetDetails(), "%v", err)
		assert.Equal(t, err.Error(), st.GetMessage())
		_, ok := clifmt.RemedyOf(Err(st))
		assert.False(t, ok, "nothing to decode as a remedy")
	}
}

// TestRefusal_RemedyHiddenBehindAnEmptyWrapper: a remediable wrapper with
// nothing to add must not hide the fix its cause names.
func TestRefusal_RemedyHiddenBehindAnEmptyWrapper(t *testing.T) {
	err := report.Error{Msg: "outer", Err: report.Errorf(remedy, "inner")}
	fix, ok := clifmt.RemedyOf(Err(Refusal(codes.Internal, err)))
	assert.True(t, ok)
	assert.Equal(t, remedy, fix)
}

// TestErr_UnknownDetailIsIgnored: a status carrying a detail this build does
// not read decodes to the refusal, not to a decode failure.
func TestErr_UnknownDetailIsIgnored(t *testing.T) {
	other, err := anypb.New(&errdetails.ErrorInfo{Reason: "SOMETHING_ELSE", Domain: "example.test"})
	require.NoError(t, err)
	st := overTheWire(t, &rpcstatus.Status{Code: int32(codes.FailedPrecondition), Message: "refused", Details: []*anypb.Any{other}})
	got := Err(st)
	require.Error(t, got)
	assert.Equal(t, "refused", got.Error())
	_, ok := clifmt.RemedyOf(got)
	assert.False(t, ok)
}

// TestErr_OKIsNil: an accepted answer is no error.
func TestErr_OKIsNil(t *testing.T) {
	assert.NoError(t, Err(&rpcstatus.Status{Code: int32(codes.OK), Message: "fine"}))
	assert.NoError(t, Err(nil))
}
