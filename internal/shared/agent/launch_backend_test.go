package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordLifecycle captures the contextHash MergeManaged receives so Setup tests
// can assert whether the SessionStart context-injection hook was suppressed (a
// "" hash ⇒ no hook appended). It deliberately does NOT expose GetHooks/GetMCP,
// so the delivery path's mergedState probe reports ok=false and Setup falls back
// to Flush — isolating the context/skills/teardown mechanics from item 8.
type recordLifecycle struct {
	merged      bool
	contextHash string
	flushed     bool
}

func (r *recordLifecycle) MergeManaged(_ *ManagedConfig, _ string, contextHash string) {
	r.merged = true
	r.contextHash = contextHash
}
func (r *recordLifecycle) Flush(string) error { r.flushed = true; return nil }

type noopSkills struct{}

func (noopSkills) RegisterFromContent(string, []CommandExport) error { return nil }

// ---- delivery-seam test doubles ---------------------------------------------

// recordFactory is a DeliveryFactory that records what each surface strategy was
// asked to deliver and returns handles that log their Cleanup into a shared
// order slice, so a test can assert LIFO teardown. contextErr forces a
// context-delivery failure to exercise the injection-hook fallback.
type recordFactory struct {
	order *[]string

	contextErr error

	gotContext    string
	gotContextDir string
	gotSkills     []CommandExport
	gotHooks      *wire.HooksConfig
	gotManage     bool
	gotMCP        *wire.MCPConfig
	gotBundleMCP  map[string]wire.MCPServer
}

func (f *recordFactory) ContextDelivery(p Placement) ContextDelivery {
	return recordContext{f: f, dir: p.Dir()}
}
func (f *recordFactory) SkillsDelivery(Placement) SkillsDelivery   { return recordSkills{f: f} }
func (f *recordFactory) SettingsDelivery(Placement) SettingsDelivery { return recordSettings{f: f} }
func (f *recordFactory) MCPDelivery(Placement) MCPDelivery         { return recordMCP{f: f} }

func (f *recordFactory) handle(label string) Delivered {
	return recordDelivered{order: f.order, label: label}
}

type recordDelivered struct {
	order *[]string
	label string
}

func (d recordDelivered) Cleanup() error {
	if d.order != nil {
		*d.order = append(*d.order, d.label)
	}
	return nil
}

type recordContext struct {
	f   *recordFactory
	dir string
}

func (c recordContext) DeliverContext(s string) (Delivered, error) {
	c.f.gotContext = s
	c.f.gotContextDir = c.dir
	if c.f.contextErr != nil {
		return nil, c.f.contextErr
	}
	return c.f.handle("context"), nil
}

type recordSkills struct{ f *recordFactory }

func (s recordSkills) DeliverSkills(cmds []CommandExport) (Delivered, error) {
	s.f.gotSkills = cmds
	return s.f.handle("skills"), nil
}

type recordSettings struct{ f *recordFactory }

func (s recordSettings) DeliverSettings(hooks *wire.HooksConfig, manageStatusline bool) (Delivered, error) {
	s.f.gotHooks = hooks
	s.f.gotManage = manageStatusline
	return s.f.handle("settings"), nil
}

type recordMCP struct{ f *recordFactory }

func (m recordMCP) DeliverMCP(mcp *wire.MCPConfig, bundle map[string]wire.MCPServer) (Delivered, error) {
	m.f.gotMCP = mcp
	m.f.gotBundleMCP = bundle
	return m.f.handle("mcp"), nil
}

// ---- helpers ----------------------------------------------------------------

func newLegacyBackend() (*LaunchBackend, *recordLifecycle) {
	rec := &recordLifecycle{}
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	b.InitLaunch(rec, noopSkills{}, NewBaseContextProvider(), nil, nil)
	return b, rec
}

func newDeliveryBackend(f DeliveryFactory) (*LaunchBackend, *recordLifecycle) {
	rec := &recordLifecycle{}
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	b.InitLaunch(rec, noopSkills{}, NewBaseContextProvider(), nil, f)
	return b, rec
}

