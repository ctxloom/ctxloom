package bundles

import (
	"errors"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// TestReadCommand_PromptsAliasReachesTheSameItem pins the fix for task
// stubborn-wow: trust.ParseSelector treats "commands" and "prompts" as
// aliases for the same trust.KindPrompt, but the old bundles.splitItemRef
// matched only the literal "commands", so "bundle#prompts/name" resolved
// through anything built on ParseSelector but failed through ReadCommand with
// "invalid command reference". ParseItemAsk routes ReadCommand through
// ParseSelector too, so both spellings must now resolve to the identical
// item.
func TestReadCommand_PromptsAliasReachesTheSameItem(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	bundleDir := bundlesDir + "/kit"
	require.NoError(t, afero.WriteFile(fsys, bundleDir+"/bundle.yaml", []byte(
		"version: \"1.0\"\ncommands:\n  deploy:\n    content: run the deploy script\n"), 0o644))

	l := NewLoader(NewProjectReader(fsys, []string{bundlesDir}))

	viaCommands, err := l.ReadCommand("kit#commands/deploy")
	require.NoError(t, err)
	require.Len(t, viaCommands, 1)

	viaPrompts, err := l.ReadCommand("kit#prompts/deploy")
	require.NoError(t, err, "the legacy 'prompts' spelling is an alias for the same trust.KindPrompt "+
		"and must reach the identical item, not 'invalid command reference'")
	require.Len(t, viaPrompts, 1)

	require.Equal(t, viaCommands[0].TrustRef, viaPrompts[0].TrustRef,
		"both spellings must resolve to the identical item — same TrustRef")
	require.Equal(t, viaCommands[0].Item, viaPrompts[0].Item)
	require.Equal(t, "deploy", viaPrompts[0].Item)
}

// TestReadFragment_CommandSelectorIsRefusedByKindNotByHash asserts that
// asking ReadFragment for an explicit "#commands/" (or "#prompts/") selector
// is reported as a KIND mismatch — a well-formed selector that names a kind
// ReadFragment does not serve — rather than as a not-found (which would
// misleadingly suggest the fragment store was searched and came up empty) or
// a generic parse failure (the selector parsed fine; it just named the wrong
// kind).
func TestReadFragment_CommandSelectorIsRefusedByKindNotByHash(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := "/bundles"
	bundleDir := bundlesDir + "/kit"
	require.NoError(t, afero.WriteFile(fsys, bundleDir+"/bundle.yaml", []byte(
		"version: \"1.0\"\ncommands:\n  deploy:\n    content: run the deploy script\n"), 0o644))

	l := NewLoader(NewProjectReader(fsys, []string{bundlesDir}))

	_, err := l.ReadFragment("kit#commands/deploy")
	require.Error(t, err)
	require.True(t, errors.Is(err, errs.ErrBadItemRef),
		"a well-formed selector naming the wrong kind must report as errs.ErrBadItemRef, got: %v", err)
	require.ErrorContains(t, err, "selects a prompt, not a fragment")

	// The "prompts" alias for the same kind is refused the identical way.
	_, err = l.ReadFragment("kit#prompts/deploy")
	require.Error(t, err)
	require.True(t, errors.Is(err, errs.ErrBadItemRef))
}
