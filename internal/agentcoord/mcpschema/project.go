package mcpschema

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// Projector turns agentcoord.v1 message descriptors into MCP tool JSON
// Schemas under the projection rules below (plan B1.6 deliverable 0; each
// rule is unit-tested):
//
//	(a) field names are the PROTO names (snake_case) — runtime marshaling
//	    uses protojson with UseProtoNames, and unmarshaling accepts both
//	    snake_case and camelCase (protojson's default leniency);
//	(b) oneof members project as FLAT OPTIONAL fields, each description
//	    stating the mutual exclusivity; server-side validation names the
//	    conflict (models handle flat-optional better than JSON-Schema oneOf);
//	(c) google.protobuf.Struct fields project as
//	    {"type": "object", "additionalProperties": true} with the annotation
//	    description — never a bare "object"; the (field_schema).property
//	    annotation adds `properties` for the keys such a Struct is known to
//	    carry (the ones with a closed vocabulary), leaving it open;
//	(d) REQUIRED-ness comes ONLY from the (field_schema).required annotation
//	    (proto3 has none);
//	(e) 64-bit integers project as {"type": ["integer", "string"]} — protojson
//	    marshals them as JSON strings and accepts either on unmarshal;
//	(f) enums project as string schemas enumerating the value names
//	    (protojson's representation);
//	(g) descriptions come from the leading comment (SourceCodeInfo),
//	    overridden by (field_schema).doc / (message_schema).doc; examples
//	    from (field_schema).example.
//
// The descriptor set MUST carry SourceCodeInfo for comments to appear —
// a buf/protoc build does, the protoc-gen-go embedded descriptors do not,
// which is why projection happens at BUILD time.
type Projector struct {
	files *protoregistry.Files
}

// NewProjector indexes a FileDescriptorSet (as built by `buf build -o` or
// `protoc --descriptor_set_out --include_source_info`).
func NewProjector(fds *descriptorpb.FileDescriptorSet) (*Projector, error) {
	files, err := protodesc.NewFiles(fds)
	if err != nil {
		return nil, fmt.Errorf("mcpschema: index descriptor set: %w", err)
	}
	return &Projector{files: files}, nil
}

// message resolves a message descriptor by full name.
func (p *Projector) message(name string) (protoreflect.MessageDescriptor, error) {
	d, err := p.files.FindDescriptorByName(protoreflect.FullName(name))
	if err != nil {
		return nil, fmt.Errorf("mcpschema: resolve %s: %w", name, err)
	}
	md, ok := d.(protoreflect.MessageDescriptor)
	if !ok {
		return nil, fmt.Errorf("mcpschema: %s is a %T, not a message", name, d)
	}
	return md, nil
}

// MessageSchema projects one message into a JSON-Schema object (rule set
// above). The top-level description is the message's doc.
func (p *Projector) MessageSchema(name string) (map[string]any, error) {
	md, err := p.message(name)
	if err != nil {
		return nil, err
	}
	return p.messageSchema(md, map[protoreflect.FullName]bool{})
}

func (p *Projector) messageDoc(md protoreflect.MessageDescriptor) string {
	if opts, ok := md.Options().(*descriptorpb.MessageOptions); ok && opts != nil {
		if proto.HasExtension(opts, agentcoordpb.E_MessageSchema) {
			ms, _ := proto.GetExtension(opts, agentcoordpb.E_MessageSchema).(*agentcoordpb.MessageSchema)
			if ms.GetDoc() != "" {
				return ms.GetDoc()
			}
		}
	}
	return p.leadingComment(md)
}

// recursionNotice is the description a TRUNCATED message projection carries.
// It is the only signal distinguishing "this object was cut off because its
// own type is already on the projection stack" from "this object has no
// declared properties", so every path that assembles a description around a
// truncated projection has to preserve it.
func recursionNotice(name protoreflect.FullName) string {
	return fmt.Sprintf("(recursive %s)", name)
}

