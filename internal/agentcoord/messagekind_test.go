package agentcoord

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
)

// ENUM INGRESS. proto3 enums are OPEN on the wire: an unrecognised number
// survives Unmarshal as itself. So "it decoded" is not "it is in the
// vocabulary", and the property the design actually needs — an unknown kind is
// REFUSED, not silently zero-valued — has to be tested at the guard, on a
// payload that really went through Unmarshal.
func TestMessageKindIngress_UnknownWireValueIsRefusedNotZeroValued(t *testing.T) {
	// Field 7 (kind), varint 99 — a value no build declares.
	wire := []byte{7 << 3, 99}
	var req PeerSendRequest
	if err := proto.Unmarshal(wire, &req); err != nil {
		t.Fatalf("proto3 keeps unrecognised enum numbers; decode should not fail: %v", err)
	}
	if got := req.GetKind(); int32(got) != 99 {
		t.Fatalf("decoded kind = %d, want the unrecognised 99 preserved (if this is 0, the "+
			"assumption this guard exists for has changed)", int32(got))
	}
	err := ValidateMessageKind(req.GetKind())
	if err == nil {
		t.Fatal("an unrecognised kind must be REFUSED at ingress, never accepted")
	}
	if !strings.Contains(err.Error(), "99") {
		t.Errorf("refusal must name the offending value; got: %v", err)
	}
	if !strings.Contains(err.Error(), "MESSAGE_KIND_RESULT") {
		t.Errorf("refusal must name the accepted vocabulary so the sender can act; got: %v", err)
	}
}

// UNSPECIFIED is invalid, not a default. A message that never named its kind
// must not arrive unclassified and inherit whatever a switch's default branch
// happens to do.
func TestMessageKindIngress_UnspecifiedIsRefused(t *testing.T) {
	var req PeerSendRequest
	if err := proto.Unmarshal(nil, &req); err != nil {
		t.Fatal(err)
	}
	if req.GetKind() != MessageKind_MESSAGE_KIND_UNSPECIFIED {
		t.Fatalf("an absent field decodes to the zero value; got %v", req.GetKind())
	}
	err := ValidateMessageKind(req.GetKind())
	if err == nil {
		t.Fatal("MESSAGE_KIND_UNSPECIFIED must be refused at ingress")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("refusal should say the kind is required; got: %v", err)
	}
}

// THE FORGERY CASE, and the reason the enum exists. A child that names
// approval_request used to have it interpolated verbatim into the frame the
// receiving model saw — minting what read as a genuine approval prompt. Every
// coordinator-reserved value is refused from a sender.
func TestMessageKindIngress_EveryReservedKindIsRefusedFromASender(t *testing.T) {
	reserved := []MessageKind{
		MessageKind_MESSAGE_KIND_APPROVAL_REQUEST,
		MessageKind_MESSAGE_KIND_USER_INJECTED,
		MessageKind_MESSAGE_KIND_USER_CONTROL,
		MessageKind_MESSAGE_KIND_EXITED,
		MessageKind_MESSAGE_KIND_STEER,
	}
	for _, k := range reserved {
		if !k.IsCoordinatorReserved() {
			t.Errorf("%v must be coordinator-reserved", k)
		}
		if k.IsSenderAllowed() {
			t.Errorf("%v must not be sender-allowed", k)
		}
		err := ValidateMessageKind(k)
		if err == nil {
			t.Errorf("%v must be refused from a sender", k)
			continue
		}
		if !strings.Contains(err.Error(), k.String()) {
			t.Errorf("refusal must name the refused kind; got: %v", err)
		}
	}
}

func TestMessageKindIngress_SenderAllowedKindsPass(t *testing.T) {
	for _, k := range []MessageKind{
		MessageKind_MESSAGE_KIND_MESSAGE,
		MessageKind_MESSAGE_KIND_RESULT,
		MessageKind_MESSAGE_KIND_ERROR,
		MessageKind_MESSAGE_KIND_QUESTION,
	} {
		if err := ValidateMessageKind(k); err != nil {
			t.Errorf("%v is sender-allowed: %v", k, err)
		}
		if k.IsCoordinatorReserved() {
			t.Errorf("%v must not be reserved", k)
		}
	}
}

