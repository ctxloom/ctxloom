//go:build acceptance

// The opencode TUI cell: `ctxloom run` on a `type: opencode` label launches
// opencode's own TUI on ctxloom's pty — against a SCRIPTED opencode (the
// label's binary_path), so the launch is proven without a model or the real
// CLI. The script records what it was handed (argv, whether its stdin and
// stdout are a terminal, the config dir, what it finds there) and exits; the
// record proves the interactive argv, the pty, the delivered context and the
// user's carried TUI preferences.
package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

const (
	opencodeTUIKey      = "opencode-tui-fake-endpoint-key-71d2"
	opencodeTUIGuidance = "TUI-GUIDANCE-4a9e"
	opencodeTUITheme    = `{"theme":"tui-cell-theme-8c1f"}`
	opencodeTUIFragment = "tui-guidance"
)

// opencodeTUIScript is the scripted opencode. %[1]s is the record file.
const opencodeTUIScript = `#!/bin/sh
if [ "$1" = "--version" ]; then echo 1.18.35; exit 0; fi
rec=%[1]q
in=pipe; [ -t 0 ] && in=terminal
out=pipe; [ -t 1 ] && out=terminal
{
  echo "argv: $*"
  echo "stdin: $in"
  echo "stdout: $out"
  echo "config_dir: $OPENCODE_CONFIG_DIR"
  echo "tui: $(cat "$OPENCODE_CONFIG_DIR/tui.json" 2>/dev/null)"
  echo "guidance: $(grep -o 'TUI-GUIDANCE-[A-Za-z0-9-]*' "$OPENCODE_CONFIG_DIR/AGENTS.md" 2>/dev/null | head -n 1)"
  echo "content: $OPENCODE_CONFIG_CONTENT"
  echo "bridge: $(grep -o '"[a-z-]*","--engine","opencode"' "$OPENCODE_CONFIG_DIR/plugins/ctxloom.js" 2>/dev/null | sort -u | tr '\n' ' ')"
  echo "wake: $CTXLOOM_OPENCODE_WAKE_SOCKET"
} >> "$rec"
echo OPENCODE-TUI-SCRIPTED-UP
`

func opencodeTUIRecord(w *World) string {
	return filepath.Join(w.env.Root, "scripted-opencode-tui", "record.txt")
}

func registerOpencodeTUISteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^Alice has an opencode label served by a scripted opencode TUI, and a fragment of guidance$`, func(c context.Context) error {
		w := worldFrom(c)
		dir := filepath.Dir(opencodeTUIRecord(w))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		bin := filepath.Join(dir, "opencode")
		if err := os.WriteFile(bin, []byte(fmt.Sprintf(opencodeTUIScript, opencodeTUIRecord(w))), 0o755); err != nil {
			return err
		}
		if err := w.env.InitGitRepo(); err != nil {
			return err
		}
		if err := testenv.WriteBundleTree(w.env.ProjectDir, opencodeTUIFragment, fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  %s:\n    content: %q\n", opencodeTUIFragment, opencodeTUIGuidance+": answer in one line.")); err != nil {
			return err
		}
		return w.env.WriteFile(".ctxloom/config.yaml", fmt.Sprintf(`schema_version: %d
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
      permissions:
        approver: none
  defaults:
    primary: fast
    fast: fast
