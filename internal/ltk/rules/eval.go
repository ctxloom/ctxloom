package rules

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// Decision is the result of evaluating a script against a config.
type Decision struct {
	Allowed bool
	Rule    *Rule            // the deny rule that fired, if any
	Command ir.SimpleCommand // the command that triggered a denial
	Reason  string           // human-facing explanation
	Suggest string           // suggested replacement command, if any
	// Confirmable reports whether this denial may be lifted by repeating the
	// command (the "confirm by repeating" override). ConfirmWindowSeconds is the
	// window for doing so; ConfirmDelaySeconds is a minimum wait before the repeat
	// counts (0 = none). An inviolate rule yields Confirmable=false.
	Confirmable          bool
	ConfirmWindowSeconds int
	ConfirmDelaySeconds  int
}

// Evaluate matches every command in the script (nested included) against the
// rules in order. The first matching deny rule wins. A matching allow rule
// clears the current command without denying it; if nothing denies, the command
// is allowed.
func Evaluate(cfg *Config, script *ir.Script) Decision {
	if script == nil {
		return Decision{Allowed: true}
	}

	var decision Decision
	denied := false
	script.Walk(func(c ir.SimpleCommand) bool {
		for i := range cfg.Rules {
			r := &cfg.Rules[i]
			if !r.isEnabled() {
				continue // `mode: disable` keeps a rule in the file but inert
			}
			if r.Match.isPathRule() {
				continue // path rules are evaluated against file edits, not commands
			}
			if !r.Match.matches(script.Shell, c) {
				continue
			}
			if r.action() == ActionDeny {
				repeatable, window, delay := r.confirmPolicy(cfg.Defaults)
				decision = Decision{
					Allowed:              false,
					Rule:                 r,
					Command:              c,
					Reason:               r.Message,
					Suggest:              r.Suggest,
					Confirmable:          repeatable,
					ConfirmWindowSeconds: window,
					ConfirmDelaySeconds:  delay,
				}
				denied = true
				return false // stop the walk; first deny wins
			}
			return true // explicit allow for this command; next command
		}
		return true
	})
	if denied {
		return decision
	}
	return Decision{Allowed: true}
}

// EvaluatePath matches a file-editing tool call (Edit/Write/…) against the
// path rules in order; the first matching deny wins. Command rules are ignored
// here, just as path rules are ignored by Evaluate. mode/confirm/message/suggest
// behave exactly as for command rules.
func EvaluatePath(cfg *Config, filePath string) Decision {
	if strings.TrimSpace(filePath) == "" {
		return Decision{Allowed: true}
	}
	for i := range cfg.Rules {
		r := &cfg.Rules[i]
		if !r.isEnabled() || !r.Match.isPathRule() {
			continue
		}
		if !r.Match.matchesPath(filePath) {
			continue
		}
		if r.action() != ActionDeny {
			return Decision{Allowed: true} // explicit allow rule
		}
		repeatable, window, delay := r.confirmPolicy(cfg.Defaults)
		return Decision{
			Allowed:              false,
			Rule:                 r,
			Reason:               r.Message,
			Suggest:              r.Suggest,
			Confirmable:          repeatable,
			ConfirmWindowSeconds: window,
			ConfirmDelaySeconds:  delay,
		}
	}
	return Decision{Allowed: true}
}
