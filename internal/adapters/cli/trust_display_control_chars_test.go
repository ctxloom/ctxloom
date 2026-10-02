package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// An item name is bundle-authored: a fragment/command/mcp key straight out of
// the bundle's own YAML, never itself put through remote.NormalizeRef before
// reaching a display line. The trust DECISION (TrustStamper.ForRef, via
// trust.Ref.Key) always normalizes its own copy, but that says nothing
// about what gets printed — printBundleItemTrust used to interpolate the raw
// name straight into the terminal line it writes for `bundle show -i`.
//
// This pins the fix: a control character in the name must never reach the
// printed line, even though the (unrelated) trust label may still come back
// Pending/withheld because no such bundle exists.
func TestPrintBundleItemTrust_StripsControlCharactersFromName(t *testing.T) {
	appDir := t.TempDir()
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	stamper := operations.NewTrustStamper(cfg)

	var out bytes.Buffer
	printBundleItemTrust(&out, stamper, "somebundle", trust.KindFragment, "solid\nEVIL-INJECTED-LINE")

	assert.NotContains(t, out.String(), "\n\n", "the printed line must not carry an embedded newline from the name")
	assert.Contains(t, out.String(), "fragments/solid^JEVIL-INJECTED-LINE:")
	// Exactly one newline: the trailing one printBundleItemTrust itself emits.
	assert.Equal(t, 1, strings.Count(out.String(), "\n"))
}

// The trust line is where a human decides whether to accept a publisher's
// content, so it is the surface on which SILENT alteration costs the most: a
// reader who cannot see that a byte was removed is being asked to approve a
// string that is not the one on disk.
//
// This is the anti-deletion pin. It fails for a render site that deletes
// control characters rather than escaping them, and it cannot be satisfied by
// a tautology, because the property it asserts — two names differing ONLY by a
// control byte must not render as the same line — is precisely the property
// deletion destroys. Under deletion "solidEVIL" and "solid\x1bEVIL" both print
// "solidEVIL" and the reviewer has no way to tell which one they accepted.
func TestPrintBundleItemTrust_ControlBytesAreEscapedNotDeleted(t *testing.T) {
	appDir := t.TempDir()
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	stamper := operations.NewTrustStamper(cfg)

	render := func(name string) string {
		var out bytes.Buffer
		printBundleItemTrust(&out, stamper, "somebundle", trust.KindFragment, name)
		return out.String()
	}

	const clean = "go-testing"
	// ESC, CR and DEL: the three that let a hostile name repaint the line.
	hostile := "go-\x1btest\ring\x7f"

	got := render(hostile)

	// 1. The alteration is REPORTED, and the report is the rendered text
	//    itself: termsafe.Field's contract is that the escaping is visible in
	//    the output, so a reader sees exactly where the publisher put a
	//    control byte. Caret notation is what `cat -v` prints.
	assert.Contains(t, got, "^[", "ESC must render as visible caret notation")
	assert.Contains(t, got, "^M", "CR must render as visible caret notation")
	assert.Contains(t, got, "^?", "DEL must render as visible caret notation")

	// 2. Nothing was silently dropped: the deleting render's output — the
	//    name with its control bytes simply gone — must NOT be what appears.
	assert.NotContains(t, got, "fragments/go-testing:",
		"a deleted control byte would make the hostile name render as the clean one")

	// 3. The bytes are inert: no live control character reaches the terminal,
	//    and the line stays one line.
	assert.NotContains(t, got, "\x1b")
	assert.NotContains(t, got, "\r")
	assert.NotContains(t, got, "\x7f")
	assert.Equal(t, 1, strings.Count(got, "\n"))

	// 4. The property the ruling turns on: two genuinely distinct names must
	//    never display identically on a trust surface.
	assert.NotEqual(t, render(clean), got,
		"a name carrying control bytes must not render the same as one that does not")
}

// printBundleHookTrust is the same trust surface keyed by a hook's
// "<event>/<index>" identity, whose event half comes straight from the
// bundle's own hooks config and is therefore just as publisher-authored as an
// item name.
func TestPrintBundleHookTrust_ControlBytesAreEscapedNotDeleted(t *testing.T) {
	appDir := t.TempDir()
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	stamper := operations.NewTrustStamper(cfg)

	var out bytes.Buffer
	printBundleHookTrust(&out, stamper, "somebundle", bundles.HookEntry{
		Event: "session-start\x1b[2K",
		Index: 0,
	})

	got := out.String()
	assert.Contains(t, got, "hooks/session-start^[[2K/0:", "the hook id must render escaped, not stripped")
	assert.NotContains(t, got, "hooks/session-start[2K/0:", "a deleted ESC would leave the erase-line text bare")
	assert.NotContains(t, got, "\x1b")
	assert.Equal(t, 1, strings.Count(got, "\n"))
}

