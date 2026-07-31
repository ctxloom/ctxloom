// Package pwsh implements the PowerShell frontend by deferring to PowerShell's
// own parser. We never hand-write a PowerShell grammar: we shell out to
// pwsh/powershell, run [System.Management.Automation.Language.Parser]::ParseInput
// on the command text (parsing only — the command is never executed), enumerate
// every CommandAst, and emit JSON which we map back into the Command-Graph IR.
//
// PowerShell is assumed present whenever it is the shell being guarded. If it is
// absent, Parse returns an error and the caller applies its on_parse_error
// policy (pass-through by default).
package pwsh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// parseTimeout bounds a single shell-out to the PowerShell parser.
const parseTimeout = 5 * time.Second

// errUnavailable means no PowerShell executable was found on PATH. Unexported
// (U070-F02): it was previously an exported sentinel with zero errors.Is/
// comparison sites anywhere in the repo, including tests — nothing ever
// distinguished "PowerShell missing" from any other run failure, so it
// carried no identity-comparison contract worth publishing. Its message still
// reaches the operator via the wrapped error / ParseError, which is all any
// caller has ever consumed.
var errUnavailable = errors.New("powershell executable not found")

// parseScript reads the target command from $env:LTK_SRC, parses it with the
// native parser (no execution), and writes JSON describing every command. It
// also reports the parser's errors ($e): a command PowerShell could not parse
// must surface as a parse error so the on_parse_error policy applies, instead
// of being matched against whatever partial AST the parser salvaged.
const parseScript = `
$ErrorActionPreference='Stop'
$src=$env:LTK_SRC
$t=$null;$e=$null
$ast=[System.Management.Automation.Language.Parser]::ParseInput($src,[ref]$t,[ref]$e)
$cmds=$ast.FindAll({param($n) $n -is [System.Management.Automation.Language.CommandAst]},$true)
$list=foreach($c in $cmds){
  $argv=foreach($el in $c.CommandElements){
    if($el -is [System.Management.Automation.Language.StringConstantExpressionAst]){@{k='lit';v=$el.Value}}
    elseif($el -is [System.Management.Automation.Language.CommandParameterAst]){@{k='param';v=('-'+$el.ParameterName)}}
    else{@{k='dyn';v=$el.Extent.Text}}
  }
  @{argv=@($argv)}
}
$errs=foreach($pe in $e){$pe.Message}
@{commands=@($list);hasErrors=($null -ne $e -and $e.Count -gt 0);errors=@($errs)}|ConvertTo-Json -Depth 8 -Compress
`

// Frontend lowers PowerShell into the IR.
type Frontend struct {
	// run executes the parse helper for src and returns its JSON stdout. It is a
	// field so tests can inject a fake without a real PowerShell install.
	run func(ctx context.Context, src string) ([]byte, error)
}

// New returns a PowerShell Frontend backed by the real parser.
func New() *Frontend {
	return &Frontend{run: runPowerShell}
}

// Shells reports the dialects handled by this frontend.
func (f *Frontend) Shells() []ir.Shell { return []ir.Shell{ir.ShellPwsh} }

// Parse lowers src by deferring to the PowerShell parser.
func (f *Frontend) Parse(ctx context.Context, shell ir.Shell, src string) (*ir.Script, error) {
	out, err := f.run(ctx, src)
	if err != nil {
		return &ir.Script{Shell: shell}, err
	}
	return lower(out, shell)
}

// JSON shapes emitted by parseScript.
type psElem struct {
	K string `json:"k"` // lit | param | dyn
	V string `json:"v"`
}

type psCommand struct {
	Argv []psElem `json:"argv"`
}

type psResult struct {
	Commands  []psCommand `json:"commands"`
	HasErrors bool        `json:"hasErrors"`
	Errors    []string    `json:"errors"`
}

// lower maps the PowerShell parser's JSON into the IR. Every CommandAst becomes
// a SimpleCommand; structure (pipelines, blocks, subexpressions) is flattened,
// which is sufficient because rules are matched against every command.
//
// When the parser reported errors, lower still lowers whatever commands it
// salvaged (the Frontend contract wants a non-nil Script) but returns a parse
// error alongside, so the caller's on_parse_error policy decides — matching a
// partial AST as if it were the whole command would let a mangled-but-runnable
// command slip past the rules.
func lower(data []byte, shell ir.Shell) (*ir.Script, error) {
	var r psResult
	if err := json.Unmarshal(data, &r); err != nil {
		return &ir.Script{Shell: shell}, fmt.Errorf("pwsh: decode parser output: %w", err)
	}
	script := &ir.Script{Shell: shell}
	for _, c := range r.Commands {
		sc := ir.SimpleCommand{}
		for _, el := range c.Argv {
			sc.Argv = append(sc.Argv, el.V)
		}
		script.Pipelines = append(script.Pipelines, ir.Pipeline{Commands: []ir.SimpleCommand{sc}})
	}
	if r.HasErrors {
		detail := strings.Join(r.Errors, "; ")
		if detail == "" {
			detail = "unspecified parse error"
		}
		return script, fmt.Errorf("pwsh: parse error: %s", detail)
	}
	return script, nil
}

var (
	binOnce sync.Once
	binPath string
)

// resolveBin finds pwsh (preferred) or powershell once.
func resolveBin() string {
	binOnce.Do(func() {
		for _, name := range []string{"pwsh", "powershell"} {
			if p, err := exec.LookPath(name); err == nil {
				binPath = p
				return
			}
		}
	})
	return binPath
}

func runPowerShell(ctx context.Context, src string) ([]byte, error) {
	bin := resolveBin()
	if bin == "" {
		return nil, errUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, parseTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "-NoProfile", "-NonInteractive", "-NoLogo", "-Command", parseScript)
	// The target command is passed as data via the environment and only parsed,
	// never executed.
	cmd.Env = append(os.Environ(), "LTK_SRC="+src)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, runErr(ctx, err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// runErr says WHICH failure a failed child run was. When the parse overruns
// parseTimeout the context kills the process, so cmd.Run reports only "signal:
// killed" — the same thing an OOM kill or any other external signal reports.
// An operator watching intermittent denials (or intermittent unanalyzed
// allows) then has nothing to act on, and cannot tell "PowerShell is slow
// here, raise the limit" from "something is killing the process". Wrapping the
// context's own error also makes the case testable with errors.Is instead of
// by matching on message text.
func runErr(ctx context.Context, err error, stderr string) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("pwsh parse exceeded its %s deadline and was killed (%v): %w", parseTimeout, err, context.DeadlineExceeded)
	case errors.Is(ctx.Err(), context.Canceled):
		return fmt.Errorf("pwsh parse was canceled (%v): %w", err, context.Canceled)
	}
	return fmt.Errorf("pwsh parse failed: %w: %s", err, stderr)
}