`, config.CurrentConfigVersion, bin))
	})

	ctx.Step(`^Alice keeps her own opencode TUI preferences$`, func(c context.Context) error {
		return worldFrom(c).env.WriteHomeFile(".config/opencode/tui.json", opencodeTUITheme)
	})

	ctx.Step(`^Alice runs the opencode label interactively with the prompt "([^"]*)"$`, func(c context.Context, prompt string) error {
		w := worldFrom(c)
		path := filepath.Dir(opencodeTUIRecord(w)) + string(os.PathListSeparator) + os.Getenv("PATH")
		sess, err := w.env.RunPTYFrom(w.env.AppBinary, 100, 30, []string{"CTXLOOM_OPENCODE_API_KEY=" + opencodeTUIKey, "PATH=" + path},
			"run", "--llm", "qwen", "-f", opencodeTUIFragment, prompt)
		if err != nil {
			return err
		}
		defer sess.Close()
		exited, err := sess.Wait(eventBudget())
		w.docStepMaterialized = sess.Output()
		switch {
		case err != nil:
			return fmt.Errorf("`ctxloom run`: %w; output:\n%s", err, sess.Output())
		case !exited:
			return fmt.Errorf("`ctxloom run` did not exit after the scripted TUI did; output:\n%s", sess.Output())
		case sess.ExitCode() != 0:
			return fmt.Errorf("`ctxloom run` exited %d; output:\n%s", sess.ExitCode(), sess.Output())
		case !strings.Contains(sess.Output(), "OPENCODE-TUI-SCRIPTED-UP"):
			return fmt.Errorf("the scripted TUI's output never reached Alice's terminal; output:\n%s", sess.Output())
		}
		return nil
	})

	ctx.Step(`^the scripted opencode TUI ran on a terminal with the prompt as a flag, the guidance and Alice's preferences in its config dir$`, func(c context.Context) error {
		w := worldFrom(c)
		rec, f, err := opencodeTUIRecorded(w)
		if err != nil {
			return err
		}
		for _, chk := range []struct {
			ok   bool
			fail string
		}{
			{f["argv"] == "--model ctxloom/stub-model --prompt=hello tui", "argv is not the TUI's"},
			{f["stdin"] == "terminal" && f["stdout"] == "terminal", "the TUI did not run on a terminal"},
			{strings.Contains(f["config_dir"], filepath.Join(".ctxloom", "sessions")), "OPENCODE_CONFIG_DIR is not a session home under the isolated ctxloom home"},
			{f["guidance"] == opencodeTUIGuidance, "the assembled guidance is not in the AGENTS.md delivered to the config dir"},
			{f["tui"] == opencodeTUITheme, "Alice's tui.json was not carried into the config dir"},
			{strings.Contains(f["content"], `"snapshot":true`), "the TUI's snapshot default is not on"},
			{!strings.Contains(rec, opencodeTUIKey), "the endpoint key's VALUE appears in what opencode was handed outside its credential env"},
		} {
			if !chk.ok {
				return fmt.Errorf("%s; the scripted opencode recorded:\n%s", chk.fail, rec)
			}
		}
		return nil
	})

	ctx.Step(`^the bridge plugin in its config dir fires ctxloom's session-start and mail-drain for opencode, and the TUI is told its wake socket$`, func(c context.Context) error {
		w := worldFrom(c)
		rec, f, err := opencodeTUIRecorded(w)
		if err != nil {
			return err
		}
		for _, chk := range []struct {
			ok   bool
			fail string
		}{
			{strings.Contains(f["bridge"], `"session-start","--engine","opencode"`), "the bridge plugin does not fire session-start naming opencode"},
			{strings.Contains(f["bridge"], `"mail-drain","--engine","opencode"`), "the bridge plugin does not fire mail-drain naming opencode"},
			{f["wake"] != "" && f["wake"] == opencodeTUIOwnWake(f), "the TUI was not told a wake socket of its session's own"},
		} {
			if !chk.ok {
				return fmt.Errorf("%s; the scripted opencode recorded:\n%s", chk.fail, rec)
			}
		}
		return nil
	})
}

// opencodeTUIOwnWake is the wake socket the recorded session owns: wake.sock
// in the short socket root (engine.Session.SocketRoot) the product derives
// from that session's home as the engine sees it — the OPENCODE_CONFIG_DIR
// the TUI recorded. Derived by the product's own launch.SocketRoot, so the
// step cannot drift from the contract; the step runs on the runner's host
// with its ambient XDG_RUNTIME_DIR and TMPDIR, so it picks the same base. ""
// when the record names no config dir.
func opencodeTUIOwnWake(f map[string]string) string {
	if f["config_dir"] == "" {
		return ""
	}
	return filepath.Join(launch.SocketRoot(f["config_dir"]), "wake.sock")
}

// opencodeTUIRecorded is the scripted TUI's record, raw and by field.
func opencodeTUIRecorded(w *World) (string, map[string]string, error) {
	raw, err := os.ReadFile(opencodeTUIRecord(w))
	if err != nil {
		return "", nil, fmt.Errorf("the scripted opencode TUI never ran: %w", err)
	}
	rec := string(raw)
	w.docStepMaterialized = rec
	f := map[string]string{}
	for _, line := range strings.Split(rec, "\n") {
		if k, v, ok := strings.Cut(line, ": "); ok {
			f[k] = v
		}
	}
	return rec, f, nil
}
