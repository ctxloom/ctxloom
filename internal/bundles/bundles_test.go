package bundles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// =============================================================================
// Bundles Package Tests
// =============================================================================
//
// This package manages context bundles - collections of fragments, prompts,
// and MCP server configurations that provide AI context.
//
// KEY CONCEPTS:
// - Bundles are YAML files containing fragments (context) and prompts (templates)
// - Fragments can be distilled (compressed) to reduce token usage
// - Content hashes enable incremental distillation (only re-distill changed content)
// - Tags enable filtering and profile assembly
//
// IMPORTANT BEHAVIORS:
// - IsDistilled flag requires BOTH preference AND availability (AND logic)
// - Tags are inherited: bundle tags + item-specific tags are merged
// - Qualified references (bundle#fragments/name) bypass search, direct lookup
// - Search is case-sensitive and order-dependent (first match wins)
//
// =============================================================================

// =============================================================================
// BundleFragment Tests
// =============================================================================
// BundleFragment represents a single context fragment within a bundle.
// Fragments support distillation (AI-compressed versions) for token efficiency.

func TestBundleFragment_ComputeContentHash(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "empty content",
			content: "",
			want:    "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name:    "simple content",
			content: "hello world",
			want:    "sha256:b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9",
		},
		{
			name:    "multiline content",
			content: "line1\nline2\nline3",
			want:    "sha256:6bb6a5ad9b9c43a7cb535e636578716b64ac42edea814a4cad102ba404946837",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &BundleFragment{Content: tt.content}
			got := f.ComputeContentHash()
			assert.Regexp(t, `^sha256:[a-f0-9]{64}$`, got)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBundleFragment_NeedsDistill(t *testing.T) {
	tests := []struct {
		name     string
		fragment BundleFragment
		want     bool
	}{
		{
			name:     "no_distill set",
			fragment: BundleFragment{NoDistill: true, Content: "test"},
			want:     false,
		},
		{
			name:     "no distilled content",
			fragment: BundleFragment{Content: "test"},
			want:     true,
		},
		{
			name:     "distilled but no hash",
			fragment: BundleFragment{Content: "test", Distilled: "distilled"},
			want:     true,
		},
		{
			name: "hash mismatch",
			fragment: BundleFragment{
				Content:     "new content",
				Distilled:   "distilled",
				ContentHash: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			},
			want: true,
		},
		{
			name: "hash matches",
			fragment: BundleFragment{
				Content:     "test",
				Distilled:   "distilled",
				ContentHash: "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fragment.NeedsDistill()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBundleFragment_EffectiveContent(t *testing.T) {
	tests := []struct {
		name            string
		fragment        BundleFragment
		preferDistilled bool
		want            string
	}{
		{
			name:            "prefer distilled but none available",
			fragment:        BundleFragment{Content: "original"},
			preferDistilled: true,
			want:            "original",
		},
		{
			name:            "prefer distilled and available",
			fragment:        BundleFragment{Content: "original", Distilled: "distilled"},
			preferDistilled: true,
			want:            "distilled",
		},
		{
			name:            "prefer original",
			fragment:        BundleFragment{Content: "original", Distilled: "distilled"},
			preferDistilled: false,
			want:            "original",
		},
		{
			name:            "no_distill true falls back to content",
			fragment:        BundleFragment{Content: "original", Distilled: "distilled", NoDistill: true},
			preferDistilled: true,
			want:            "original",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fragment.EffectiveContent(tt.preferDistilled)
			assert.Equal(t, tt.want, got)
		})
	}
}

// =============================================================================
// BundleCommand Tests
// =============================================================================

func TestBundleCommand_ComputeContentHash(t *testing.T) {
	p := &BundleCommand{Content: "test prompt"}
	got := p.ComputeContentHash()
	assert.Regexp(t, `^sha256:[a-f0-9]{64}$`, got)
}

func TestBundleCommand_NeedsDistill(t *testing.T) {
	tests := []struct {
		name   string
		prompt BundleCommand
		want   bool
	}{
		{
			name:   "no_distill set",
			prompt: BundleCommand{NoDistill: true, Content: "test"},
			want:   false,
		},
		{
			name:   "no distilled content",
			prompt: BundleCommand{Content: "test"},
			want:   true,
		},
		{
			name:   "distilled but no hash",
			prompt: BundleCommand{Content: "test", Distilled: "distilled"},
			want:   true,
		},
		{
			name: "hash mismatch",
			prompt: BundleCommand{
				Content:     "new content",
				Distilled:   "distilled",
				ContentHash: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			},
			want: true,
		},
		{
			name: "hash matches",
			prompt: BundleCommand{
				Content:     "test",
				Distilled:   "distilled",
				ContentHash: "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.prompt.NeedsDistill()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBundleCommand_EffectiveContent(t *testing.T) {
	tests := []struct {
		name            string
		prompt          BundleCommand
		preferDistilled bool
		want            string
	}{
		{
			name:            "prefer distilled but none available",
			prompt:          BundleCommand{Content: "original"},
			preferDistilled: true,
			want:            "original",
		},
		{
			name:            "prefer distilled and available",
			prompt:          BundleCommand{Content: "original", Distilled: "distilled"},
			preferDistilled: true,
			want:            "distilled",
		},
		{
			name:            "prefer original",
			prompt:          BundleCommand{Content: "original", Distilled: "distilled"},
			preferDistilled: false,
			want:            "original",
		},
		{
			name:            "no_distill true falls back to content",
			prompt:          BundleCommand{Content: "original", Distilled: "distilled", NoDistill: true},
			preferDistilled: true,
			want:            "original",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.prompt.EffectiveContent(tt.preferDistilled)
			assert.Equal(t, tt.want, got)
		})
	}
}

// =============================================================================
// Effective-content hash (trust rework TR0)
// =============================================================================
// EffectiveContentHash hashes EXACTLY the bytes EffectiveContent returns and
// reports their form. The trust gate binds to this — never the recorded
// ContentHash field, never a raw fallback when distilled is served. A raw-form
// grant must NOT validate a distilled exposure (different hash AND different form).

func TestBundleFragment_EffectiveContentHash(t *testing.T) {
	frag := BundleFragment{Content: "RAW-BYTES", Distilled: "DISTILLED-BYTES"}

	rawHash, rawForm := frag.EffectiveContentHash(false)
	distHash, distForm := frag.EffectiveContentHash(true)

	// Hashes cover exactly the bytes EffectiveContent would serve.
	assert.Equal(t, hashContent([]byte("RAW-BYTES")), rawHash)
	assert.Equal(t, hashContent([]byte("DISTILLED-BYTES")), distHash)
	assert.Equal(t, FormRaw, rawForm)
	assert.Equal(t, FormDistilled, distForm)

	// Raw vs distilled exposure differ on BOTH hash and form, so a raw-form
	// grant can never validate a distilled exposure.
	assert.NotEqual(t, rawHash, distHash)
	assert.NotEqual(t, rawForm, distForm)

	// NoDistill pins the form to raw even when distilled is preferred — no
	// raw fallback ambiguity: the served bytes are raw and the hash says raw.
	noDistill := BundleFragment{Content: "RAW-BYTES", Distilled: "DISTILLED-BYTES", NoDistill: true}
	h, form := noDistill.EffectiveContentHash(true)
	assert.Equal(t, hashContent([]byte("RAW-BYTES")), h)
	assert.Equal(t, FormRaw, form)

	// The recorded ContentHash field is irrelevant to the effective hash — a
	// forged recorded value cannot move it.
	forged := BundleFragment{Content: "RAW-BYTES", ContentHash: "sha256:deadbeef"}
	fh, _ := forged.EffectiveContentHash(false)
	assert.Equal(t, hashContent([]byte("RAW-BYTES")), fh)
}

func TestBundleCommand_EffectiveContentHash(t *testing.T) {
	prompt := BundleCommand{Content: "RAW-BYTES", Distilled: "DISTILLED-BYTES"}

	rawHash, rawForm := prompt.EffectiveContentHash(false)
	distHash, distForm := prompt.EffectiveContentHash(true)

	assert.Equal(t, hashContent([]byte("RAW-BYTES")), rawHash)
	assert.Equal(t, hashContent([]byte("DISTILLED-BYTES")), distHash)
	assert.Equal(t, FormRaw, rawForm)
	assert.Equal(t, FormDistilled, distForm)
	assert.NotEqual(t, rawHash, distHash)
	assert.NotEqual(t, rawForm, distForm)
}

// =============================================================================
// BundleMCP content hash (trust rework TR0)
// =============================================================================
// The MCP hash binds an executable surface: Command + Args + Env + Installation.
// Env keys are canonicalized (order-insensitive); Args order is significant;
// Notes are excluded (human-only, never executed).

func TestBundleMCP_ComputeContentHash(t *testing.T) {
	base := BundleMCP{
		Command:      "postgres-mcp",
		Args:         []string{"--host", "db", "--port", "5432"},
		Env:          map[string]string{"PGUSER": "admin", "PGPASSWORD": "secret", "PGDATABASE": "app"},
		Installation: "npm i -g postgres-mcp",
		Notes:        "human-only notes",
	}
	baseHash := base.ComputeContentHash()
	assert.Regexp(t, `^sha256:[a-f0-9]{64}$`, baseHash)

	// Deterministic across calls.
	assert.Equal(t, baseHash, base.ComputeContentHash())

	// Env key order does not affect the hash (json.Marshal sorts map keys).
	envReordered := base
	envReordered.Env = map[string]string{"PGDATABASE": "app", "PGPASSWORD": "secret", "PGUSER": "admin"}
	assert.Equal(t, baseHash, envReordered.ComputeContentHash(), "env key order must not change the hash")

	// Notes are excluded.
	notesChanged := base
	notesChanged.Notes = "totally different notes"
	assert.Equal(t, baseHash, notesChanged.ComputeContentHash(), "Notes must be excluded from the hash")

	// Arg order is significant.
	argsReordered := base
	argsReordered.Args = []string{"--port", "5432", "--host", "db"}
	assert.NotEqual(t, baseHash, argsReordered.ComputeContentHash(), "arg order must change the hash")

	// Installation is part of the hash.
	installChanged := base
	installChanged.Installation = "different install steps"
	assert.NotEqual(t, baseHash, installChanged.ComputeContentHash(), "Installation must be part of the hash")

	// Env value changes change the hash.
	envValueChanged := base
	envValueChanged.Env = map[string]string{"PGUSER": "admin", "PGPASSWORD": "changed", "PGDATABASE": "app"}
	assert.NotEqual(t, baseHash, envValueChanged.ComputeContentHash(), "env value must be part of the hash")

	// Command changes change the hash.
	cmdChanged := base
	cmdChanged.Command = "mysql-mcp"
	assert.NotEqual(t, baseHash, cmdChanged.ComputeContentHash(), "Command must be part of the hash")
}

// =============================================================================
// ContentPayload — the single preimage builder invariant (signature envelope
// spec §3.2: "there must be exactly one definition of 'the bytes of item X in
// form F' in the codebase"). These tests prove ComputeContentHash/
// EffectiveContentHash hash EXACTLY the bytes ContentPayload returns, so a
// countersignature built over ContentPayload's output and a hash computed by
// these methods can never drift apart.
// =============================================================================

func TestBundleFragment_ContentPayload_IsHashPreimage(t *testing.T) {
	frag := BundleFragment{Content: "RAW-BYTES", Distilled: "DISTILLED-BYTES"}

	rawPayload, rawForm := frag.ContentPayload(false)
	distPayload, distForm := frag.ContentPayload(true)

	assert.Equal(t, []byte("RAW-BYTES"), rawPayload)
	assert.Equal(t, FormRaw, rawForm)
	assert.Equal(t, []byte("DISTILLED-BYTES"), distPayload)
	assert.Equal(t, FormDistilled, distForm)

	// EffectiveContentHash must hash exactly these bytes — same function,
	// not a re-derivation.
	rawHash, rawHashForm := frag.EffectiveContentHash(false)
	distHash, distHashForm := frag.EffectiveContentHash(true)
	assert.Equal(t, hashContent(rawPayload), rawHash)
	assert.Equal(t, rawForm, rawHashForm)
	assert.Equal(t, hashContent(distPayload), distHash)
	assert.Equal(t, distForm, distHashForm)
}

func TestBundleCommand_ContentPayload_IsHashPreimage(t *testing.T) {
	cmd := BundleCommand{Content: "RAW-BYTES", Distilled: "DISTILLED-BYTES"}

	rawPayload, rawForm := cmd.ContentPayload(false)
	distPayload, distForm := cmd.ContentPayload(true)

	assert.Equal(t, []byte("RAW-BYTES"), rawPayload)
	assert.Equal(t, FormRaw, rawForm)
	assert.Equal(t, []byte("DISTILLED-BYTES"), distPayload)
	assert.Equal(t, FormDistilled, distForm)

	rawHash, _ := cmd.EffectiveContentHash(false)
	distHash, _ := cmd.EffectiveContentHash(true)
	assert.Equal(t, hashContent(rawPayload), rawHash)
	assert.Equal(t, hashContent(distPayload), distHash)
}

func TestBundleMCP_ContentPayload_IsHashPreimage(t *testing.T) {
	mcp := BundleMCP{
		Command:      "postgres-mcp",
		Args:         []string{"--host", "db"},
		Env:          map[string]string{"PGUSER": "admin"},
		Installation: "npm i -g postgres-mcp",
		Notes:        "human-only, excluded",
	}

	payload, err := mcp.ContentPayload()
	require.NoError(t, err)
	// The `preimage` field is the exec-preimage contract version (spec §3.3.2).
	// It was ADDED to this fixture when the version carrier landed, which
	// changed the preimage bytes and therefore invalidated every pre-existing
	// MCP approval — the one-time, deliberate mass re-review the version exists
	// to make announced rather than accidental. It cost nothing here only
	// because v0.7.0-pre1 has never shipped. See exec_preimage_test.go, which
	// pins the exact byte layout and its field ORDER (JSONEq below is
	// order-insensitive and would not catch a misplaced version carrier).
	assert.JSONEq(t, `{"preimage":"ctxloom-exec/1","command":"postgres-mcp","args":["--host","db"],"env":{"PGUSER":"admin"},"installation":"npm i -g postgres-mcp"}`, string(payload))

	// ComputeContentHash must hash exactly these bytes.
	assert.Equal(t, hashContent(payload), mcp.ComputeContentHash())
}

func TestBundleHook_ContentPayload_IsHashPreimage(t *testing.T) {
	hook := BundleHook{
		Matcher:         "Bash",
		Type:            "command",
		Command:         "echo hi",
		Prompt:          "",
		PreToolFallback: true,
	}

	payload, err := hook.ContentPayload()
	require.NoError(t, err)
	assert.Equal(t, hashContent(payload), hook.ComputeContentHash())
}

// TestBundleSkill_ToManifest proves the bundle.yaml `files:` map (BundleSkill.
// Files) converts into the canonical, sorted SkillManifest shape — the same
// shape ParseSkillPackage computes fresh from a source tree — regardless of
// the map's iteration order.
func TestBundleSkill_ToManifest(t *testing.T) {
	skill := BundleSkill{Files: map[string]SkillFileMeta{
		"scripts/run.sh":  {SHA256: "sha256:script1", Mode: "0755"},
		"SKILL.md":        {SHA256: "sha256:skillmd1", Mode: "0644"},
		"assets/logo.png": {SHA256: "sha256:asset1", Mode: "0644"},
	}}
	got := skill.ToManifest()
	assert.Equal(t, SkillManifest{
		{Path: "SKILL.md", SHA256: "sha256:skillmd1", Mode: "0644"},
		{Path: "assets/logo.png", SHA256: "sha256:asset1", Mode: "0644"},
		{Path: "scripts/run.sh", SHA256: "sha256:script1", Mode: "0755"},
	}, got, "entries must be sorted by path, independent of map iteration order")
}

// TestBundleSkill_ContentPayload_IsHashPreimage proves ComputeContentHash
// hashes EXACTLY ContentPayload's output — the single-preimage-builder
// contract every other kind's ContentPayload/ComputeContentHash pair holds
// (see BundleFragment/BundleMCP/BundleHook above) — and that the payload is a
// canonical encoding of the manifest, versioned like the other exec-shaped
// preimages.
func TestBundleSkill_ContentPayload_IsHashPreimage(t *testing.T) {
	skill := BundleSkill{Files: map[string]SkillFileMeta{
		"SKILL.md":       {SHA256: "sha256:skillmd1", Mode: "0644"},
		"scripts/run.sh": {SHA256: "sha256:script1", Mode: "0755"},
	}}

	payload, err := skill.ContentPayload(nil, "", "")
	require.NoError(t, err)
	assert.JSONEq(t,
		`{"preimage":"ctxloom-exec/1","manifest":[`+
			`{"path":"SKILL.md","sha256":"sha256:skillmd1","mode":"0644"},`+
			`{"path":"scripts/run.sh","sha256":"sha256:script1","mode":"0755"}]}`,
		string(payload))

	assert.Equal(t, hashContent(payload), skill.ComputeContentHash(nil, "", ""))
}

// TestBundleSkill_ComputeContentHash proves editing ANY single file in the
// manifest — content, path, or MODE (the scripts/ exec bit) — changes the
// hash, which is what re-triggers review/sign on a script edit (skill/command
// split plan §3.1).
func TestBundleSkill_ComputeContentHash(t *testing.T) {
	base := BundleSkill{Files: map[string]SkillFileMeta{
		"SKILL.md":       {SHA256: "sha256:skillmd1", Mode: "0644"},
		"scripts/run.sh": {SHA256: "sha256:script1", Mode: "0755"},
	}}
	baseHash := base.ComputeContentHash(nil, "", "")
	assert.Regexp(t, `^sha256:[a-f0-9]{64}$`, baseHash)
	assert.Equal(t, baseHash, base.ComputeContentHash(nil, "", ""), "deterministic across calls")

	// Map iteration order must not affect the hash (Serialize sorts by path).
	reordered := BundleSkill{Files: map[string]SkillFileMeta{
		"scripts/run.sh": {SHA256: "sha256:script1", Mode: "0755"},
		"SKILL.md":       {SHA256: "sha256:skillmd1", Mode: "0644"},
	}}
	assert.Equal(t, baseHash, reordered.ComputeContentHash(nil, "", ""))

	// A script's content hash changing (a tampered/edited scripts/run.sh)
	// changes the whole-package hash.
	contentChanged := BundleSkill{Files: map[string]SkillFileMeta{
		"SKILL.md":       {SHA256: "sha256:skillmd1", Mode: "0644"},
		"scripts/run.sh": {SHA256: "sha256:different", Mode: "0755"},
	}}
	assert.NotEqual(t, baseHash, contentChanged.ComputeContentHash(nil, "", ""), "editing a file's content must change the package hash")

	// A mode flip alone (e.g. an exec bit added/removed) also changes the hash.
	modeChanged := BundleSkill{Files: map[string]SkillFileMeta{
		"SKILL.md":       {SHA256: "sha256:skillmd1", Mode: "0644"},
		"scripts/run.sh": {SHA256: "sha256:script1", Mode: "0644"},
	}}
	assert.NotEqual(t, baseHash, modeChanged.ComputeContentHash(nil, "", ""), "a mode-only change (e.g. losing the exec bit) must change the package hash")

	// Adding or removing a file changes the hash.
	fileAdded := BundleSkill{Files: map[string]SkillFileMeta{
		"SKILL.md":         {SHA256: "sha256:skillmd1", Mode: "0644"},
		"scripts/run.sh":   {SHA256: "sha256:script1", Mode: "0755"},
		"assets/README.md": {SHA256: "sha256:extra", Mode: "0644"},
	}}
	assert.NotEqual(t, baseHash, fileAdded.ComputeContentHash(nil, "", ""), "adding a file must change the package hash")
}

// =============================================================================
// Bundle Tests
// =============================================================================

func TestBundle_HasMCP(t *testing.T) {
	tests := []struct {
		name   string
		bundle Bundle
		want   bool
	}{
		{
			name:   "no MCP",
			bundle: Bundle{},
			want:   false,
		},
		{
			name:   "has MCP",
			bundle: Bundle{MCP: map[string]BundleMCP{"test": {Command: "cmd"}}},
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.bundle.HasMCP())
		})
	}
}

func TestBundle_MCPCount(t *testing.T) {
	bundle := Bundle{
		MCP: map[string]BundleMCP{
			"mcp1": {Command: "cmd1"},
			"mcp2": {Command: "cmd2"},
		},
	}
	assert.Equal(t, 2, bundle.MCPCount())
}

func TestBundle_MCPNames(t *testing.T) {
	bundle := Bundle{
		MCP: map[string]BundleMCP{
			"zebra": {Command: "cmd1"},
			"alpha": {Command: "cmd2"},
		},
	}
	names := bundle.MCPNames()
	assert.Equal(t, []string{"alpha", "zebra"}, names)
}

func TestBundle_FragmentCount(t *testing.T) {
	bundle := Bundle{
		Fragments: map[string]BundleFragment{
			"frag1": {Content: "c1"},
			"frag2": {Content: "c2"},
		},
	}
	assert.Equal(t, 2, bundle.FragmentCount())
}

func TestBundle_CommandCount(t *testing.T) {
	bundle := Bundle{
		Commands: map[string]BundleCommand{
			"prompt1": {Content: "c1"},
		},
	}
	assert.Equal(t, 1, bundle.CommandCount())
}

func TestBundle_FragmentNames(t *testing.T) {
	bundle := Bundle{
		Fragments: map[string]BundleFragment{
			"zebra": {Content: "c1"},
			"alpha": {Content: "c2"},
		},
	}
	names := bundle.FragmentNames()
	assert.Equal(t, []string{"alpha", "zebra"}, names)
}

func TestBundle_PromptNames(t *testing.T) {
	bundle := Bundle{
		Commands: map[string]BundleCommand{
			"zebra": {Content: "c1"},
			"alpha": {Content: "c2"},
		},
	}
	names := bundle.PromptNames()
	assert.Equal(t, []string{"alpha", "zebra"}, names)
}

func TestFSStore_Save(t *testing.T) {
	tmpDir := t.TempDir()
	bundlePath := filepath.Join(tmpDir, "test-bundle.yaml")

	bundle := &Bundle{
		Path:    bundlePath,
		Version: "1.0",
		Fragments: map[string]BundleFragment{
			"test": {Content: "test content"},
		},
	}

	err := NewFSStore(nil, []string{tmpDir}).Save(bundle)
	require.NoError(t, err)

	// Verify file was written
	data, err := os.ReadFile(bundlePath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "version: \"1.0\"")
	assert.Contains(t, string(data), "test content")
}

func TestFSStore_Save_NoPath(t *testing.T) {
	err := NewFSStore(nil, nil).Save(&Bundle{Version: "1.0"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no path set")
}

// =============================================================================
// ParseBundle Tests
// =============================================================================

func TestParseBundle(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		check   func(t *testing.T, b *Bundle)
	}{
		{
			name: "valid bundle",
			yaml: `
version: "1.0"
tags:
  - golang
fragments:
  test-frag:
    content: |
      test content
commands:
  test-prompt:
    content: prompt content
`,
			wantErr: false,
			check: func(t *testing.T, b *Bundle) {
				assert.Equal(t, "1.0", b.Version)
				assert.Contains(t, b.Tags, "golang")
				assert.Len(t, b.Fragments, 1)
				assert.Len(t, b.Commands, 1)
			},
		},
		{
			name: "empty bundle initializes maps",
			yaml: `
version: "1.0"
`,
			wantErr: false,
			check: func(t *testing.T, b *Bundle) {
				assert.NotNil(t, b.Fragments)
				assert.NotNil(t, b.Commands)
				assert.NotNil(t, b.MCP)
			},
		},
		{
			name:    "invalid yaml",
			yaml:    `invalid: [`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bundle, err := ParseBundle([]byte(tt.yaml))
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.check != nil {
				tt.check(t, bundle)
			}
		})
	}
}

// =============================================================================
// ValidateBundleName Tests
// =============================================================================

func TestValidateBundleName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid simple", "my-bundle", false},
		{"valid with slash", "github.com/user/repo", false},
		{"empty", "", true},
		{"path traversal", "../secret", true},
		{"absolute path", "/etc/passwd", true},
		{"null byte", "bundle\x00evil", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBundleName(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// extractBundleName Tests
// =============================================================================

func TestExtractBundleName(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/path/to/my-bundle.yaml", "my-bundle"},
		{"/path/to/bundle/bundle.yaml", "bundle"},
		{"simple.yaml", "simple"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := ExtractBundleName(tt.path)
			assert.Equal(t, tt.want, got)
		})
	}
}

// =============================================================================
// ClaudeCodeConfig Tests
// =============================================================================

func TestClaudeCodeConfig_IsEnabled(t *testing.T) {
	trueBool := true
	falseBool := false

	tests := []struct {
		name   string
		config ClaudeCodeConfig
		want   bool
	}{
		{"nil enabled (default true)", ClaudeCodeConfig{}, true},
		{"explicitly enabled", ClaudeCodeConfig{Enabled: &trueBool}, true},
		{"explicitly disabled", ClaudeCodeConfig{Enabled: &falseBool}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.config.IsEnabled())
		})
	}
}

// =============================================================================
// Loader Tests
// =============================================================================

// A loader is composed of readers, and what it can see is exactly what they
// report — so the observable fact to assert is the CONTENT, never a stashed
// copy of the arguments. Asserting the arguments back would pass for a loader
// that read nothing at all, which is this project's characteristic bug.
func TestNewLoader_ReadsWhatItsReadersReport(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/bundles/kit.yaml", []byte("version: \"1.0\"\n"), 0o644))
	loader := NewLoader(NewProjectReader(fs, []string{"/bundles"}))

	infos, err := loader.List()

	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, "kit", infos[0].Name)
	assert.Equal(t, fs, loader.FS(), "the loader reads skill trees through the same fs its project reader used")
}