// The split is EXHAUSTIVE over the enum: every declared value is either
// sender-allowed or coordinator-reserved, and UNSPECIFIED is neither. A value
// added to the proto without landing in one of the two lists would otherwise
// end up reserved by accident — which is the safe direction, but silently, and
// this is the test that makes it a decision.
func TestMessageKindClassificationIsExhaustive(t *testing.T) {
	for v, name := range MessageKind_name {
		k := MessageKind(v)
		if k == MessageKind_MESSAGE_KIND_UNSPECIFIED {
			if k.IsSenderAllowed() || k.IsCoordinatorReserved() {
				t.Error("UNSPECIFIED is invalid, which is neither allowed nor reserved")
			}
			continue
		}
		if k.IsSenderAllowed() == k.IsCoordinatorReserved() {
			t.Errorf("%s (%d) is neither exactly sender-allowed nor exactly coordinator-reserved: "+
				"classify it in messagekind.go", name, v)
		}
	}
}

func TestParseMessageKind(t *testing.T) {
	k, err := ParseMessageKind("MESSAGE_KIND_RESULT")
	if err != nil || k != MessageKind_MESSAGE_KIND_RESULT {
		t.Fatalf("ParseMessageKind: %v, %v", k, err)
	}
	// The RETIRED free-string vocabulary must not resolve: "result" was the old
	// convention's spelling, and accepting it would quietly reopen the open
	// vocabulary this change closed.
	for _, legacy := range []string{"result", "error", "question", "message", "approval_request", ""} {
		if _, err := ParseMessageKind(legacy); err == nil {
			t.Errorf("the retired free-string kind %q must not parse", legacy)
		}
	}
	if _, err := ParseMessageKind("MESSAGE_KIND_NOPE"); err == nil {
		t.Error("an unknown name must not parse")
	}
}

// §4.E: UNSPECIFIED must be invalid at every consumer, so an unrecognised
// initiator fails CLOSED rather than inheriting the narrower branch's
// privileges by default.
func TestControlInitiatorKind_UnspecifiedAndUnknownFailClosed(t *testing.T) {
	if err := ValidateControlInitiatorKind(ControlInitiatorKind_CONTROL_INITIATOR_KIND_UNSPECIFIED); err == nil {
		t.Error("CONTROL_INITIATOR_KIND_UNSPECIFIED must be refused")
	}
	if err := ValidateControlInitiatorKind(ControlInitiatorKind(42)); err == nil {
		t.Error("an unrecognised initiator must be refused")
	}
	for _, k := range []ControlInitiatorKind{
		ControlInitiatorKind_CONTROL_INITIATOR_KIND_HUMAN,
		ControlInitiatorKind_CONTROL_INITIATOR_KIND_AGENT,
	} {
		if err := ValidateControlInitiatorKind(k); err != nil {
			t.Errorf("%v is valid: %v", k, err)
		}
	}
	// No speculative values: HUMAN and AGENT only. Adding one later is
	// additive, which is precisely the property the enum buys — but it should
	// be a deliberate act, with this count updated.
	if got := len(ControlInitiatorKind_name); got != 3 {
		t.Errorf("ControlInitiatorKind declares %d values, want 3 (UNSPECIFIED/HUMAN/AGENT)", got)
	}
}

// The whole point of 4.B is that the kind is a FIELD now. Pin that both wire
// messages carry it, so a future edit cannot quietly revert to the
// structured["kind"] convention and leave the enum orphaned.
func TestBothPeerMessagesCarryTheKindField(t *testing.T) {
	for _, m := range []proto.Message{&PeerSendRequest{}, &PeerMessage{}} {
		md := m.ProtoReflect().Descriptor()
		fld := md.Fields().ByName("kind")
		if fld == nil {
			t.Fatalf("%s has no kind field", md.FullName())
		}
		if fld.Enum() == nil || fld.Enum().FullName() != "agentcoord.v1.MessageKind" {
			t.Errorf("%s.kind is not a MessageKind", md.FullName())
		}
	}
}
