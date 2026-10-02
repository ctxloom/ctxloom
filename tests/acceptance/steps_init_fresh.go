//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"
)

// Steps for init.feature's one fresh-project scenario: the interview's
// questions answered over a real pty (driveFreshInitInterview).
func registerFreshInitSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^claude-code is the only engine installed, and nothing reaches the network$`, func(c context.Context) error {
		return installFreshInitEngineStub(worldFrom(c))
	})

	ctx.Step(`^Alice answers init's questions at a terminal, taking every recommendation$`, func(c context.Context) error {
		w := worldFrom(c)
		out, err := driveFreshInitInterview(w)
		w.initInterview = out
		return err
	})

	ctx.Step(`^init told her which engine it chose, "([^"]*)"$`, func(c context.Context, want string) error {
		w := worldFrom(c)
		if !strings.Contains(w.initInterview, want) {
			return fmt.Errorf("init's terminal output does not say %q:\n%s", want, w.initInterview)
		}
		return nil
	})

	ctx.Step(`^no engine ran during the interview$`, func(c context.Context) error {
		w := worldFrom(c)
		if ran, err := os.ReadFile(freshInitStubRan(w)); err == nil {
			return fmt.Errorf("the engine stub was executed (argv: %q) — the interview was supposed to launch nothing", strings.TrimSpace(string(ran)))
		}
		return nil
	})

	// The posture is read from the parsed config, under the default agent,
	// so a `permissions:` written anywhere else cannot satisfy it.
	ctx.Step(`^the default agent's headless posture is "([^"]*)"$`, func(c context.Context, want string) error {
		w := worldFrom(c)
		body, err := w.env.ReadFile(".ctxloom/config.yaml")
		if err != nil {
			return err
		}
		var cfg struct {
			DefaultAgent string `yaml:"default_agent"`
			Agents       map[string]struct {
				// One block per engine; init writes the chosen engine's.
				Permissions map[string]struct {
					Mode string `yaml:"mode"`
				} `yaml:"permissions"`
			} `yaml:"agents"`
		}
		if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
			return fmt.Errorf("parse config.yaml: %w\n%s", err, body)
		}
		agent, ok := cfg.Agents[cfg.DefaultAgent]
		if cfg.DefaultAgent == "" || !ok {
			return fmt.Errorf("config.yaml names no default agent it defines (default_agent %q):\n%s", cfg.DefaultAgent, body)
		}
		var got string
		for _, block := range agent.Permissions {
			got = block.Mode
		}
		if len(agent.Permissions) > 1 || got != want {
			return fmt.Errorf("default agent %q has permissions %v, want one engine block with mode %q:\n%s", cfg.DefaultAgent, agent.Permissions, want, body)
		}
		return nil
	})
}