// A loader with no readers is empty, not broken: it reports nothing and says
// so with an error on the ask, rather than resolving against the process
// working directory.
func TestNewLoader_NoReadersSeesNothing(t *testing.T) {
	loader := NewLoader()

	infos, err := loader.List()

	require.NoError(t, err)
	assert.Empty(t, infos)
	_, lerr := loader.Load("anything")
	assert.ErrorIs(t, lerr, errs.ErrBundleNotFound)
}

func TestAntigravityConfig_IsEnabled(t *testing.T) {
	trueBool := true
	falseBool := false

	tests := []struct {
		name   string
		config AntigravityConfig
		want   bool
	}{
		{"nil enabled (default true)", AntigravityConfig{}, true},
		{"explicitly enabled", AntigravityConfig{Enabled: &trueBool}, true},
		{"explicitly disabled", AntigravityConfig{Enabled: &falseBool}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.config.IsEnabled())
		})
	}
}

func TestLoader_Find(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test bundle file
	bundlePath := filepath.Join(tmpDir, "test-bundle.yaml")
	err := os.WriteFile(bundlePath, []byte("version: 1.0"), 0644)
	require.NoError(t, err)

	// Create directory-style bundle
	dirBundle := filepath.Join(tmpDir, "dir-bundle")
	require.NoError(t, os.MkdirAll(dirBundle, 0755))
	err = os.WriteFile(filepath.Join(dirBundle, "bundle.yaml"), []byte("version: 1.0"), 0644)
	require.NoError(t, err)

	loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))

	t.Run("find file bundle", func(t *testing.T) {
		path, err := loader.Find("test-bundle")
		require.NoError(t, err)
		assert.Equal(t, bundlePath, path)
	})

	t.Run("find directory bundle", func(t *testing.T) {
		path, err := loader.Find("dir-bundle")
		require.NoError(t, err)
		assert.Contains(t, path, "bundle.yaml")
	})

	t.Run("not found", func(t *testing.T) {
		_, err := loader.Find("nonexistent")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("invalid name", func(t *testing.T) {
		_, err := loader.Find("../escape")
		assert.Error(t, err)
	})
}

func TestLoader_LoadFile(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `
version: "2.0"
description: Test bundle
fragments:
  test-frag:
    tags:
      - test
    content: |
      Fragment content
`
	bundlePath := filepath.Join(tmpDir, "test.yaml")
	err := os.WriteFile(bundlePath, []byte(bundleYAML), 0644)
	require.NoError(t, err)

	loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))
	bundle, err := loader.LoadFile(bundlePath)
	require.NoError(t, err)

	assert.Equal(t, "2.0", bundle.Version)
	assert.Equal(t, "Test bundle", bundle.Description)
	assert.Equal(t, "test", bundle.Name)
	assert.Equal(t, bundlePath, bundle.Path)
	assert.Len(t, bundle.Fragments, 1)
}

