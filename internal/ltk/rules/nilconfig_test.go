package rules

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// TestEvaluateToleratesNilConfig pins U073-F11. Both evaluators guarded their
// other argument (a nil script, an empty path) and then dereferenced cfg
// unguarded, so a nil *Config panicked on cfg.Rules. A panic on ltk's analysis
// path is worse than any rule miss: it is the guard failing on a command
// nobody wrote a rule against, and the only in-package signal is a stack
// trace. No rules is the same situation Empty() describes, so it decides the
// same way — allow.
func TestEvaluateToleratesNilConfig(t *testing.T) {
	// A non-empty script is required: Walk never calls the visitor (and so
	// never reaches cfg.Rules) for a script with no pipelines, which would make
	// this pass for the wrong reason.
	script := cmd(ir.ShellBash, "rm", "-rf", "/")
	if len(script.Commands()) == 0 {
		t.Fatal("fixture is not hostile: the script has no commands, so cfg is never dereferenced")
	}
	if d := Evaluate(nil, script); !d.Allowed {
		t.Error("Evaluate(nil, script) must allow, not deny")
	}
}

func TestEvaluatePathToleratesNilConfig(t *testing.T) {
	if d := EvaluatePath(nil, "/proj/VERSION"); !d.Allowed {
		t.Error("EvaluatePath(nil, path) must allow, not deny")
	}
}