// ---- legacy (antigravity) path ----------------------------------------------

// TestSetup_LegacyPath_KeepsContextHash proves a backend WITHOUT a delivery
// factory (antigravity/codex/kiro/acp) takes the unchanged lifecycle path: it
// keeps the context hash so its SessionStart hook injects context, and flushes.
func TestSetup_LegacyPath_KeepsContextHash(t *testing.T) {
	b, rec := newLegacyBackend()
	require.Nil(t, b.delivery, "legacy backend must have no delivery factory")

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
		Fragments: []*Fragment{{Content: "project rules"}},
		Managed:   &ManagedConfig{},
	}))

	assert.NotEmpty(t, rec.contextHash,
		"a legacy backend keeps the hash so its SessionStart hook injects context")
	assert.True(t, rec.flushed, "legacy path flushes hooks + MCP to the settings file")
	assert.Empty(t, b.delivered, "legacy path collects no delivery handles")
}

// ---- delivery (claude) path -------------------------------------------------

// TestSetup_DeliveryPath_SuppressesHookAndRoutesContext proves the delivery path
// hands MergeManaged an empty hash (so no SessionStart context-injection hook is
// appended — context rides the launch flag), routes the assembled context string
// to the factory at the harp's ephemeral dir, and delivers skills.
func TestSetup_DeliveryPath_SuppressesHookAndRoutesContext(t *testing.T) {
	var order []string
	f := &recordFactory{order: &order}
	b, rec := newDeliveryBackend(f)

	ephem, err := paths.HarpEphemeralDir("perky-same-chevy")
	require.NoError(t, err)

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
		Env:       map[string]string{SessionHarpEnv: "perky-same-chevy"},
		Fragments: []*Fragment{{Content: "project rules"}},
		Managed:   &ManagedConfig{Skills: []CommandExport{{Name: "demo"}}},
	}))

	require.True(t, rec.merged, "MergeManaged must run to fold hooks/MCP")
	assert.Equal(t, "", rec.contextHash,
		"delivery must withhold the hash so no SessionStart context-injection hook is appended")
	assert.Equal(t, "project rules", f.gotContext, "the assembled context string is routed to the factory")
	assert.Equal(t, ephem, f.gotContextDir,
		"context scratch lands in the harp's PRIVATE ephemeral dir, not the project tree")
	assert.Equal(t, []CommandExport{{Name: "demo"}}, f.gotSkills, "skills are delivered through the seam")
}

// TestSetup_DeliveryPath_ContextFailureFallsBackToHook proves the CLAUDE.md
// fault tolerance: a context-delivery error keeps the injection hook alive
// (materialize the raw file via Provide, keep the hash for MergeManaged) so the
// user never loses context, and collects no context handle.
func TestSetup_DeliveryPath_ContextFailureFallsBackToHook(t *testing.T) {
	var order []string
	f := &recordFactory{order: &order, contextErr: errors.New("disk full")}
	b, rec := newDeliveryBackend(f)

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
		Fragments: []*Fragment{{Content: "project rules"}},
		Managed:   &ManagedConfig{},
	}))

	assert.NotEmpty(t, rec.contextHash,
		"a failed context delivery must keep the hash so the SessionStart hook still injects context")
	assert.NotContains(t, order, "context", "a failed context delivery collects no cleanup handle")
}

// TestCleanup_RunsDeliveredHandlesLIFO proves Cleanup reverses every delivered
// surface in last-in-first-out order (mcp, settings, skills, context here) and
// clears the handle set.
func TestCleanup_RunsDeliveredHandlesLIFO(t *testing.T) {
	var order []string
	f := &recordFactory{order: &order}
	b, _ := newDeliveryBackend(f)

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
		Fragments: []*Fragment{{Content: "rules"}},
		Managed:   &ManagedConfig{Skills: []CommandExport{{Name: "demo"}}},
	}))

	require.NoError(t, b.Cleanup(context.Background()))
	// Delivery order was context, skills, then (recordLifecycle lacks GetHooks so
	// mergedState is false → Flush fallback, no settings/mcp handles). LIFO ⇒
	// skills before context.
	assert.Equal(t, []string{"skills", "context"}, order, "Cleanup runs handles LIFO")
	assert.Empty(t, b.delivered, "Cleanup clears the handle set")
}