// messageSchema is the recursive body. seen guards self-recursive shapes
// (e.g. Usage.per_model): a message already on the projection stack renders
// as an open object naming the recursion.
func (p *Projector) messageSchema(md protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool) (map[string]any, error) {
	if seen[md.FullName()] {
		return map[string]any{
			"type":                 "object",
			"additionalProperties": true,
			"description":          recursionNotice(md.FullName()),
		}, nil
	}
	seen[md.FullName()] = true
	defer delete(seen, md.FullName())

	props := map[string]any{}
	var required []string
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fld := fields.Get(i)
		schema, err := p.fieldSchema(fld, seen)
		if err != nil {
			return nil, err
		}
		props[string(fld.Name())] = schema
		if ann := fieldAnnotation(fld); ann.GetRequired() {
			required = append(required, string(fld.Name()))
		}
	}
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if doc := p.messageDoc(md); doc != "" {
		out["description"] = doc
	}
	if len(required) > 0 {
		sort.Strings(required)
		out["required"] = required
	}
	return out, nil
}

// fieldSchema projects one field: base kind schema, repeated/map wrapping,
// description assembly, example.
func (p *Projector) fieldSchema(fld protoreflect.FieldDescriptor, seen map[protoreflect.FullName]bool) (map[string]any, error) {
	schema, err := p.baseFieldSchema(fld, seen)
	if err != nil {
		return nil, err
	}
	if err := applyPropertyAnnotations(fld, schema); err != nil {
		return nil, err
	}
	if desc := p.fieldDescription(fld, seen); desc != "" {
		schema["description"] = desc
	}
	ex := fieldAnnotation(fld).GetExample()
	if ex == "" {
		return schema, nil
	}
	var v any
	if err := json.Unmarshal([]byte(ex), &v); err != nil {
		// The annotation is hand-authored in the .proto and read only here,
		// so a typo that costs the model its example is invisible unless the
		// generator refuses it.
		return nil, fmt.Errorf("mcpschema: field %s: (field_schema).example is not valid JSON: %w", fld.FullName(), err)
	}
	schema["examples"] = []any{v}
	return schema, nil
}

// applyPropertyAnnotations projects (field_schema).property onto an open
// object schema: the declared keys become `properties`, and the object stays
// open (additionalProperties keeps whatever the base projection set, which
// for a Struct is true) so the undeclared keys are still accepted.
//
// It refuses to apply to anything that is not an object, and refuses a
// property with no name: both mean the annotation says something the emitted
// schema cannot carry, and a generator that dropped it silently would leave
// a vocabulary undeclared on the model-facing surface with nothing to say so.
func applyPropertyAnnotations(fld protoreflect.FieldDescriptor, schema map[string]any) error {
	declared := fieldAnnotation(fld).GetProperty()
	if len(declared) == 0 {
		return nil
	}
	if schema["type"] != "object" {
		return fmt.Errorf("mcpschema: field %s carries (field_schema).property but does not project as an object", fld.FullName())
	}
	props := map[string]any{}
	for _, d := range declared {
		if d.GetName() == "" {
			return fmt.Errorf("mcpschema: field %s has a (field_schema).property with no name", fld.FullName())
		}
		kind := d.GetType()
		if kind == "" {
			kind = "string"
		}
		prop := map[string]any{"type": kind}
		if doc := d.GetDoc(); doc != "" {
			prop["description"] = doc
		}
		if members := d.GetEnumValues(); len(members) > 0 {
			enum := make([]any, 0, len(members))
			for _, m := range members {
				enum = append(enum, m)
			}
			prop["enum"] = enum
		}
		props[d.GetName()] = prop
	}
	schema["properties"] = props
	return nil
}

