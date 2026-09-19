package conformance_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/engine/conformance"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// TestEngine_Mock_Conforms is the shape every engine package copies verbatim
// with its own constructor.
func TestEngine_Mock_Conforms(t *testing.T) { conformance.Run(t, mock.New()) }

// TestConformance_DefinitionIsAValue_InstanceBindsTheSession: the engine kind
// is a value — its Definition is obtainable with no session and equal on
// every call — and only Instance needs a Session.
func TestConformance_DefinitionIsAValue_InstanceBindsTheSession(t *testing.T) {
	eng := mock.New()
	d1, d2 := eng.Root(), eng.Root()
	require.NoError(t, d1.Validate())
	require.Equal(t, d1.Name, d2.Name)
	require.Equal(t, d1.Modes, d2.Modes)
	inst, err := eng.Instance(conformance.SessionFor(t, eng, engine.Interactive))
	require.NoError(t, err)
	require.NotNil(t, inst)
}

// TestConformance_SurfacesAreDerivedFromTheTypedFields: the table the
// planner reads is a walk over the typed fields — one approach per kind, the
// Traits the planner reads are the approach's own, Carries/Static agree
// with it, and a nil field is simply absent.
func TestConformance_SurfacesAreDerivedFromTheTypedFields(t *testing.T) {
	def := mock.New().Root()
	s := def.Surfaces()
	require.Same(t, def.Context, s[present.Context])
	require.Same(t, def.MCP, s[present.MCP])
	require.Same(t, def.Settings, s[present.Settings])
	require.Same(t, def.Hooks, s[present.Hooks])
	require.Same(t, def.Commands, s[present.Commands])
	require.Same(t, def.Skills, s[present.Skills])
	for k, a := range s {
		require.True(t, def.Carries(k))
		require.NotEmpty(t, a.Traits().Roots, "an approach offers at least one root; the first is its default")
	}
	require.Equal(t, len(def.Static()), len(s), "Static() and Surfaces() walk the same fields")
	lossy := mock.New(mock.Without(present.Skills)).Root()
	require.False(t, lossy.Carries(present.Skills), "uncarried is a nil field; nothing declares it")
	_, has := lossy.Surfaces()[present.Skills]
	require.False(t, has)
}

// TestConformance_Requiredness_IsRefusedAtInstance_NotAtCompileTime: a nil
// field for a kind the engine needs to run a session is refused LOUDLY when
// the session is bound, naming the kind; the Definition itself is legal.
func TestConformance_Requiredness_IsRefusedAtInstance_NotAtCompileTime(t *testing.T) {
	eng := mock.New(mock.Without(present.Context))
	require.NoError(t, eng.Root().Validate(), "a missing surface is not a definition error")
	_, err := eng.Instance(conformance.SessionFor(t, eng, engine.Interactive))
	var unsupported engine.ErrUnsupported
	require.True(t, errors.As(err, &unsupported))
	require.Equal(t, "context", unsupported.Capability)
}

// TestConformance_Constructor_RefusesAnIncoherentDeclaration: the engine
// package's constructor is the ONE place an incoherent declaration is
// refused — here a declared mode with no argv grammar. Kinds need no such
// check: a missing or duplicate kind is a compile error on the typed
// fields. NewRegistry does not re-check; Run asserts Validate holds for
// every engine it is handed. The shape is the plain constructor (options,
// then Validate once); a typestate builder is rejected as non-obvious
// machinery and is the fallback only if requiredness must ever become
// compile-time.
func TestConformance_Constructor_RefusesAnIncoherentDeclaration(t *testing.T) {
	_, err := mock.Build("mock", mock.WithoutGrammar(engine.Structured))
	require.Error(t, err, "a mode with no grammar is refused at construction, not at registry build or at exec time")
	ok, err := mock.Build("mock")
	require.NoError(t, err)
	require.NoError(t, ok.Root().Validate())
}

// TestConformance_Base_DelegatesPrefaceToDynamic_OnlyWhenProvided: the
// engine root's common decisioning, written once in core. Preface items
// (premised fragments) are withheld from static delivery and delegated to
// the engine's PROVIDED dynamic approach (claude); non-preface items go to
// the static approach types; with no dynamic approach (mock) everything
// goes static; a dynamic approach with no MCP approach to name the endpoint
// is refused at construction. Route applies the delegation; neither engine
// re-implements it.
func TestConformance_Base_DelegatesPrefaceToDynamic_OnlyWhenProvided(t *testing.T) {
	items := engine.Items{
		Fragments: []engine.FragmentItem{{Ref: "b#fragment/always", Name: "always"}, {Ref: "b#fragment/when-go", Name: "when-go", Premise: "the task touches Go"}},
		Commands:  []engine.CommandItem{{Ref: "b#command/go", Name: "go"}},
	}
	both, err := claude.Build()
	require.NoError(t, err)
	require.NotNil(t, both.Root().Dynamic)
	d := both.Root().Delegate(items)
	require.Equal(t, []string{"b#fragment/when-go"}, d.Dynamic, "the preface item rides the endpoint")
	require.Contains(t, d.Static, present.Context, "the unpremised fragment is static")
	require.Contains(t, d.Static, present.Commands)
	require.Contains(t, d.Static, present.MCP, "the endpoint itself is an MCP entry")

	staticOnly := mock.New() // provides no dynamic approach
	require.Nil(t, staticOnly.Root().Dynamic)
	d = staticOnly.Root().Delegate(items)
	require.Empty(t, d.Dynamic, "no dynamic approach: everything goes static")
	require.Contains(t, d.Static, present.Context)

	_, err = mock.Build("mock", mock.Without(present.MCP), mock.WithDynamic())
	require.Error(t, err, "a dynamic approach without an MCP approach is refused at construction")
}

// TestConformance_SessionCarriesNoCredentialNoPackageNoAxes pins the
// engine-facing projection: the fields an engine must never be handed do not
// exist on the type at all.
func TestConformance_SessionCarriesNoCredentialNoPackageNoAxes(t *testing.T) {
	typ := reflect.TypeOf(engine.Session{})
	for _, forbidden := range []string{"Credential", "Package", "Axes", "Runtime", "Workspace", "ReachBack", "Plan", "Trust", "Gate"} {
		_, has := typ.FieldByName(forbidden)
		require.False(t, has, "engine.Session must not carry %s", forbidden)
	}
}
