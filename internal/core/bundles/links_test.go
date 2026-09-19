package bundles

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
)

// A LINK GROUP IS ONE DELIVERY UNIT. Items in one bundle that share a
// `ctxloom:link_id=<name>` tag deliver together or not at all: when any MCP
// member of the group did not reach the run's granted set, every other member
// is withheld, so a fragment is never handed over beside a tool it depends on
// that is not there. The tag rides in the already-classified selection
// surface (ItemBody.Tags), so it is not a preimage widening.

// The parser is the one place the tag's spelling is known.
func TestParseLinkTag_RecognisesOnlyTheLinkNamespace(t *testing.T) {
	id, ok := ParseLinkTag("ctxloom:link_id=think")
	assert.True(t, ok)
	assert.Equal(t, "think", id)

	for _, tag := range []string{"link_id=think", "ctxloom:link_id", "ctxloom:link_id=", "ctxloom:other=think", "think"} {
		_, ok := ParseLinkTag(tag)
		assert.False(t, ok, "%q is not a link tag", tag)
	}
}

func TestLinkIDs_CollectsEveryLinkTagOnce(t *testing.T) {
	got := LinkIDs([]string{"go", "ctxloom:link_id=think", "ctxloom:link_id=search", "ctxloom:link_id=think"})
	assert.Equal(t, []string{"think", "search"}, got)
	assert.Nil(t, LinkIDs([]string{"go", "testing"}))
}

func linkedBundle() *Bundle {
	link := []string{"ctxloom:link_id=think"}
	return &Bundle{
		Name:    "b",
		Version: "1.0",
		Fragments: map[string]BundleFragment{
			"guide":    {ItemBody: ItemBody{Content: "GUIDE", Tags: link}},
			"unlinked": {ItemBody: ItemBody{Content: "UNLINKED"}},
		},
		Commands: map[string]BundleCommand{
			"plan": {ItemBody: ItemBody{Content: "PLAN", Tags: link}},
		},
		MCP: map[string]BundleMCP{
			"think": {Command: "think-server", Tags: link},
		},
		Hooks: BundleHooks{
			SessionStart: []BundleHook{{Command: "think-warmup", Tags: link}},
			PreTool:      []BundleHook{{Command: "unlinked-guard"}},
		},
	}
}

// Membership is computed from each item's EFFECTIVE tags — bundle tags merged
// onto the item's, exactly as the reader merges them — so a bundle-level link
// tag binds every item and the group the pipeline sees is the group the
// author declared.
func TestBundle_LinkGroups_GroupsEveryKindByEffectiveTags(t *testing.T) {
	groups := linkedBundle().LinkGroups()
	require.Contains(t, groups, "think")
	assert.Equal(t, []LinkMember{
		{Kind: trust.KindFragment, Name: "guide"},
		{Kind: trust.KindHook, Name: "session_start/0"},
		{Kind: trust.KindMCP, Name: "think"},
		{Kind: trust.KindPrompt, Name: "plan"},
	}, groups["think"].Members, "a hook joins by its trust identity, <event>/<index>: hooks have no author-given name")
	assert.Equal(t, []string{"think"}, groups["think"].MCPMembers())

	whole := &Bundle{
		Name: "w", Version: "1.0", Tags: []string{"ctxloom:link_id=all"},
		Fragments: map[string]BundleFragment{"f": {ItemBody: ItemBody{Content: "F"}}},
		Skills:    map[string]BundleSkill{"s": {}},
	}
	all := whole.LinkGroups()["all"]
	assert.Equal(t, []LinkMember{
		{Kind: trust.KindFragment, Name: "f"},
		{Kind: trust.KindSkill, Name: "s"},
	}, all.Members)
	assert.Empty(t, all.MCPMembers())
}

// THE LOAD-TIME FINDING. A link is two edits that must agree, and a typo on
// one side would silently unlink the pair — delivering the fragment beside the
// tool it lacks, the exact failure the tag exists to stop. A link id carried by
// exactly one item in a bundle is therefore refused at parse, loudly, naming
// the lone member.
func TestParseBundle_DanglingLinkIDIsRefused(t *testing.T) {
	_, err := ParseBundle([]byte(`version: "1.0"
fragments:
  guide:
    content: GUIDE
    tags: [ctxloom:link_id=think]
mcp:
  think:
    command: think-server
    tags: [ctxloom:link_id=thinc]
`))
	require.Error(t, err)
	assert.True(t, errors.Is(err, errs.ErrDanglingLink), "got %v", err)
	assert.Contains(t, err.Error(), "think")
	assert.Contains(t, err.Error(), "guide")
}

// A hook is a member like any other, so a hook-side link with no other side
// is the same typo and the same refusal — a session_start hook that calls a
// tool its server provides must not load beside a misspelled server tag.
func TestParseBundle_DanglingHookLinkIDIsRefused(t *testing.T) {
	_, err := ParseBundle([]byte(`version: "1.0"
hooks:
  session_start:
    - command: think-warmup
      tags: [ctxloom:link_id=think]
mcp:
  think:
    command: think-server
    tags: [ctxloom:link_id=thinc]
`))
	require.Error(t, err)
	assert.True(t, errors.Is(err, errs.ErrDanglingLink), "got %v", err)
	assert.Contains(t, err.Error(), "hook/session_start/0")
}

