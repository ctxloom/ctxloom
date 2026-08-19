package archlint

import (
	"go/ast"
	"go/token"
	"strconv"

	"golang.org/x/tools/go/analysis"
)

// vocabScopes are the subtrees this rule reads: production code only, both the
// library and the binaries that drive it.
var vocabScopes = []string{"internal", "cmd"}

// vocabMinMembers is how many typed string constants a defined string type
// needs before it counts as a closed vocabulary. Two is the smallest set where
// "which one is it" is a question a consumer can get wrong.
const vocabMinMembers = 2

// vocabFact carries the closed vocabularies a package declares to the packages
// that import it: type name -> the literal values of its typed constants.
//
// Facts travel only along import edges, which is exactly the reach the RAW
// CONVERSION rule needs — a `pkg.T(s)` conversion can only be written by a
// package that imports pkg. The other two rules in this family have no such
// edge and stay in tests/arch; see VocabularyAnalyzer's doc.
type vocabFact struct {
	Vocabs map[string]map[string]bool
}

func (*vocabFact) AFact() {}

func (f *vocabFact) String() string { return "closed vocabularies" }

// VocabularyAnalyzer enforces that a closed vocabulary is consumed through its
// owner rather than re-spelled.
//
// A closed vocabulary is a small set of user-typeable string values with one
// package that owns them: a defined string type plus its typed constants. The
// owner is normally correct; what fails is ADOPTION. `pkg.T(s)` outside T's
// own package turns an arbitrary runtime string into a vocabulary value by
// assertion — whatever the owner offers to validate it was not called, so an
// unrecognized value becomes a well-typed value nobody rejected, and a user's
// typo resolves to a silently different posture instead of an error.
//
// SCOPE. This analyzer implements the RAW CONVERSION rule only. The
// vocabulary family's other two rules cannot be expressed here and remain in
// tests/arch:
//
//   - DUPLICATED MEMBERSHIP TEST compares a function's literals against EVERY
//     vocabulary in the module, including owners the function does not import.
//     A consumer that re-spells a vocabulary without importing its owner is
//     the worst case and the one with no import edge to carry a fact.
//   - PARALLEL LIST compares functions in two unrelated packages pairwise.
//     Neither imports the other; there is no owner yet, which is the finding.
//
// Narrowing either to importers only would cover less while looking
// equivalent. They are left whole rather than approximated.
var VocabularyAnalyzer = &analysis.Analyzer{
	Name: "archvocabulary",
	Doc:  "closed vocabularies must be consumed through their owner, not converted from raw strings",
	Run:  runVocabulary,
	FactTypes: []analysis.Fact{
		(*vocabFact)(nil),
	},
}

func runVocabulary(pass *analysis.Pass) (any, error) {
	if SkipPass(pass) {
		return nil, nil
	}
	dir := PkgDir(pass)
	if dir == "" || !inScopes(dir, vocabScopes) {
		return nil, nil
	}
	files := ProdFiles(pass)

	// Publish what this package owns, so importers can check against it.
	if owned := discoverVocabularies(files); len(owned) > 0 {
		pass.ExportPackageFact(&vocabFact{Vocabs: owned})
	}

	for _, file := range files {
		rel := FileRel(pass, file)
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			sym := FuncSymbol(fd)
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) != 1 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				owner, members := lookupVocabulary(pass, ident.Name, sel.Sel.Name)
				if members == nil || LocalDir(owner) == dir {
					return true
				}
				lit, isLit := StringLit(call.Args[0])
				if isLit && (lit == "" || members[lit]) {
					return true
				}
				key := rel + "#" + sym + "#" + LocalDir(owner) + "." + sel.Sel.Name
				if _, ok := vocabConversionAllowed[key]; ok {
					return true
				}
				what := "a runtime string"
				if isLit {
					what = "the literal " + strconv.Quote(lit) + ", which is not one of its members,"
				}
				pass.Reportf(call.Pos(),
					"%s converts %s into the closed vocabulary %s.%s — a conversion asserts membership "+
						"instead of checking it, so an unrecognized value becomes a well-typed value nobody "+
						"rejected. Call the owner's parser or compare against its exported constants. If this "+
						"is a deliberate, reviewed exception, add %q to vocabConversionAllowed in "+
						"internal/archlint/vocabulary.go naming the fix required to remove it.",
					sym, what, LocalDir(owner), sel.Sel.Name, key)
				return true
			})
		}
	}
	return nil, nil
}

