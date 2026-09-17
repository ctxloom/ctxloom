//go:build acceptance

// Next-step capture and the task hint it feeds the distiller
// (next_step_capture.feature, distill_task_hint.feature).
//
// Every assertion here reads the CAPTURED FILE or the distiller's OWN RECORD
// of the prompt it received, never the hook's exit status or the command's
// report of what it did. `hook next-step` exits 0 on every failure it can
// foresee, so a capture that wrote nothing is indistinguishable from one that
// worked until something reads the payload back.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// nextStepState carries what a scenario generated or observed so a later step
// can compare against it rather than against a constant re-typed in the
// feature: the over-bound closing text (too long to spell in Gherkin) and the
// hint-free distillation prompt the hinted one is compared with.
type nextStepState struct {
	longClosingText string
	hintFreeRecord  string
}

func nextStepOf(w *World) *nextStepState {
	if w.nextStep == nil {
		w.nextStep = &nextStepState{}
	}
	return w.nextStep
}

// longTextTailSentinel ends the over-bound closing text. Its ABSENCE from the
// captured file is what proves truncation actually dropped the tail, rather
// than the file merely being shorter than some other text.
const longTextTailSentinel = "LONG-TEXT-TAIL-THAT-MUST-NOT-SURVIVE"

// sessionLogOpenLine is the line Compactor.runDistill opens the transcript
// payload with. Splitting the recorded prompt at its LAST occurrence
// separates the INSTRUCTIONS (where a task hint lands) from the session log
// (which a hint must leave untouched). Last, not first: the instructions
// themselves mention the tag in prose when telling the model what the
// payload is. Renaming the tag breaks this split loudly — the step refuses a
// prompt it cannot find the line in.
const sessionLogOpenLine = "\n<session_log>\n"

// mockRecordPromptHeader precedes the prompt in the mock backend's record
// file (backends.writeMockRecord). Only the prompt section is compared:
// the sections before it carry cwd, hostname and env, which are the same
// across two runs in one scenario but are not the claim.
const mockRecordPromptHeader = "\n=== Prompt ===\n"

// vendorTranscriptLines renders one user prompt followed by one assistant
// message in the named engine's transcript format, so the hook's engine-routed
// reader has a genuine vendor file to read rather than a canonical one.
//
// Hand-rendered rather than produced through the reader packages' own types
// so a format change surfaces as a deliberate fixture update instead of the
// fixture silently reshaping itself to match. The claude-code lines mirror the
// minimal shape internal/cli's hook tests use; the mock line is the whole of
// the mock adapter's format.
func vendorTranscriptLines(engine, closing string) (string, error) {
	text, err := json.Marshal(closing)
	if err != nil {
		return "", err
	}
	switch engine {
	case config.BackendClaudeCode:
		user := `{"type":"user","isSidechain":false,"cwd":"/repo","sessionId":"s","version":"2.1.44","message":{"role":"user","content":[{"type":"text","text":"go"}]},"uuid":"u1","timestamp":"2026-08-22T10:00:00.000Z"}`
		assistant := `{"type":"assistant","isSidechain":false,"cwd":"/repo","sessionId":"s","version":"2.1.44","message":{"model":"m","id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":` + string(text) +
			`}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}},"uuid":"a1","timestamp":"2026-08-22T10:00:02.000Z"}`
		return user + "\n" + assistant + "\n", nil
	case config.BackendMock:
		return `{"role":"user","text":"go","ts":"2026-08-22T10:00:00Z"}` + "\n" +
			`{"role":"assistant","text":` + string(text) + `,"ts":"2026-08-22T10:00:02Z"}` + "\n", nil
	default:
		return "", fmt.Errorf("no vendor transcript fixture for engine %q", engine)
	}
}

// writeIndexEntry seeds one session for harp on engine by writing its
// per-harp sidecar. The session store is the set of session directories plus
// their session.yaml (the global index.yaml is gone), so seeding the sidecar
// is what makes the session exist -- and it means a scenario can REWRITE the
// seeded state after a command has run, which an index.yaml rewrite cannot do
// once the store has migrated and retired it. withVersion seeds the pinned
// engine version the way SessionStart records it; without it the entry is the
// never-recorded state the hook must refuse to read with a guessed adapter.
func writeIndexEntry(w *World, harp, engine string, withVersion bool) error {
	version := ""
	if withVersion {
		version = j001000SeededEngineVersion(engine)
		if version == "" {
			return fmt.Errorf("no pinned engine version for %q: the fixture would seed a refusal while claiming to seed a readable session", engine)
		}
	}
	return writeSessionSidecar(w, harp, engine, version)
}

