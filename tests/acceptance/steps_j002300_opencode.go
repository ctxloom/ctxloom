//go:build acceptance

// J002300's opencode cell: a child delegated to an agent on a `type: opencode`
// label runs through the opencode engine's own runner — delivery into its
// session home, Exec, the per-turn driver, the line codec — against a
// SCRIPTED opencode binary (the label's binary_path), so the whole tail is
// proven without a model or the real CLI. The script records what it was
// handed (argv, the env opencode reads, stdin) and answers in opencode's
// `run --format json` event shape with the guidance it finds in the
// AGENTS.md ctxloom delivered to its config dir: the child's own canonical
// transcript then shows the composed context reached opencode.
package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/core/config"
)

// j002300OpencodeKey is the endpoint key the owner exports for its opencode
// children: a fake, which must reach the scripted binary's env and never its
// argv.
const j002300OpencodeKey = "j002300-fake-endpoint-key-5be1"

// j002300OpencodeScript is the scripted opencode. %[1]s is the record file.
// It reports the pinned version (the runner's floor check probes it), and
// answers one turn: a step, the text naming the guidance its delivered
// AGENTS.md carries, a final step.
const j002300OpencodeScript = `#!/bin/sh
if [ "$1" = "--version" ]; then echo 1.18.35; exit 0; fi
rec=%[1]q
{
  echo "argv: $*"
  echo "config_dir: $OPENCODE_CONFIG_DIR"
  echo "data_home: $XDG_DATA_HOME"
  echo "test_home: $OPENCODE_TEST_HOME"
  echo "project_config_disabled: $OPENCODE_DISABLE_PROJECT_CONFIG"
  [ "$CTXLOOM_OPENCODE_API_KEY" = %[2]q ] && echo "key: exported"
  echo "content: $OPENCODE_CONFIG_CONTENT"
  echo "stdin: $(cat)"
} >> "$rec"
guidance=$(grep -o 'J002300-GUIDANCE-[A-Za-z0-9-]*' "$OPENCODE_CONFIG_DIR/AGENTS.md" 2>/dev/null | head -n 1)
printf '%%s\n' '{"type":"step_start","timestamp":1,"sessionID":"ses_j002300oc","part":{"type":"step-start"}}'
printf '{"type":"text","timestamp":2,"sessionID":"ses_j002300oc","part":{"type":"text","text":"opencode child reads %%s","time":{"start":1,"end":2}}}\n' "$guidance"
printf '%%s\n' '{"type":"step_finish","timestamp":3,"sessionID":"ses_j002300oc","part":{"type":"step-finish","reason":"stop","cost":0,"tokens":{"input":10,"output":3,"reasoning":0,"cache":{"read":0,"write":0}}}}'
`

// j002300OpencodeRecord is where the scripted opencode records its turns.
func j002300OpencodeRecord(w *World) string {
	return filepath.Join(w.env.Root, "scripted-opencode", "record.txt")
}

