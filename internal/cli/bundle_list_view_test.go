package cli

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// =============================================================================
// bundleListRow — `bundle list`'s structured contract
// =============================================================================

// TestBundleListRow_JSONShape pins the wire shape of one `bundle list` entry.
// The key set is the contract a script reads, so it is asserted whole: a field
// added to bundles.BundleInfo must be projected here deliberately, and a
// field dropped from the row is a consumer-visible break either way.
func TestBundleListRow_JSONShape(t *testing.T) {
	info := &bundles.BundleInfo{
		Name:            "developer",
		Ref:             trust.BundleKey("local:developer"),
		Path:            "/proj/.ctxloom/content/bundles/developer.yaml",
		Version:         "1.2.0",
		Description:     "Dev context",
		Tags:            []string{"go", "review"},
		FragmentCount:   3,
		CommandCount:    2,
		MCPCount:        1,
		ProfileCount:    1,
		Held:            true,
		Retracted:       true,
		RetractedReason: "superseded by v2",
		Signer:          "alice@example.com",
	}

	b, err := json.Marshal(newBundleListRow(info))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))

	assert.ElementsMatch(t, []string{
		"name", "ref", "path", "version", "description", "tags",
		"fragment_count", "command_count", "mcp_count", "profile_count",
		"deleted", "held", "retracted", "retracted_reason",
		"signed", "signer",
	}, keysOf(got))

	assert.Equal(t, "developer", got["name"])
	assert.Equal(t, "local:developer", got["ref"])
	assert.Equal(t, "/proj/.ctxloom/content/bundles/developer.yaml", got["path"])
	assert.Equal(t, "1.2.0", got["version"])
	assert.Equal(t, "Dev context", got["description"])
	assert.Equal(t, []any{"go", "review"}, got["tags"])
	assert.EqualValues(t, 3, got["fragment_count"])
	assert.EqualValues(t, 2, got["command_count"])
	assert.EqualValues(t, 1, got["mcp_count"])
	assert.EqualValues(t, 1, got["profile_count"])
	assert.Equal(t, false, got["deleted"])
	assert.Equal(t, true, got["held"])
	assert.Equal(t, true, got["retracted"])
	assert.Equal(t, "superseded by v2", got["retracted_reason"])
	assert.Equal(t, true, got["signed"])
	assert.Equal(t, "alice@example.com", got["signer"])
}

// TestBundleListRow_UnsignedMinimalEntry: the counts and state flags are
// ALWAYS present — a script asking "is this held?" must read false, not a
// missing key — while the optional prose (description, tags, reason, signer)
// is omitted when empty. `signed` is derived from Signer: "" is unsigned.
func TestBundleListRow_UnsignedMinimalEntry(t *testing.T) {
	info := &bundles.BundleInfo{Name: "bare", Ref: trust.BundleKey("local:bare"), Path: "/p/bare.yaml"}

	b, err := json.Marshal(newBundleListRow(info))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))

	assert.ElementsMatch(t, []string{
		"name", "ref", "path",
		"fragment_count", "command_count", "mcp_count", "profile_count",
		"deleted", "held", "retracted", "signed",
	}, keysOf(got))
	assert.EqualValues(t, 0, got["fragment_count"])
	assert.Equal(t, false, got["held"])
	assert.Equal(t, false, got["signed"])
}

// TestBundleListRow_DeletedUpstreamEntry: a removed-upstream entry carries
// only its name and the deleted flag (there is no content left to describe),
// and the listing must say so rather than render it as an empty live bundle.
func TestBundleListRow_DeletedUpstreamEntry(t *testing.T) {
	row := newBundleListRow(&bundles.BundleInfo{Name: "gone", Deleted: true})
	assert.True(t, row.Deleted)
	assert.Equal(t, "gone", row.Name)
	assert.False(t, row.Signed)
}