func TestLoader_Load(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `version: "1.0"`
	bundlePath := filepath.Join(tmpDir, "my-bundle.yaml")
	err := os.WriteFile(bundlePath, []byte(bundleYAML), 0644)
	require.NoError(t, err)

	loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))
	bundle, err := loader.Load("my-bundle")
	require.NoError(t, err)

	assert.Equal(t, "1.0", bundle.Version)
	assert.Equal(t, "my-bundle", bundle.Name)
}

func TestLoader_List(t *testing.T) {
	tmpDir := t.TempDir()

	// Create multiple bundles
	bundle1 := filepath.Join(tmpDir, "bundle1.yaml")
	bundle2 := filepath.Join(tmpDir, "bundle2.yaml")

	err := os.WriteFile(bundle1, []byte(`version: "1.0"
description: Bundle 1
fragments:
  frag1:
    content: c1`), 0644)
	require.NoError(t, err)

	err = os.WriteFile(bundle2, []byte(`version: "2.0"
description: Bundle 2`), 0644)
	require.NoError(t, err)

	loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))
	bundles, err := loader.List()
	require.NoError(t, err)

	assert.Len(t, bundles, 2)
	// Should be sorted by name
	assert.Equal(t, "bundle1", bundles[0].Name)
	assert.Equal(t, "bundle2", bundles[1].Name)
}

