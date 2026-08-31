//go:build acceptance

package acceptance

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/config"
)

// acpLocalTerminalProjectConfig renders this feature's fixture project
// config.yaml: a "worker" agent bound to the "engine" (mock) backend as
// default, plus a "forwarding" ACP-client label whose command re-invokes
// THIS scenario's own freshly-built ctxloom binary as `acp serve` — the
// same loopback shape j002100_delegation.feature already uses for its own
// ACP-forwarding scenario (an `llm: type: acp, command: "<ctxloom> acp
// serve"` label makes `ctxloom run --llm <label>` spawn ctxloom itself as
// the ACP agent, opening a REAL second engine session with the project's
// own mock backend behind it).
//
// acpLocalTerminalHere, when true, embeds acp_local_terminal directly in
// this PROJECT file — used ONLY by the "cannot set it here" scenario, which
// expects layerscope to refuse it (ScopeMachine: a project file may not
// state a fact about this box's own filesystem).
func acpLocalTerminalProjectConfig(appBinary string, acpLocalTerminalHere bool) string {
	extra := ""
	if acpLocalTerminalHere {
		extra = "acp_local_terminal: true\n"
	}
	return fmt.Sprintf(`version: %d
default_agent: worker
%sagents:
  worker:
    llm: engine
llm:
  configs:
    engine:
      type: mock
    forwarding:
      type: acp
      command: %q
  defaults:
    primary: engine
`, config.CurrentConfigVersion, extra, appBinary+" acp serve")
}

// acpLocalTerminalHomeConfig renders this feature's fixture home
// config.yaml: the no-op editor pin every fixture needs (editor.command is
// ScopeMachine, so it must live here regardless), plus acp_local_terminal
// when the scenario wants it on — the ONLY layer this key may be set from.
func acpLocalTerminalHomeConfig(on bool) string {
	extra := ""
	if on {
		extra = "acp_local_terminal: true\n"
	}
	return fmt.Sprintf("version: %d\neditor:\n  command: \"true\"\n%s", config.CurrentConfigVersion, extra)
}

func registerCLIACPLocalTerminalSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^a project wired to drive an engine's terminal/\* calls through a scripted mock turn$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := w.env.WriteFile(".ctxloom/config.yaml", acpLocalTerminalProjectConfig(w.env.AppBinary, false)); err != nil {
			return err
		}
		return w.env.WriteHomeFile(".ctxloom/config.yaml", acpLocalTerminalHomeConfig(false))
	})

	ctx.Step(`^Alice sets acp_local_terminal in the PROJECT config instead of home$`, func(c context.Context) error {
		w := worldFrom(c)
		return w.env.WriteFile(".ctxloom/config.yaml", acpLocalTerminalProjectConfig(w.env.AppBinary, true))
	})

	ctx.Step(`^Alice turns acp_local_terminal on in her home config$`, func(c context.Context) error {
		w := worldFrom(c)
		return w.env.WriteHomeFile(".ctxloom/config.yaml", acpLocalTerminalHomeConfig(true))
	})
}
