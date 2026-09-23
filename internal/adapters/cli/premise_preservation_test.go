package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// A premise is the one field that turns a fragment from always-loaded into
// conditional, and "" is a valid value meaning ALWAYS LOAD — so a CLI write
// path that forgets to carry it does not fail, it silently changes delivery.
// Each CLI caller that builds a BundleFragmentInput gets one test here.

const cliSeededPremise = "You are about to remove a worktree."

func seedPremisedDemo(t *testing.T, cfg *config.Config) {
	t.Helper()
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{
		Name: "demo",
		Fragments: map[string]operations.BundleFragmentInput{
			"x":       {Content: "v1", Premise: cliSeededPremise, Notes: "origin", NoDistill: true},
			"sibling": {Content: "s", NoDistill: true},
		},
	})
	require.NoError(t, err)
}

func demoPremise(t *testing.T, cfg *config.Config) string {
	t.Helper()
	got, err := operations.GetItemContent(context.Background(), cfg, operations.GetItemRequest{
		Bundle: "demo", Kind: operations.ItemKindFragment, Name: "x",
	})
	require.NoError(t, err)
	return got.Premise
}

func TestEditItem_PreservesPremise(t *testing.T) {
	cfg := setupEditProject(t)
	seedPremisedDemo(t, cfg)
	setFakeEditor(t, "v2")

	cmd, _ := testCmd()
	require.NoError(t, editItem(cmd, "demo#fragments/x", ItemTypeFragment, true))

	assert.Equal(t, cliSeededPremise, demoPremise(t, cfg), "a body edit must not wipe the premise")
}

func TestBundleEdit_UnrelatedEditsPreservePremise(t *testing.T) {
	for name, set := range map[string]func(){
		"add tag":                      func() { bundleEditAddTags = []string{"unrelated"} },
		"description":                  func() { bundleEditDesc = "new description" },
		"add-fragment an existing one": func() { bundleEditAddFragment = []string{"x"}; bundleEditAddTags = []string{"t"} },
		"remove a sibling fragment":    func() { bundleEditRemoveFragment = []string{"sibling"} },
	} {
		t.Run(name, func(t *testing.T) {
			resetApp()
			t.Cleanup(resetApp)
			cfg := setupEditProject(t)
			seedPremisedDemo(t, cfg)
			set()
			t.Cleanup(func() {
				bundleEditAddTags, bundleEditDesc, bundleEditAddFragment, bundleEditRemoveFragment = nil, "", nil, nil
			})

			cmd, _ := formatCmd("text")
			cmd.SetContext(context.Background())
			require.NoError(t, runBundleEdit(cmd, []string{"demo"}))

			assert.Equal(t, cliSeededPremise, demoPremise(t, cfg))
		})
	}
}
