package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/trust"
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
// Its row() closure builds Name/Bundle/Ref straight from the operations
// projection's Name/Source fields, which are just as bundle-authored as the
// name printBundleItemTrust receives, and reached the listing unnormalized for
// the same reason.
//
// The listing is a trust surface in its own right: `fragment list --format
// json` carries the trust stamp each row was decided under, so a row a reader
// cannot tell apart from another row is a row they cannot decide about. The
// two names below differ ONLY by an ESC and must not arrive as the same
// string.
//
// Ref is deliberately NOT asserted to differ: it is the canonical identifier
// `show` and assemble accept, it goes through remote.NormalizeRef, and
// deleting is still the right ingest answer there. Display and ingest are two
// policies on purpose — what changed is that the DISPLAY field stopped
// borrowing the ingest one.
func TestListItemRows_ControlBytesInNameAreEscapedNotDeleted(t *testing.T) {
	appDir := t.TempDir()
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	const clean = "go-testing"
	hostile := "go-\x1btesting"
	// One bundle carrying both, so the listing has to keep them apart.
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{
		Name: "demo",
		Fragments: map[string]operations.BundleFragmentInput{
			clean:   {Content: "clean body", NoDistill: true},
			hostile: {Content: "hostile body", NoDistill: true},
		},
	})
	require.NoError(t, err)

	rows, err := listItemRows(cfg, ItemTypeFragment)
	require.NoError(t, err)

	names := map[string]bool{}
	for _, r := range rows {
		if r.Bundle == "demo" {
			names[r.Name] = true
		}
	}

	assert.True(t, names["go-^[testing"],
		"the ESC must render as visible caret notation; got names %v", names)
	assert.True(t, names[clean], "the clean name must still render byte for byte; got names %v", names)
	assert.Len(t, names, 2,
		"two names differing only by a control byte must not collapse onto one listing row; got %v", names)
	for name := range names {
		assert.NotContains(t, name, "\x1b", "no live ESC may reach the listing")
	}
}
