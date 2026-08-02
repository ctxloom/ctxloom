package mcpschema

// The `<ctxloom-reminder>` frame encoder generator (plane-2 §6.6).
//
// ctxloom injects tiny out-of-band notices into a session's turn stream, and
// the model reads whatever arrives in the user turn as the human speaking
// unless it is framed. Framing is therefore security-relevant, and until now it
// was HAND-WRITTEN at each delivery site — which is exactly why one site
// interpolated a sender-controlled `kind` into the frame text and another
// injected raw bytes with no frame at all.
//
// This file makes framing a property of GENERATED code. The frame shape is
// declared in proto field options (annotations.proto's XmlRole), and a
// generator — unlike a runtime-reflective renderer — can FAIL THE BUILD:
//
//	1. a field of an xml_like message with NO xml_role option is a build
//	   failure. XML_OMIT being the enum's zero value makes the semantics fail
//	   safe (rendering is opt-in, so a credential added later cannot leak by
//	   omission); requiring the annotation to be WRITTEN makes the omission a
//	   decision rather than an oversight;
//	2. XML_ATTRIBUTE is legal only for enums and singular numeric/bool
//	   scalars. A string/bytes/repeated/message field annotated XML_ATTRIBUTE
//	   is a build failure — untrusted multi-line text does not belong in an
//	   attribute, so the author must choose XML_CONTENT or XML_OMIT;
//	3. escaping lives in the EMITTED code, so missing it once is a compile
//	   error rather than a forged frame. Enums render as value NAMES from a
//	   closed set and are unforgeable by construction.
//
// Nothing here runs at runtime: `just gen-mcp-schemas` emits the encoders and
// the result is checked in, like the schemas/*.json goldens next door.

import (
	"fmt"
	"go/format"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// ReminderTag is the one element name every injected frame uses.
//
// Hyphenated, NOT colon-namespaced: `<ctxloom:reminder>` with no `xmlns:ctxloom`
// binding in scope is a namespace-well-formedness ERROR, the conventions the
// model already carries priors for are flat-hyphenated (`<system-reminder>`,
// `<task-notification>`), and ctxloom PARSES TRANSCRIPTS — these frames land in
// captured sessions that get re-imported, where a colon-prefixed tag through
// naive tooling is avoidable risk.
const ReminderTag = "ctxloom-reminder"

// XmlLikeFrame is one xml_like message's VALIDATED frame plan: everything the
// emitter needs, with all three build-time constraints already enforced.
type XmlLikeFrame struct {
	// GoType is the generated Go type the encoder hangs off (the proto message
	// name; protoc-gen-go keeps it verbatim for a top-level message).
	GoType string
	// Kind is the frame's `kind` attribute, derived from the MESSAGE NAME — a
	// closed set by construction. No field or sender value can influence it.
	Kind string
	// Text is literal element content from (message_schema).xml_text, a
	// build-time constant. Mutually exclusive with Content.
	Text string
	// Attributes render as `name="value"`, in field-number order.
	Attributes []XmlLikeAttribute
	// Content, when set, is the single XML_CONTENT field whose (escaped) value
	// becomes the element's content.
	Content *XmlLikeContent
}

// XmlLikeAttribute is one attribute-rendered field.
type XmlLikeAttribute struct {
	Name   string // the proto field name, used verbatim as the attribute name
	Getter string // the generated getter, e.g. "GetCount"
	// Expr is the Go expression converting the getter's result to a string.
	// %s is the receiver.
	Expr string
}

// XmlLikeContent is the single content-rendered field.
type XmlLikeContent struct {
	Name   string
	Getter string
}

// XmlLikeFrames projects and VALIDATES every (message_schema).xml_like message
// in the descriptor set, in a stable order (by full name).
//
// An error here is a BUILD FAILURE by design: `just gen-mcp-schemas` exits
// non-zero, no file is written, and the offending field is named. That is the
// entire reason these encoders are generated instead of reflected.
func (p *Projector) XmlLikeFrames() ([]XmlLikeFrame, error) {
	var frames []XmlLikeFrame
	var errs []string
	p.files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		msgs := fd.Messages()
		for i := 0; i < msgs.Len(); i++ {
			md := msgs.Get(i)
			ms := messageAnnotation(md)
			if !ms.GetXmlLike() {
				continue
			}
			frame, err := xmlLikeFrame(md, ms)
			if err != nil {
				errs = append(errs, err.Error())
				continue
			}
			frames = append(frames, frame)
		}
		return true
	})
	if len(errs) > 0 {
		sort.Strings(errs)
		return nil, fmt.Errorf("mcpschema: refusing to generate <%s> encoders:\n  - %s",
			ReminderTag, strings.Join(errs, "\n  - "))
	}
	sort.Slice(frames, func(i, j int) bool { return frames[i].GoType < frames[j].GoType })
	return frames, nil
}