// TestBundleListRows_PreservesOrderAndNeverNil: the listing is a JSON array
// in the loader's order, and an empty listing encodes as [] not null — the
// acceptance suite asserts "$" is empty, and null is not an empty array.
func TestBundleListRows_PreservesOrderAndNeverNil(t *testing.T) {
	rows := newBundleListRows(nil)
	require.NotNil(t, rows)
	b, err := json.Marshal(rows)
	require.NoError(t, err)
	assert.Equal(t, "[]", string(b))

	rows = newBundleListRows([]*bundles.BundleInfo{{Name: "b"}, {Name: "a"}})
	require.Len(t, rows, 2)
	assert.Equal(t, "b", rows[0].Name)
	assert.Equal(t, "a", rows[1].Name)
}

// =============================================================================
// bundleShowView — `bundle show`'s structured contract
// =============================================================================

func showViewBundle() *bundles.Bundle {
	order := 2
	b := &bundles.Bundle{
		Name:         "developer",
		Path:         "/proj/.ctxloom/content/bundles/developer.yaml",
		Version:      "1.0.0",
		Author:       "alice",
		Description:  "Dev context",
		Tags:         []string{"go", "review"},
		Notes:        "Internal usage only.",
		Installation: "run make setup",
		Fragments: map[string]bundles.BundleFragment{
			"styles": {
				ItemBody: bundles.ItemBody{
					Tags:      []string{"docs"},
					Distilled: "compressed",
					Content:   "First line\nrest of the content",
				},
				Premise: "editing Go",
			},
			"plain": {
				ItemBody: bundles.ItemBody{NoDistill: true, Content: "  padded first line  \nsecond"},
			},
		},
		Commands: map[string]bundles.BundleCommand{
			"review": {
				ItemBody:    bundles.ItemBody{Tags: []string{"review"}, NoDistill: true, Content: "Body irrelevant for show"},
				Description: "Run a code review",
			},
		},
		MCP: map[string]bundles.BundleMCP{
			"fs": {
				Command:      "mcp-fs",
				Args:         []string{"--root", "/tmp"},
				Env:          map[string]string{"DEBUG": "1"},
				Notes:        "Filesystem access",
				Installation: "go install ...",
				ContentHash:  "sha256:deadbeef",
			},
		},
		Skills: map[string]bundles.BundleSkill{
			"deploy": {Path: "skills/deploy", Tags: []string{"ops"}, Notes: "needs kubectl"},
		},
		Profiles: map[string]bundles.BundleProfile{
			"dev": {Description: "the dev profile"},
			"ci":  {Description: "the ci profile"},
		},
		Hooks: bundles.BundleHooks{
			PreTool:      []bundles.BundleHook{{Command: "lint"}, {Command: "fmt", Order: &order}},
			SessionStart: []bundles.BundleHook{{Command: "hello"}},
		},
	}
	b.StampSigner("alice@example.com")
	return b
}