func TestLoader_ListAllFragments(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `
version: "1.0"
tags:
  - bundle-tag
fragments:
  frag1:
    tags:
      - frag-tag
    content: content 1
  frag2:
    content: content 2
`
	err := os.WriteFile(filepath.Join(tmpDir, "test.yaml"), []byte(bundleYAML), 0644)
	require.NoError(t, err)

	loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))
	infos, err := loader.ListAllFragments()
	require.NoError(t, err)

	assert.Len(t, infos, 2)

	// Find frag1
	var frag1 *ContentInfo
	for i := range infos {
		if infos[i].Name == "frag1" {
			frag1 = &infos[i]
			break
		}
	}
	require.NotNil(t, frag1)
	assert.Contains(t, frag1.Tags, "bundle-tag")
	assert.Contains(t, frag1.Tags, "frag-tag")
	assert.Equal(t, "fragment", frag1.ItemType)
}

func TestLoader_ListAllCommands(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `
version: "1.0"
commands:
  prompt1:
    content: prompt content
`
	err := os.WriteFile(filepath.Join(tmpDir, "test.yaml"), []byte(bundleYAML), 0644)
	require.NoError(t, err)

	loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))
	infos, err := loader.ListAllCommands()
	require.NoError(t, err)

	assert.Len(t, infos, 1)
	assert.Equal(t, "prompt1", infos[0].Name)
	assert.Equal(t, "command", infos[0].ItemType)
}

// =============================================================================
// GetFragment Tests
// =============================================================================
// GetFragment retrieves fragments by name, supporting both simple names
// (searched across all bundles) and qualified names (bundle#fragments/name).
// Tags are inherited from both bundle and fragment levels.

func TestLoader_GetFragment(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `
version: "1.0"
tags:
  - bundle-tag
fragments:
  my-frag:
    tags:
      - frag-tag
    content: |
      Fragment content here
    distilled: Distilled version
`
	err := os.WriteFile(filepath.Join(tmpDir, "test-bundle.yaml"), []byte(bundleYAML), 0644)
	require.NoError(t, err)

	t.Run("simple name lookup", func(t *testing.T) {
		loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))
		content, err := ungated(loader, false).GetFragment("my-frag")
		require.NoError(t, err)
		assert.Contains(t, content.Content, "Fragment content")
		assert.Contains(t, content.Tags, "bundle-tag")
		assert.Contains(t, content.Tags, "frag-tag")
	})

	t.Run("qualified name lookup", func(t *testing.T) {
		loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))
		content, err := ungated(loader, false).GetFragment("test-bundle#fragments/my-frag")
		require.NoError(t, err)
		assert.Contains(t, content.Content, "Fragment content")
	})

	t.Run("prefer distilled", func(t *testing.T) {
		loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))
		content, err := ungated(loader, true).GetFragment("my-frag")
		require.NoError(t, err)
		assert.Equal(t, "Distilled version", content.Content)
		assert.True(t, content.IsDistilled)
	})

	t.Run("not found", func(t *testing.T) {
		loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))
		_, err := ungated(loader, false).GetFragment("nonexistent")
		assert.Error(t, err)
	})

	t.Run("invalid qualified reference", func(t *testing.T) {
		loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))
		_, err := ungated(loader, false).GetFragment("test-bundle#invalid/path")
		assert.Error(t, err)
	})
}