// messageAnnotation reads the (message_schema) option, zero-valued when absent.
func messageAnnotation(md protoreflect.MessageDescriptor) *agentcoordpb.MessageSchema {
	opts, ok := md.Options().(*descriptorpb.MessageOptions)
	if !ok || opts == nil || !proto.HasExtension(opts, agentcoordpb.E_MessageSchema) {
		return &agentcoordpb.MessageSchema{}
	}
	ms, ok := proto.GetExtension(opts, agentcoordpb.E_MessageSchema).(*agentcoordpb.MessageSchema)
	if !ok || ms == nil {
		return &agentcoordpb.MessageSchema{}
	}
	return ms
}

// xmlRole reports a field's declared role and whether the option was PRESENT.
// The presence bit is what constraint 1 turns on: XML_OMIT and "no annotation"
// have the same value and must not have the same consequence.
func xmlRole(fld protoreflect.FieldDescriptor) (agentcoordpb.XmlRole, bool) {
	opts, ok := fld.Options().(*descriptorpb.FieldOptions)
	if !ok || opts == nil || !proto.HasExtension(opts, agentcoordpb.E_XmlRole) {
		return agentcoordpb.XmlRole_XML_OMIT, false
	}
	role, ok := proto.GetExtension(opts, agentcoordpb.E_XmlRole).(agentcoordpb.XmlRole)
	if !ok {
		return agentcoordpb.XmlRole_XML_OMIT, false
	}
	return role, true
}

// xmlLikeFrame validates one xml_like message and builds its frame plan.
func xmlLikeFrame(md protoreflect.MessageDescriptor, ms *agentcoordpb.MessageSchema) (XmlLikeFrame, error) {
	frame := XmlLikeFrame{
		GoType: string(md.Name()),
		Kind:   frameKind(md.Name()),
		Text:   ms.GetXmlText(),
	}
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		if err := addFieldToFrame(&frame, md, fields.Get(i)); err != nil {
			return frame, err
		}
	}
	if frame.Text != "" && frame.Content != nil {
		return frame, fmt.Errorf(
			"%s declares both (message_schema).xml_text and an XML_CONTENT field (%s): element content has "+
				"ONE source, and the frame's shape is decided at build time",
			md.FullName(), frame.Content.Name)
	}
	return frame, nil
}

// addFieldToFrame folds one field into the frame plan, applying constraint 1
// (the annotation must be PRESENT) and dispatching on the declared role.
func addFieldToFrame(frame *XmlLikeFrame, md protoreflect.MessageDescriptor, fld protoreflect.FieldDescriptor) error {
	role, present := xmlRole(fld)
	if !present {
		return fmt.Errorf(
			"%s.%s has no (xml_role): every field of an xml_like message must declare one. "+
				"Rendering is opt-IN so a field added later cannot leak into a model's context by "+
				"omission — say XML_OMIT explicitly if that is what you mean",
			md.FullName(), fld.Name())
	}
	switch role {
	case agentcoordpb.XmlRole_XML_OMIT:
		// Not rendered, and deliberately not recorded: the emitted source must
		// not so much as name it (the default-OMIT leak test).
		return nil
	case agentcoordpb.XmlRole_XML_ATTRIBUTE:
		attr, err := attributeFor(md, fld)
		if err != nil {
			return err
		}
		frame.Attributes = append(frame.Attributes, attr)
		return nil
	case agentcoordpb.XmlRole_XML_CONTENT:
		return setFrameContent(frame, md, fld)
	default:
		return fmt.Errorf("%s.%s: unrecognised xml_role %d", md.FullName(), fld.Name(), role)
	}
}

