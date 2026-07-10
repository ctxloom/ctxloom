package mcpschema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	pdesc "google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// syntheticFDS builds a small FileDescriptorSet exercising every projection
// rule: comments (leading + trailing via SourceCodeInfo), a D3 required/doc/
// example annotation, a oneof, a Struct field, 64-bit ints, an enum, a map,
// and a repeated field.
func syntheticFDS(t *testing.T) *descriptorpb.FileDescriptorSet {
	t.Helper()

	fieldOpts := func(fs *agentcoordpb.FieldSchema) *descriptorpb.FieldOptions {
		o := &descriptorpb.FieldOptions{}
		proto.SetExtension(o, agentcoordpb.E_FieldSchema, fs)
		return o
	}

	strKind := descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
	f := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("synthetic.proto"),
		Package: proto.String("synthetic.v1"),
		Syntax:  proto.String("proto3"),
		Dependency: []string{
			"google/protobuf/struct.proto",
		},
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: proto.String("Mood"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: proto.String("MOOD_UNSPECIFIED"), Number: proto.Int32(0)},
				{Name: proto.String("MOOD_HAPPY"), Number: proto.Int32(1)},
			},
		}},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Thing"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{
					Name: proto.String("name"), Number: proto.Int32(1), Type: strKind,
					Label:   descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					Options: fieldOpts(&agentcoordpb.FieldSchema{Required: true, Example: `"widget"`}),
				},
				{
					Name: proto.String("blob"), Number: proto.Int32(2),
					Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
					TypeName: proto.String(".google.protobuf.Struct"),
					Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					Options:  fieldOpts(&agentcoordpb.FieldSchema{Doc: "open JSON payload"}),
				},
				{
					Name: proto.String("big"), Number: proto.Int32(3),
					Type:  descriptorpb.FieldDescriptorProto_TYPE_UINT64.Enum(),
					Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				},
				{
					Name: proto.String("mood"), Number: proto.Int32(4),
					Type:     descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(),
					TypeName: proto.String(".synthetic.v1.Mood"),
					Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				},
				{
					Name: proto.String("tags"), Number: proto.Int32(5), Type: strKind,
					Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
				},
				{
					Name: proto.String("left"), Number: proto.Int32(6), Type: strKind,
					Label:      descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					OneofIndex: proto.Int32(0),
				},
				{
					Name: proto.String("right"), Number: proto.Int32(7), Type: strKind,
					Label:      descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					OneofIndex: proto.Int32(0),
				},
			},
			OneofDecl: []*descriptorpb.OneofDescriptorProto{{Name: proto.String("side")}},
		}},
		SourceCodeInfo: &descriptorpb.SourceCodeInfo{
			Location: []*descriptorpb.SourceCodeInfo_Location{
				{
					// message Thing (path: message_type[0])
					Path:            []int32{4, 0},
					Span:            []int32{1, 0, 1},
					LeadingComments: proto.String(" A thing under test.\n"),
				},
				{
					// Thing.name (path: message_type[0].field[0])
					Path:            []int32{4, 0, 2, 0},
					Span:            []int32{2, 0, 1},
					LeadingComments: proto.String(" The thing's name.\n"),
				},
				{
					// Thing.big — trailing comment only (the fallback rule).
					Path:             []int32{4, 0, 2, 2},
					Span:             []int32{3, 0, 1},
					TrailingComments: proto.String(" fits in 64 bits\n"),
				},
			},
		},
	}

	// The Struct dependency descriptor comes from the live registry.
	structFD := pdesc.ToFileDescriptorProto(structpb.File_google_protobuf_struct_proto)
	return &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{structFD, f}}
}

// projectorFor builds a Projector over the synthetic set.
func projectorFor(t *testing.T) *Projector {
	t.Helper()
	p, err := NewProjector(syntheticFDS(t))
	require.NoError(t, err)
	return p
}

func schemaOf(t *testing.T, p *Projector, name string) map[string]any {
	t.Helper()
	s, err := p.MessageSchema(name)
	require.NoError(t, err)
	return s
}

func props(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	m, ok := schema["properties"].(map[string]any)
	require.True(t, ok, "schema has properties: %v", schema)
	return m
}

// Rule (a): proto (snake_case) names are what the schema advertises.
func TestProjection_ProtoNames(t *testing.T) {
	p := projectorFor(t)
	fields := props(t, schemaOf(t, p, "synthetic.v1.Thing"))
	assert.Contains(t, fields, "name")
	assert.Contains(t, fields, "big")
	assert.NotContains(t, fields, "Name")
}

