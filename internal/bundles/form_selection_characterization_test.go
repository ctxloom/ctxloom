package bundles

import (
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"testing"
)

// Form-selection characterization.
//
// WHY THIS FILE EXISTS: EffectiveContentHash feeds the per-item TRUST GATE, and
// a grant binds the PAIR (bytes, form) — blessing the raw form must never
// validate a distilled exposure. So any refactor that moves WHERE raw-vs-
// distilled is decided must not move WHICH BYTES get hashed for a given
// exposure: if it did, every recorded user approval would silently stale and
// every user would be re-prompted (or worse, an approval would match content
// nobody blessed).
//
// The golden table below is therefore the contract, not an implementation
// detail. The hashes are literal sha256 digests of literal fixture bodies,
// computed independently of this package: if a change makes them disagree, the
// change altered the trust preimage and the change is wrong — never the table.
//
// Every exposure path that reaches the gate is driven through the SAME table:
// qualified and bare-name fragment/command resolution, the per-bundle command
// sweep, the version-aware entry points, and the ContentPayload/
// EffectiveContentHash primitives the gate's preimage is defined by.

// Fixture bodies. Kept as constants so the digests below can be verified by
// hand without running Go. A FRAGMENT's preimage is signing.FragmentPreimage
// over (premise, body) — these fixtures carry no premise, so:
//
//	printf 'ctxloom-fragment/1\npremise-len: 0\ncontent-len: %d\n\n\n%s' "${#body}" "$body" | sha256sum
//
// A COMMAND's preimage is signing.CommandPreimage over (description, exports,
// body) — these fixtures carry no description and the zero export config, so
// with exports='{"claude-code":{"enabled":true,"description":"","argument_hint":"","allowed_tools":[],"model":""}}':
//
//	printf 'ctxloom-command/1\ndescription-len: 0\nexports-len: %d\ncontent-len: %d\n\n\n%s\n%s' "${#exports}" "${#body}" "$exports" "$body" | sha256sum
//
// Each kind's digests moved ONCE, deliberately, when its presented values
// entered the preimage under its own contract string.
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

// Independently computed sha256 digests of the bodies above.
const (
	hashFragRaw       = "sha256:80db856affbc3ee7c0ed656cad590673ed2416867bd307a1a023d034a22042a9"
	hashFragDistilled = "sha256:b697beba57b970d5e92f5dc08bf6581dcefb367f0f430bb1ff4e966b8f089606"
	hashFragPlain     = "sha256:d495f54e8c396f53225661e480da5fe6c697262cd6cdced12a910526a0334029"
	hashFragNoDistill = "sha256:a4bb261976ae0804d8fc8ac58982c084586df5b406b3ed576161394e6215914f"

	hashCmdRaw       = "sha256:8652275f4c036c272dc969d99bfac47a20a095fb4076d439d6450babfafd1a44"
	hashCmdDistilled = "sha256:70c8c3deecc610f3c62716fe16171dbbf87ce4fd7746e54e08be474fb0484e8d"
	hashCmdPlain     = "sha256:89ddaa24be0b2c5efa5246244ce1873bbdf801e79b6e650d543c59b65917e7bc"
	hashCmdNoDistill = "sha256:d114a21386da623b2bcc1de6425f86a80a6d89ca5c38c77214b3df5e27f12071"
)

// charExpectation is one pinned exposure: for a given gate ref and form
// preference, exactly these bytes (by hash), served in exactly this form, with
// exactly this body handed to the caller.
type charExpectation struct {
	gateRef string // the ref the trust gate is keyed by
	form    string // "raw" | "distilled" — the form half of the grant
	hash    string // sha256 of the EXACT bytes hashed for the gate
	body    string // the exact bytes exposed to the caller
}

// charFragments pins the three fragment shapes under both preferences.
// Index: [preferDistilled][fragment name].
var charFragments = map[bool]map[string]charExpectation{
	false: {
		"distillable": {gateRef: "chars#fragments/distillable", form: "raw", hash: hashFragRaw, body: charFragRaw},
		"plain":       {gateRef: "chars#fragments/plain", form: "raw", hash: hashFragPlain, body: charFragPlain},
		"nodistill":   {gateRef: "chars#fragments/nodistill", form: "raw", hash: hashFragNoDistill, body: charFragNoDistill},
	},
	true: {
		// The ONLY cell that differs: a fragment that HAS a distilled form and
		// does not forbid it. Everything else falls back to raw, and that
		// fallback is itself part of the contract.
		"distillable": {gateRef: "chars#fragments/distillable", form: "distilled", hash: hashFragDistilled, body: charFragDistilled},
		"plain":       {gateRef: "chars#fragments/plain", form: "raw", hash: hashFragPlain, body: charFragPlain},
		"nodistill":   {gateRef: "chars#fragments/nodistill", form: "raw", hash: hashFragNoDistill, body: charFragNoDistill},
	},
}