// TestBundleShowView_JSONShape pins `bundle show`'s wire shape: the header,
// the trust state, and one map per section keyed by item name — the
// container's STRUCTURE, which is what `show` is for.
func TestBundleShowView_JSONShape(t *testing.T) {
	b, err := json.Marshal(newBundleShowView(showViewBundle()))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))

	assert.ElementsMatch(t, []string{
		"name", "path", "version", "author", "description", "tags",
		"notes", "installation", "signed", "signer",
		"fragments", "commands", "mcp", "skills", "profiles", "hooks",
	}, keysOf(got))

	assert.Equal(t, "developer", got["name"])
	assert.Equal(t, "/proj/.ctxloom/content/bundles/developer.yaml", got["path"])
	assert.Equal(t, "1.0.0", got["version"])
	assert.Equal(t, "alice", got["author"])
	assert.Equal(t, "Dev context", got["description"])
	assert.Equal(t, []any{"go", "review"}, got["tags"])
	assert.Equal(t, "Internal usage only.", got["notes"])
	assert.Equal(t, "run make setup", got["installation"])
	assert.Equal(t, true, got["signed"])
	assert.Equal(t, "alice@example.com", got["signer"])

	fragments := got["fragments"].(map[string]any)
	require.Len(t, fragments, 2)
	styles := fragments["styles"].(map[string]any)
	assert.ElementsMatch(t, []string{"tags", "preview", "distilled", "no_distill", "premise"}, keysOf(styles))
	assert.Equal(t, []any{"docs"}, styles["tags"])
	assert.Equal(t, "First line", styles["preview"])
	assert.Equal(t, true, styles["distilled"])
	assert.Equal(t, false, styles["no_distill"])
	assert.Equal(t, "editing Go", styles["premise"])
	plain := fragments["plain"].(map[string]any)
	assert.ElementsMatch(t, []string{"preview", "distilled", "no_distill"}, keysOf(plain))
	assert.Equal(t, "padded first line", plain["preview"])
	assert.Equal(t, true, plain["no_distill"])

	commands := got["commands"].(map[string]any)
	require.Len(t, commands, 1)
	review := commands["review"].(map[string]any)
	assert.ElementsMatch(t, []string{"tags", "description", "preview", "distilled", "no_distill"}, keysOf(review))
	assert.Equal(t, "Run a code review", review["description"])
	assert.Equal(t, "Body irrelevant for show", review["preview"])
	assert.Equal(t, true, review["no_distill"])

	mcp := got["mcp"].(map[string]any)
	require.Len(t, mcp, 1)
	fs := mcp["fs"].(map[string]any)
	assert.ElementsMatch(t, []string{"command", "args", "env", "notes", "installation"}, keysOf(fs))
	assert.Equal(t, "mcp-fs", fs["command"])
	assert.Equal(t, []any{"--root", "/tmp"}, fs["args"])
	assert.Equal(t, map[string]any{"DEBUG": "1"}, fs["env"])
	assert.Equal(t, "Filesystem access", fs["notes"])
	assert.Equal(t, "go install ...", fs["installation"])

	skills := got["skills"].(map[string]any)
	require.Len(t, skills, 1)
	deploy := skills["deploy"].(map[string]any)
	assert.ElementsMatch(t, []string{"path", "tags", "notes"}, keysOf(deploy))
	assert.Equal(t, "skills/deploy", deploy["path"])

	// Profiles are listed by name, sorted: the definition is `profile show`'s.
	assert.Equal(t, []any{"ci", "dev"}, got["profiles"])
	// Hooks are listed by their trust identity "<event>/<index>", in canonical
	// event order — the id a consumer hands back to `ctxloom review`.
	assert.Equal(t, []any{"pre_tool/0", "pre_tool/1", "session_start/0"}, got["hooks"])
}

// TestBundleShowView_NeverCarriesItemBodies: `show` is the map, `view` is
// the payload — the structured form must not quietly become a way to dump a
// bundle's content. Bodies, distilled renderings and content hashes stay out.
func TestBundleShowView_NeverCarriesItemBodies(t *testing.T) {
	b, err := json.Marshal(newBundleShowView(showViewBundle()))
	require.NoError(t, err)
	s := string(b)
	assert.NotContains(t, s, "rest of the content")
	assert.NotContains(t, s, "compressed")
	assert.NotContains(t, s, "deadbeef")
	assert.NotContains(t, s, `"content"`)
	assert.NotContains(t, s, `"content_hash"`)
}

// TestBundleShowView_PreviewIsTheRenderedFirstLine: the preview is exactly
// what the text renderer prints — first line, trimmed, capped at 70 bytes.
func TestBundleShowView_PreviewIsTheRenderedFirstLine(t *testing.T) {
	long := strings.Repeat("x", 100)
	v := newBundleShowView(&bundles.Bundle{
		Fragments: map[string]bundles.BundleFragment{
			"long": {ItemBody: bundles.ItemBody{Content: long + "\nmore"}},
		},
	})
	preview := v.Fragments["long"].Preview
	assert.LessOrEqual(t, len(preview), 70)
	assert.True(t, strings.HasPrefix(preview, "xxxxxxxxxx"))
	assert.NotEqual(t, long, preview)
}

