package runner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// npmShim is where npm installs an engine's .cmd shim for a Windows user
// whose name holds a space: the path CreateProcess mangles when it runs a
// batch script itself.
const npmShim = `C:\Users\Ben Abbitt\AppData\Roaming\npm\claude.cmd`

// TestBatchScript pins which engine paths run through cmd.exe: a .cmd or .bat
// extension in any case, including the trailing dots and spaces Windows
// drops from a file name, and nothing else.
func TestBatchScript(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{npmShim, true},
		{`C:\tools\engine.bat`, true},
		{`C:\TOOLS\ENGINE.CMD`, true},
		{`C:\tools\engine.Bat`, true},
		{`C:\tools\engine.cmd.`, true},
		{`C:\tools\engine.cmd `, true},
		{`C:\tools\engine.cmd. .`, true},
		{`C:\Program Files\engine\claude.exe`, false},
		{`C:\tools\engine.cmd\claude.exe`, false},
		{`C:\tools\engine`, false},
		{`C:\tools\engine.cmdx`, false},
		{`C:\tools\batcmd`, false},
		{`/usr/local/bin/claude`, false},
		{``, false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, batchScript(tc.path), "batchScript(%q)", tc.path)
	}
}

// TestBatchCommandLine pins the cmd.exe command line a batch engine runs on:
// the whole command wrapped in one more pair of quotes, so that cmd.exe's /c
// quote stripping takes those and leaves the script path's own quotes intact
// (a path with spaces then still names one file), and every argument that
// cmd.exe would otherwise read as syntax quoted, its quotes doubled and its
// percent signs broken up so no variable expands.
func TestBatchCommandLine(t *testing.T) {
	line := func(script, args string) string {
		return `cmd.exe /e:ON /v:OFF /d /c ""` + script + `"` + args + `"`
	}
	cases := []struct {
		name   string
		script string
		args   []string
		want   string
	}{
		{"path with spaces, no arguments", npmShim, nil, line(npmShim, ``)},
		{".bat path", `C:\My Tools\engine.bat`, []string{"--print"}, line(`C:\My Tools\engine.bat`, ` --print`)},
		{"plain arguments stay bare", npmShim, []string{"--model", "haiku", "-p"}, line(npmShim, ` --model haiku -p`)},
		{"a bare path argument stays bare", npmShim, []string{`C:\work\repo\.mcp.json`}, line(npmShim, ` C:\work\repo\.mcp.json`)},
		{"argument with spaces", npmShim, []string{"--name", "my session"}, line(npmShim, ` --name "my session"`)},
		{"argument path with spaces", npmShim, []string{"--settings", `C:\Users\Ben Abbitt\s.json`}, line(npmShim, ` --settings "C:\Users\Ben Abbitt\s.json"`)},
		{"empty argument survives", npmShim, []string{"--tools", ""}, line(npmShim, ` --tools ""`)},
		{"ampersand", npmShim, []string{"a&b"}, line(npmShim, ` "a&b"`)},
		{"pipe and redirects", npmShim, []string{"a|b", "<in", ">out"}, line(npmShim, ` "a|b" "<in" ">out"`)},
		{"caret and parentheses", npmShim, []string{"^x", "(y)"}, line(npmShim, ` "^x" "(y)"`)},
		{"comma, semicolon and equals", npmShim, []string{"a,b", "c;d", "e=f"}, line(npmShim, ` "a,b" "c;d" "e=f"`)},
		{"exclamation marks", npmShim, []string{"!PATH!"}, line(npmShim, ` "!PATH!"`)},
		{"percent signs never expand", npmShim, []string{"%PATH%"}, line(npmShim, ` "%%cd:~,%PATH%%cd:~,%"`)},
		{"embedded quotes are doubled", npmShim, []string{`say "hi"`}, line(npmShim, ` "say ""hi"""`)},
		{"inline JSON", npmShim, []string{"--settings", `{"a":1}`}, line(npmShim, ` --settings "{""a"":1}"`)},
		{"backslashes before a quote are doubled", npmShim, []string{`a\"b`}, line(npmShim, ` "a\\""b"`)},
		{"a trailing backslash is quoted and doubled", npmShim, []string{`C:\dir\`}, line(npmShim, ` "C:\dir\\"`)},
		{"inner backslashes stay single", npmShim, []string{`C:\a b\c`}, line(npmShim, ` "C:\a b\c"`)},
		{"a backslash run ends at any other character", npmShim, []string{`x\y"z`}, line(npmShim, ` "x\y""z"`)},
		{"a backslash run ends at a quote", npmShim, []string{`a\""b`}, line(npmShim, ` "a\\""""b"`)},
		{"a backslash run ends at a percent sign", npmShim, []string{`a\%"b`}, line(npmShim, ` "a\%%cd:~,%""b"`)},
		{"letters and digits at the ranges' ends stay bare", npmShim, []string{"09azAZ"}, line(npmShim, ` 09azAZ`)},
		{"of the characters beside those ranges, only the known-bare punctuation stays bare", npmShim, []string{"/", ":", "@", "[", "`", "{"}, line(npmShim, ` / : @ "[" "`+"`"+`" "{"`)},
		{"tab", npmShim, []string{"a\tb"}, line(npmShim, " \"a\tb\"")},
		{"non-ASCII letters stay bare", npmShim, []string{"héllo"}, line(npmShim, ` héllo`)},
		{"unicode control character", npmShim, []string{"a\u0085b"}, line(npmShim, " \"a\u0085b\"")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := batchCommandLine(tc.script, tc.args)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestBatchCommandLine_Refuses pins the launches no quoting can carry through
// cmd.exe: a line break or NUL in an argument ends the command there, and a
// script path holding a quote or ending in a backslash cannot be quoted as
// one file name.
func TestBatchCommandLine_Refuses(t *testing.T) {
	cases := []struct {
		name   string
		script string
		args   []string
		want   error
	}{
		{"line feed in an argument", npmShim, []string{"line one\nline two"}, errBatchArgument},
		{"carriage return in an argument", npmShim, []string{"a\rb"}, errBatchArgument},
		{"NUL in an argument", npmShim, []string{"a\x00b"}, errBatchArgument},
		{"quote in the script path", `C:\a"b\claude.cmd`, nil, errBatchScriptPath},
		{"script path ending in a backslash", `C:\tools\`, nil, errBatchScriptPath},
		{"NUL in the script path", "C:\\a\x00\\claude.cmd", nil, errBatchScriptPath},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := batchCommandLine(tc.script, tc.args)
			require.ErrorIs(t, err, tc.want)
		})
	}
}