// TestLoader_GetFragment_IsDistilledFlag verifies the IsDistilled flag follows
// the same AND logic as prompts: requires BOTH preferDistilled=true AND
// non-empty distilled content.
//
// NON-OBVIOUS: A fragment with preferDistilled=true but empty Distilled field
// returns IsDistilled=false. The flag reflects actual usage, not preference.
func TestLoader_GetFragment_IsDistilledFlag(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `
version: "1.0"
fragments:
  has-distilled:
    content: Original fragment
    distilled: Distilled fragment
  no-distilled:
    content: Original only
`
	err := os.WriteFile(filepath.Join(tmpDir, "bundle.yaml"), []byte(bundleYAML), 0644)
	require.NoError(t, err)

	tests := []struct {
		name            string
		fragName        string
		preferDistilled bool
		wantIsDistilled bool
		wantContent     string
	}{
		{
			name:            "prefer distilled with content",
			fragName:        "has-distilled",
			preferDistilled: true,
			wantIsDistilled: true,
			wantContent:     "Distilled fragment",
		},
		{
			name:            "prefer distilled without content",
			fragName:        "no-distilled",
			preferDistilled: true,
			wantIsDistilled: false,
			wantContent:     "Original only",
		},
		{
			name:            "prefer original with distilled available",
			fragName:        "has-distilled",
			preferDistilled: false,
			wantIsDistilled: false,
			wantContent:     "Original fragment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))
			content, err := ungated(loader, tt.preferDistilled).GetFragment(tt.fragName)
			require.NoError(t, err)
			assert.Equal(t, tt.wantIsDistilled, content.IsDistilled)
			assert.Equal(t, tt.wantContent, content.Content)
		})
	}
}

// =============================================================================
// GetCommand Tests
// =============================================================================
// GetCommand retrieves prompts by name with distillation preference.
// The IsDistilled flag in the result indicates whether distilled content was
// actually used - this requires BOTH preferDistilled=true AND distilled content
// to exist. This is critical for UI/logging to accurately report content source.

func TestLoader_GetCommand(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `
version: "1.0"
commands:
  my-prompt:
    content: Prompt content
    distilled: Distilled prompt
  no-distilled:
    content: Original only
`
	err := os.WriteFile(filepath.Join(tmpDir, "test-bundle.yaml"), []byte(bundleYAML), 0644)
	require.NoError(t, err)

	t.Run("simple name lookup", func(t *testing.T) {
		loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))
		content, err := ungated(loader, false).GetCommand("my-prompt")
		require.NoError(t, err)
		assert.Equal(t, "Prompt content", content.Content)
	})

	t.Run("qualified name lookup", func(t *testing.T) {
		loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))
		content, err := ungated(loader, false).GetCommand("test-bundle#commands/my-prompt")
		require.NoError(t, err)
		assert.Equal(t, "Prompt content", content.Content)
	})

	t.Run("prefer distilled", func(t *testing.T) {
		loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))
		content, err := ungated(loader, true).GetCommand("my-prompt")
		require.NoError(t, err)
		assert.Equal(t, "Distilled prompt", content.Content)
	})

	t.Run("not found", func(t *testing.T) {
		loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))
		_, err := ungated(loader, false).GetCommand("nonexistent")
		assert.Error(t, err)
	})
}

// TestLoader_GetCommand_IsDistilledFlag verifies the IsDistilled flag is set
// correctly based on the combination of preferDistilled setting AND actual
// distilled content availability.
//
// EDGE CASE: IsDistilled requires BOTH conditions to be true. If either
// preferDistilled is false OR distilled content is empty, IsDistilled must
// be false. This prevents false reporting of distilled usage.
func TestLoader_GetCommand_IsDistilledFlag(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `
version: "1.0"
commands:
  has-distilled:
    content: Original
    distilled: Distilled
  no-distilled:
    content: Original only
`
	err := os.WriteFile(filepath.Join(tmpDir, "bundle.yaml"), []byte(bundleYAML), 0644)
	require.NoError(t, err)

	tests := []struct {
		name            string
		promptName      string
		preferDistilled bool
		wantIsDistilled bool
		wantContent     string
		reason          string
	}{
		{
			name:            "prefer distilled with distilled content available",
			promptName:      "has-distilled",
			preferDistilled: true,
			wantIsDistilled: true,
			wantContent:     "Distilled",
			reason:          "Both conditions met: preference AND availability",
		},
		{
			name:            "prefer distilled but no distilled content",
			promptName:      "no-distilled",
			preferDistilled: true,
			wantIsDistilled: false,
			wantContent:     "Original only",
			reason:          "Preference set but no distilled content exists - must use original",
		},
		{
			name:            "prefer original even with distilled available",
			promptName:      "has-distilled",
			preferDistilled: false,
			wantIsDistilled: false,
			wantContent:     "Original",
			reason:          "User explicitly prefers original content",
		},
		{
			name:            "prefer original with no distilled",
			promptName:      "no-distilled",
			preferDistilled: false,
			wantIsDistilled: false,
			wantContent:     "Original only",
			reason:          "Neither preference nor availability - straightforward original",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))
			content, err := ungated(loader, tt.preferDistilled).GetCommand(tt.promptName)
			require.NoError(t, err, "prompt should be found")
			assert.Equal(t, tt.wantIsDistilled, content.IsDistilled,
				"IsDistilled mismatch: %s", tt.reason)
			assert.Equal(t, tt.wantContent, content.Content,
				"Content mismatch: %s", tt.reason)
		})
	}
}

func TestLoader_ListByTags(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `
version: "1.0"
fragments:
  golang-frag:
    tags:
      - golang
      - programming
    content: Go content
  python-frag:
    tags:
      - python
      - programming
    content: Python content
  docs-frag:
    tags:
      - documentation
    content: Docs content
`
	err := os.WriteFile(filepath.Join(tmpDir, "test.yaml"), []byte(bundleYAML), 0644)
	require.NoError(t, err)

	loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))

	t.Run("single tag", func(t *testing.T) {
		infos, err := loader.ListByTags([]string{"golang"})
		require.NoError(t, err)
		assert.Len(t, infos, 1)
		assert.Equal(t, "golang-frag", infos[0].Name)
	})

	t.Run("multiple tags (OR logic)", func(t *testing.T) {
		infos, err := loader.ListByTags([]string{"golang", "python"})
		require.NoError(t, err)
		assert.Len(t, infos, 2)
	})

	t.Run("shared tag", func(t *testing.T) {
		infos, err := loader.ListByTags([]string{"programming"})
		require.NoError(t, err)
		assert.Len(t, infos, 2)
	})

	t.Run("no matches", func(t *testing.T) {
		infos, err := loader.ListByTags([]string{"nonexistent"})
		require.NoError(t, err)
		assert.Len(t, infos, 0)
	})
}

// =============================================================================
// Edge Cases and Error Handling
// =============================================================================
// These tests verify graceful degradation under unusual conditions.
// ctxloom should be fault-tolerant - misconfiguration shouldn't crash the system.

// TestLoader_EmptySearchDirs verifies the loader handles no search directories.
// FAULT TOLERANCE: Empty config should not error, just return no bundles.
func TestLoader_EmptySearchDirs(t *testing.T) {
	loader := NewLoader(NewProjectReader(nil, []string{}))

	bundles, err := loader.List()
	require.NoError(t, err, "empty dirs should not error")
	assert.Empty(t, bundles)
}

// TestLoader_NonexistentSearchDir verifies missing directories are skipped.
// FAULT TOLERANCE: Invalid paths in config should be silently ignored.
// This enables portable configs that reference optional bundle locations.
func TestLoader_NonexistentSearchDir(t *testing.T) {
	loader := NewLoader(NewProjectReader(nil, []string{"/nonexistent/path"}))

	bundles, err := loader.List()
	require.NoError(t, err, "nonexistent dir should not error")
	assert.Empty(t, bundles)
}

// TestLoader_LoadFile_NotFound verifies proper error on missing files.
// Unlike directory searches, explicit file loads SHOULD error - the user
// specifically requested a file that doesn't exist.
func TestLoader_LoadFile_NotFound(t *testing.T) {
	loader := NewLoader(NewProjectReader(nil, []string{}))
	_, err := loader.LoadFile("/nonexistent/bundle.yaml")
	assert.Error(t, err, "explicit file load should error when not found")
}

// TestLoader_LoadFile_InvalidYAML verifies malformed bundles are rejected.
// Unlike missing files, corrupt bundles indicate a real problem that
// the user needs to fix.
func TestLoader_LoadFile_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	bundlePath := filepath.Join(tmpDir, "invalid.yaml")
	err := os.WriteFile(bundlePath, []byte("invalid: ["), 0644)
	require.NoError(t, err)

	loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))
	_, err = loader.LoadFile(bundlePath)
	assert.Error(t, err, "invalid YAML should error")
}

