package mcpschema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	pdesc "google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// recursiveFDS is a self-recursive message: a SINGULAR field of its own type
// carrying a leading comment, plus the repeated and mapped forms of the same
// recursion. The singular form is the one whose truncation notice has to
// survive the field's own description.
func recursiveFDS(t *testing.T) *descriptorpb.FileDescriptorSet {
	t.Helper()
	msgKind := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum()
	return &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
		Name:    proto.String("recursive.proto"),
		Package: proto.String("recursive.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Node"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{
					Name: proto.String("parent"), Number: proto.Int32(1), Type: msgKind,
					TypeName: proto.String(".recursive.v1.Node"),
					Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				},
				{
					Name: proto.String("children"), Number: proto.Int32(2), Type: msgKind,
					TypeName: proto.String(".recursive.v1.Node"),
					Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
				},
			},
		}},
		SourceCodeInfo: &descriptorpb.SourceCodeInfo{
			Location: []*descriptorpb.SourceCodeInfo_Location{{
				// Node.parent (path: message_type[0].field[0])
				Path:            []int32{4, 0, 2, 0},
				Span:            []int32{2, 0, 1},
				LeadingComments: proto.String(" The enclosing node.\n"),
			}},
		},
	}}}
}

// The "(recursive X)" marker is the ONLY signal that a projected
// object is truncated rather than empty. A commented (or doc-annotated) field
// used to overwrite it wholesale, so the model was handed an open object with
// no hint that its shape had been cut off.
func TestProjection_RecursionNoticeSurvivesAFieldDescription(t *testing.T) {
	p, err := NewProjector(recursiveFDS(t))
	require.NoError(t, err)
	schema, err := p.MessageSchema("recursive.v1.Node")
	require.NoError(t, err)

	parent := props(t, schema)["parent"].(map[string]any)
	desc, _ := parent["description"].(string)
	assert.Contains(t, desc, "The enclosing node.", "the field's own doc still leads")
	assert.Contains(t, desc, "(recursive recursive.v1.Node)",
		"the truncation notice must survive the field description")
	assert.Equal(t, true, parent["additionalProperties"], "a truncated object stays open")
}

// The repeated form has always been safe (the notice lives on `items`, which no
// field description touches) — pin it so the fix cannot regress it into the
// singular form's bug.
func TestProjection_RecursionNoticeOnRepeatedItems(t *testing.T) {
	p, err := NewProjector(recursiveFDS(t))
	require.NoError(t, err)
	schema, err := p.MessageSchema("recursive.v1.Node")
	require.NoError(t, err)

	children := props(t, schema)["children"].(map[string]any)
	items := children["items"].(map[string]any)
	assert.Contains(t, items["description"], "(recursive recursive.v1.Node)")
}

// exampleFDS builds a one-field message whose (field_schema).example carries the
// supplied literal.
func exampleFDS(t *testing.T, example string) *descriptorpb.FileDescriptorSet {
	t.Helper()
	opts := &descriptorpb.FieldOptions{}
	proto.SetExtension(opts, agentcoordpb.E_FieldSchema, &agentcoordpb.FieldSchema{Example: example})
	return &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
		Name:    proto.String("example.proto"),
		Package: proto.String("example.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Sample"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name: proto.String("name"), Number: proto.Int32(1),
				Type:    descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				Label:   descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Options: opts,
			}},
		}},
	}}}
}

// A malformed (field_schema).example was dropped on the floor. The
// annotation is authored by hand in the .proto and only the generator ever
// reads it, so a typo that silently costs the model its example is invisible
// until someone diffs a golden by eye.
func TestProjection_MalformedExampleFailsGeneration(t *testing.T) {
	p, err := NewProjector(exampleFDS(t, `{"unterminated":`))
	require.NoError(t, err)
	_, err = p.MessageSchema("example.v1.Sample")
	require.Error(t, err, "a malformed example annotation must fail the build")
	assert.Contains(t, err.Error(), "example")
	assert.Contains(t, err.Error(), "name", "the error names the offending field")
}

