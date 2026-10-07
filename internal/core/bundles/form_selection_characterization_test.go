package bundles

import (
	"testing"
)

// Form-selection characterization.
//
// WHY THIS FILE EXISTS: any refactor that moves WHERE raw-vs-distilled is
// decided must not move WHICH BYTES are served, in WHICH FORM, for a given
// exposure. Every exposure path is driven through the SAME table: qualified
// and bare-name fragment/command resolution, the per-bundle command sweep, and
// the version-aware entry points.

const (
	charFragRaw       = "raw fragment body"
	charFragDistilled = "distilled fragment body"
	charFragPlain     = "plain fragment body"
	charFragNoDistill = "nodistill fragment body"

	charCmdRaw       = "raw command body"
	charCmdDistilled = "distilled command body"
	charCmdPlain     = "plain command body"
	charCmdNoDistill = "nodistill command body"
)

// charExpectation is one pinned exposure: for a given item and form
// preference, served in exactly this form, with exactly this body handed to
// the caller.
type charExpectation struct {
	form string // "raw" | "distilled"
	body string // the exact bytes exposed to the caller
}

// charFragments pins the three fragment shapes under both preferences.
// Index: [preferDistilled][fragment name].
var charFragments = map[bool]map[string]charExpectation{
	false: {
		"distillable": {form: "raw", body: charFragRaw},
		"plain":       {form: "raw", body: charFragPlain},
		"nodistill":   {form: "raw", body: charFragNoDistill},
	},
	true: {
		// The ONLY cell that differs: a fragment that HAS a distilled form and
		// does not forbid it. Everything else falls back to raw, and that
		// fallback is itself part of the contract.
		"distillable": {form: "distilled", body: charFragDistilled},
		"plain":       {form: "raw", body: charFragPlain},
		"nodistill":   {form: "raw", body: charFragNoDistill},
	},
}

// charCommands is the command half of the same table. Note the gate ref keeps
// the "#prompts/" kind segment even though the load selector is "#commands/":
// re-keying it would invalidate every existing grant.
var charCommands = map[bool]map[string]charExpectation{
	false: {
		"distillable": {form: "raw", body: charCmdRaw},
		"plain":       {form: "raw", body: charCmdPlain},
		"nodistill":   {form: "raw", body: charCmdNoDistill},
	},
	true: {
		"distillable": {form: "distilled", body: charCmdDistilled},
		"plain":       {form: "raw", body: charCmdPlain},
		"nodistill":   {form: "raw", body: charCmdNoDistill},
	},
}

func charFragmentItems() map[string]BundleFragment {
	return map[string]BundleFragment{
		"distillable": {
			ItemBody: ItemBody{
				Content:   charFragRaw,
				Distilled: charFragDistilled,
			},
		},
		"plain": {
			ItemBody: ItemBody{
				Content: charFragPlain,
			},
		},
		"nodistill": {
			ItemBody: ItemBody{
				Content:   charFragNoDistill,
				Distilled: "nodistill fragment distilled (never served)",
				NoDistill: true,
			},
		},
	}
}

func charCommandItems() map[string]BundleCommand {
	return map[string]BundleCommand{
		"distillable": {
			ItemBody: ItemBody{
				Content:   charCmdRaw,
				Distilled: charCmdDistilled,
			},
		},
		"plain": {
			ItemBody: ItemBody{
				Content: charCmdPlain,
			},
		},
		"nodistill": {
			ItemBody: ItemBody{
				Content:   charCmdNoDistill,
				Distilled: "nodistill command distilled (never served)",
				NoDistill: true,
			},
		},
	}
}

func charSeed() map[string]*Bundle {
	return map[string]*Bundle{
		"chars": {
			Name:      "chars",
			Fragments: charFragmentItems(),
			Commands:  charCommandItems(),
		},
	}
}

// charExposure is the ONLY part of this file that knows how a form preference
// reaches the read path. Every assertion below goes through it, so relocating
// the preference (constructor → per-call argument → wherever it lands next)
// changes these method bodies and nothing else. If a relocation forces an edit
// anywhere BELOW this type, the relocation changed behavior.
type charExposure struct {
	pipe   *Pipeline
	prefer bool
}

func newCharExposure(preferDistilled bool) *charExposure {
	return &charExposure{
		pipe:   admitAllPipe(NewLoader(seedLocal(charSeed())), preferDistilled),
		prefer: preferDistilled,
	}
}

func (e *charExposure) fragment(name string) (*LoadedContent, error) {
	return e.pipe.GetFragment(name)
}

func (e *charExposure) command(name string) (*LoadedContent, error) {
	return e.pipe.GetCommand(name)
}

func (e *charExposure) commandsFromBundle(bundleRef string) []*LoadedContent {
	return e.pipe.CommandsFromBundleRef(bundleRef)
}

func (e *charExposure) fragmentAtVersion(ref, commit string) (*LoadedContent, error) {
	return e.pipe.GetFragmentAtVersion(ref, commit)
}

func (e *charExposure) promptAtVersion(ref, commit string) (*LoadedContent, error) {
	return e.pipe.GetPromptAtVersion(ref, commit)
}

func (e *charExposure) fragmentVersions(ref string, commits []string) []*LoadedContent {
	return e.pipe.ResolveFragmentVersions(ref, commits)
}