// charCommands is the command half of the same table. Note the gate ref keeps
// the "#prompts/" kind segment even though the load selector is "#commands/":
// re-keying it would invalidate every existing grant.
var charCommands = map[bool]map[string]charExpectation{
	false: {
		"distillable": {gateRef: "chars#prompts/distillable", form: "raw", hash: hashCmdRaw, body: charCmdRaw},
		"plain":       {gateRef: "chars#prompts/plain", form: "raw", hash: hashCmdPlain, body: charCmdPlain},
		"nodistill":   {gateRef: "chars#prompts/nodistill", form: "raw", hash: hashCmdNoDistill, body: charCmdNoDistill},
	},
	true: {
		"distillable": {gateRef: "chars#prompts/distillable", form: "distilled", hash: hashCmdDistilled, body: charCmdDistilled},
		"plain":       {gateRef: "chars#prompts/plain", form: "raw", hash: hashCmdPlain, body: charCmdPlain},
		"nodistill":   {gateRef: "chars#prompts/nodistill", form: "raw", hash: hashCmdNoDistill, body: charCmdNoDistill},
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
	seen   map[string][2]string // gate ref → {hash, form}
	prefer bool
}

func newCharExposure(preferDistilled bool) *charExposure {
	seen := map[string][2]string{}
	return &charExposure{
		pipe:   NewPipeline(NewLoader(seedLocal(charSeed())), blockingGate(seen), LinksUnchecked(), preferDistilled),
		seen:   seen,
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

// fragmentPreimage / commandPreimage exercise the preimage builders the gate's
// hash is DEFINED by, at the same seam.
func (e *charExposure) fragmentPreimage(f BundleFragment) (string, ContentForm) {
	return f.EffectiveContentHash(e.prefer)
}

func (e *charExposure) commandPreimage(c BundleCommand) (string, ContentForm) {
	return c.EffectiveContentHash(e.prefer)
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
	hf, ok := e.seen[want.gateRef]
	if !ok {
		t.Fatalf("%s: trust gate never keyed on %q; saw %v", path, want.gateRef, collections.SortedKeys(e.seen))
	}
	if hf[0] != want.hash {
		t.Errorf("%s: gate hashed %s, want %s — THE TRUST PREIMAGE MOVED; every existing grant for this item just staled", path, hf[0], want.hash)
	}
	if hf[1] != want.form {
		t.Errorf("%s: gate saw form %q, want %q — a grant binds (bytes, form); the form half moved", path, hf[1], want.form)
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

// TestFormSelection_Characterization_Preimages pins the preimage builders
// themselves — EffectiveContentHash is the single definition of "the bytes of
// this item", and the gate binds to exactly its output.
func TestFormSelection_Characterization_Preimages(t *testing.T) {
	frags := charFragmentItems()
	cmds := charCommandItems()
	for _, prefer := range []bool{false, true} {
		e := newCharExposure(prefer)
		for name, want := range charFragments[prefer] {
			hash, form := e.fragmentPreimage(frags[name])
			if hash != want.hash || string(form) != want.form {
				t.Errorf("%s: EffectiveContentHash = (%s, %s), want (%s, %s)",
					describe("fragment preimage", name, prefer), hash, form, want.hash, want.form)
			}
		}
		for name, want := range charCommands[prefer] {
			hash, form := e.commandPreimage(cmds[name])
			if hash != want.hash || string(form) != want.form {
				t.Errorf("%s: EffectiveContentHash = (%s, %s), want (%s, %s)",
					describe("command preimage", name, prefer), hash, form, want.hash, want.form)
			}
		}
	}
}

func describe(path, name string, prefer bool) string {
	if prefer {
		return path + "/" + name + " [preferDistilled=true]"
	}
	return path + "/" + name + " [preferDistilled=false]"
}
