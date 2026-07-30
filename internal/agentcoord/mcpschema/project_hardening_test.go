package mcpschema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

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

// U026-F17: the "(recursive X)" marker is the ONLY signal that a projected
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

// U026-F08: a malformed (field_schema).example was dropped on the floor. The
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