// lookupVocabulary resolves a qualified identifier to the vocabulary its
// declaring package published, or nil when it names no vocabulary.
func lookupVocabulary(pass *analysis.Pass, pkgName, typeName string) (string, map[string]bool) {
	for _, imported := range pass.Pkg.Imports() {
		if imported.Name() != pkgName {
			continue
		}
		var fact vocabFact
		if !pass.ImportPackageFact(imported, &fact) {
			continue
		}
		if members, ok := fact.Vocabs[typeName]; ok {
			return imported.Path(), members
		}
	}
	return "", nil
}

// discoverVocabularies finds this package's closed vocabularies: any defined
// string type with vocabMinMembers or more typed string constants.
//
// Discovery rather than an enumerated list, so a vocabulary added tomorrow is
// governed the day it is declared. An enumerated gate whose coverage list can
// silently omit a member is the same defect one level up.
func discoverVocabularies(files []*ast.File) map[string]map[string]bool {
	vocabs := map[string]map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			// A grouped const block repeats its type only on the first spec,
			// so carry it forward the way the language does.
			var carried string
			for _, sp := range gd.Specs {
				vs, ok := sp.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if id, ok := vs.Type.(*ast.Ident); ok {
					carried = id.Name
				}
				if carried == "" {
					continue
				}
				for _, val := range vs.Values {
					lit, ok := StringLit(val)
					if !ok {
						continue
					}
					if vocabs[carried] == nil {
						vocabs[carried] = map[string]bool{}
					}
					vocabs[carried][lit] = true
				}
			}
		}
	}
	for name, members := range vocabs {
		if len(members) < vocabMinMembers {
			delete(vocabs, name)
		}
	}
	return vocabs
}

// vocabConversionAllowed is the ratchet baseline for the RAW CONVERSION rule:
// a durable "dir#Symbol#owner.Type" reference mapped to the fix required to
// remove the entry.