// writeSessionSidecar writes the harp's session.yaml with the fields nothing
// else on disk can supply. Every fixture that needs a session to exist goes
// through here, so the sidecar's spelling lives in one place.
func writeSessionSidecar(w *World, harp, engine, engineVersion string) error {
	body := fmt.Sprintf(
		"session_id: seeded-%s\n"+
			"backend: %s\n"+
			"project_dir: %s\n"+
			"started_at: 2026-03-14T00:00:00Z\n", harp, engine, w.env.ProjectDir)
	if engineVersion != "" {
		body += "engine_version: " + engineVersion + "\n"
	}
	return w.env.WriteHomeFile(".ctxloom/sessions/"+harp+"/"+paths.SessionSidecarFileName, body)
}

// readCapturedNextStep reads the harp's next-step file straight off disk,
// under the isolated HOME, at the path the production writer uses.
func readCapturedNextStep(w *World, harp string) (string, error) {
	body, err := w.env.ReadHomeFile(j001200HarpHome(harp) + "/" + paths.NextStepFileName)
	if err != nil {
		return "", fmt.Errorf("read the captured next step for %s: %w (hook output:\n%s)", harp, err, w.env.LastOutput())
	}
	return body, nil
}

// recordedPrompt returns the prompt section of the mock's record file.
func recordedPrompt(w *World) (string, error) {
	if w.mock == nil {
		return "", fmt.Errorf("no mock distiller configured — put `the mock distiller is configured to respond` before this step")
	}
	recorded, err := w.mock.GetRecordedInput()
	if err != nil {
		return "", fmt.Errorf("read mock recorded input: %w", err)
	}
	_, prompt, found := strings.Cut(recorded, mockRecordPromptHeader)
	if !found {
		return "", fmt.Errorf("the mock's record carries no prompt section; recorded:\n%s", recorded)
	}
	return prompt, nil
}

// splitInstructionsFromLog separates a distillation prompt into the
// instructions and the session log at the last sessionLogOpenLine.
func splitInstructionsFromLog(prompt string) (instructions, log string, err error) {
	at := strings.LastIndex(prompt, sessionLogOpenLine)
	if at < 0 {
		return "", "", fmt.Errorf("the recorded prompt carries no %q line, so the instructions cannot be told from the transcript; prompt:\n%s", sessionLogOpenLine, prompt)
	}
	return prompt[:at], prompt[at+len(sessionLogOpenLine):], nil
}

func registerNextStepCaptureSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the session index records harp "([^"]*)" on engine "([^"]*)"$`, func(c context.Context, harp, engine string) error {
		return writeIndexEntry(worldFrom(c), harp, engine, true)
	})

	ctx.Step(`^the session index records harp "([^"]*)" on engine "([^"]*)" with no engine version$`, func(c context.Context, harp, engine string) error {
		return writeIndexEntry(worldFrom(c), harp, engine, false)
	})

	ctx.Step(`^a "([^"]*)" transcript at "([^"]*)" whose turn ends with:$`, func(c context.Context, engine, rel string, doc *godog.DocString) error {
		body, err := vendorTranscriptLines(engine, doc.Content)
		if err != nil {
			return err
		}
		return worldFrom(c).env.WriteFile(rel, body)
	})

	ctx.Step(`^a "([^"]*)" transcript at "([^"]*)" whose turn ends with only whitespace$`, func(c context.Context, engine, rel string) error {
		body, err := vendorTranscriptLines(engine, " \n\t \n")
		if err != nil {
			return err
		}
		return worldFrom(c).env.WriteFile(rel, body)
	})

	// Comfortably over the bound — twice it — so the assertion below is about
	// truncation and not about an off-by-one at the edge.
	ctx.Step(`^a "([^"]*)" transcript at "([^"]*)" whose turn ends with a closing text longer than the next-step bound$`, func(c context.Context, engine, rel string) error {
		w := worldFrom(c)
		var b strings.Builder
		for b.Len() < 2*memory.MaxNextStepBytes {
			b.WriteString("Next I will paste the whole file into my reply, which is the case the bound exists for. ")
		}
		b.WriteString(longTextTailSentinel)
		nextStepOf(w).longClosingText = b.String()
		body, err := vendorTranscriptLines(engine, b.String())
		if err != nil {
			return err
		}
		return w.env.WriteFile(rel, body)
	})

	// Exact equality, not Contains: the overwrite and the refusal scenarios
	// both turn on the file holding ONE turn's text and nothing of another's.
	ctx.Step(`^the captured next step for harp "([^"]*)" is:$`, func(c context.Context, harp string, doc *godog.DocString) error {
		got, err := readCapturedNextStep(worldFrom(c), harp)
		if err != nil {
			return err
		}
		if got != doc.Content {
			return fmt.Errorf("the captured next step for %s is not the turn's closing text;\nwant:\n%s\ngot:\n%s", harp, doc.Content, got)
		}
		return nil
	})

	ctx.Step(`^the captured next step for harp "([^"]*)" is the bounded head of that closing text$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		long := nextStepOf(w).longClosingText
		if long == "" {
			return fmt.Errorf("no over-bound closing text was generated — put the `longer than the next-step bound` step before this one")
		}
		got, err := readCapturedNextStep(w, harp)
		if err != nil {
			return err
		}
		if len(got) > memory.MaxNextStepBytes {
			return fmt.Errorf("the captured next step is %d bytes, over the %d-byte bound", len(got), memory.MaxNextStepBytes)
		}
		// The head must be the SAME text, not merely something short: a hook
		// that stored a different message would also be under the bound.
		const head = 200
		if !strings.HasPrefix(got, long[:head]) {
			return fmt.Errorf("the captured next step does not begin with the turn's closing text;\nwant prefix:\n%s\ngot:\n%s", long[:head], got)
		}
		if strings.Contains(got, longTextTailSentinel) {
			return fmt.Errorf("the captured next step still carries the closing text's tail, so nothing was truncated; got %d bytes:\n%s", len(got), got)
		}
		return nil
	})

	ctx.Step(`^the distiller's recorded prompt is kept as the hint-free baseline$`, func(c context.Context) error {
		w := worldFrom(c)
		prompt, err := recordedPrompt(w)
		if err != nil {
			return err
		}
		nextStepOf(w).hintFreeRecord = prompt
		return nil
	})

	// The byte-identity arm, in the only form observable from outside: the
	// hint-free instructions are a STRICT PREFIX of the hinted ones (the hint
	// is purely additive and lands after them — a no-hint distill that
	// rendered an empty hint section would diverge from the hinted one where
	// the hint's text begins, and fail here), and the session log the two
	// runs sent is the same bytes.
	ctx.Step(`^the hinted distill sent the hint-free prompt unchanged, with the next step added after the instructions$`, func(c context.Context) error {
		w := worldFrom(c)
		bare := nextStepOf(w).hintFreeRecord
		if bare == "" {
			return fmt.Errorf("no hint-free baseline was kept — put `the distiller's recorded prompt is kept as the hint-free baseline` after the bare distill")
		}
		hinted, err := recordedPrompt(w)
		if err != nil {
			return err
		}
		bareInstr, bareLog, err := splitInstructionsFromLog(bare)
		if err != nil {
			return fmt.Errorf("hint-free run: %w", err)
		}
		hintedInstr, hintedLog, err := splitInstructionsFromLog(hinted)
		if err != nil {
			return fmt.Errorf("hinted run: %w", err)
		}
		if !strings.HasPrefix(hintedInstr, bareInstr) {
			return fmt.Errorf("the hinted distill did not send the hint-free instructions unchanged;\nhint-free instructions:\n%s\nhinted instructions:\n%s", bareInstr, hintedInstr)
		}
		if len(hintedInstr) == len(bareInstr) {
			return fmt.Errorf("the hinted distill sent the same instructions as the hint-free one, so the captured next step never reached the distiller;\ninstructions:\n%s", hintedInstr)
		}
		if bareLog != hintedLog {
			return fmt.Errorf("the two distills sent different session logs, so the prompts are not comparable;\nhint-free log:\n%s\nhinted log:\n%s", bareLog, hintedLog)
		}
		return nil
	})
}
