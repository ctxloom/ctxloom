package mcpschema

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// The negative-codegen tests below are the LOAD-BEARING tests of the whole
// generated-encoder approach. §6.6's decisive argument for generating encoders
// instead of reflecting over messages at runtime is that "a generator can FAIL
// THE BUILD": the invalid frame becomes unrepresentable at build time rather
// than warned about at runtime. Without these, that is a convention someone has
// to remember, not a property of the build — and the escaping guarantee behind
// every injected `<ctxloom-reminder>` frame rests on it.

// fieldOpts builds a FieldOptions carrying an EXPLICIT xml_role.
func fieldOpts(role agentcoordpb.XmlRole) *descriptorpb.FieldOptions {
	opts := &descriptorpb.FieldOptions{}
	proto.SetExtension(opts, agentcoordpb.E_XmlRole, role)
	return opts
}

// msgOpts builds a MessageOptions marking the message xml_like.
func msgOpts(text string) *descriptorpb.MessageOptions {
	opts := &descriptorpb.MessageOptions{}
	proto.SetExtension(opts, agentcoordpb.E_MessageSchema, &agentcoordpb.MessageSchema{
		XmlLike: true,
		XmlText: text,
	})
	return opts
}

// syntheticFrames indexes a one-message descriptor set and projects its frames.
// The message is always marked xml_like; the caller supplies its fields.
func syntheticFrames(t *testing.T, name string, opts *descriptorpb.MessageOptions, fields ...*descriptorpb.FieldDescriptorProto) ([]XmlLikeFrame, error) {
	t.Helper()
	fds := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
		Name:    proto.String("synthetic.proto"),
		Package: proto.String("synthetic.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name:    proto.String(name),
			Options: opts,
			Field:   fields,
		}},
	}}}
	p, err := NewProjector(fds)
	if err != nil {
		t.Fatalf("index synthetic descriptor set: %v", err)
	}
	return p.XmlLikeFrames()
}

func strField(name string, num int32, opts *descriptorpb.FieldOptions) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:     proto.String(name),
		Number:   proto.Int32(num),
		Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
		JsonName: proto.String(name),
		Options:  opts,
	}
}

func uintField(name string, num int32, opts *descriptorpb.FieldOptions) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:     proto.String(name),
		Number:   proto.Int32(num),
		Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:     descriptorpb.FieldDescriptorProto_TYPE_UINT32.Enum(),
		JsonName: proto.String(name),
		Options:  opts,
	}
}

// CONSTRAINT 1 (build failure). A field of an xml_like message with NO
// xml_role option must FAIL THE BUILD. XML_OMIT being the enum's zero value
// makes the SEMANTICS fail safe; requiring the annotation to be WRITTEN makes
// the omission a decision rather than an oversight. Belt, then braces.
func TestXmlLikeFrames_UnannotatedFieldFailsTheBuild(t *testing.T) {
	_, err := syntheticFrames(t, "ThingReminder", msgOpts("do the thing"),
		strField("secret_run_token", 1, nil))
	if err == nil {
		t.Fatal("an unannotated field on an xml_like message must fail the build, not be silently omitted")
	}
	for _, want := range []string{"secret_run_token", "xml_role"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("build failure must name %q so the author knows what to fix; got: %v", want, err)
		}
	}
}

// CONSTRAINT 2 (build failure). A free-text `string` field annotated
// XML_ATTRIBUTE must FAIL THE BUILD: multi-line untrusted text does not belong
// in an attribute at all, so the author must choose XML_CONTENT or XML_OMIT.
func TestXmlLikeFrames_FreeTextAttributeFailsTheBuild(t *testing.T) {
	_, err := syntheticFrames(t, "ThingReminder", msgOpts("do the thing"),
		strField("from", 1, fieldOpts(agentcoordpb.XmlRole_XML_ATTRIBUTE)))
	if err == nil {
		t.Fatal("a free-text string field annotated XML_ATTRIBUTE must fail the build")
	}
	for _, want := range []string{"from", "XML_ATTRIBUTE", "XML_CONTENT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("build failure must name %q; got: %v", want, err)
		}
	}
}

// CONSTRAINT 2, the repeated case. `repeated` is refused as an attribute
// whatever its element type: an attribute holds one value.
func TestXmlLikeFrames_RepeatedAttributeFailsTheBuild(t *testing.T) {
	f := uintField("counts", 1, fieldOpts(agentcoordpb.XmlRole_XML_ATTRIBUTE))
	f.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	_, err := syntheticFrames(t, "ThingReminder", msgOpts("do the thing"), f)
	if err == nil {
		t.Fatal("a repeated field annotated XML_ATTRIBUTE must fail the build")
	}
	if !strings.Contains(err.Error(), "counts") {
		t.Errorf("build failure must name the field; got: %v", err)
	}
}

// CONSTRAINT 3's corollary: element content has ONE source. xml_text is a
// build-time constant and XML_CONTENT is a runtime value; two sources would
// have no defined order, and the encoder's whole point is that the frame's
// shape is decided at build time.
func TestXmlLikeFrames_TwoContentSourcesFailTheBuild(t *testing.T) {
	_, err := syntheticFrames(t, "ThingReminder", msgOpts("do the thing"),
		strField("body", 1, fieldOpts(agentcoordpb.XmlRole_XML_CONTENT)))
	if err == nil {
		t.Fatal("xml_text together with an XML_CONTENT field must fail the build")
	}
	if !strings.Contains(err.Error(), "xml_text") {
		t.Errorf("build failure must name xml_text; got: %v", err)
	}
}

