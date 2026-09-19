package mcp

import "github.com/ctxloom/ctxloom/internal/adapters/operations"

// assembleContextOutput is what the assemble_context TOOL returns, and it is a
// deliberately different shape from operations.AssembleContextResult.
//
// The divergence IS the point. Before this type existed the domain struct WAS
// the wire contract — the tool returned it wholesale — so every field anyone
// added for an internal reason was published to every MCP consumer by
// construction, and the only thing preventing a leak was that nobody added one.
// That is not a boundary, it is a convention, and it had already failed once:
// ProfileLLM exists so a CLI launch can pick a backend and shipped to MCP
// callers who have no use for it.
//
// It nearly failed much worse. Emitting premised fragments as skills needs the
// withheld BODIES, and putting them on the result would have injected them into
// every assemble_context response — including the dynamic callers that withheld
// precisely to avoid carrying them, defeating the mechanism with its own output.
// AssembleContextResult.WithheldFragments now carries those bodies for
// in-process callers, and they cannot reach the wire because THIS type does not
// mention them. Structural, not vigilant.
//
// What a consumer needs: the assembled context, and an honest account of what
// was and was not included.
type assembleContextOutput struct {
	Profiles         []string `json:"profiles"`
	Context          string   `json:"context"`
	FragmentsLoaded  []string `json:"fragments_loaded"`
	MissingFragments []string `json:"missing_fragments,omitempty"`
	MissingTags      []string `json:"missing_tags,omitempty"`
}

// assembleContextDTO projects the domain result onto the wire.
//
// Two fields are deliberately NOT projected:
//
//   - ProfileLLM — which backend a CLI launch should pick. Internal routing; an
//     MCP caller neither chooses nor invokes a backend.
//   - PremiseIndex — the conditional-guidance menu. It rode this tool's response
//     only because the resources/list projection dropped the premise, which it
//     no longer does: ctxloom://fragments carries each fragment's premise and
//     qualified ref. MCP separates descriptors from contents, and the menu
//     belongs on the descriptor side, not bolted onto an assembly.
func assembleContextDTO(r *operations.AssembleContextResult) *assembleContextOutput {
	if r == nil {
		return nil
	}
	return &assembleContextOutput{
		Profiles:         r.Profiles,
		Context:          r.Context,
		FragmentsLoaded:  r.FragmentsLoaded,
		MissingFragments: r.MissingFragments,
		MissingTags:      r.MissingTags,
	}
}