// setFrameContent claims the single content slot, refusing a second claimant.
func setFrameContent(frame *XmlLikeFrame, md protoreflect.MessageDescriptor, fld protoreflect.FieldDescriptor) error {
	if err := contentEligible(md, fld); err != nil {
		return err
	}
	if frame.Content != nil {
		return fmt.Errorf(
			"%s declares two XML_CONTENT fields (%s and %s): an element has one content, and "+
				"concatenating two would have no defined order",
			md.FullName(), frame.Content.Name, fld.Name())
	}
	frame.Content = &XmlLikeContent{Name: string(fld.Name()), Getter: fieldGetter(fld)}
	return nil
}

// attributeFor enforces constraint 2 and builds the attribute plan.
//
// Only enums and singular numeric/bool scalars are attribute-eligible. An enum
// renders as its value NAME — a name from a closed set, so it is unforgeable
// and off the escaping-dependent path entirely; that property is the second
// argument for MessageKind being an enum at all.
func attributeFor(md protoreflect.MessageDescriptor, fld protoreflect.FieldDescriptor) (XmlLikeAttribute, error) {
	attr := XmlLikeAttribute{Name: string(fld.Name()), Getter: fieldGetter(fld)}
	refuse := func(why string) error {
		return fmt.Errorf(
			"%s.%s is annotated XML_ATTRIBUTE but %s: attributes hold ONE short value from a closed or "+
				"numeric domain. Use XML_CONTENT (the only place free text may render, escaped by the "+
				"generated encoder) or XML_OMIT",
			md.FullName(), fld.Name(), why)
	}
	if fld.IsList() || fld.IsMap() {
		return attr, refuse("it is repeated")
	}
	switch fld.Kind() {
	case protoreflect.EnumKind:
		attr.Expr = "%s.String()"
	case protoreflect.BoolKind:
		attr.Expr = "strconv.FormatBool(%s)"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		attr.Expr = "strconv.FormatInt(int64(%s), 10)"
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		attr.Expr = "strconv.FormatUint(uint64(%s), 10)"
	case protoreflect.StringKind, protoreflect.BytesKind:
		return attr, refuse("it is free text, which can be multi-line and is never trustworthy")
	default:
		return attr, refuse(fmt.Sprintf("its type (%s) has no short single-value rendering", fld.Kind()))
	}
	return attr, nil
}

// contentEligible enforces what may be element content: a singular string.
// Bytes are binary, a message has no text rendering, and repeated content has
// no separator anyone chose.
func contentEligible(md protoreflect.MessageDescriptor, fld protoreflect.FieldDescriptor) error {
	if fld.IsList() || fld.IsMap() {
		return fmt.Errorf("%s.%s is annotated XML_CONTENT but is repeated: element content is one value",
			md.FullName(), fld.Name())
	}
	if fld.Kind() != protoreflect.StringKind {
		return fmt.Errorf("%s.%s is annotated XML_CONTENT but is a %s: only a singular string may be element content",
			md.FullName(), fld.Name(), fld.Kind())
	}
	return nil
}