// TestLoader_LoadFile_Caching verifies that bundles are cached after loading.
// This optimization avoids redundant disk reads when the same bundle is
// referenced multiple times (e.g., by multiple profiles).
func TestLoader_LoadFile_Caching(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `version: "1.0"
fragments:
  test-frag:
    content: Test content`
	bundlePath := filepath.Join(tmpDir, "test.yaml")
	err := os.WriteFile(bundlePath, []byte(bundleYAML), 0644)
	require.NoError(t, err)

	loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))

	// First load
	bundle1, err := loader.LoadFile(bundlePath)
	require.NoError(t, err)
	assert.Equal(t, "1.0", bundle1.Version)

	// Modify file on disk
	modifiedYAML := `version: "2.0"
fragments:
  test-frag:
    content: Modified content`
	err = os.WriteFile(bundlePath, []byte(modifiedYAML), 0644)
	require.NoError(t, err)

	// Second load should return cached version (version 1.0)
	bundle2, err := loader.LoadFile(bundlePath)
	require.NoError(t, err)
	assert.Equal(t, "1.0", bundle2.Version, "should return cached bundle, not re-read from disk")

	// Same pointer (cached)
	assert.Same(t, bundle1, bundle2, "should return same bundle instance from cache")
}

// TestLoader_NestedBundles verifies deep directory structures are traversed.
// NON-OBVIOUS: Bundle names preserve the relative path structure.
// A bundle at vendor/github.com/user/bundle.yaml gets name "vendor/github.com/user".
// This enables namespacing and prevents collisions between remote sources.
func TestLoader_NestedBundles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create nested directory structure
	nestedDir := filepath.Join(tmpDir, "vendor", "github.com", "user")
	require.NoError(t, os.MkdirAll(nestedDir, 0755))

	bundleYAML := `version: "1.0"
fragments:
  nested-frag:
    content: Nested content`
	err := os.WriteFile(filepath.Join(nestedDir, "bundle.yaml"), []byte(bundleYAML), 0644)
	require.NoError(t, err)

	loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))
	bundles, err := loader.List()
	require.NoError(t, err)

	// Should find the nested bundle
	var found bool
	for _, b := range bundles {
		if b.Name == "vendor/github.com/user" {
			found = true
			break
		}
	}
	assert.True(t, found, "should find nested bundle with path-based name")
}

// =============================================================================
// ExpandBundleRefs Tests
// =============================================================================
//
// ExpandBundleRefs is the bridge between profile-style bundle references
// (which list bundles or cherry-picked items) and the GetFragment loading
// pipeline (which expects canonical "<bundle>#fragments/<name>" refs).
// These tests exercise each branch of the expansion grammar.

// expandRefsFixture builds an afero-backed loader with two bundles:
//
//	test/alpha — contains fragments a1, a2 and a prompt p1
//	test/beta  — contains fragments one, two
//
// One whole-bundle and one cherry-pick reference exercise both branches
// of ExpandBundleRefs.
func expandRefsFixture(t *testing.T) *Loader {
	t.Helper()
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/bundles/test", 0755))

	alpha := []byte(`version: "1.0.0"
fragments:
  a1:
    content: "ALPHA-ONE"
  a2:
    content: "ALPHA-TWO"
commands:
  p1:
    content: "ALPHA-PROMPT"
`)
	beta := []byte(`version: "1.0.0"
fragments:
  one:
    content: "BETA-ONE"
  two:
    content: "BETA-TWO"
`)
	require.NoError(t, afero.WriteFile(fs, "/bundles/test/alpha.yaml", alpha, 0644))
	require.NoError(t, afero.WriteFile(fs, "/bundles/test/beta.yaml", beta, 0644))

	return NewLoader(NewProjectReader(fs, []string{"/bundles"}))
}

func TestLoader_ExpandBundleRefs_WholeBundleExpandsAllFragmentsSorted(t *testing.T) {
	loader := expandRefsFixture(t)

	got := loader.ExpandBundleRefs([]string{"test/alpha"})

	// Whole-bundle ref returns every fragment, alphabetically sorted to keep
	// downstream context hashes stable across map-iteration randomness. Local
	// bundle names canonicalize to their ctxloom:local identity.
	assert.Equal(t, []ExpandedRef{
		{Name: "ctxloom:local@bundles/test/alpha#fragments/a1"},
		{Name: "ctxloom:local@bundles/test/alpha#fragments/a2"},
	}, got)
}

func TestLoader_ExpandBundleRefs_CanonicalHashSyntaxPassesThrough(t *testing.T) {
	loader := expandRefsFixture(t)

	got := loader.ExpandBundleRefs([]string{"test/alpha#fragments/a2"})

	assert.Equal(t, []ExpandedRef{{Name: "ctxloom:local@bundles/test/alpha#fragments/a2"}}, got)
}

func TestLoader_ExpandBundleRefs_ColonSyntaxRewrittenToHash(t *testing.T) {
	loader := expandRefsFixture(t)

	got := loader.ExpandBundleRefs([]string{"test/beta:fragments/two"})

	assert.Equal(t, []ExpandedRef{{Name: "ctxloom:local@bundles/test/beta#fragments/two"}}, got)
}

func TestLoader_ExpandBundleRefs_PromptsAndMCPRefsAreSkipped(t *testing.T) {
	loader := expandRefsFixture(t)

	got := loader.ExpandBundleRefs([]string{
		"test/alpha:commands/p1", // prompt — not a fragment
		"test/alpha:mcp",         // mcp section — not a fragment
		"test/alpha#commands/p1", // prompt via canonical syntax — also skipped
	})

	assert.Empty(t, got, "non-fragment refs must not become fragment names")
}

// Regression: a canonical URL ref's scheme colon must NOT be mistaken for the
// item selector. A URL-form cherry-pick passes through intact (the bundle name
// retains the full URL); previously IndexAny(":#") split on "https:" and dropped
// every URL cherry-pick. The cherry-pick path returns the canonical name without
// loading, so this asserts the parse independent of resolution.
func TestLoader_ExpandBundleRefs_CanonicalURLCherryPickPassesThrough(t *testing.T) {
	loader := expandRefsFixture(t)

	got := loader.ExpandBundleRefs([]string{
		"https://github.com/ctxloom/ctxloom-default@bundles/aspects#fragments/security",
	})

	assert.Equal(t, []ExpandedRef{
		{Name: "https://github.com/ctxloom/ctxloom-default@bundles/aspects#fragments/security"},
	}, got)
}

// A URL-form ref targeting prompts/mcp is still skipped (not a fragment), and the
// scheme colon doesn't cause it to be mis-parsed into a bogus fragment.
func TestLoader_ExpandBundleRefs_CanonicalURLNonFragmentSkipped(t *testing.T) {
	loader := expandRefsFixture(t)

	got := loader.ExpandBundleRefs([]string{
		"https://github.com/ctxloom/ctxloom-default@bundles/aspects#commands/p1",
	})

	assert.Empty(t, got)
}

func TestLoader_ExpandBundleRefs_MissingBundleSkippedSilently(t *testing.T) {
	loader := expandRefsFixture(t)

	got := loader.ExpandBundleRefs([]string{"test/does-not-exist", "test/alpha"})

	// The missing bundle is dropped without an error so the rest of the
	// profile still resolves — same tolerance LoadMultiple uses.
	assert.Equal(t, []ExpandedRef{
		{Name: "ctxloom:local@bundles/test/alpha#fragments/a1"},
		{Name: "ctxloom:local@bundles/test/alpha#fragments/a2"},
	}, got)
}

func TestLoader_ExpandBundleRefs_DeduplicatesAcrossRefs(t *testing.T) {
	loader := expandRefsFixture(t)

	got := loader.ExpandBundleRefs([]string{
		"test/alpha",              // expands to a1, a2
		"test/alpha#fragments/a1", // duplicate of a1
		"test/alpha:fragments/a2", // duplicate of a2 via colon syntax
	})

	// Every spelling canonicalizes to the same identity, so the duplicates
	// collapse — the dedup guarantee canonicalization exists to provide.
	assert.Equal(t, []ExpandedRef{
		{Name: "ctxloom:local@bundles/test/alpha#fragments/a1"},
		{Name: "ctxloom:local@bundles/test/alpha#fragments/a2"},
	}, got)
}

func TestLoader_ExpandBundleRefs_EmptyInputs(t *testing.T) {
	loader := expandRefsFixture(t)

	assert.Nil(t, loader.ExpandBundleRefs(nil))
	assert.Nil(t, loader.ExpandBundleRefs([]string{}))
	assert.Nil(t, loader.ExpandBundleRefs([]string{""}))
}

