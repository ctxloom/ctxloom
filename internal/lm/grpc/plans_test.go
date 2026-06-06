package grpc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/agent"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadPlanFiles(t *testing.T) {
	testsupport.Isolate(t) // isolated HOME → paths.HarpDir resolves under it
	dir, err := paths.HarpDir("brisk-harp")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "v1"+paths.PlanFileExt), []byte("# v1 plan"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "arch"+paths.PlanFileExt), []byte("# arch plan"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.md"), []byte("not a plan"), 0o644))

	got := ReadPlanFiles("brisk-harp")
	require.Len(t, got, 2, "only *.plan.md files, not notes.md")
	assert.Equal(t, "arch", got[0].Name, "sorted by name")
	assert.Equal(t, "# arch plan", got[0].Content)
	assert.Equal(t, "v1", got[1].Name)
}

func TestReadPlanFiles_MissingDirAndEmptyHarp(t *testing.T) {
	testsupport.Isolate(t)
	assert.Nil(t, ReadPlanFiles("never-created"), "missing session dir → no plans, no error")
	assert.Nil(t, ReadPlanFiles(""), "empty harp → no plans")
}

func TestPlanFilesRoundTrip(t *testing.T) {
	in := []agent.PlanFile{{Name: "a", Content: "x"}, {Name: "b", Content: "y"}}
	got := planFilesFromProto(planFilesToProto(in))
	assert.Equal(t, in, got)
	assert.Nil(t, planFilesToProto(nil))
	assert.Nil(t, planFilesFromProto(nil))
}

func TestGRPCServer_GetPlans_ServesHarpDir(t *testing.T) {
	testsupport.Isolate(t)
	dir, err := paths.HarpDir("srv-harp")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "p"+paths.PlanFileExt), []byte("body"), 0o644))

	srv := &GRPCServer{Impl: &fakeBackend{name: "claude-code"}} // plans are agent-agnostic
	out, err := srv.GetPlans(context.Background(), &GetPlansRequest{Harp: "srv-harp"})
	require.NoError(t, err)
	require.Len(t, out.GetPlans(), 1)
	assert.Equal(t, "p", out.GetPlans()[0].GetName())
	assert.Equal(t, "body", out.GetPlans()[0].GetContent())
}

func TestGRPCClient_GetPlans_Delegates(t *testing.T) {
	fake := &fakeLLMClient{plansResp: &PlansData{Plans: []*PlanFile{{Name: "a", Content: "x"}}}}
	c := &GRPCClient{client: fake}
	got, err := c.GetPlans(context.Background(), "harp")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "a", got[0].Name)
}

func TestSessionReader_GetPlans(t *testing.T) {
	mock := &MockClient{
		GetPlansFunc: func(_ context.Context, harp string) ([]agent.PlanFile, error) {
			return []agent.PlanFile{{Name: harp}}, nil
		},
	}
	r := NewSessionReaderWithFactory("gemini", 0, MockClientFactory(mock))
	got, err := r.GetPlans(context.Background(), "h1")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "h1", got[0].Name)
	assert.Equal(t, 1, mock.KillCalls)
}
