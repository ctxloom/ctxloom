//go:build arch

package mcpschema

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// THE CLASS GATE: every projected input field must be READ by its handler.
//
// The defect this closes: the tool
// surface is a faithful projection of the proto contract, and NOTHING checked
// that a projected argument was ever read. A model was told `budget`,
// `constraints`, `notify_on`, `task_id`, `include_descendants`, `grace`,
// `reason` and `artifact_ids` existed, filled them in, and the handler threw
// them away behind a success receipt — the project's characteristic
// silent-no-op, hoisted to the agent control plane. The generated public
// reference published all of them as real, so four artifacts agreed and only
// the handler disagreed.
//
// The rule: for each tool, every top-level property of its INPUT schema must
// have a reader inside that tool's declared handler scope. A field that
// cannot be honoured must be deleted from the proto, not left advertised.
//
// WHAT THE GATE CANNOT REACH, and why (a silently-unreachable field in the
// gate is the same bug one layer up, so each of these fails loudly instead):
//
//   - Handler scope is DECLARED, not inferred. handlerScopes below must name
//     a scope for every binding in CoordinationBindings(); a tool with no
//     entry fails the gate rather than being skipped. Adding a tool without
//     declaring where it is served is therefore red, not silent.
//   - Reads through a helper the scope does not name are invisible. That is
//     deliberate: it forces the scope declaration to stay honest. The cost is
//     a false red, which is loud; the alternative (whole-repo name matching)
//     produces false GREENS — `GetArtifactIds` has a real reader for
//     Summary in coord/reports.go, which would have hidden F05's dead
//     PeerSendRequest.artifact_ids entirely.
//   - Open google.protobuf.Struct fields (agent_run's `input`) project as
//     `additionalProperties: true`; their KEYS are not proto fields and the
//     gate cannot enumerate them. It checks only that the Struct itself is
//     read. Struct-key contracts (input.prompt / input.workspace /
//     input.dirty_tree_handler) are covered by the coord handler tests.
//   - Nested message-typed input fields are checked recursively (see
//     nestedInputPaths); a nested leaf with no reader is red. Today no input
//     message has one, so the list is empty — the check exists so the next
//     one cannot slip in unexamined.
//   - OUTPUT schemas are out of scope: an unpopulated output field is a
//     different defect (it lies about a result rather than discarding an
//     argument). child_task_id and exited_within_grace were
//     removed by hand for that reason.
// ---------------------------------------------------------------------------

// handlerScope names where one tool's arguments are consumed. Files are
// relative to this package directory.
type handlerScope struct {
	// funcs are "<relative file>:<func name>" pairs whose bodies are scanned
	// for reads. A method's name is its bare identifier (serveStopRun).
	funcs []string
	// exempt maps a field name to the WRITTEN reason it cannot be checked
	// statically. Never add one without a reason; an empty reason fails.
	exempt map[string]string
}

var handlerScopes = map[string]handlerScope{
	ToolAgentRun: {
		funcs: []string{"../coord/runchannel.go:serveSpawnAgent"},
	},
	ToolAgentSend: {
		funcs: []string{"../coord/runchannel.go:servePeerSend"},
	},
	ToolAgentStop: {
		funcs: []string{"../coord/runchannel.go:serveStopRun"},
	},
	ToolRoster: {
		funcs: []string{"../coord/runchannel.go:serveListRuns"},
	},
	ToolAgentReport: {
		// agent_report's Summary is consumed in two places: the runner-side
		// handler (validation + plan-manifest stamping) and the coordinator's
		// journal fold.
		funcs: []string{
			"../../mcp/mcp_runner.go:reportHandler",
			"../coord/reports.go:recordSummary",
		},
	},
	ToolAgentRecv: {
		funcs: []string{"../../mcp/mcp_runner.go:recvHandler"},
	},
	ToolAgentFetchArtifact: {
		funcs: []string{"../../mcp/mcp_runner.go:fetchArtifactHandler"},
	},
}

func TestArch_MCPToolSchemas_EveryInputFieldIsReadByItsHandler(t *testing.T) {
	tools, err := Tools()
	require.NoError(t, err)
	require.NotEmpty(t, tools, "no generated tool schemas — the gate would pass vacuously")

	// Every binding must declare a scope: an undeclared tool is red, never
	// skipped.
	for _, b := range CoordinationBindings() {
		if _, ok := handlerScopes[b.Tool]; !ok {
			t.Errorf("tool %q has no handlerScope: declare where its arguments are read (or the gate silently skips it)", b.Tool)
		}
	}

	for _, tool := range tools {
		scope, ok := handlerScopes[tool.Name]
		if !ok {
			continue // already reported above
		}
		readers, err := readsInScope(scope.funcs)
		if !assert.NoErrorf(t, err, "tool %s: scan handler scope", tool.Name) {
			continue
		}
		for _, field := range inputPaths(t, tool) {
			leaf := field[strings.LastIndex(field, ".")+1:]
			if reason, exempted := scope.exempt[field]; exempted {
				assert.NotEmptyf(t, reason, "tool %s: field %q is exempted with no written reason", tool.Name, field)
				continue
			}
			assert.Truef(t, readers[leaf],
				"tool %s advertises input field %q to the model and no handler in %v reads it — "+
					"wire it up, or delete it from the proto. An argument that cannot be honoured must not be offered.",
				tool.Name, field, scope.funcs)
		}
	}
}