func TestParseBundle_TwoSidedLinkParses(t *testing.T) {
	b, err := ParseBundle([]byte(`version: "1.0"
fragments:
  guide:
    content: GUIDE
    tags: [ctxloom:link_id=think]
mcp:
  think:
    command: think-server
    tags: [ctxloom:link_id=think]
`))
	require.NoError(t, err)
	assert.Equal(t, []string{"ctxloom:link_id=think"}, b.MCP["think"].Tags)
}

// grantOnly is a LinkGrant that grants exactly the named servers.
func grantOnly(servers ...string) LinkGrant {
	return LinkGrantFunc(func(_ BundleRead, server string) bool {
		for _, s := range servers {
			if s == server {
				return true
			}
		}
		return false
	})
}

func TestPipeline_LinkedItemDeliversWhenItsMCPMemberIsGranted(t *testing.T) {
	seed := map[string]*Bundle{"b": linkedBundle()}
	pipe := NewPipeline(NewLoader(seedLocal(seed)), admitAllForTest(), grantOnly("think"), false)

	got, err := pipe.GetFragment("b#fragments/guide")
	require.NoError(t, err)
	assert.Equal(t, "GUIDE", got.Content)

	cmd, err := pipe.GetCommand("b#commands/plan")
	require.NoError(t, err)
	assert.Equal(t, "PLAN", cmd.Content)
	assert.Empty(t, pipe.Withheld())
}

func TestPipeline_LinkedItemIsWithheldWhenItsMCPMemberIsNotGranted(t *testing.T) {
	seed := map[string]*Bundle{"b": linkedBundle()}
	pipe := NewPipeline(NewLoader(seedLocal(seed)), admitAllForTest(), grantOnly(), false)

	_, err := pipe.GetFragment("b#fragments/guide")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld), "got %v", err)

	_, err = pipe.GetCommand("b#commands/plan")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errs.ErrCommandWithheld), "got %v", err)

	assert.Len(t, pipe.Withheld(), 2, "a link withhold is tallied like a trust withhold")
}

// An item in no link group is untouched by the grant: withholding is a
// property of the GROUP, never collateral for the bundle.
func TestPipeline_UnlinkedItemIgnoresTheLinkGrant(t *testing.T) {
	seed := map[string]*Bundle{"b": linkedBundle()}
	pipe := NewPipeline(NewLoader(seedLocal(seed)), admitAllForTest(), grantOnly(), false)

	got, err := pipe.GetFragment("b#fragments/unlinked")
	require.NoError(t, err)
	assert.Equal(t, "UNLINKED", got.Content)
}

// The grant is asked about the server AS SHIPPED BY THIS BUNDLE: it receives
// the read, so a same-named server from another bundle cannot answer for it.
func TestPipeline_LinkGrantIsAskedForTheOwningBundle(t *testing.T) {
	seed := map[string]*Bundle{"b": linkedBundle()}
	var asked []string
	grant := LinkGrantFunc(func(read BundleRead, server string) bool {
		asked = append(asked, read.DisplayName()+"/"+server)
		return true
	})
	_, err := NewPipeline(NewLoader(seedLocal(seed)), admitAllForTest(), grant, false).GetFragment("b#fragments/guide")
	require.NoError(t, err)
	assert.Equal(t, []string{"b/think"}, asked)
}

// A nil grant is an omission, not a statement, and it fails CLOSED for every
// linked item — the same rule a nil authorizer follows. Not checking links is
// spelled LinksUnchecked, out loud.
func TestPipeline_NilLinkGrantWithholdsLinkedItemsOnly(t *testing.T) {
	seed := map[string]*Bundle{"b": linkedBundle()}
	pipe := NewPipeline(NewLoader(seedLocal(seed)), admitAllForTest(), nil, false)

	_, err := pipe.GetFragment("b#fragments/guide")
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld), "got %v", err)
	got, err := pipe.GetFragment("b#fragments/unlinked")
	require.NoError(t, err)
	assert.Equal(t, "UNLINKED", got.Content)

	unchecked := NewPipeline(NewLoader(seedLocal(seed)), admitAllForTest(), LinksUnchecked(), false)
	got, err = unchecked.GetFragment("b#fragments/guide")
	require.NoError(t, err)
	assert.Equal(t, "GUIDE", got.Content)
}

// Tags are outside the MCP executable preimage: linking a server changes
// nothing an approval was granted over.
func TestBundleMCP_TagsAreOutsideTheExecutablePreimage(t *testing.T) {
	plain := BundleMCP{Command: "think-server", Args: []string{"--x"}}
	linked := plain
	linked.Tags = []string{"ctxloom:link_id=think"}
	assert.Equal(t, plain.ComputeContentHash(), linked.ComputeContentHash())
}

// Tags are outside the hook executable preimage too: hooks share
// ExecPreimageContract with MCP, and linking a hook to its server must not
// invalidate the approval granted over what the hook runs.
func TestBundleHook_TagsAreOutsideTheExecutablePreimage(t *testing.T) {
	plain := BundleHook{Matcher: "Bash", Command: "think-warmup"}
	linked := plain
	linked.Tags = []string{"ctxloom:link_id=think"}
	assert.Equal(t, plain.ComputeContentHash(), linked.ComputeContentHash())
}