// listItemRows is the `fragment list` / `command list` read path (item_list.go).
// Its rows feed BOTH the human listing and `--format json`, so the row holds
// the publisher's ACTUAL bytes and only the text renderer (printItemInfos)
// escapes them. termsafe exists to protect a terminal; a JSON consumer is not
// one, and JSON's own grammar already renders a control byte inert inside a
// string.
//
// hostileItemProject seeds one bundle whose name and one of whose fragment
// names and tags carry an ESC, beside a clean fragment whose name differs from
// the hostile one ONLY by that ESC — so collapsing them, deleting the byte or
// escaping it on the wrong path each shows up as a distinct failure.
const (
	cleanItemName   = "go-testing"
	hostileItemName = "go-\x1btesting"
	hostileBundle   = "de\x1bmo"
	hostileTag      = "t\x1bag"
)

func hostileItemProject(t *testing.T) *config.Config {
	t.Helper()
	cfg := itemFormatProject(t)
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{
		Name: hostileBundle,
		Fragments: map[string]operations.BundleFragmentInput{
			cleanItemName:   {Content: "clean body", NoDistill: true},
			hostileItemName: {Content: "hostile body", Tags: []string{hostileTag}, NoDistill: true},
		},
	})
	require.NoError(t, err)
	return cfg
}

// TestListItemRows_HoldRawBytes pins the struct half of the ruling: the row
// carries what is on disk, byte for byte, and two names differing only by a
// control byte stay two rows.
//
// Ref is deliberately NOT asserted: it is the canonical identifier `show` and
// assemble accept, and it goes through remote.NormalizeRef, an ingest boundary
// with its own policy.
func TestListItemRows_HoldRawBytes(t *testing.T) {
	cfg := hostileItemProject(t)

	rows, err := listItemRows(cfg, ItemTypeFragment)
	require.NoError(t, err)

	names := map[string]bool{}
	for _, r := range rows {
		if r.Bundle == hostileBundle {
			names[r.Name] = true
		}
	}
	assert.Equal(t, map[string]bool{cleanItemName: true, hostileItemName: true}, names,
		"the rows must carry the on-disk names unmodified, under the on-disk bundle name")
}

// TestListItems_TextEscapesAndJSONCarriesRaw is the two-armed render test the
// ruling requires. Every field the text listing prints that a publisher wrote
// must arrive ESCAPED there, and the same fields must arrive RAW in --format
// json — so a script can read a name out of the json and pass it straight
// back. One-armed coverage is what would let the other arm rot.
func TestListItems_TextEscapesAndJSONCarriesRaw(t *testing.T) {
	t.Run("text escapes", func(t *testing.T) {
		hostileItemProject(t)
		cmd, buf := testCmd()

		require.NoError(t, listItems(cmd, ItemTypeFragment, ""))

		got := buf.String()
		assert.NotContains(t, got, "\x1b", "no live ESC may reach the terminal")
		assert.Contains(t, got, "  de^[mo:\n", "the bundle heading renders escaped")
		assert.Contains(t, got, "    - go-^[testing [t^[ag]\n", "the name and its tags render escaped")
		assert.Contains(t, got, "    - go-testing\n", "the clean name renders byte for byte")
	})

	t.Run("json carries raw and round-trips", func(t *testing.T) {
		hostileItemProject(t)
		cmd, out := itemFormatCmd(t, string(clifmt.FormatJSON))

		require.NoError(t, listItems(cmd, ItemTypeFragment, ""))

		var rows []itemRow
		require.NoError(t, json.Unmarshal(out(), &rows), "--format json must emit the rows")
		var hostile *itemRow
		for i := range rows {
			if rows[i].Name == hostileItemName {
				hostile = &rows[i]
			}
		}
		require.NotNil(t, hostile, "the hostile name must read back out of the json exactly as it is on disk; got %+v", rows)
		assert.Equal(t, hostileBundle, hostile.Bundle, "the bundle reads back raw")
		assert.Equal(t, []string{hostileTag}, hostile.Tags, "the tags read back raw")

		// The round trip: what the json said is a filter the CLI accepts.
		cmd, out = itemFormatCmd(t, string(clifmt.FormatJSON))
		require.NoError(t, listItems(cmd, ItemTypeFragment, hostile.Bundle))
		var filtered []itemRow
		require.NoError(t, json.Unmarshal(out(), &filtered))
		assert.Len(t, filtered, 2, "the bundle name read out of the json selects that bundle's items")
	})
}
