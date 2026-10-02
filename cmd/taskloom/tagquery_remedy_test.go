package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// TestWrapTagQueryError_TheGrammarIsItsRemedy: a malformed --tag-query names
// the postfix grammar as its REMEDY (clifmt.RemedyOf), so RenderError prints
// it as the fix line in every format; the message stays the query's own
// error, and the sentinel stays reachable.
func TestWrapTagQueryError_TheGrammarIsItsRemedy(t *testing.T) {
	bad := fmt.Errorf("%w: stack underflow", tasks.ErrTagQuery)
	got := wrapTagQueryError(bad)

	fix, ok := clifmt.RemedyOf(got)
	require.True(t, ok, "a malformed query must carry the grammar as its remedy")
	assert.Equal(t, tagQueryRemedy, fix)
	assert.Equal(t, bad.Error(), got.Error(), "the remedy travels beside the message, not spliced into it")
	assert.ErrorIs(t, got, tasks.ErrTagQuery)

	other := errors.New("store unreadable")
	assert.Same(t, other, wrapTagQueryError(other), "an unrelated error gets no grammar remedy")
	_, ok = clifmt.RemedyOf(wrapTagQueryError(other))
	assert.False(t, ok)
}

// TestTaskListOverProtocol_MalformedTagQueryCarriesTheRemedy: the agent that
// types tag_query is the caller most likely to get the grammar wrong and the
// only one with no RenderError of its own. The MCP SDK reports a tool error
// as err.Error(), which leaves a report.Error's Fix out — so the tool's error
// text must carry the fix line itself.
func TestTaskListOverProtocol_MalformedTagQueryCarriesTheRemedy(t *testing.T) {
	withProjectDir(t)
	ctx := context.Background()
	_, _, err := handleTaskAdd(ctx, nil, taskAddInput{Text: "a task"})
	require.NoError(t, err)

	serverT, clientT := mcp.NewInMemoryTransports()
	_, err = newMCPServer().Connect(ctx, serverT, nil)
	require.NoError(t, err)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientT, nil)
	require.NoError(t, err)
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "task_list", Arguments: map[string]any{"tag_query": "and"}})
	require.NoError(t, err)
	require.True(t, res.IsError, "a malformed tag query must fail loud")
	require.NotEmpty(t, res.Content)
	text, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, text.Text, clifmt.FixLine("", tagQueryRemedy))
}