// baseFieldSchema projects the field's kind, wrapped for repeated and map
// cardinalities.
func (p *Projector) baseFieldSchema(fld protoreflect.FieldDescriptor, seen map[protoreflect.FullName]bool) (map[string]any, error) {
	switch {
	case fld.IsMap():
		val, err := p.singularSchema(fld.MapValue(), seen)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "object", "additionalProperties": val}, nil
	case fld.IsList():
		item, err := p.singularSchema(fld, seen)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": item}, nil
	default:
		return p.singularSchema(fld, seen)
	}
}

// fieldDescription assembles a field's description: its doc (comment or
// annotation override), the oneof exclusivity note, and the truncation notice
// when the field projects onto a message already on the projection stack.
func (p *Projector) fieldDescription(fld protoreflect.FieldDescriptor, seen map[protoreflect.FullName]bool) string {
	desc := p.fieldDoc(fld)
	// Rule (b): oneof members are flat optional fields whose descriptions
	// state the mutual exclusivity; the server names the conflict.
	if oo := fld.ContainingOneof(); oo != nil && !oo.IsSynthetic() {
		var members []string
		for i := 0; i < oo.Fields().Len(); i++ {
			members = append(members, string(oo.Fields().Get(i).Name()))
		}
		desc = joinSentences(desc, fmt.Sprintf("At most one of %s is set", strings.Join(members, " / ")))
	}
	if name, truncated := truncatedTarget(fld, seen); truncated {
		desc = joinSentences(desc, recursionNotice(name))
	}
	return desc
}

// truncatedTarget names the message a SINGULAR field projects onto when that
// message is already on the projection stack — the one shape whose truncation
// notice sits directly on the field schema (a repeated or mapped recursion
// carries it on `items`/`additionalProperties`, out of the description's way).
func truncatedTarget(fld protoreflect.FieldDescriptor, seen map[protoreflect.FullName]bool) (protoreflect.FullName, bool) {
	if fld.IsList() || fld.IsMap() {
		return "", false
	}
	if fld.Kind() != protoreflect.MessageKind && fld.Kind() != protoreflect.GroupKind {
		return "", false
	}
	name := fld.Message().FullName()
	return name, seen[name]
}

// joinSentences appends a clause to a description, tolerating an empty lead.
func joinSentences(desc, clause string) string {
	if desc == "" {
		return clause
	}
	return desc + ". " + clause
}

// scalarSchema maps a scalar proto kind onto its JSON shape, reporting whether
// the kind is a scalar at all.
func scalarSchema(kind protoreflect.Kind) (map[string]any, bool) {
	switch kind {
	case protoreflect.BoolKind:
		return map[string]any{"type": "boolean"}, true
	case protoreflect.StringKind:
		return map[string]any{"type": "string"}, true
	case protoreflect.BytesKind:
		return map[string]any{"type": "string", "contentEncoding": "base64"}, true
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return map[string]any{"type": "integer"}, true
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		// Rule (e): protojson marshals 64-bit integers as strings and
		// accepts either representation on unmarshal.
		return map[string]any{"type": []any{"integer", "string"}}, true
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return map[string]any{"type": "number"}, true
	}
	return nil, false
}

// enumSchema projects an enum as a string over its value names (rule (f):
// protojson's representation).
func enumSchema(ed protoreflect.EnumDescriptor) map[string]any {
	vals := ed.Values()
	names := make([]any, 0, vals.Len())
	for i := 0; i < vals.Len(); i++ {
		names = append(names, string(vals.Get(i).Name()))
	}
	return map[string]any{"type": "string", "enum": names}
}

// singularSchema maps one scalar/message/enum occurrence to its JSON shape.
// Every kind protodesc admits is covered; anything else is a corrupt
// descriptor and must not project as an unconstrained "any JSON value".
func (p *Projector) singularSchema(fld protoreflect.FieldDescriptor, seen map[protoreflect.FullName]bool) (map[string]any, error) {
	if schema, ok := scalarSchema(fld.Kind()); ok {
		return schema, nil
	}
	switch fld.Kind() {
	case protoreflect.EnumKind:
		return enumSchema(fld.Enum()), nil
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return p.wellKnownOrMessage(fld.Message(), seen)
	}
	return nil, fmt.Errorf("mcpschema: field %s has unprojectable kind %v", fld.FullName(), fld.Kind())
}

