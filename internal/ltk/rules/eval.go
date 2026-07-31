package rules

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// Decision is the result of evaluating a script against a config.
type Decision struct {
	Allowed bool
	// RuleID is the id of the deny rule that fired, "" if none (U073-F15: this
	// used to be Rule *Rule, but the whole *Rule was read only by this
	// package's own tests, and only ever for its ID -- RuleID gives them the
	// same assertion with a far smaller, loggable value instead of a pointer
	// into the live rule table).
	RuleID  string
	Reason  string // human-facing explanation
	Suggest string // suggested replacement command, if any
	// Confirmable reports whether this denial may be lifted by repeating the
	// command (the "confirm by repeating" override). ConfirmWindowSeconds is the
	// window for doing so; ConfirmDelaySeconds is a minimum wait before the repeat
	// counts (0 = none). An inviolate rule yields Confirmable=false.
	Confirmable          bool
	ConfirmWindowSeconds int
	ConfirmDelaySeconds  int
}

// Evaluate matches every command in the script (nested included) against the
// rules. A matching allow rule clears the current command without denying it;
// if nothing denies, the command is allowed.
//
// The two orders nest, and COMMAND order is the outer one. Commands are
// visited in walk order; for each, the rule list is scanned in file order and
// the first rule that matches decides that command. The first DENY reached
// ends the whole walk. So when two different commands on one line would each
// trip a different deny rule, the denial reported is the earlier COMMAND's,
// whatever the rules' order in the file: `git push --force && rm x` reports
// the force-push rule even if the rm rule is written first. Either way the
// line is denied — what the order picks is the Reason and Suggest the operator
// actually sees. Rule order decides only WITHIN a single command, which is
// where an allow carve-out placed above a broad deny does its work.
func Evaluate(cfg *Config, script *ir.Script) Decision {
	if script == nil {
		return Decision{Allowed: true}
	}

	var decision Decision
	denied := false
	script.Walk(func(owner *ir.Script, c ir.SimpleCommand) bool {
		for i := range cfg.Rules {
			r := &cfg.Rules[i]
			if !r.isEnabled() {
				continue // `mode: disable` keeps a rule in the file but inert
			}
			if r.Match.isPathRule() {
				continue // path rules are evaluated against file edits, not commands
			}
			// Match against the shell that actually OWNS this command, not the
			// top-level script's — a nested wrapper body (e.g. a `cmd.exe /c …`
			// re-parsed inside a bash script) carries its own dialect, and a
			// `shells:` rule must be judged against that, not the outer script
			// (U072-F01/U073-F01).
			shell := owner.Shell
			// Allow rules match positionals with a strict, position-anchored
			// prefix; deny rules keep the permissive ordered-subsequence match.
			// See Match.Command ("Allow vs. deny: matching discipline").
			if !r.Match.matches(shell, c, r.action() == ActionAllow) {
				continue
			}
			if r.action() == ActionDeny {
				repeatable, window, delay := r.confirmPolicy(cfg.Defaults)
				decision = Decision{
					Allowed:              false,
					RuleID:               r.ID,
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
			RuleID:               r.ID,
			Reason:               r.Message,
			Suggest:              r.Suggest,
			Confirmable:          repeatable,
			ConfirmWindowSeconds: window,
			ConfirmDelaySeconds:  delay,
		}
	}
	return Decision{Allowed: true}
}