var vocabConversionAllowed = map[string]string{
	"cmd/ltk/check.go#checkFlags.run#internal/ltk/ir.Shell":       "the --shell flag value is asserted into ir.Shell; internal/ltk/ir declares the vocabulary but ships no parser for it — add one and call it here (shellenv.ShellFromPath is the nearest existing membership decision)",
	"cmd/ltk/evaluate.go#evaluateFlags.run#internal/ltk/ir.Shell": "same --shell assertion as cmd/ltk/check.go; both wait on a parser in internal/ltk/ir",

	"internal/agentcoord/coord/enginehost.go#EngineHost.startRun#internal/transcript.RawPolicy": "raw-transcript policy string asserted into the enum; internal/transcript ships no parser for RawPolicy — add one and call it",
	"internal/lm/grpc/chat.go#GRPCClient.openRecorder#internal/transcript.RawPolicy":            "same RawPolicy assertion as coord.EngineHost.startRun, reached from the wire side",

	"internal/cli/acp_run_cmd.go#warnACPSessionWorkspaceAxis#internal/lm/isolation.WorkspaceAxis": "the workspace axis has NO validator anywhere: an unrecognized --workspace value converts cleanly and then reads as the shared-checkout default, so a typo runs in the parent's live tree. Needs a ParseWorkspaceAxis beside isolation.ParseRuntimeAxis, then every one of these five sites routed through it",
	"internal/cli/run.go#runState.buildRunRequest#internal/lm/isolation.WorkspaceAxis":            "workspace axis asserted from session state; waits on the same ParseWorkspaceAxis",
	"internal/operations/delegate.go#delegatedAxes#internal/lm/isolation.WorkspaceAxis":           "workspace axis asserted from a delegation request field; waits on the same ParseWorkspaceAxis",
	"internal/operations/engine_session.go#acpWorkspaceAxis#internal/lm/isolation.WorkspaceAxis":  "workspace axis asserted from flag/agent/project layering; waits on the same ParseWorkspaceAxis",
	"internal/operations/oneshot.go#RunOneshot#internal/lm/isolation.WorkspaceAxis":               "workspace axis asserted from config; waits on the same ParseWorkspaceAxis",

	"internal/cli/run.go#runState.buildRunRequest#internal/shared/agent.RuntimeAxis":                      "agent.ParseRuntimeAxis EXISTS and errors on an unrecognized value — this asserts the string past it instead of calling it",
	"internal/operations/agents.go#validateContainerAuth#internal/shared/agent.RuntimeAxis":               "same bypass of the existing agent.ParseRuntimeAxis",
	"internal/operations/delegate.go#delegatedAxes#internal/shared/agent.RuntimeAxis":                     "same bypass of the existing agent.ParseRuntimeAxis",
	"internal/operations/oneshot.go#RunOneshot#internal/shared/agent.RuntimeAxis":                         "same bypass of the existing agent.ParseRuntimeAxis",
	"internal/operations/runtime_offer.go#RuntimeOffer.OffersContainer#internal/shared/agent.RuntimeAxis": "same bypass of the existing agent.ParseRuntimeAxis",

	"internal/lm/grpc/chat.go#chatStartFromProto#internal/shared/agent.MCPTransport":            "a proto string field asserted into the transport enum; an unknown wire value becomes a well-typed value nothing rejects",
	"internal/lm/grpc/sessionhistory.go#entryFromProto#internal/shared/agent.SessionEntryType":  "a proto string field asserted into the entry-type enum; same unchecked-wire-value shape",
	"internal/lm/grpc/sessionhistory.go#entryFromProto#internal/shared/agent.SessionSystemKind": "a proto string field asserted into the system-kind enum; same unchecked-wire-value shape",
	"internal/transcript/history.go#entriesFromRecord#internal/shared/agent.SessionEntryType":   "a stored record's string asserted into the entry-type enum; same unchecked-input shape as the grpc side",
	"internal/transcript/history.go#entriesFromRecord#internal/shared/agent.SessionSystemKind":  "a stored record's string asserted into the system-kind enum; same unchecked-input shape as the grpc side",

	"internal/operations/agents.go#SetAgent#internal/agents.DrivingMode":          "a user-set config value asserted into the driving-mode enum; internal/agents ships no parser for DrivingMode",
	"internal/operations/agents.go#validateAgentAxes#internal/agents.DrivingMode": "same DrivingMode assertion inside the routine that is supposed to be VALIDATING the axes",

	"internal/operations/countersign_records.go#countersignRecords.Approved#internal/signing.Form": "a stored record's form string asserted into signing.Form; internal/signing ships no parser for it",
	"internal/operations/review.go#reviewEnumerator.classify#internal/signing.Form":                "same signing.Form assertion from the review side",
	"internal/operations/review.go#reviewEnumerator.classify#internal/bundles.ContentForm":         "a stored string asserted into bundles.ContentForm; internal/bundles ships no parser for it",

	"internal/operations/signable.go#bundleSignable.Kind#internal/trust.ItemKind": "MINTS trust.ItemKind(\"bundle\"), a value outside the declared set — the call site's own comment records that no constant names a whole bundle. Either declare it or model a whole bundle as a different type; today the trust tier sees a kind its own vocabulary does not contain",

	"internal/taskloom/config/config.go#Config.ResolveMode#internal/shared/tasks/paths.Mode": "a config string asserted into paths.Mode; internal/shared/tasks/paths ships no parser for it",
}