// wellKnownOrMessage maps the well-known types onto their protojson wire
// shapes and recurses into everything else.
func (p *Projector) wellKnownOrMessage(md protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool) (map[string]any, error) {
	switch md.FullName() {
	case "google.protobuf.Struct":
		// Rule (c): open JSON object, never a bare "object".
		return map[string]any{"type": "object", "additionalProperties": true}, nil
	case "google.protobuf.Value":
		return map[string]any{}, nil // any JSON value
	case "google.protobuf.Duration":
		return map[string]any{"type": "string", "description": "Duration, e.g. \"3s\""}, nil
	case "google.protobuf.Timestamp":
		return map[string]any{"type": "string", "format": "date-time"}, nil
	default:
		return p.messageSchema(md, seen)
	}
}

// fieldDoc returns the field's description: (field_schema).doc override,
// else the leading comment.
func (p *Projector) fieldDoc(fld protoreflect.FieldDescriptor) string {
	if doc := fieldAnnotation(fld).GetDoc(); doc != "" {
		return doc
	}
	return p.leadingComment(fld)
}

// fieldAnnotation reads the D3 (field_schema) option, zero-valued when
// absent.
func fieldAnnotation(fld protoreflect.FieldDescriptor) *agentcoordpb.FieldSchema {
	opts, ok := fld.Options().(*descriptorpb.FieldOptions)
	if !ok || opts == nil || !proto.HasExtension(opts, agentcoordpb.E_FieldSchema) {
		return &agentcoordpb.FieldSchema{}
	}
	fs, ok := proto.GetExtension(opts, agentcoordpb.E_FieldSchema).(*agentcoordpb.FieldSchema)
	if !ok || fs == nil {
		return &agentcoordpb.FieldSchema{}
	}
	return fs
}

// leadingComment extracts a descriptor's comment from SourceCodeInfo
// (present only in build-time descriptor sets), normalized to single-space
// prose. Leading comments win; a same-line trailing comment (`string x = 1;
// // doc` — the file's dominant field-doc style) is the fallback.
func (p *Projector) leadingComment(d protoreflect.Descriptor) string {
	loc := d.ParentFile().SourceLocations().ByDescriptor(d)
	if c := normalizeComment(loc.LeadingComments); c != "" {
		return c
	}
	return normalizeComment(loc.TrailingComments)
}

// normalizeComment flattens a proto comment block into one prose line: each
// line's leading space trimmed, lines joined with single spaces, trailing
// period-noise preserved as written.
func normalizeComment(c string) string {
	if c == "" {
		return ""
	}
	lines := strings.Split(c, "\n")
	var parts []string
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			parts = append(parts, ln)
		}
	}
	return strings.Join(parts, " ")
}

// ToolSpec is one generated tool surface entry — the checked-in golden's
// shape and the runtime registration payload.
type ToolSpec struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
}

// ProjectTool builds one binding's ToolSpec: input schema (message-backed or
// synthetic, closed against invented argument names), output schema, and the
// tool description (message doc, or the binding's synthetic one).
func (p *Projector) ProjectTool(b Binding) (*ToolSpec, error) {
	spec := &ToolSpec{Name: b.Tool, Description: b.Description}

	in, doc, err := p.toolInput(b)
	if err != nil {
		return nil, err
	}
	if doc != "" {
		spec.Description = doc
	}
	if spec.Description == "" {
		return nil, fmt.Errorf("tool %s: no description (annotate the message with (message_schema).doc or set Binding.Description)", b.Tool)
	}
	if spec.InputSchema, err = marshalSchema(in); err != nil {
		return nil, err
	}

	out, err := p.toolOutput(b)
	if err != nil {
		return nil, err
	}
	if out != nil {
		if spec.OutputSchema, err = marshalSchema(out); err != nil {
			return nil, err
		}
	}
	return spec, nil
}