// A cherry-pick that pins a content version keeps the "@<commit>" on
// ExpandedRef.Version while the Name stays the version-agnostic canonical
// identity (so dedup/ordering remain version-agnostic). The cherry-pick path
// does not load the bundle, so this asserts the parse independent of resolution.
func TestLoader_ExpandBundleRefs_CherryPickVersionPreserved(t *testing.T) {
	loader := expandRefsFixture(t)

	got := loader.ExpandBundleRefs([]string{cqRef + "@deadbeef:fragments/solid"})

	assert.Equal(t, []ExpandedRef{
		{Name: cqRef + "#fragments/solid", Version: "deadbeef"},
	}, got)
}

// A whole-bundle "@<commit>" enumerates the PINNED version's fragment set (which
// may differ from the lockfile default) and stamps every item with the commit.
func TestLoader_ExpandBundleRefs_WholeBundleVersionEnumeratesPinned(t *testing.T) {
	def := &Bundle{Fragments: map[string]BundleFragment{"solid": {Content: "default body"}}}
	versions := map[string]*Bundle{
		"c1": {Fragments: map[string]BundleFragment{"alpha": {Content: "a"}, "beta": {Content: "b"}}},
	}
	l := versionedLoader(t, cqRef, def, versions, nil)

	got := l.Loader().ExpandBundleRefs([]string{cqRef + "@c1"})

	assert.Equal(t, []ExpandedRef{
		{Name: cqRef + "#fragments/alpha", Version: "c1"},
		{Name: cqRef + "#fragments/beta", Version: "c1"},
	}, got)
}

// Dedup is version-agnostic: a whole-bundle default and a cherry-pick of the
// same item at an explicit commit collapse to ONE ref, and the explicit
// "@<commit>" wins over the default version.
func TestLoader_ExpandBundleRefs_ExplicitVersionWinsOverDefault(t *testing.T) {
	def := &Bundle{Fragments: map[string]BundleFragment{"solid": {Content: "default body"}}}
	versions := map[string]*Bundle{
		"c1": {Fragments: map[string]BundleFragment{"solid": {Content: "v1 body"}}},
	}
	l := versionedLoader(t, cqRef, def, versions, nil)

	got := l.Loader().ExpandBundleRefs([]string{cqRef, cqRef + "@c1:fragments/solid"})

	assert.Equal(t, []ExpandedRef{{Name: cqRef + "#fragments/solid", Version: "c1"}}, got)
}

// A whole-bundle "@<commit>" whose version fails to fetch is dropped (fault
// tolerance) rather than aborting the expansion — the safe withhold direction.
func TestLoader_ExpandBundleRefs_WholeBundleVersionFetchFailureSkipped(t *testing.T) {
	def := &Bundle{Fragments: map[string]BundleFragment{"solid": {Content: "default body"}}}
	l := versionedLoader(t, cqRef, def, map[string]*Bundle{}, nil) // resolver errors on every commit

	assert.Empty(t, l.Loader().ExpandBundleRefs([]string{cqRef + "@missing"}))
}

// The names ExpandBundleRefs emits must load: the ctxloom:local canonical
// identity round-trips through GetFragment back to the fs bundle.
func TestLoader_GetFragment_LocalCanonicalRefRoundTrips(t *testing.T) {
	loader := expandRefsFixture(t)

	got, err := ungated(loader, false).GetFragment("ctxloom:local@bundles/test/alpha#fragments/a1")
	require.NoError(t, err)
	assert.Equal(t, "ALPHA-ONE", got.Content)
}

func TestLoader_ResolveFragmentAsk(t *testing.T) {
	loader := expandRefsFixture(t)

	// A bare unique name qualifies to its canonical pipeline form.
	assert.Equal(t, "ctxloom:local@bundles/test/alpha#fragments/a1",
		loader.ResolveFragmentAsk("a1"))
	// Qualified asks canonicalize their bundle part.
	assert.Equal(t, "ctxloom:local@bundles/test/alpha#fragments/a1",
		loader.ResolveFragmentAsk("test/alpha#fragments/a1"))
	// Unknown names pass through unchanged for the load step to report.
	assert.Equal(t, "nope", loader.ResolveFragmentAsk("nope"))
}

func TestBundleHook_ComputeContentHash(t *testing.T) {
	base := BundleHook{
		Matcher:         "Bash",
		Command:         "echo hi",
		Type:            "command",
		Prompt:          "do the thing",
		Timeout:         30,
		Async:           true,
		PreToolFallback: true,
	}
	baseHash := base.ComputeContentHash()
	assert.Regexp(t, `^sha256:[a-f0-9]{64}$`, baseHash)
	assert.Equal(t, baseHash, base.ComputeContentHash(), "deterministic across calls")

	// Operational knobs (Timeout/Async) are excluded from the executable hash.
	knobs := base
	knobs.Timeout = 99
	knobs.Async = false
	assert.Equal(t, baseHash, knobs.ComputeContentHash(), "Timeout/Async must not change the hash")

	// Each executable-surface field is part of the hash.
	for name, mut := range map[string]func(*BundleHook){
		"Matcher":         func(h *BundleHook) { h.Matcher = "Write" },
		"Command":         func(h *BundleHook) { h.Command = "rm -rf /" },
		"Type":            func(h *BundleHook) { h.Type = "prompt" },
		"Prompt":          func(h *BundleHook) { h.Prompt = "something else" },
		"PreToolFallback": func(h *BundleHook) { h.PreToolFallback = false },
	} {
		changed := base
		mut(&changed)
		assert.NotEqualf(t, baseHash, changed.ComputeContentHash(), "%s must be part of the hash", name)
	}
}

func TestBundleHooks_EntriesAndEntryByID(t *testing.T) {
	hooks := BundleHooks{
		PreTool: []BundleHook{
			{Command: "echo a", Type: "command"},
			{Command: "echo b", Type: "command"},
		},
		PostFileEdit: []BundleHook{
			{Command: "echo c", Type: "command"},
		},
	}
	entries := hooks.Entries()
	require.Len(t, entries, 3)
	// Canonical order: pre_tool before post_file_edit; index per event.
	assert.Equal(t, "pre_tool/0", entries[0].ID())
	assert.Equal(t, "pre_tool/1", entries[1].ID())
	assert.Equal(t, "post_file_edit/0", entries[2].ID())

	// Round-trip: every id resolves back to the same hook.
	for _, e := range entries {
		got, ok := hooks.EntryByID(e.ID())
		require.Truef(t, ok, "EntryByID(%q) must resolve", e.ID())
		assert.Equal(t, e.Hook.Command, got.Hook.Command)
	}

	// Fail-closed on malformed / out-of-range ids.
	for _, bad := range []string{"pre_tool", "pre_tool/9", "pre_tool/-1", "unknown/0", "pre_tool/x"} {
		_, ok := hooks.EntryByID(bad)
		assert.Falsef(t, ok, "EntryByID(%q) must report not-found", bad)
	}
}

// TestParseBundle_RejectsADocumentThatDeclaresNothing is the regression guard
// for the root cause: gopkg.in/yaml.v3 returns a nil
// error for every one of these inputs, unmarshalling each into a zero-value
// Bundle, so ParseBundle handed callers a VALID EMPTY BUNDLE for a truncated
// bundle.yaml, a zero-length companion loadout payload, or an empty remote
// blob. Nothing downstream could tell that apart from a bundle that genuinely
// ships nothing: bundles.Loader returned it as a successful load, and
// expandBundleRef only warns when Load ERRORS, so the bundle enumerated zero
// items with no diagnostic anywhere.
func TestParseBundle_RejectsADocumentThatDeclaresNothing(t *testing.T) {
	for name, doc := range map[string]string{
		"zero bytes":              "",
		"whitespace only":         "\n   \n \n",
		"comment only":            "# a bundle used to live here\n",
		"document separator only": "---\n",
		"explicit null":           "null\n",
		"empty mapping":           "{}\n",
		"metadata but no content": "name: orphan\ndescription: says nothing\n",
	} {
		t.Run(name, func(t *testing.T) {
			b, err := ParseBundle([]byte(doc))
			require.Error(t, err, "a document declaring nothing must not parse as a valid bundle")
			assert.Nil(t, b)
			assert.Contains(t, err.Error(), "empty")
		})
	}
}

// TestParseBundle_AcceptsAVersionOnlySkeleton is the other half of the rule:
// the rejection must not swallow a legitimately contentless bundle. CreateBundle
// writes exactly this -- a version and nothing else -- and publishing one to
// claim a name is deliberate authoring, not a failure. A single declared item
// with no version is equally real (an authored bundle mid-edit).
func TestParseBundle_AcceptsAVersionOnlySkeleton(t *testing.T) {
	b, err := ParseBundle([]byte("version: \"1.0.0\"\n"))
	require.NoError(t, err, "a version-only skeleton is what CreateBundle writes and must stay loadable")
	require.NotNil(t, b)
	assert.NotNil(t, b.Fragments, "the nil-map initialization must still happen")

	b, err = ParseBundle([]byte("fragments:\n  only:\n    content: hi\n"))
	require.NoError(t, err, "an item with no version declared is content, not emptiness")
	require.NotNil(t, b)
	assert.Len(t, b.Fragments, 1)
}