// A well-formed example still projects.
func TestProjection_WellFormedExampleProjects(t *testing.T) {
	p, err := NewProjector(exampleFDS(t, `"widget"`))
	require.NoError(t, err)
	schema, err := p.MessageSchema("example.v1.Sample")
	require.NoError(t, err)
	assert.Equal(t, []any{"widget"}, toAnySlice(props(t, schema)["name"].(map[string]any)["examples"]))
}

// nestedFDS builds a tool-input-shaped message with a nested message field, a
// repeated nested field, a map of nested values, and a Struct field — the four
// places an object can appear below the top level of an input schema.
func nestedFDS(t *testing.T) *descriptorpb.FileDescriptorSet {
	t.Helper()
	msgKind := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum()
	strKind := descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
	inner := &descriptorpb.DescriptorProto{
		Name: proto.String("Inner"),
		Field: []*descriptorpb.FieldDescriptorProto{{
			Name: proto.String("label"), Number: proto.Int32(1), Type: strKind,
			Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		}},
	}
	entry := &descriptorpb.DescriptorProto{
		Name:    proto.String("ByNameEntry"),
		Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
		Field: []*descriptorpb.FieldDescriptorProto{
			{
				Name: proto.String("key"), Number: proto.Int32(1), Type: strKind,
				Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			},
			{
				Name: proto.String("value"), Number: proto.Int32(2), Type: msgKind,
				TypeName: proto.String(".nested.v1.Inner"),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			},
		},
	}
	outer := &descriptorpb.DescriptorProto{
		Name:       proto.String("Outer"),
		NestedType: []*descriptorpb.DescriptorProto{entry},
		Field: []*descriptorpb.FieldDescriptorProto{
			{
				Name: proto.String("inner"), Number: proto.Int32(1), Type: msgKind,
				TypeName: proto.String(".nested.v1.Inner"),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			},
			{
				Name: proto.String("many"), Number: proto.Int32(2), Type: msgKind,
				TypeName: proto.String(".nested.v1.Inner"),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
			},
			{
				Name: proto.String("by_name"), Number: proto.Int32(3), Type: msgKind,
				TypeName: proto.String(".nested.v1.Outer.ByNameEntry"),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
			},
			{
				Name: proto.String("blob"), Number: proto.Int32(4), Type: msgKind,
				TypeName: proto.String(".google.protobuf.Struct"),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			},
		},
	}
	structFD := pdesc.ToFileDescriptorProto(structpb.File_google_protobuf_struct_proto)
	return &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{structFD, {
		Name:        proto.String("nested.proto"),
		Package:     proto.String("nested.v1"),
		Syntax:      proto.String("proto3"),
		Dependency:  []string{"google/protobuf/struct.proto"},
		MessageType: []*descriptorpb.DescriptorProto{inner, outer},
	}}}
}

// `additionalProperties: false` used to be stamped only on the top
// level, so a model was told it could invent keys inside a nested argument
// object — while the runner's protojson unmarshal (no DiscardUnknown) rejects
// an invented key at ANY depth. The schema has to say what the handler does.
func TestProjectTool_ClosesNestedInputObjects(t *testing.T) {
	p, err := NewProjector(nestedFDS(t))
	require.NoError(t, err)
	spec, err := p.ProjectTool(Binding{Tool: "nested", Input: "nested.v1.Outer", Description: "d"})
	require.NoError(t, err)

	var in map[string]any
	require.NoError(t, json.Unmarshal(spec.InputSchema, &in))
	assert.Equal(t, false, in["additionalProperties"], "top level stays closed")

	fields := props(t, in)
	assert.Equal(t, false, fields["inner"].(map[string]any)["additionalProperties"],
		"a nested message argument is closed")
	assert.Equal(t, false, fields["many"].(map[string]any)["items"].(map[string]any)["additionalProperties"],
		"a repeated message argument's items are closed")
	assert.Equal(t, false,
		fields["by_name"].(map[string]any)["additionalProperties"].(map[string]any)["additionalProperties"],
		"a map's value schema is closed")
}