// toolInput resolves, validates and closes one binding's input schema, and
// reports the description the bound message carries (empty when the binding's
// own Description stands).
func (p *Projector) toolInput(b Binding) (map[string]any, string, error) {
	in, err := p.sideSchema(b.Input, b.SyntheticInput)
	if err != nil {
		return nil, "", fmt.Errorf("tool %s input: %w", b.Tool, err)
	}
	if in == nil {
		return nil, "", fmt.Errorf("tool %s: no input schema (binding needs Input or SyntheticInput)", b.Tool)
	}
	if err := assertObjectSchema(b.Tool, "input", in); err != nil {
		return nil, "", err
	}
	closeObjects(in)
	// The top level is closed unconditionally: whatever a synthetic builder
	// declared, models must not invent argument names.
	in["additionalProperties"] = false
	if b.Input == "" {
		return in, "", nil
	}
	md, derr := p.message(b.Input)
	if derr != nil {
		return in, "", nil
	}
	doc := p.messageDoc(md)
	if doc != "" {
		// The tool description carries the doc; keep the schema lean.
		delete(in, "description")
	}
	return in, doc, nil
}

// toolOutput resolves and validates one binding's output schema, or nil when
// the tool has no structured output.
func (p *Projector) toolOutput(b Binding) (map[string]any, error) {
	out, err := p.sideSchema(b.Output, b.SyntheticOutput)
	if err != nil {
		return nil, fmt.Errorf("tool %s output: %w", b.Tool, err)
	}
	if out == nil {
		return nil, nil
	}
	if err := assertObjectSchema(b.Tool, "output", out); err != nil {
		return nil, err
	}
	return out, nil
}

// assertObjectSchema enforces what the MCP SDK requires of a registered tool:
// mcp.Server.AddTool PANICS on an input or output schema whose type is not
// "object", and these schemas are checked in and embedded, so a bad one takes
// down every runner at startup. Refuse it at generation time instead.
func assertObjectSchema(tool, side string, schema map[string]any) error {
	if schema["type"] != "object" {
		return fmt.Errorf("tool %s %s schema: type is %v, MCP requires \"object\"", tool, side, schema["type"])
	}
	return nil
}

// closeObjects closes every object in an input schema that has not declared
// its own openness. protojson unmarshals tool arguments WITHOUT
// DiscardUnknown, so an invented key is rejected at any depth — a schema that
// says so only at the top level under-states what the handler enforces, and a
// model learns the constraint from a failed call instead of from the schema. A
// schema that sets additionalProperties itself (Struct's open object, a map's
// value schema, a truncated recursion) is left exactly as projected.
func closeObjects(schema map[string]any) {
	if schema["type"] == "object" {
		if _, declared := schema["additionalProperties"]; !declared {
			schema["additionalProperties"] = false
		}
	}
	for _, key := range []string{"items", "additionalProperties"} {
		if child, ok := schema[key].(map[string]any); ok {
			closeObjects(child)
		}
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return
	}
	for _, v := range props {
		if child, ok := v.(map[string]any); ok {
			closeObjects(child)
		}
	}
}

// sideSchema resolves one side of a binding: the bound message's projection,
// the synthetic builder, or nil when the side is absent (a tool with no
// structured output).
func (p *Projector) sideSchema(msg string, synthetic func(*Projector) (map[string]any, error)) (map[string]any, error) {
	switch {
	case msg != "":
		return p.MessageSchema(msg)
	case synthetic != nil:
		return synthetic(p)
	default:
		return nil, nil
	}
}

func marshalSchema(m map[string]any) (json.RawMessage, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("mcpschema: marshal schema: %w", err)
	}
	return raw, nil
}
