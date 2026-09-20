//go:build schemagen

package operations

import (
	"reflect"

	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/schemagen"
)

// SchemaTargets lists the JSON output structs in this package that publish a
// JSON Schema, plus every registered engine's export-block schema
// (EngineExportSchemaTargets). Build-tagged so it and its reflection import
// stay out of the production binary; consumed only by cmd/gen-schemas
// (-tags schemagen).
func SchemaTargets() []schemagen.Target {
	return append(EngineExportSchemaTargets(), operationsSchemaTargets()...)
}

// EngineExportSchemaTargets publishes each shipped engine's export-block
// schema — the document a bundle item's block under that engine's name is
// decoded against (engine.Definition.ExportSchema) — as
// "engine-exports-<name>", so an author validates against the same bytes the
// engine decodes with. The set is engines.Build()'s: what the composition
// root ships, composed here because the generator is its own process. An
// engine that declares no schema publishes nothing.
func EngineExportSchemaTargets() []schemagen.Target {
	reg, err := engines.Build()
	if err != nil {
		panic("schemagen: " + err.Error())
	}
	var targets []schemagen.Target
	for _, name := range reg.Names(nil) {
		eng, _ := reg.Lookup(name)
		schema := eng.Root().ExportSchema
		if len(schema) == 0 {
			continue
		}
		targets = append(targets, schemagen.Target{Name: "engine-exports-" + string(name), Schema: schema})
	}
	return targets
}

func operationsSchemaTargets() []schemagen.Target {
	return []schemagen.Target{
		{Type: reflect.TypeOf(AddItemResult{})},
		{Type: reflect.TypeOf(AddRemoteResult{})},
		{Type: reflect.TypeOf(ApplyHooksResult{})},
		{Type: reflect.TypeOf(AssembleContextResult{})},
		{Type: reflect.TypeOf(BrowseRemoteResult{})},
		{Type: reflect.TypeOf(CheckMissingDependenciesResult{})},
		{Type: reflect.TypeOf(CreateBundleResult{})},
		{Type: reflect.TypeOf(CreateProfileResult{})},
		{Type: reflect.TypeOf(DefaultRemoteResult{})},
		{Type: reflect.TypeOf(DeleteBundleResult{})},
		{Type: reflect.TypeOf(DeleteItemResult{})},
		{Type: reflect.TypeOf(DeleteProfileResult{})},
		{Type: reflect.TypeOf(DiscoverRemotesResult{})},
		{Type: reflect.TypeOf(DistillBundleFileResult{})},
		{Type: reflect.TypeOf(DistillItemResult{})},
		{Type: reflect.TypeOf(DistillResult{})},
		{Type: reflect.TypeOf(EnsureRemoteClonesResult{})},
		{Type: reflect.TypeOf(ExportBundleResult{})},
		{Type: reflect.TypeOf(ExportProfileResult{})},
		{Type: reflect.TypeOf(GetBundleMCPResult{})},
		{Type: reflect.TypeOf(GetFragmentResult{})},
		{Type: reflect.TypeOf(GetItemResult{})},
		{Type: reflect.TypeOf(GetProfileContentResult{})},
		{Type: reflect.TypeOf(GetCommandResult{})},
		{Type: reflect.TypeOf(GetProfileResult{})},
		{Type: reflect.TypeOf(HarnessStatusResult{})},
		{Type: reflect.TypeOf(ImportBundleResult{})},
		{Type: reflect.TypeOf(ImportProfileResult{})},
		{Type: reflect.TypeOf(InitializeProjectResult{})},
		{Type: reflect.TypeOf(ListFragmentsResult{})},
		{Type: reflect.TypeOf(ListMCPServersResult{})},
		{Type: reflect.TypeOf(ListCommandsResult{})},
		{Type: reflect.TypeOf(ListProfilesResult{})},
		{Type: reflect.TypeOf(ListRemotesResult{})},
		{Type: reflect.TypeOf(LockDependenciesResult{})},
		{Type: reflect.TypeOf(PushBundleResult{})},
		{Type: reflect.TypeOf(ReadBundleResult{})},
		{Type: reflect.TypeOf(RemoveHooksResult{})},
		{Type: reflect.TypeOf(RemoveLocalItemsResult{})},
		{Type: reflect.TypeOf(RemoveRemoteResult{})},
		{Type: reflect.TypeOf(SearchContentResult{})},
		{Type: reflect.TypeOf(SearchRemotesResult{})},
		{Type: reflect.TypeOf(SearchResult{})},
		{Type: reflect.TypeOf(SetBundleMCPResult{})},
		{Type: reflect.TypeOf(SetDefaultLLMResult{})},
		{Type: reflect.TypeOf(SetItemContentResult{})},
		{Type: reflect.TypeOf(SetProfileContentResult{})},
		{Type: reflect.TypeOf(SetStatuslineResult{})},
		{Type: reflect.TypeOf(SyncDependenciesResult{})},
		{Type: reflect.TypeOf(UpdateBundleResult{})},
		{Type: reflect.TypeOf(UpdateProfileResult{})},
	}
}
