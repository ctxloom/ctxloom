package agentcoord

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
)

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

// CROSS-VOCABULARY AGREEMENT. While both guards exist — the string-level
// chokepoint check (coord.SenderMailKind) and this typed enum — they must accept
// the SAME four kinds. They live in packages that cannot import each other, so
// the link is the derived spelling: if this test is green, an enum value and its
// legacy string are the same word, and the merge that unifies them cannot
// mismatch by spelling.
//
// The four names below are copied deliberately, not derived: a test that derived
// its own expectation from the code under test would pass no matter what the
// code said.
func TestLegacySenderKindNames_MatchTheStringVocabulary(t *testing.T) {
	want := []string{"message", "result", "error", "question"}
	got := LegacySenderKindNames()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("sender-allowed legacy spellings are %v, want %v — the typed enum and the "+
			"string-level guard would accept different sets", got, want)
	}
}

// Every RESERVED value's legacy spelling must round-trip too, so the merge can
// repoint the string guard's reserved list at the enum with the same confidence.
func TestLegacyKindName_CoversTheReservedVocabulary(t *testing.T) {
	for k, want := range map[MessageKind]string{
		MessageKind_MESSAGE_KIND_USER_INJECTED: "user_injected",
		MessageKind_MESSAGE_KIND_USER_CONTROL:  "user_control",
		MessageKind_MESSAGE_KIND_EXITED:        "exited",
		MessageKind_MESSAGE_KIND_STEER:         "steer",
		MessageKind_MESSAGE_KIND_REPORT:        "report",
		MessageKind_MESSAGE_KIND_SUMMARIZE:     "summarize",
	} {
		if got := LegacyKindName(k); got != want {
			t.Errorf("LegacyKindName(%v) = %q, want %q", k, got, want)
		}
		back, err := MessageKindForLegacyName(want)
		if err != nil || back != k {
			t.Errorf("MessageKindForLegacyName(%q) = %v, %v; want %v", want, back, err, k)
		}
	}
	// "unkinded" was the EMPTY STRING in the free-string vocabulary, and the
	// enum deliberately does not represent it — MESSAGE_KIND_MESSAGE is what an
	// unkinded message becomes. So UNSPECIFIED has no legacy spelling, and an
	// unrecognised number has none either.
	if got := LegacyKindName(MessageKind_MESSAGE_KIND_UNSPECIFIED); got != "" {
		t.Errorf("UNSPECIFIED has no legacy spelling, got %q", got)
	}
	if got := LegacyKindName(MessageKind(99)); got != "" {
		t.Errorf("an unrecognised value has no legacy spelling, got %q", got)
	}
}

// The inverse is the receive side's only way back onto the wire: "" is
// UNSPECIFIED (the unkinded Message, minted by no producer), anything outside
// the derived spellings is an error rather than a silent zero.
func TestMessageKindForLegacyName_RefusesUnknownSpellings(t *testing.T) {
	if got, err := MessageKindForLegacyName(""); err != nil || got != MessageKind_MESSAGE_KIND_UNSPECIFIED {
		t.Errorf("MessageKindForLegacyName(\"\") = %v, %v; want UNSPECIFIED, nil", got, err)
	}
	for _, name := range []string{"MESSAGE_KIND_RESULT", "Result", "result ", "task"} {
		if got, err := MessageKindForLegacyName(name); err == nil {
			t.Errorf("MessageKindForLegacyName(%q) = %v, want an error", name, got)
		}
	}
}

// LegacyReservedKindNames is what coord's reserved list is BUILT from, so its
// membership is pinned here as copied literals in enum-declaration order — the
// order the refusal text enumerates.
func TestLegacyReservedKindNames_CoversTheReservedVocabulary(t *testing.T) {
	want := []string{"user_injected", "user_control", "exited", "steer", "report", "summarize"}
	got := LegacyReservedKindNames()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("reserved legacy spellings are %v, want %v", got, want)
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