// Rule (d): required-ness comes only from the D3 annotation; rule (g):
// examples ride the annotation too.
func TestProjection_RequiredAndExampleFromAnnotation(t *testing.T) {
	p := projectorFor(t)
	schema := schemaOf(t, p, "synthetic.v1.Thing")
	assert.Equal(t, []any{"name"}, toAnySlice(schema["required"]))
	name := props(t, schema)["name"].(map[string]any)
	assert.Equal(t, []any{"widget"}, toAnySlice(name["examples"]))
}

// Rule (g): leading comments become descriptions; trailing comments are the
// fallback; the annotation doc overrides both.
func TestProjection_CommentsAndDocOverride(t *testing.T) {
	p := projectorFor(t)
	schema := schemaOf(t, p, "synthetic.v1.Thing")
	assert.Equal(t, "A thing under test.", schema["description"])
	fields := props(t, schema)
	assert.Equal(t, "The thing's name.", fields["name"].(map[string]any)["description"])
	assert.Contains(t, fields["big"].(map[string]any)["description"], "fits in 64 bits")
	assert.Equal(t, "open JSON payload", fields["blob"].(map[string]any)["description"])
}

// Rule (c): Struct fields are open objects, never bare "object".
func TestProjection_StructIsOpenObject(t *testing.T) {
	p := projectorFor(t)
	blob := props(t, schemaOf(t, p, "synthetic.v1.Thing"))["blob"].(map[string]any)
	assert.Equal(t, "object", blob["type"])
	assert.Equal(t, true, blob["additionalProperties"])
}

// Rule (e): 64-bit integers accept integer or string (protojson).
func TestProjection_SixtyFourBitDualType(t *testing.T) {
	p := projectorFor(t)
	big := props(t, schemaOf(t, p, "synthetic.v1.Thing"))["big"].(map[string]any)
	assert.ElementsMatch(t, []any{"integer", "string"}, toAnySlice(big["type"]))
}

// Rule (f): enums are string schemas over the value names.
func TestProjection_EnumValueNames(t *testing.T) {
	p := projectorFor(t)
	mood := props(t, schemaOf(t, p, "synthetic.v1.Thing"))["mood"].(map[string]any)
	assert.Equal(t, "string", mood["type"])
	assert.ElementsMatch(t, []any{"MOOD_UNSPECIFIED", "MOOD_HAPPY"}, toAnySlice(mood["enum"]))
}

// Repeated fields project as arrays.
func TestProjection_RepeatedIsArray(t *testing.T) {
	p := projectorFor(t)
	tags := props(t, schemaOf(t, p, "synthetic.v1.Thing"))["tags"].(map[string]any)
	assert.Equal(t, "array", tags["type"])
	assert.Equal(t, "string", tags["items"].(map[string]any)["type"])
}

// Rule (b): oneof members are flat optional fields whose descriptions state
// the mutual exclusivity.
func TestProjection_OneofFlattens(t *testing.T) {
	p := projectorFor(t)
	fields := props(t, schemaOf(t, p, "synthetic.v1.Thing"))
	for _, member := range []string{"left", "right"} {
		desc, _ := fields[member].(map[string]any)["description"].(string)
		assert.Contains(t, desc, "At most one of left / right is set", "member %s", member)
	}
	// Not required: oneof members stay optional.
	req := toAnySlice(schemaOf(t, p, "synthetic.v1.Thing")["required"])
	assert.NotContains(t, req, "left")
}

// Rule (a) runtime half: protojson unmarshal accepts BOTH proto names and
// camelCase for the real contract messages the tools bind.
func TestProtojson_AcceptsBothNameForms(t *testing.T) {
	for _, raw := range []string{
		`{"in_reply_to": "m-1", "text": "hi"}`,
		`{"inReplyTo": "m-1", "text": "hi"}`,
	} {
		var msg agentcoordpb.PeerSendRequest
		require.NoError(t, protojson.Unmarshal([]byte(raw), &msg), raw)
		assert.Equal(t, "m-1", msg.GetInReplyTo())
		assert.Equal(t, "hi", msg.GetText())
	}
}

// Rule (a) marshal half: UseProtoNames emits snake_case, matching the
// advertised schemas.
func TestProtojson_MarshalUsesProtoNames(t *testing.T) {
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(
		&agentcoordpb.PeerSendRequest{InReplyTo: "m-1", Text: "hi"})
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.Contains(t, m, "in_reply_to")
	assert.NotContains(t, m, "inReplyTo")
}

func toAnySlice(v any) []any {
	switch s := v.(type) {
	case []any:
		return s
	case []string:
		out := make([]any, len(s))
		for i, x := range s {
			out[i] = x
		}
		return out
	}
	return nil
}