// TestCleanup_AttemptsAllReturnsFirstError proves Cleanup keeps going after a
// handle errors and reports the first failure.
func TestCleanup_AttemptsAllReturnsFirstError(t *testing.T) {
	boom := errors.New("boom")
	var ran int
	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	// Cleanup undoes LIFO (last appended, first undone). The last-appended handle
	// errors → it is the first error encountered; a later handle also errors but
	// must not overwrite it, and the clean handle must still run.
	b.delivered = []Delivered{
		deliveredFn(func() error { ran++; return errors.New("undone-last") }), // undone last
		deliveredFn(func() error { ran++; return nil }),                       // undone second
		deliveredFn(func() error { ran++; return boom }),                      // undone first → first error
	}

	err := b.Cleanup(context.Background())
	assert.Equal(t, boom, err, "Cleanup returns the FIRST error encountered in LIFO order")
	assert.Equal(t, 3, ran, "Cleanup attempts every handle despite an error")
}

type deliveredFn func() error

func (d deliveredFn) Cleanup() error { return d() }

// ---- item 8: settings + MCP routed through the seam -------------------------

// flushSpy is a WriteSettingsFunc that records whether Flush called it, so a
// seam-routing test can assert Flush was SKIPPED (no double-write).
type flushSpy struct{ called bool }

func (s *flushSpy) write(_ string, _ *wire.HooksConfig, _ *wire.MCPConfig, _ map[string]wire.MCPServer, _ string, _ ...SettingsOption) error {
	s.called = true
	return nil
}

// TestSetup_DeliveryPath_RoutesSettingsAndMCPSkippingFlush proves that when the
// lifecycle exposes its merged state (BaseLifecycle), the delivery path routes
// the settings + MCP surfaces through the factory and SKIPS Flush entirely (no
// double-write). The merged hooks and MCP handed to the factory are exactly what
// Flush would have written; manageStatusline mirrors the managed config.
func TestSetup_DeliveryPath_RoutesSettingsAndMCPSkippingFlush(t *testing.T) {
	var order []string
	f := &recordFactory{order: &order}
	spy := &flushSpy{}

	b := &LaunchBackend{}
	b.BaseBackend = NewBaseBackend("test", "1.0.0")
	b.InitLaunch(NewBaseLifecycle("test", spy.write), noopSkills{}, NewBaseContextProvider(), nil, f)

	managed := &ManagedConfig{
		ManageStatusline: true,
		Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "ctxloom hook session-bind", Type: "command"}},
		}},
		MCP:       &wire.MCPConfig{Servers: map[string]wire.MCPServer{"srv": {Command: "run"}}},
		BundleMCP: map[string]wire.MCPServer{"bundle-srv": {Command: "brun"}},
	}
	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
		Fragments: []*Fragment{{Content: "rules"}},
		Managed:   managed,
	}))

	assert.False(t, spy.called, "seam path must SKIP Flush (no double-write)")
	require.NotNil(t, f.gotHooks, "settings surface routed through the factory")
	assert.True(t, f.gotManage, "manageStatusline mirrors the managed config")
	assert.NotEmpty(t, f.gotHooks.Unified.SessionStart, "merged hooks carry the session-bind hook")
	require.NotNil(t, f.gotMCP, "MCP surface routed through the factory")
	assert.Contains(t, f.gotMCP.Servers, "srv", "merged MCP carries the managed server")
	assert.Equal(t, managed.BundleMCP, f.gotBundleMCP, "bundle MCP is passed straight through")
	// No skills in this managed config, so the delivered surfaces are context,
	// settings, MCP (Flush skipped).
	require.Len(t, b.delivered, 3, "context + settings + MCP handles collected via the seam")
}