func registerJ002300OpencodeSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^Alice's coordinator can delegate to "([^"]*)", an agent on an opencode label served by a scripted opencode, carrying its own guidance$`,
		func(c context.Context, name string) error {
			w := worldFrom(c)
			spec := &j002300AgentSpec{
				Name: name, Profile: name + "-profile", Bundle: "bundle-" + name, Fragment: "guidance",
				Guidance: "J002300-GUIDANCE-" + strings.ToUpper(name) + "-0c3e5a: answer in one line.",
			}
			j002300Of(w).specs[name] = spec
			dir := filepath.Dir(j002300OpencodeRecord(w))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			bin := filepath.Join(dir, "opencode")
			if err := os.WriteFile(bin, []byte(fmt.Sprintf(j002300OpencodeScript, j002300OpencodeRecord(w), j002300OpencodeKey)), 0o755); err != nil {
				return err
			}
			if err := w.env.InitGitRepo(); err != nil {
				return err
			}
			if err := j002300WriteAgent(w, spec); err != nil {
				return err
			}
			cfg := fmt.Sprintf(`schema_version: %d
workspace: none
llm:
  configs:
    fast:
      type: mock
    qwen:
      type: opencode
      model: stub-model
      context_window: 32768
      binary_path: %q
      provider:
        base_url: http://127.0.0.1:9/v1
        output_limit: 1024
        header_timeout_ms: 0
      permissions:
        approver: none
  defaults:
    primary: fast
    fast: fast
agents:
  %s:
    llm: qwen
    profiles:
      - %s
%s`, config.CurrentConfigVersion, bin, spec.Name, spec.Profile, permissionsBlock("opencode", "build"))
			return w.env.WriteFile(".ctxloom/config.yaml", cfg)
		})

	ctx.Step(`^a session owner is standing with the opencode endpoint key exported$`, func(c context.Context) error {
		w := worldFrom(c)
		// The version probe finds the engine's binary on PATH; the label's
		// binary_path names the same script for the turns.
		path := filepath.Dir(j002300OpencodeRecord(w)) + string(os.PathListSeparator) + os.Getenv("PATH")
		return w.standSessionOwner(w.env.AppBinary, "CTXLOOM_OPENCODE_API_KEY="+j002300OpencodeKey, "PATH="+path)
	})

	ctx.Step(`^"([^"]*)"'s reported turn carries the guidance opencode read from its delivered context$`,
		func(c context.Context, name string) error {
			w := worldFrom(c)
			st := j002300Of(w)
			harp, ok := st.harps[name]
			if !ok {
				return fmt.Errorf("j002300: no session harp remembered for %q", name)
			}
			assistants, err := j002300TranscriptAssistantCount(w, harp, 1)
			if err != nil {
				return err
			}
			body := assistants[0].Content
			w.docStepMaterialized = fmt.Sprintf("%s — %s's first reported turn:\n  %s", j002300TranscriptPath(w, harp), name, body)
			want := "opencode child reads " + strings.SplitN(st.specs[name].Guidance, ":", 2)[0]
			if body != want {
				return fmt.Errorf("%s's reported turn is %q, want %q: the scripted opencode did not find the agent's guidance in the AGENTS.md delivered to its config dir", name, body, want)
			}
			return nil
		})

	ctx.Step(`^the scripted opencode ran the structured protocol in the session home, with the key only in its environment$`,
		func(c context.Context) error {
			w := worldFrom(c)
			raw, err := os.ReadFile(j002300OpencodeRecord(w))
			if err != nil {
				return fmt.Errorf("j002300: the scripted opencode never ran: %w", err)
			}
			rec := string(raw)
			w.docStepMaterialized = rec
			fields := map[string]string{}
			for _, line := range strings.Split(rec, "\n") {
				if k, v, ok := strings.Cut(line, ": "); ok {
					fields[k] = v
				}
			}
			wantArgv := "run --format json --thinking --model ctxloom/stub-model"
			switch {
			case fields["argv"] != wantArgv:
				return fmt.Errorf("argv = %q, want %q", fields["argv"], wantArgv)
			case strings.Contains(rec, j002300OpencodeKey):
				return fmt.Errorf("the endpoint key's VALUE appears in what opencode was handed outside its credential env:\n%s", rec)
			case fields["key"] != "exported":
				return fmt.Errorf("CTXLOOM_OPENCODE_API_KEY did not reach opencode's env:\n%s", rec)
			case !strings.Contains(fields["config_dir"], filepath.Join(".ctxloom", "sessions")):
				return fmt.Errorf("OPENCODE_CONFIG_DIR = %q, not a session home under the isolated ctxloom home", fields["config_dir"])
			case fields["data_home"] != filepath.Join(fields["config_dir"], ".xdg", "data"):
				return fmt.Errorf("XDG_DATA_HOME = %q, want the session home's merged data base", fields["data_home"])
			case fields["test_home"] != filepath.Join(fields["config_dir"], "home"):
				return fmt.Errorf("OPENCODE_TEST_HOME = %q, want a directory of the session home", fields["test_home"])
			case fields["project_config_disabled"] != "1":
				return fmt.Errorf("an untrusted repository's opencode config is not disabled:\n%s", rec)
			case !strings.Contains(fields["content"], `"baseURL":"http://127.0.0.1:9/v1"`) || !strings.Contains(fields["content"], `"headerTimeout":false`) || !strings.Contains(fields["content"], `"context":32768`):
				return fmt.Errorf("OPENCODE_CONFIG_CONTENT does not carry the label's endpoint, window and disabled header timeout:\n%s", fields["content"])
			case fields["stdin"] != "go":
				return fmt.Errorf("stdin = %q, want the prompt", fields["stdin"])
			}
			return nil
		})
}