// TestLoader_IsDistilled_HonoursNoDistill pins that IsDistilled describes
// the BYTES that were served, so it must be derived from the same decision that
// chose them (resolveEffective's ContentForm), never re-derived from a subset of
// its terms.
//
// The `no_distill: true` item is exactly where the two predicates part company:
// resolveEffective refuses the distilled form and serves the raw content, while
// `preferDistilled && Distilled != ""` still reports true. A consumer is then
// told it received a distillation it did not receive — and IsDistilled travels
// off this package, through internal/lm/grpc, to whoever is deciding whether the
// full text is still available.
func TestLoader_IsDistilled_HonoursNoDistill(t *testing.T) {
	tmpDir := t.TempDir()
	bundleYAML := `
version: "1.0"
fragments:
  pinned-raw:
    content: Original fragment
    distilled: Distilled fragment
    no_distill: true
commands:
  pinned-raw-cmd:
    content: Original command
    distilled: Distilled command
    no_distill: true
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "bundle.yaml"), []byte(bundleYAML), 0644))

	loader := NewLoader(NewProjectReader(nil, []string{tmpDir}))

	frag, err := ungated(loader, true).GetFragment("pinned-raw")
	require.NoError(t, err)
	assert.Equal(t, "Original fragment", frag.Content, "no_distill must serve the raw content")
	assert.False(t, frag.IsDistilled, "the flag must describe the bytes actually served")

	cmd, err := ungated(loader, true).GetCommand("pinned-raw-cmd")
	require.NoError(t, err)
	assert.Equal(t, "Original command", cmd.Content, "no_distill must serve the raw content")
	assert.False(t, cmd.IsDistilled, "the flag must describe the bytes actually served")
}

// TestExpandBundleRef_TargetedSelectorGrammar pins the ':' selector
// aliases expandBundleRef actually recognises. The inline comment claimed
// "{fragments|prompts|mcp}", but the ':' marker list was rewritten to
// ":commands/" by the prompt→command item-kind rename and ":prompts/" was
// deliberately NOT carried as a shim.
//
// The consequence of the stale name is not cosmetic: an unrecognised ':'
// selector is not an error — it falls through to the whole-bundle branch, where
// the ENTIRE string is taken as a bundle name. "b:prompts/x" therefore looks up
// a bundle literally called "b:prompts/x". This test is what makes the retired
// alias visible if someone re-adds it, or renames the live one again.
func TestExpandBundleRef_TargetedSelectorGrammar(t *testing.T) {
	tmpDir := t.TempDir()
	bundleYAML := `
version: "1.0"
fragments:
  f1:
    content: one
commands:
  c1:
    content: two
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "b.yaml"), []byte(bundleYAML), 0644))
	l := NewLoader(NewProjectReader(nil, []string{tmpDir}))

	// Recognised, fragment-targeted: expands to exactly that item.
	for _, ref := range []string{"b:fragments/f1", "b#fragments/f1"} {
		got := l.expandBundleRef(ref)
		require.Len(t, got, 1, "ref %q", ref)
		assert.Equal(t, "ctxloom:local@bundles/b#fragments/f1", got[0].Name, "ref %q", ref)
	}

	// Recognised, NOT fragment-targeted: yields nothing (and must not be
	// mistaken for a whole-bundle expansion).
	for _, ref := range []string{"b:commands/c1", "b#commands/c1", "b:mcp/srv", "b#prompts/c1"} {
		assert.Empty(t, l.expandBundleRef(ref), "ref %q", ref)
	}

	// The retired ':prompts/' alias is not a selector at all. It reaches the
	// whole-bundle branch, where the whole string is the bundle name — which
	// resolves to no bundle, hence nothing.
	assert.Empty(t, l.expandBundleRef("b:prompts/c1"))

	// Whole-bundle ref still enumerates every fragment.
	whole := l.expandBundleRef("b")
	require.Len(t, whole, 1)
	assert.Equal(t, "ctxloom:local@bundles/b#fragments/f1", whole[0].Name)
}

// TestInstallation_IsNeverInTheModelFacingBytes pins the invariant the
// corrected field comments now assert. `installation:` is operator-facing setup prose
// (surfaced in review/pull/list output); it must never reach the model. The two
// field comments used to say OPPOSITE things about identically-plumbed fields —
// BundleFragment's "not sent to AI" and BundleCommand's "sent to AI" — and
// nothing executable could tell you which was right.
//
// The bytes the trust gate decides on ARE the bytes the agent sees
// (ContentPayload is the single preimage builder), so asserting the payload
// covers both questions at once. If installation prose is ever folded into
// content, this goes red.
func TestInstallation_IsNeverInTheModelFacingBytes(t *testing.T) {
	const secretish = "run: curl example.invalid/install.sh | sh"

	frag := BundleFragment{Content: "fragment body", Installation: secretish}
	cmd := BundleCommand{Content: "command body", Installation: secretish}

	for _, preferDistilled := range []bool{false, true} {
		fragPayload, _ := frag.ContentPayload(preferDistilled)
		assert.NotContains(t, string(fragPayload), secretish)
		assert.Equal(t, "fragment body", string(fragPayload))

		cmdPayload, _ := cmd.ContentPayload(preferDistilled)
		assert.NotContains(t, string(cmdPayload), secretish)
		assert.Equal(t, "command body", string(cmdPayload))
	}

	// And the loader carries it as sidecar metadata, never spliced into Content.
	tmpDir := t.TempDir()
	bundleYAML := "version: \"1.0\"\ncommands:\n  c1:\n    content: command body\n    installation: '" + secretish + "'\n"
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "b.yaml"), []byte(bundleYAML), 0644))
	lc, err := ungated(NewLoader(NewProjectReader(nil, []string{tmpDir})), false).GetCommand("c1")
	require.NoError(t, err)
	assert.Equal(t, "command body", lc.Content)
	assert.Equal(t, secretish, lc.Installation)
}

// TestBundleAccessorTier_Characterization pins the whole accessor tier,
// so a later collapse into a generic accessor is provably
// behaviour-preserving rather than hopefully so.
//
// Measured against the census: the tier was 13 methods and is now 10 —
// SkillCount, HasProfiles and AllTags have already been removed, and every
// survivor has at least one production caller, so there is
// no dead member left to delete. What IS worth locking is the only real
// behaviour any of them has: the *Names accessors return SORTED slices, which
// downstream reproducibility (command-file writes, review output) depends on
// and which a naive `for k := range m` rewrite would silently lose.
func TestBundleAccessorTier_Characterization(t *testing.T) {
	b := &Bundle{
		Fragments: map[string]BundleFragment{"zf": {}, "af": {}, "mf": {}},
		Commands:  map[string]BundleCommand{"zc": {}, "ac": {}},
		MCP:       map[string]BundleMCP{"zm": {}, "am": {}},
		Skills:    map[string]BundleSkill{"zs": {}, "as": {}},
		Profiles:  map[string]BundleProfile{"zp": {}, "ap": {}},
	}

	assert.Equal(t, 3, b.FragmentCount())
	assert.Equal(t, 2, b.CommandCount())
	assert.Equal(t, 2, b.MCPCount())
	assert.Equal(t, 2, b.ProfileCount())
	assert.True(t, b.HasMCP())

	assert.Equal(t, []string{"af", "mf", "zf"}, b.FragmentNames())
	assert.Equal(t, []string{"ac", "zc"}, b.PromptNames())
	assert.Equal(t, []string{"am", "zm"}, b.MCPNames())
	assert.Equal(t, []string{"as", "zs"}, b.SkillNames())
	assert.Equal(t, []string{"ap", "zp"}, b.ProfileNames())

	// The empty bundle: every count zero, every name list empty, HasMCP false.
	// Callers branch on these, so the zero behaviour is part of the contract.
	empty := &Bundle{}
	assert.False(t, empty.HasMCP())
	assert.Zero(t, empty.FragmentCount())
	assert.Zero(t, empty.CommandCount())
	assert.Zero(t, empty.MCPCount())
	assert.Zero(t, empty.ProfileCount())
	assert.Empty(t, empty.FragmentNames())
	assert.Empty(t, empty.PromptNames())
	assert.Empty(t, empty.MCPNames())
	assert.Empty(t, empty.SkillNames())
	assert.Empty(t, empty.ProfileNames())
}