// inputPaths returns the tool's projected input field paths, recursing into
// nested object schemas that declare their own properties (an open
// additionalProperties Struct has none, so it yields just its own name).
func inputPaths(t *testing.T, tool ToolSpec) []string {
	t.Helper()
	var schema map[string]any
	require.NoErrorf(t, json.Unmarshal(tool.InputSchema, &schema), "tool %s: parse input schema", tool.Name)
	var out []string
	collectPaths(schema, "", &out)
	sort.Strings(out)
	return out
}

func collectPaths(schema map[string]any, prefix string, out *[]string) {
	props, _ := schema["properties"].(map[string]any)
	for name, raw := range props {
		sub, _ := raw.(map[string]any)
		path := prefix + name
		*out = append(*out, path)
		if sub == nil {
			continue
		}
		if items, ok := sub["items"].(map[string]any); ok {
			collectPaths(items, path+".", out)
			continue
		}
		collectPaths(sub, path+".", out)
	}
}

// readsInScope parses the named files and collects, for every declared
// function in scope, the set of field names it reads: a `GetFooBar()` call
// yields "foo_bar", and any string literal in the body yields itself (which
// is how synthetic inputs and Struct keys are consumed — agent_recv's `wait`
// arrives as a `json:"wait"` struct tag, not a proto accessor).
func readsInScope(specs []string) (map[string]bool, error) {
	byFile := map[string][]string{}
	for _, s := range specs {
		file, fn, ok := strings.Cut(s, ":")
		if !ok {
			return nil, fmt.Errorf("handlerScope entry %q is not \"<file>:<func>\"", s)
		}
		byFile[file] = append(byFile[file], fn)
	}
	reads := map[string]bool{}
	for file, fns := range byFile {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", file, err)
		}
		want := map[string]bool{}
		for _, fn := range fns {
			want[fn] = true
		}
		found := map[string]bool{}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil || !want[fd.Name.Name] {
				continue
			}
			found[fd.Name.Name] = true
			collectReads(fd.Body, reads)
		}
		for fn := range want {
			if !found[fn] {
				return nil, fmt.Errorf("%s: no func %q — the handlerScope declaration is stale", file, fn)
			}
		}
	}
	return reads, nil
}

func collectReads(body ast.Node, reads map[string]bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			if name, ok := strings.CutPrefix(v.Sel.Name, "Get"); ok && name != "" {
				reads[snake(name)] = true
			}
		case *ast.BasicLit:
			if v.Kind == token.STRING {
				if s, err := strconv.Unquote(v.Value); err == nil && s != "" {
					reads[s] = true
				}
			}
		case *ast.Field:
			// Struct tags: `json:"wait"` names a synthetic input field.
			if v.Tag != nil {
				if s, err := strconv.Unquote(v.Tag.Value); err == nil {
					for _, part := range strings.FieldsFunc(s, func(r rune) bool {
						return r == '"' || r == ':' || r == ' ' || r == ','
					}) {
						reads[part] = true
					}
				}
			}
		}
		return true
	})
}

// snake converts a Go accessor suffix (ArtifactIds) to its proto field name
// (artifact_ids), matching protoc-gen-go's CamelCase mapping closely enough
// for field identity: runs of capitals stay together (Sha256, Id).
func snake(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		upper := r >= 'A' && r <= 'Z'
		if upper && i > 0 {
			prevLower := runes[i-1] >= 'a' && runes[i-1] <= 'z' || runes[i-1] >= '0' && runes[i-1] <= '9'
			nextLower := i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z'
			if prevLower || nextLower {
				b.WriteByte('_')
			}
		}
		if upper {
			b.WriteRune(r - 'A' + 'a')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// TestArch_MCPToolSchemas_RosterPhaseVocabularyMatchesTheFold pins the `phase` vocabulary the
// roster's OUTPUT schema documents to the roster fold's actual states.
// The comment used to read "StatusChanged.Phase name or
// \"TERMINAL\"" — a PHASE_* enum that is never constructed anywhere — and the
// wrong vocabulary was copied verbatim into schemas/roster.json, so a
// coordinator matching the documented names never matched a single run.
//
// The source of truth is coord/folds.go's State* constants; this test reads
// them out of the source (the coord package imports this one, so it cannot be
// imported back) and requires each to appear in the projected description.
func TestArch_MCPToolSchemas_RosterPhaseVocabularyMatchesTheFold(t *testing.T) {
	states, err := rosterStateConstants("../coord/folds.go")
	require.NoError(t, err)
	require.Len(t, states, 5, "roster state constants changed — update roster.json's phase doc with them")

	tool, ok := ToolByName(ToolRoster)
	require.True(t, ok)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(tool.OutputSchema, &schema))

	runs, _ := schema["properties"].(map[string]any)["runs"].(map[string]any)
	items, _ := runs["items"].(map[string]any)
	phase, _ := items["properties"].(map[string]any)["phase"].(map[string]any)
	desc, _ := phase["description"].(string)
	require.NotEmpty(t, desc, "roster's phase field has no description")

	for _, s := range states {
		assert.Containsf(t, desc, `"`+s+`"`, "roster phase doc does not name the fold state %q", s)
	}
	assert.NotContainsf(t, desc, "StatusChanged", "roster phase doc names a vocabulary nothing produces")
}

// rosterStateConstants extracts the State* string constants from folds.go.
func rosterStateConstants(file string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
				continue
			}
			if !strings.HasPrefix(vs.Names[0].Name, "State") {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			v, err := strconv.Unquote(lit.Value)
			if err == nil {
				out = append(out, v)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}
