package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
)

// TestHandleTagSchemaResource_DefaultSchema exercises the tag-schema
// resource against a fresh project (no config.yaml — the project's default
// tag_schema, taskloomconfig.DefaultTagSchema, resolves): the response must
// carry triage:kind's declared scalar arity, its enum, and its
// priority_fn/decay_fn formulas, plus triage:effort's declared 0-5 range.
func TestHandleTagSchemaResource_DefaultSchema(t *testing.T) {
	withProjectDir(t)

	res, err := handleTagSchemaResource(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, res.Contents, 1)
	assert.Equal(t, tagSchemaResourceURI, res.Contents[0].URI)
	assert.Equal(t, "application/json", res.Contents[0].MIMEType)
	require.NotEmpty(t, res.Contents[0].Text)

	var doc tagSchemaDoc
	require.NoError(t, json.Unmarshal([]byte(res.Contents[0].Text), &doc))

	byTarget := map[string]tagSchemaTargetDoc{}
	for _, e := range doc.Targets {
		byTarget[e.Target] = e
	}

	require.Contains(t, byTarget, "triage:kind")
	kind := byTarget["triage:kind"]
	assert.True(t, kind.Scalar, "triage:kind must be reported scalar")
	assert.Contains(t, kind.Enum, "defect")
	assert.Contains(t, kind.Enum, "capability")
	assert.Contains(t, kind.Enum, "chore")
	assert.NotEmpty(t, kind.PriorityFn, "triage:kind's priority_fn must be present")
	assert.NotEmpty(t, kind.DecayFn, "triage:kind's decay_fn must be present")

	require.Contains(t, byTarget, "triage:effort")
	effort := byTarget["triage:effort"]
	assert.True(t, effort.Scalar)
	require.NotNil(t, effort.Range, "triage:effort must carry a declared range")
	assert.Equal(t, 0.0, effort.Range.Min)
	assert.Equal(t, 5.0, effort.Range.Max)
}

// TestHandleTagVocabularyResource_ReflectsTagsInUse proves the vocabulary
// resource is the live MCP twin of `taskloom tags`: a tag applied to two
// tasks comes back with active=2, total=2.
func TestHandleTagVocabularyResource_ReflectsTagsInUse(t *testing.T) {
	withProjectDir(t)

	_, _, err := handleTaskAdd(context.Background(), nil, taskAddInput{Text: "a", Tags: []string{"urgent"}})
	require.NoError(t, err)
	_, _, err = handleTaskAdd(context.Background(), nil, taskAddInput{Text: "b", Tags: []string{"urgent"}})
	require.NoError(t, err)

	res, err := handleTagVocabularyResource(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, res.Contents, 1)
	assert.Equal(t, tagVocabularyResourceURI, res.Contents[0].URI)
	assert.Equal(t, "application/json", res.Contents[0].MIMEType)

	var doc tagVocabularyDoc
	require.NoError(t, json.Unmarshal([]byte(res.Contents[0].Text), &doc))

	var urgent *operations.TagCount
	for i := range doc.Tags {
		if doc.Tags[i].Tag == "urgent" {
			urgent = &doc.Tags[i]
		}
	}
	require.NotNil(t, urgent, "urgent must appear in the vocabulary: %+v", doc.Tags)
	assert.Equal(t, 2, urgent.Active)
	assert.Equal(t, 2, urgent.Total)
}

// TestHandleTagVocabularyResource_EmptyProjectReturnsEmptyList proves an
// empty vocabulary comes back as an empty (never null/omitted) tags array —
// the same silent-no-op discipline the rest of this codebase applies to
// every other list-shaped result.
func TestHandleTagVocabularyResource_EmptyProjectReturnsEmptyList(t *testing.T) {
	withProjectDir(t)

	res, err := handleTagVocabularyResource(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, res.Contents, 1)

	var doc tagVocabularyDoc
	require.NoError(t, json.Unmarshal([]byte(res.Contents[0].Text), &doc))
	assert.NotNil(t, doc.Tags, "tags must be an empty array, not null, when nothing is in use")
	assert.Empty(t, doc.Tags)
}

// TestMCPResourcesRegisteredAndReadableOverProtocol proves both resources
// are reachable through a real MCP client/server round trip — listed by
// ListResources and readable by ReadResource — not just as directly-called
// Go functions. This is exactly how an agent discovers/reads them.
func TestMCPResourcesRegisteredAndReadableOverProtocol(t *testing.T) {
	withProjectDir(t)
	ctx := context.Background()

	serverT, clientT := mcp.NewInMemoryTransports()
	_, err := newMCPServer().Connect(ctx, serverT, nil)
	require.NoError(t, err)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientT, nil)
	require.NoError(t, err)
	defer cs.Close()

	listRes, err := cs.ListResources(ctx, nil)
	require.NoError(t, err)
	gotURIs := map[string]bool{}
	for _, r := range listRes.Resources {
		gotURIs[r.URI] = true
	}
	assert.True(t, gotURIs[tagSchemaResourceURI])
	assert.True(t, gotURIs[tagVocabularyResourceURI])

	for _, uri := range []string{tagSchemaResourceURI, tagVocabularyResourceURI} {
		readRes, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		require.NoError(t, err, "read %s", uri)
		require.Len(t, readRes.Contents, 1)
		assert.NotEmpty(t, readRes.Contents[0].Text)
	}
}