// TestBundleShowView_OmitsEmptySections: a bundle with nothing in a section
// omits that section's key, the structural analogue of the text renderer
// suppressing the "Fragments (N):" header.
func TestBundleShowView_OmitsEmptySections(t *testing.T) {
	b, err := json.Marshal(newBundleShowView(&bundles.Bundle{Name: "minimal", Path: "/p"}))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))
	assert.ElementsMatch(t, []string{"name", "path", "signed"}, keysOf(got))
	assert.Equal(t, false, got["signed"])
}

// =============================================================================
// The contract is CLI-owned: no on-disk schema type reaches emit()
// =============================================================================

// TestBundleViews_CarryNoSchemaTypes is the static guard for the ruling
// that `bundle list`/`bundle show` publish a CLI-owned struct, not the YAML
// schema: every field of every view type, recursively, is json-tagged, is
// never yaml-tagged, and is never a type from the bundles package. Adding a
// `bundles.BundleFragment` field to a view would compile — this is what
// makes it fail.
func TestBundleViews_CarryNoSchemaTypes(t *testing.T) {
	roots := []reflect.Type{
		reflect.TypeOf(bundleListRow{}),
		reflect.TypeOf(bundleShowView{}),
	}
	seen := map[reflect.Type]bool{}
	var walk func(rt reflect.Type)
	walk = func(rt reflect.Type) {
		for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Map {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || seen[rt] {
			return
		}
		seen[rt] = true
		assert.NotContains(t, rt.PkgPath(), "/internal/bundles", "view type %s is a bundles schema type", rt)
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			_, hasYAML := f.Tag.Lookup("yaml")
			assert.False(t, hasYAML, "%s.%s carries a yaml tag; views are json-only", rt, f.Name)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			assert.NotEmpty(t, name, "%s.%s has no json name", rt, f.Name)
			walk(f.Type)
		}
	}
	for _, rt := range roots {
		walk(rt)
	}
}

// =============================================================================
// The commands actually hand emit() the views
// =============================================================================

// TestBundleListAndShow_FormatJSON_EmitTheViews drives the real RunE bodies
// against a project fixture and reads the payload back: the entry a script
// sees is the CLI-owned row (snake_case keys), not bundles.BundleInfo /
// bundles.Bundle. The view-shape tests above cannot catch a command that
// builds the view and then passes the schema type to emit() anyway — this
// is the test that does.
func TestBundleListAndShow_FormatJSON_EmitTheViews(t *testing.T) {
	testsupport.ProjectDir(t)
	config.Invalidate()
	t.Cleanup(config.Invalidate)

	create, _ := formatCmd("text")
	create.SetContext(context.Background())
	require.NoError(t, runBundleCreate(create, []string{"demo"}))

	t.Run("bundle list", func(t *testing.T) {
		cmd, out := formatCmd("json")
		cmd.SetContext(context.Background())
		require.NoError(t, runBundleList(cmd, nil))

		var entries []map[string]any
		require.NoError(t, json.Unmarshal(out.Bytes(), &entries), out.String())
		var demo map[string]any
		for _, e := range entries {
			if e["name"] == "demo" {
				demo = e
			}
		}
		require.NotNil(t, demo, "the created bundle must be listed under its name key; got %s", out.String())
		assert.Subset(t, keysOf(demo), []string{"name", "ref", "path", "fragment_count", "signed"})
		assert.NotContains(t, keysOf(demo), "Name", "the schema type's field names must not be the contract")
		assert.NotContains(t, keysOf(demo), "FragmentCount")
	})

	t.Run("bundle show", func(t *testing.T) {
		cmd, out := formatCmd("json")
		cmd.SetContext(context.Background())
		require.NoError(t, runBundleShow(cmd, []string{"demo"}))

		var got map[string]any
		require.NoError(t, json.Unmarshal(out.Bytes(), &got), out.String())
		assert.Equal(t, "demo", got["name"])
		assert.Contains(t, keysOf(got), "signed")
		assert.NotContains(t, keysOf(got), "Name", "the schema type's field names must not be the contract")
		assert.NotContains(t, keysOf(got), "Fragments")
		assert.NotContains(t, out.String(), `"content"`, "show never carries an item's body")
	})
}