// frameKind derives the frame's kind attribute from the message name:
// MailPendingReminder -> "mail-pending". A trailing "Reminder" is dropped
// (every frame is one), and the rest is kebab-cased. Deriving from the
// DESCRIPTOR rather than an option means the vocabulary is closed by
// construction — there is no value anyone could set to widen it.
func frameKind(name protoreflect.Name) string {
	s := strings.TrimSuffix(string(name), "Reminder")
	if s == "" {
		s = string(name)
	}
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// fieldGetter is protoc-gen-go's getter name for a field: GetSnakeCase ->
// GetCamelCase.
func fieldGetter(fld protoreflect.FieldDescriptor) string {
	var b strings.Builder
	b.WriteString("Get")
	up := true
	for _, r := range string(fld.Name()) {
		if r == '_' {
			up = true
			continue
		}
		if up && r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		up = false
		b.WriteRune(r)
	}
	return b.String()
}

// RenderXmlLikeGo emits the generated encoder source for package pkg.
//
// The escaping helpers are emitted ALONGSIDE the encoders rather than
// hand-written next to them, so constraint 3 ("escaping lives in the generated
// code") is literally true: there is no hand-written frame construction
// anywhere in the repo for a future edit to get wrong.
//
// Zero frames is an ERROR, not an empty file (the same lesson from the
// binding table): a generator that silently removed every encoder would take
// the injected-frame surface with it and report success.
func RenderXmlLikeGo(pkg string, frames []XmlLikeFrame) ([]byte, error) {
	if len(frames) == 0 {
		return nil, fmt.Errorf("mcpschema: no (message_schema).xml_like messages to project — "+
			"refusing to write a <%s> encoder file with no encoders in it", ReminderTag)
	}
	var b strings.Builder
	writeXmlLikeHeader(&b, pkg)
	for _, f := range frames {
		writeXmlLikeEncoder(&b, f)
	}
	writeXmlLikeHelpers(&b)
	src, err := format.Source([]byte(b.String()))
	if err != nil {
		return nil, fmt.Errorf("mcpschema: emitted encoder source does not parse (generator bug): %w", err)
	}
	return src, nil
}

func writeXmlLikeHeader(b *strings.Builder, pkg string) {
	fmt.Fprintf(b, `// Code generated by internal/agentcoord/mcpschema/gen. DO NOT EDIT.
//
// <%[1]s> frame encoders, generated from the (message_schema).xml_like and
// (xml_role) options in internal/agentcoord/*.proto. Regenerate with
// `+"`just gen-mcp-schemas`"+`.
//
// These are the ONLY place a frame is constructed. Hand-writing one elsewhere
// bypasses the escaping and the opt-in rendering rules the generator enforces
// at build time, so don't: add a message, annotate its fields, regenerate.

package %[2]s

import (
	"strconv"
	"strings"
)

`, ReminderTag, pkg)
}

func writeXmlLikeEncoder(b *strings.Builder, f XmlLikeFrame) {
	fmt.Fprintf(b, "// XmlLike renders x as its injected <%s> frame.\n", ReminderTag)
	fmt.Fprintf(b, "func (x *%s) XmlLike() string {\n", f.GoType)
	b.WriteString("\tvar b strings.Builder\n")
	fmt.Fprintf(b, "\tb.WriteString(%s)\n", goLit("<"+ReminderTag+` kind="`+f.Kind+`"`))
	for _, a := range f.Attributes {
		fmt.Fprintf(b, "\txmlLikeAttr(&b, %s, "+a.Expr+")\n", goLit(a.Name), "x."+a.Getter+"()")
	}
	b.WriteString("\tb.WriteString(\">\")\n")
	switch {
	case f.Content != nil:
		fmt.Fprintf(b, "\tb.WriteString(xmlLikeEscape(x.%s()))\n", f.Content.Getter)
	case f.Text != "":
		fmt.Fprintf(b, "\tb.WriteString(%s)\n", goLit(xmlLikeEscapeLiteral(f.Text)))
	}
	fmt.Fprintf(b, "\tb.WriteString(%s)\n", goLit("</"+ReminderTag+">"))
	b.WriteString("\treturn b.String()\n}\n\n")
}

// goLit renders s as a Go string literal, preferring a BACKQUOTED raw string so
// the emitted frame text reads as the frame — `kind="mail-pending"` rather than
// a thicket of backslash-escaped quotes. Every literal the generator emits is a
// build-time constant from a proto option, so the fallback is only ever reached
// by a string carrying a backtick or a newline.
func goLit(s string) string {
	if !strings.ContainsAny(s, "`\n\r") {
		return "`" + s + "`"
	}
	return fmt.Sprintf("%q", s)
}

func writeXmlLikeHelpers(b *strings.Builder) {
	fmt.Fprintf(b, `// xmlLikeAttr appends one escaped attribute. Only enums and short numeric
// scalars reach here (the generator refuses free text as an attribute), so the
// escaping is belt over braces — the guarantee lives in the CODE rather than in
// an argument about which values are safe.
func xmlLikeAttr(b *strings.Builder, name, value string) {
	b.WriteString(" ")
	b.WriteString(name)
	b.WriteString("=\"")
	b.WriteString(xmlLikeEscape(value))
	b.WriteString("\"")
}

// xmlLikeEscape neutralises every byte that could close, reopen, or forge a
// frame. It is deliberately blunt: this is presentation encoding for an LLM,
// not a standards-conformant XML serializer, and over-escaping costs nothing a
// model notices while under-escaping is a forged <%[1]s>.
func xmlLikeEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&#39;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
`, ReminderTag)
}

// xmlLikeEscapeLiteral escapes build-time constant content at GENERATION time.
// It mirrors the emitted xmlLikeEscape exactly; keeping the two in step is
// pinned by a test that round-trips a constant carrying every escaped byte.
func xmlLikeEscapeLiteral(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&#39;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
