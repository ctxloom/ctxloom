//go:build acceptance

// Steps for cli/acp.feature — the per-noun spec for the editor door.
//
// Only one assertion here is not already in the generic vocabulary, and it is
// the one the noun exists for: the block a user pastes has to be JSON, and it
// has to carry EVERY entry the listing just claimed to advertise. A renderer
// that prints three entries in prose and emits one in the block is the failure
// this catches, and it satisfies both a substring check and a bare parse.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/cucumber/godog"
)

// acpEntryCount reads the count out of the listing's own header, so the
// expected number comes from what the command SAID rather than from a constant
// this test would have to keep in step with the fixture.
var acpEntryCount = regexp.MustCompile(`ACP agent-server entries \((\d+)\):`)

func registerCLIACPSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the pasteable editor block parses as JSON and names every advertised entry$`,
		func(c context.Context) error {
			w := worldFrom(c)
			out := w.env.LastOutput()
			if strings.TrimSpace(out) == "" {
				return fmt.Errorf("`acp list` printed nothing (exit %d); its whole job is to hand back a block to paste",
					w.env.LastExitCode())
			}

			m := acpEntryCount.FindStringSubmatch(out)
			if m == nil {
				return fmt.Errorf("the listing never states how many entries it advertises, so there is no claim to check "+
					"the pasteable block against. It printed:\n%s", out)
			}
			want, err := strconv.Atoi(m[1])
			if err != nil {
				return err
			}
			if want == 0 {
				return fmt.Errorf("the listing advertises zero entries, so this assertion would be trivially satisfied by " +
					"an empty JSON object — the fixture is wrong, not the product")
			}

			start := strings.Index(out, "{")
			end := strings.LastIndex(out, "}")
			if start < 0 || end <= start {
				return fmt.Errorf("the listing contains no JSON object, so there is nothing pasteable in it:\n%s", out)
			}
			var block map[string]json.RawMessage
			if err := json.Unmarshal([]byte(out[start:end+1]), &block); err != nil {
				return fmt.Errorf("the pasteable block does not parse as JSON (%v), so pasting it breaks an editor's "+
					"config:\n%s", err, out)
			}

			// The count claim and the block must agree. A listing that narrates
			// N entries and pastes fewer sends someone away with an editor
			// missing exactly the agent they went looking for.
			if len(block) != want {
				names := make([]string, 0, len(block))
				for k := range block {
					names = append(names, k)
				}
				return fmt.Errorf("the listing advertises %d entries but the pasteable block carries %d (%s); "+
					"what a user pastes must be what they were told they had:\n%s",
					want, len(block), strings.Join(names, ", "), out)
			}

			// Each entry has to carry a command for an editor to execute. A key
			// present with an empty or command-less value is an entry in name
			// only, and pastes into a binding that launches nothing.
			for name, raw := range block {
				var entry struct {
					Command string   `json:"command"`
					Args    []string `json:"args"`
				}
				if err := json.Unmarshal(raw, &entry); err != nil {
					return fmt.Errorf("entry %q is not an object an editor can read (%v):\n%s", name, err, out)
				}
				if strings.TrimSpace(entry.Command) == "" {
					return fmt.Errorf("entry %q carries no command, so pasting it binds an editor to nothing:\n%s", name, out)
				}
			}
			return nil
		})
}