// --- assertions (must survive any relocation of form selection UNEDITED) ----

func assertExposure(t *testing.T, path string, e *charExposure, want charExpectation, got *LoadedContent, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", path, err)
	}
	if got == nil {
		t.Fatalf("%s: nil content", path)
	}
	if got.Content != want.body {
		t.Errorf("%s: exposed body = %q, want %q", path, got.Content, want.body)
	}
	if got.IsDistilled != (want.form == "distilled") {
		t.Errorf("%s: IsDistilled = %v, want %v", path, got.IsDistilled, want.form == "distilled")
	}
}

// TestFormSelection_Characterization_QualifiedFragment pins the primary
// exposure path (ctxloom:// fragment resources, assembly) for every fragment
// shape under both form preferences.
func TestFormSelection_Characterization_QualifiedFragment(t *testing.T) {
	for _, prefer := range []bool{false, true} {
		for name, want := range charFragments[prefer] {
			e := newCharExposure(prefer)
			got, err := e.fragment("chars#fragments/" + name)
			assertExposure(t, describe("GetFragment", name, prefer), e, want, got, err)
		}
	}
}

// TestFormSelection_Characterization_BareFragmentSearch pins the bare-name
// search path, which resolves through the same content choke.
func TestFormSelection_Characterization_BareFragmentSearch(t *testing.T) {
	for _, prefer := range []bool{false, true} {
		for name, want := range charFragments[prefer] {
			e := newCharExposure(prefer)
			got, err := e.fragment(name)
			assertExposure(t, describe("GetFragment(bare)", name, prefer), e, want, got, err)
		}
	}
}

// TestFormSelection_Characterization_QualifiedCommand pins command exposure —
// including that the gate ref keeps the "prompts" kind segment.
func TestFormSelection_Characterization_QualifiedCommand(t *testing.T) {
	for _, prefer := range []bool{false, true} {
		for name, want := range charCommands[prefer] {
			e := newCharExposure(prefer)
			got, err := e.command("chars#commands/" + name)
			assertExposure(t, describe("GetCommand", name, prefer), e, want, got, err)
		}
	}
}

// TestFormSelection_Characterization_BareCommandSearch pins the bare-name
// command search path.
func TestFormSelection_Characterization_BareCommandSearch(t *testing.T) {
	for _, prefer := range []bool{false, true} {
		for name, want := range charCommands[prefer] {
			e := newCharExposure(prefer)
			got, err := e.command(name)
			assertExposure(t, describe("GetCommand(bare)", name, prefer), e, want, got, err)
		}
	}
}

// TestFormSelection_Characterization_CommandsFromBundleRef pins the per-bundle
// command sweep that backs slash-command export — the path a profile's bundle
// commands take.
func TestFormSelection_Characterization_CommandsFromBundleRef(t *testing.T) {
	for _, prefer := range []bool{false, true} {
		e := newCharExposure(prefer)
		got := e.commandsFromBundle("chars")
		byItem := map[string]*LoadedContent{}
		for _, lc := range got {
			byItem[lc.Item] = lc
		}
		if len(byItem) != len(charCommands[prefer]) {
			t.Fatalf("CommandsFromBundleRef(prefer=%v) returned %d commands, want %d", prefer, len(byItem), len(charCommands[prefer]))
		}
		for name, want := range charCommands[prefer] {
			assertExposure(t, describe("CommandsFromBundleRef", name, prefer), e, want, byItem[name], nil)
		}
	}
}

// TestFormSelection_Characterization_AtVersionDefault pins the version-aware
// entry points on their default (unpinned) path, which must be byte-identical
// to the ordinary path — each historical version is gated by its OWN effective
// hash, so the default must not drift from GetFragment/GetCommand.
func TestFormSelection_Characterization_AtVersionDefault(t *testing.T) {
	for _, prefer := range []bool{false, true} {
		for name, want := range charFragments[prefer] {
			e := newCharExposure(prefer)
			got, err := e.fragmentAtVersion("chars#fragments/"+name, "")
			assertExposure(t, describe("GetFragmentAtVersion", name, prefer), e, want, got, err)
		}
		for name, want := range charCommands[prefer] {
			e := newCharExposure(prefer)
			got, err := e.promptAtVersion("chars#commands/"+name, "")
			assertExposure(t, describe("GetPromptAtVersion", name, prefer), e, want, got, err)
		}
	}
}

// TestFormSelection_Characterization_ResolveFragmentVersions pins the
// multi-version primitive's single-default case.
func TestFormSelection_Characterization_ResolveFragmentVersions(t *testing.T) {
	for _, prefer := range []bool{false, true} {
		for name, want := range charFragments[prefer] {
			e := newCharExposure(prefer)
			got := e.fragmentVersions("chars#fragments/"+name, nil)
			if len(got) != 1 {
				t.Fatalf("ResolveFragmentVersions(%s, prefer=%v) = %d items, want 1", name, prefer, len(got))
			}
			assertExposure(t, describe("ResolveFragmentVersions", name, prefer), e, want, got[0], nil)
		}
	}
}

func describe(path, name string, prefer bool) string {
	if prefer {
		return path + "/" + name + " [preferDistilled=true]"
	}
	return path + "/" + name + " [preferDistilled=false]"
}
