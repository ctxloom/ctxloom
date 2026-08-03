package archive

import (
	"archive/zip"
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// zipOf builds a zip whose entries are exactly the given paths, each under a
// single top-level directory (the shape HardenedExtract requires).
func zipOf(t *testing.T, files map[string]string, execPaths ...string) []byte {
	t.Helper()
	exec := map[string]bool{}
	for _, p := range execPaths {
		exec[p] = true
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if exec[name] {
			hdr.SetMode(0o755)
		} else {
			hdr.SetMode(0o644)
		}
		w, err := zw.CreateHeader(hdr)
		require.NoError(t, err)
		_, err = w.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func sampleArchive(t *testing.T) []byte {
	return zipOf(t, map[string]string{
		"core/fragments/style.md":           "STYLE-BODY\n",
		"core/mcp/ledger.yaml":              "command: /bin/ledger\n",
		"core/hooks/pre_tool/00-guard.yaml": "type: command\ncommand: guard\n",
	})
}

func TestArchiveStore_ReadsBundlesOutOfAPackedArchive(t *testing.T) {
	ctx := context.Background()
	st, err := New(sampleArchive(t), content.Provenance{IsLocal: true})
	require.NoError(t, err)

	ids, err := st.Bundles(ctx)
	require.NoError(t, err)
	assert.Equal(t, []content.BundleID{"core"}, ids)

	b, err := st.Open(ctx, "core")
	require.NoError(t, err)
	refs, err := b.Refs(ctx)
	require.NoError(t, err)

	got := map[trust.ItemKind][]string{}
	for _, r := range refs {
		got[r.Kind] = append(got[r.Kind], r.Name)
	}
	assert.Equal(t, []string{"style"}, got[trust.KindFragment])
	assert.Equal(t, []string{"ledger"}, got[trust.KindMCP])
	assert.Equal(t, []string{"pre_tool/00-guard"}, got[trust.KindHook])
}

func TestArchiveStore_ReadsFileBytesBackVerbatim(t *testing.T) {
	ctx := context.Background()
	st, err := New(sampleArchive(t), content.Provenance{IsLocal: true})
	require.NoError(t, err)
	b, err := st.Open(ctx, "core")
	require.NoError(t, err)

	body, err := b.ReadFile(ctx, "fragments/style.md")
	require.NoError(t, err)
	assert.Equal(t, "STYLE-BODY\n", string(body))
}

// An archive arrives from somewhere else by definition, so its extraction is
// the one place a traversal entry could plant a file outside the destination.
// This backend must NOT reimplement that check — it must go through the
// hardened extractor that already has it, and this asserts the entry is
// refused rather than silently landing.
func TestArchiveStore_RefusesATraversalEntryRatherThanWritingOutsideItsRoot(t *testing.T) {
	bad := zipOf(t, map[string]string{
		"core/fragments/ok.md":  "OK\n",
		"core/../../escaped.md": "ESCAPED\n",
	})
	_, err := New(bad, content.Provenance{IsLocal: true})
	require.Error(t, err, "a traversal entry must be refused, not extracted")
}

// The read-only asymmetry again: an archive is a fixed set of bytes, and a
// Writer over it would mutate a temporary extraction nobody reads back.
func TestArchiveStore_IsNotAWriter(t *testing.T) {
	st, err := New(sampleArchive(t), content.Provenance{IsLocal: true})
	require.NoError(t, err)
	_, isWriter := any(st).(content.Writer)
	assert.False(t, isWriter, "an archive store must not implement Writer")
}

// skillModes reads one skill's per-file declared modes back through L0.
func skillModes(t *testing.T, data []byte) map[string]content.ComponentMode {
	t.Helper()
	st, err := New(data, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	ctx := context.Background()
	b, err := st.Open(ctx, "core")
	require.NoError(t, err)
	it, err := b.Item(ctx, trust.Ref{Bundle: "core", Kind: trust.KindSkill, Name: "rev"})
	require.NoError(t, err)
	surf, err := it.Surface(ctx)
	require.NoError(t, err)
	skill, ok := surf.(content.Skill)
	require.True(t, ok)

	modes := map[string]content.ComponentMode{}
	for _, f := range skill.Files {
		modes[f.Path] = f.Mode
	}
	return modes
}

// Executability is DECLARED, in the sidecar, and travels as bytes inside a
// hashed component — it is therefore attested. This is the positive case: an
// archive carrying the sidecar yields an executable component.
func TestArchiveStore_DeclaredExecutabilitySurvivesTheArchive(t *testing.T) {
	modes := skillModes(t, zipOf(t, map[string]string{
		"core/bundle.yaml":              "version: \"1.0.0\"\n",
		"core/skills/.rev.meta.yaml":    "executable:\n  - scripts/go.sh\n",
		"core/skills/rev/SKILL.md":      "---\nname: rev\n---\nBODY\n",
		"core/skills/rev/scripts/go.sh": "#!/bin/sh\n",
	}, "core/skills/rev/scripts/go.sh"))

	assert.Equal(t, content.ModeExecutable, modes["scripts/go.sh"],
		"a declared-executable component must read back executable")
	assert.Equal(t, content.ModeRegular, modes["SKILL.md"],
		"a file not on the executable list must not be promoted")
}

// THE NEGATIVE CASE, AND IT IS THE IMPORTANT ONE. A filesystem exec bit alone
// confers NOTHING: the same archive without the sidecar reads back regular,
// even though the zip entry's mode is 0755.
//
// This is a deliberate property rather than a gap. A mode bit is not portable
// (on Windows Go toggles only the read-only bit), so a digest that covered it
// would be platform-dependent and a checkout there would fail its own
// signature. Declaring executability in a hashed sidecar makes it ATTESTED
// instead — but it also means an archive built by a tool that packs modes and
// forgets the sidecar produces a skill whose script will not be marked
// executable on delivery. Pinning it here is what makes that a known contract
// rather than a surprise.
func TestArchiveStore_AFilesystemExecBitAloneDoesNotConferDeclaredExecutability(t *testing.T) {
	modes := skillModes(t, zipOf(t, map[string]string{
		"core/bundle.yaml":              "version: \"1.0.0\"\n",
		"core/skills/rev/SKILL.md":      "---\nname: rev\n---\nBODY\n",
		"core/skills/rev/scripts/go.sh": "#!/bin/sh\n",
	}, "core/skills/rev/scripts/go.sh"))

	assert.Equal(t, content.ModeRegular, modes["scripts/go.sh"],
		"without a sidecar declaration, a 0755 archive entry must NOT read back as executable")
}

func TestArchiveStore_EmptyArchiveIsRefusedRatherThanReadingAsAnEmptyStore(t *testing.T) {
	_, err := New(nil, content.Provenance{IsLocal: true})
	require.Error(t, err, "no bytes at all is a caller error, not an empty store")
}

func TestArchiveStore_InvalidProvenanceIsRefused(t *testing.T) {
	_, err := New(sampleArchive(t), content.Provenance{})
	require.Error(t, err, "a store must declare where its content came from")
}
