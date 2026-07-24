package mockengine

import (
	"encoding/json"
	"io"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// renderClaudeOneshot is claude's per-personality oneshot WIRE adapter. It
// renders the runtime's Outcome onto claude's oneshot format — two shapes,
// exactly as the real claude -p produces and the driver consumes
// (internal/claude/claudecode.go). It is one of two per-personality oneshot
// wire adapters (see oneshot_codex.go for codex's); the DISCOVERY walk and the
// prompt extraction are shared and L1-driven, only this wire rendering is
// claude-specific. See oneshot.go for the engine dispatch.
//
//   - Normal oneshot: the result is plain text on stdout.
//   - Under SkipSetup the driver adds `--output-format json` and then PARSES
//     stdout as {"result": ..., "modelUsage": {"<model>": {"inputTokens": N,
//     "outputTokens": N}}}, attributing the result to the model with the most
//     output tokens (parseClaudeJSONResult). So when that flag is present the
//     mock MUST emit that envelope — anything else makes the driver's json
//     decode fail on a run it thinks succeeded.
//
// The `--output-format json` signal is claude's oneshot convention, not
// something L1 tags semantically (L1 declares only that the flag exists and
// takes a string). This adapter is claude's oneshot wire adapter, so it owns
// that convention; it still reads the value off the shared ParsedArgv rather
// than re-scanning os.Args, and only fires on the exact value the driver's own
// json gate keys on.
const (
	oneshotOutputFormatFlag = "--output-format"
	oneshotJSONValue        = "json"
	oneshotModelFlag        = "--model"
	oneshotDefaultModel     = "mock-model"
)

// claudeJSONEnvelope mirrors the fields parseClaudeJSONResult reads. Extra
// fields the real CLI emits are irrelevant to the driver and omitted.
type claudeJSONEnvelope struct {
	Result     string                     `json:"result"`
	ModelUsage map[string]claudeModelToks `json:"modelUsage"`
}

type claudeModelToks struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// oneshotWantsJSON reports whether this argv asked for the JSON envelope — the
// SkipSetup path.
func oneshotWantsJSON(argv agent.ParsedArgv) bool {
	v, ok := argv.Value(oneshotOutputFormatFlag)
	return ok && v == oneshotJSONValue
}

// oneshotModel resolves the model id the envelope attributes the result to: the
// --model value when present, else a deterministic stand-in.
func oneshotModel(argv agent.ParsedArgv) string {
	if v, ok := argv.Value(oneshotModelFlag); ok && v != "" {
		return v
	}
	return oneshotDefaultModel
}

// renderClaudeOneshot writes the outcome to stdout in the shape this argv
// selected. Deterministic token counts (a stable function of the byte lengths)
// keep the envelope reproducible; with one model entry the driver's max-output
// pick is unambiguous.
func renderClaudeOneshot(w io.Writer, argv agent.ParsedArgv, promptLen int, out Outcome) error {
	if !oneshotWantsJSON(argv) {
		_, err := io.WriteString(w, out.Response)
		return err
	}
	env := claudeJSONEnvelope{
		Result: out.Response,
		ModelUsage: map[string]claudeModelToks{
			oneshotModel(argv): {
				InputTokens:  tokenEstimate(promptLen),
				OutputTokens: tokenEstimate(len(out.Response)),
			},
		},
	}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// tokenEstimate is a deterministic byte→token stand-in (~4 bytes/token, floor
// 1). Its only contract is reproducibility and being the largest for the single
// model entry, so the driver's pick is stable.
func tokenEstimate(n int) int {
	t := n / 4
	if t < 1 {
		t = 1
	}
	return t
}
