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
//	    description — never a bare "object";
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
	return p.messageSchema(md, map[protoreflect.FullName]bool{}), nil
}

// MessageDoc returns the message's LLM-facing description:
// (message_schema).doc when annotated, else its leading comment.
func (p *Projector) MessageDoc(name string) (string, error) {
	md, err := p.message(name)
	if err != nil {
		return "", err
	}
	return p.messageDoc(md), nil
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

// messageSchema is the recursive body. seen guards self-recursive shapes
// (e.g. Usage.per_model): a message already on the projection stack renders
// as an open object naming the recursion.
func (p *Projector) messageSchema(md protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool) map[string]any {
	if seen[md.FullName()] {
		return map[string]any{
			"type":                 "object",
			"additionalProperties": true,
			"description":          fmt.Sprintf("(recursive %s)", md.FullName()),
		}
	}
	seen[md.FullName()] = true
	defer delete(seen, md.FullName())

	props := map[string]any{}
	var required []string
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fld := fields.Get(i)
		schema := p.fieldSchema(fld, seen)
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
	return out
}

// fieldSchema projects one field: base kind schema, repeated/map wrapping,
// description assembly (comment/doc-override, oneof exclusivity note,
// example).
func (p *Projector) fieldSchema(fld protoreflect.FieldDescriptor, seen map[protoreflect.FullName]bool) map[string]any {
	var schema map[string]any
	switch {
	case fld.IsMap():
		schema = map[string]any{
			"type":                 "object",
			"additionalProperties": p.singularSchema(fld.MapValue(), seen),
		}
	case fld.IsList():
		schema = map[string]any{
			"type":  "array",
			"items": p.singularSchema(fld, seen),
		}
	default:
		schema = p.singularSchema(fld, seen)
	}

	desc := p.fieldDoc(fld)
	// Rule (b): oneof members are flat optional fields whose descriptions
	// state the mutual exclusivity; the server names the conflict.
	if oo := fld.ContainingOneof(); oo != nil && !oo.IsSynthetic() {
		var members []string
		for i := 0; i < oo.Fields().Len(); i++ {
			members = append(members, string(oo.Fields().Get(i).Name()))
		}
		note := fmt.Sprintf("At most one of %s is set", strings.Join(members, " / "))
		if desc == "" {
			desc = note
		} else {
			desc += ". " + note
		}
	}
	if desc != "" {
		schema["description"] = desc
	}
	if ex := fieldAnnotation(fld).GetExample(); ex != "" {
		var v any
		if err := json.Unmarshal([]byte(ex), &v); err == nil {
			schema["examples"] = []any{v}
		}
	}
	return schema
}

// singularSchema maps one scalar/message/enum occurrence to its JSON shape.
func (p *Projector) singularSchema(fld protoreflect.FieldDescriptor, seen map[protoreflect.FullName]bool) map[string]any {
	switch fld.Kind() {
	case protoreflect.BoolKind:
		return map[string]any{"type": "boolean"}
	case protoreflect.StringKind:
		return map[string]any{"type": "string"}
	case protoreflect.BytesKind:
		return map[string]any{"type": "string", "contentEncoding": "base64"}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return map[string]any{"type": "integer"}
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		// Rule (e): protojson marshals 64-bit integers as strings and
		// accepts either representation on unmarshal.
		return map[string]any{"type": []any{"integer", "string"}}
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return map[string]any{"type": "number"}
	case protoreflect.EnumKind:
		// Rule (f): protojson uses value names.
		vals := fld.Enum().Values()
		names := make([]any, 0, vals.Len())
		for i := 0; i < vals.Len(); i++ {
			names = append(names, string(vals.Get(i).Name()))
		}
		return map[string]any{"type": "string", "enum": names}
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return p.wellKnownOrMessage(fld.Message(), seen)
	default:
		return map[string]any{}
	}
}

// wellKnownOrMessage maps the well-known types onto their protojson wire
// shapes and recurses into everything else.
func (p *Projector) wellKnownOrMessage(md protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool) map[string]any {
	switch md.FullName() {
	case "google.protobuf.Struct":
		// Rule (c): open JSON object, never a bare "object".
		return map[string]any{"type": "object", "additionalProperties": true}
	case "google.protobuf.Value":
		return map[string]any{} // any JSON value
	case "google.protobuf.Duration":
		return map[string]any{"type": "string", "description": "Duration, e.g. \"3s\""}
	case "google.protobuf.Timestamp":
		return map[string]any{"type": "string", "format": "date-time"}
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
	return strings.TrimSuffix(strings.Join(parts, " "), " ")
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
// synthetic, additionalProperties always closed on inputs), output schema,
// and the tool description (message doc, or the binding's synthetic one).
func (p *Projector) ProjectTool(b Binding) (*ToolSpec, error) {
	spec := &ToolSpec{Name: b.Tool, Description: b.Description}

	in, err := p.sideSchema(b.Input, b.SyntheticInput)
	if err != nil {
		return nil, fmt.Errorf("tool %s input: %w", b.Tool, err)
	}
	if in == nil {
		return nil, fmt.Errorf("tool %s: no input schema (binding needs Input or SyntheticInput)", b.Tool)
	}
	// Inputs are closed: models must not invent argument names.
	in["additionalProperties"] = false
	if b.Input != "" {
		if doc, derr := p.MessageDoc(b.Input); derr == nil && doc != "" {
			spec.Description = doc
			// The tool description carries the doc; keep the schema lean.
			delete(in, "description")
		}
	}
	if spec.InputSchema, err = marshalSchema(in); err != nil {
		return nil, err
	}

	out, err := p.sideSchema(b.Output, b.SyntheticOutput)
	if err != nil {
		return nil, fmt.Errorf("tool %s output: %w", b.Tool, err)
	}
	if out != nil {
		if spec.OutputSchema, err = marshalSchema(out); err != nil {
			return nil, err
		}
	}
	if spec.Description == "" {
		return nil, fmt.Errorf("tool %s: no description (annotate the message with (message_schema).doc or set Binding.Description)", b.Tool)
	}
	return spec, nil
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
