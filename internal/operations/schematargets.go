//go:build schemagen

package operations

import (
	"reflect"

	"github.com/ctxloom/ctxloom/internal/schemagen"
)

// SchemaTargets lists the JSON output structs in this package that publish a
// JSON Schema. Build-tagged so it and its reflection import stay
// out of the production binary; consumed only by cmd/gen-schemas (-tags schemagen).
func SchemaTargets() []schemagen.Target {
	return []schemagen.Target{
		{Type: reflect.TypeOf(AddItemResult{})},
		{Type: reflect.TypeOf(AddMCPServerResult{})},
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
		{Type: reflect.TypeOf(RemoveMCPServerResult{})},
		{Type: reflect.TypeOf(RemoveRemoteResult{})},
		{Type: reflect.TypeOf(RunOneshotResult{})},
		{Type: reflect.TypeOf(SearchContentResult{})},
		{Type: reflect.TypeOf(SearchRemotesResult{})},
		{Type: reflect.TypeOf(SearchResult{})},
		{Type: reflect.TypeOf(SetBundleMCPResult{})},
		{Type: reflect.TypeOf(SetDefaultLLMResult{})},
		{Type: reflect.TypeOf(SetItemContentResult{})},
		{Type: reflect.TypeOf(SetMCPAutoRegisterResult{})},
		{Type: reflect.TypeOf(SetProfileContentResult{})},
		{Type: reflect.TypeOf(SetStatuslineResult{})},
		{Type: reflect.TypeOf(SyncDependenciesResult{})},
		{Type: reflect.TypeOf(UpdateBundleResult{})},
		{Type: reflect.TypeOf(UpdateProfileResult{})},
	}
}