// The counterweight: schemas that declare their own openness keep it. A Struct
// is an open JSON payload by rule (c), and a map's own additionalProperties is
// the VALUE SCHEMA, not a boolean — closing either would corrupt the surface.
func TestProjectTool_LeavesDeliberatelyOpenObjectsOpen(t *testing.T) {
	p, err := NewProjector(nestedFDS(t))
	require.NoError(t, err)
	spec, err := p.ProjectTool(Binding{Tool: "nested", Input: "nested.v1.Outer", Description: "d"})
	require.NoError(t, err)

	var in map[string]any
	require.NoError(t, json.Unmarshal(spec.InputSchema, &in))
	fields := props(t, in)
	assert.Equal(t, true, fields["blob"].(map[string]any)["additionalProperties"],
		"a Struct argument stays an open JSON object")
	assert.IsType(t, map[string]any{}, fields["by_name"].(map[string]any)["additionalProperties"],
		"a map keeps its value schema, not a boolean")
}

// mcp.Server.AddTool PANICS when a registered tool's input or output
// schema is not type "object" (go-sdk mcp/server.go). These schemas are checked
// in and embedded, so a non-object one takes down every child runner at
// startup — the generator must refuse it instead.
func TestProjectTool_RejectsANonObjectSchema(t *testing.T) {
	p := projectorFor(t)
	notAnObject := func(*Projector) (map[string]any, error) {
		return map[string]any{"type": "string"}, nil
	}
	object := func(*Projector) (map[string]any, error) {
		return map[string]any{"type": "object", "properties": map[string]any{}}, nil
	}

	_, err := p.ProjectTool(Binding{Tool: "bad_in", Description: "d", SyntheticInput: notAnObject})
	require.Error(t, err, "a non-object INPUT schema must not reach a golden")
	assert.Contains(t, err.Error(), "object")

	_, err = p.ProjectTool(Binding{Tool: "bad_out", Description: "d", SyntheticInput: object, SyntheticOutput: notAnObject})
	require.Error(t, err, "a non-object OUTPUT schema must not reach a golden")
	assert.Contains(t, err.Error(), "object")
}

// A finding is REFUTED on reachability, and this is the pin. singularSchema's
// fallthrough cannot silently emit "any JSON value" for a real descriptor:
// protodesc rejects an unresolvable or out-of-range field kind before any
// Projector exists, so no field reaching the projector can miss the switch.
func TestProjection_ProtodescRejectsAnUnprojectableKind(t *testing.T) {
	for name, typ := range map[string]*descriptorpb.FieldDescriptorProto_Type{
		"unset":        nil,
		"out of range": descriptorpb.FieldDescriptorProto_Type(99).Enum(),
	} {
		_, err := NewProjector(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
			Name:    proto.String("bad.proto"),
			Package: proto.String("bad.v1"),
			Syntax:  proto.String("proto3"),
			MessageType: []*descriptorpb.DescriptorProto{{
				Name: proto.String("Bad"),
				Field: []*descriptorpb.FieldDescriptorProto{{
					Name: proto.String("x"), Number: proto.Int32(1), Type: typ,
					Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				}},
			}},
		}}})
		require.Error(t, err, "a %s field kind must be rejected at indexing time", name)
	}
}

// Every kind the projector can actually be handed projects to a constrained
// schema — the other half of the refutation above: the switch is exhaustive over
// what protodesc admits, so the fallthrough is unreachable rather than merely
// unexercised.
func TestProjection_EveryAdmittedKindProjectsConstrained(t *testing.T) {
	p := projectorFor(t)
	schema := schemaOf(t, p, "synthetic.v1.Thing")
	for name, field := range props(t, schema) {
		assert.NotEmpty(t, field.(map[string]any)["type"], "field %s projects a type", name)
	}
	nested, err := NewProjector(nestedFDS(t))
	require.NoError(t, err)
	outer, err := nested.MessageSchema("nested.v1.Outer")
	require.NoError(t, err)
	for name, field := range props(t, outer) {
		assert.NotEmpty(t, field.(map[string]any)["type"], "field %s projects a type", name)
	}
}