func TestXmlLikeFrames_TwoContentFieldsFailTheBuild(t *testing.T) {
	_, err := syntheticFrames(t, "ThingReminder", msgOpts(""),
		strField("a", 1, fieldOpts(agentcoordpb.XmlRole_XML_CONTENT)),
		strField("b", 2, fieldOpts(agentcoordpb.XmlRole_XML_CONTENT)))
	if err == nil {
		t.Fatal("two XML_CONTENT fields must fail the build")
	}
}

// DEFAULT-OMIT LEAK TEST. A field the author explicitly declared XML_OMIT must
// never reach the generated encoder at all — not as a getter call, not as an
// attribute name. Rendering is opt-IN precisely so that a field added later (a
// run token, an internal id, a credential) cannot leak into a model's context
// by being forgotten; if the emitted source mentions it, that guarantee is
// theatre.
func TestXmlLikeFrames_OmittedFieldNeverRenders(t *testing.T) {
	frames, err := syntheticFrames(t, "ThingReminder", msgOpts("do the thing"),
		uintField("count", 1, fieldOpts(agentcoordpb.XmlRole_XML_ATTRIBUTE)),
		strField("secret_run_token", 2, fieldOpts(agentcoordpb.XmlRole_XML_OMIT)))
	if err != nil {
		t.Fatalf("explicit XML_OMIT is legal: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("projected %d frames, want 1", len(frames))
	}
	for _, a := range frames[0].Attributes {
		if a.Name == "secret_run_token" {
			t.Fatal("an XML_OMIT field must not become an attribute")
		}
	}
	if frames[0].Content != nil {
		t.Fatal("an XML_OMIT field must not become element content")
	}

	src, err := RenderXmlLikeGo("agentcoord", frames)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, leak := range []string{"secret_run_token", "SecretRunToken", "GetSecretRunToken"} {
		if strings.Contains(string(src), leak) {
			t.Errorf("emitted encoder mentions the omitted field %q — default-OMIT is not enforced", leak)
		}
	}
}

// A message NOT marked xml_like gets no encoder, and its un-annotated fields
// are nobody's business: the constraints apply only where frames are rendered.
func TestXmlLikeFrames_OnlyAnnotatedMessagesProjectAndUnmarkedOnesAreUnconstrained(t *testing.T) {
	frames, err := syntheticFrames(t, "OrdinaryWireMessage", nil,
		strField("anything", 1, nil))
	if err != nil {
		t.Fatalf("a message that is not xml_like must not be constrained: %v", err)
	}
	if len(frames) != 0 {
		t.Fatalf("projected %d frames for a non-xml_like message, want 0", len(frames))
	}
}

// The frame's kind attribute comes from the message NAME — a closed set by
// construction. No field, option, or sender value can influence it.
func TestXmlLikeFrames_KindIsDerivedFromTheMessageName(t *testing.T) {
	for _, tc := range []struct{ msg, kind string }{
		{"MailPendingReminder", "mail-pending"},
		{"PausedReminder", "paused"},
		{"QuestionPendingReminder", "question-pending"},
	} {
		frames, err := syntheticFrames(t, tc.msg, msgOpts("x"))
		if err != nil {
			t.Fatalf("%s: %v", tc.msg, err)
		}
		if len(frames) != 1 || frames[0].Kind != tc.kind {
			t.Errorf("%s: kind = %q, want %q", tc.msg, frames[0].Kind, tc.kind)
		}
	}
}

// An empty frame set is a failure, not a silent success: the emitted file's
// escaping helpers would be unreferenced, and — like the empty-binding-table
// guard — a generator that produced nothing must say so rather than
// write a file that quietly removes every encoder.
func TestRenderXmlLikeGo_NoFramesIsAnError(t *testing.T) {
	if _, err := RenderXmlLikeGo("agentcoord", nil); err == nil {
		t.Fatal("rendering zero frames must be an error")
	}
}

// The emitted source must compile-shape correctly and be gofmt-stable; the
// generator gofmts its own output so the checked-in file never drifts against
// the formatting gate.
func TestRenderXmlLikeGo_EmitsAGoFormattedEncoderPerFrame(t *testing.T) {
	frames, err := syntheticFrames(t, "MailPendingReminder", msgOpts("call agent_recv"),
		uintField("count", 1, fieldOpts(agentcoordpb.XmlRole_XML_ATTRIBUTE)))
	if err != nil {
		t.Fatal(err)
	}
	src, err := RenderXmlLikeGo("agentcoord", frames)
	if err != nil {
		t.Fatal(err)
	}
	got := string(src)
	for _, want := range []string{
		"// Code generated by",
		"DO NOT EDIT",
		"package agentcoord",
		"func (x *MailPendingReminder) XmlLike() string",
		`kind="mail-pending"`,
		"GetCount()",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("emitted source is missing %q; got:\n%s", want, got)
		}
	}
}
