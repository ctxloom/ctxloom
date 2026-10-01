#!/usr/bin/env pwsh
# Reports what cross-session messaging endpoint the pinned claude CLI creates
# on this OS, and whether a post without / with the inbox auth line is taken.
# Row worried-chief, decision D4: the wake ctxloom posts to an interactive root
# coordinator rides this endpoint, which claude does not document.
#
# Needs NO credentials: claude binds its inbox during setup, before any model
# call. Two legs: one names the endpoint (--messaging-socket-path, as ctxloom
# would), one omits it to show the auto endpoint claude picks itself. stdin is
# held open (stream-json input) so the process stays up while it is listed.
#
# Never prints a token: the inbox key is read into memory and only its field
# NAMES are reported.
param(
    [Parameter(Mandatory)] [string] $Evidence
)
$ErrorActionPreference = 'Stop'
New-Item -ItemType Directory -Force -Path $Evidence | Out-Null
$summary = Join-Path $Evidence 'summary.txt'
function Note([string] $s) { $s | Tee-Object -FilePath $summary -Append | Out-Host }

$isWin = $IsWindows
$claude = Join-Path (npm root -g) '@anthropic-ai/claude-code/bin/claude.exe'
if (-not (Test-Path $claude)) { $claude = (Get-Command claude).Source }
Note "os=$([System.Runtime.InteropServices.RuntimeInformation]::OSDescription) claude=$claude"
Note "version=$(& $claude --version)"

function List-Endpoints([string] $label) {
    if ($isWin) {
        $pipes = [System.IO.Directory]::GetFiles('\\.\pipe\') | Where-Object { $_ -match 'cc-msg|ctxloom|claude|cc-daemon' }
        Note "[$label] named pipes matching cc-msg|ctxloom|claude|cc-daemon: $(@($pipes).Count)"
        $pipes | ForEach-Object { Note "[$label]   pipe $_" }
    } else {
        $roots = @('/tmp', $env:XDG_RUNTIME_DIR, $env:RUNNER_TEMP) | Where-Object { $_ }
        $socks = @(& find @roots -type s 2>$null)
        $socks | ForEach-Object { Note "[$label]   sock $_ $(& stat -c '%A %U' $_)" }
    }
}

function Start-Claude([string] $label, [string[]] $extra) {
    $cfg = Join-Path ([IO.Path]::GetTempPath()) "wp-$label-cfg"
    Remove-Item -Recurse -Force $cfg -ErrorAction SilentlyContinue
    New-Item -ItemType Directory -Force -Path $cfg | Out-Null
    $psi = [System.Diagnostics.ProcessStartInfo]::new($claude)
    foreach ($a in @('-p', '--input-format', 'stream-json', '--output-format', 'stream-json', '--verbose', '--debug') + $extra) { $psi.ArgumentList.Add($a) }
    $psi.RedirectStandardInput = $true
    $psi.UseShellExecute = $false
    $psi.WorkingDirectory = $cfg
    $psi.Environment['CLAUDE_CONFIG_DIR'] = $cfg
    $psi.Environment['DISABLE_AUTOUPDATER'] = '1'
    foreach ($k in 'ANTHROPIC_API_KEY', 'CLAUDE_CODE_OAUTH_TOKEN', 'ANTHROPIC_AUTH_TOKEN', 'CLAUDE_CODE_MESSAGING_SOCKET', 'CLAUDE_CODE_MESSAGING_TOKEN') { $psi.Environment.Remove($k) | Out-Null }
    $p = [System.Diagnostics.Process]::Start($psi)
    Note "[$label] started pid=$($p.Id) args=$($extra -join ' ')"
    return @{ Proc = $p; Cfg = $cfg }
}

function Wait-Endpoint([string] $path, [int] $seconds) {
    $deadline = (Get-Date).AddSeconds($seconds)
    while ((Get-Date) -lt $deadline) {
        if ($isWin) {
            $name = $path -replace '^\\\\\.\\pipe\\', ''
            if ([System.IO.Directory]::GetFiles('\\.\pipe\') | Where-Object { $_ -like "*$name" }) { return $true }
        } elseif (Test-Path $path) { return $true }
        Start-Sleep -Milliseconds 500
    }
    return $false
}

function Read-PeerToken([string] $cfg) {
    $keys = @(Get-ChildItem -Path (Join-Path $cfg 'sessions') -Filter '*.key' -ErrorAction SilentlyContinue)
    Note "  key files in <cfg>/sessions: $($keys.Count) $(($keys | ForEach-Object { ($_.Name -split '\.')[0] + '.<sha256>.key' }) -join ' ')"
    if ($keys.Count -eq 0) { return $null }
    $j = Get-Content -Raw $keys[0].FullName | ConvertFrom-Json
    Note "  key fields: $(($j.PSObject.Properties.Name | Sort-Object) -join ',')"
    return $j.peerToken
}

function Post([string] $path, [string] $token, [string] $text) {
    $lines = @()
    if ($token) { $lines += (@{ type = 'auth'; token = $token } | ConvertTo-Json -Compress) }
    $lines += (@{ type = 'user'; message = @{ role = 'user'; content = $text } } | ConvertTo-Json -Compress -Depth 4)
    $bytes = [Text.Encoding]::UTF8.GetBytes(($lines -join "`n") + "`n")
    try {
        if ($isWin) {
            $c = [System.IO.Pipes.NamedPipeClientStream]::new('.', ($path -replace '^\\\\\.\\pipe\\', ''), [System.IO.Pipes.PipeDirection]::InOut)
            $c.Connect(5000); $c.Write($bytes, 0, $bytes.Length); $c.Flush()
            Start-Sleep -Milliseconds 1500
            $c.Dispose()
        } else {
            $s = [System.Net.Sockets.Socket]::new([System.Net.Sockets.AddressFamily]::Unix, [System.Net.Sockets.SocketType]::Stream, [System.Net.Sockets.ProtocolType]::Unspecified)
            $s.Connect([System.Net.Sockets.UnixDomainSocketEndPoint]::new($path))
            [void]$s.Send($bytes); $s.Shutdown([System.Net.Sockets.SocketShutdown]::Send)
            Start-Sleep -Milliseconds 1500
            $s.Dispose()
        }
        Note "  post '$text' auth=$([bool]$token): written"
    } catch { Note "  post '$text' auth=$([bool]$token): FAILED $($_.Exception.Message)" }
}

function Dump-Debug([string] $label, [string] $cfg) {
    $out = Join-Path $Evidence "$label-debug.txt"
    Get-ChildItem -Path (Join-Path $cfg 'debug') -File -ErrorAction SilentlyContinue | Where-Object { -not $_.LinkType } | ForEach-Object {
        Get-Content $_.FullName | Where-Object { $_ -match 'uds-messaging|cross-session|uds-auth|peer' } |
            ForEach-Object { $_ -replace '[0-9a-f]{32}', '<hex32>' }
    } | Set-Content $out
    Note "[$label] debug lines about messaging: $((Get-Content $out -ErrorAction SilentlyContinue | Measure-Object).Count) (in $(Split-Path -Leaf $out))"
}

# Leg AUTO: no --messaging-socket-path; claude picks the endpoint.
List-Endpoints 'before'
$auto = Start-Claude 'auto' @()
Start-Sleep -Seconds 12
List-Endpoints 'auto'
Dump-Debug 'auto' $auto.Cfg
$auto.Proc.Kill($true)

# Leg EXPLICIT: the path ctxloom would name.
$path = if ($isWin) { '\\.\pipe\ctxloom-wake-probe' } else { Join-Path $env:RUNNER_TEMP 'wp/wake.sock' }
if (-not $isWin) { New-Item -ItemType Directory -Force -Path (Split-Path $path) | Out-Null; chmod 700 (Split-Path $path) }
$exp = Start-Claude 'explicit' @('--messaging-socket-path', $path)
$up = Wait-Endpoint $path 45
Note "[explicit] endpoint $path up=$up"
List-Endpoints 'explicit'
$token = Read-PeerToken $exp.Cfg
if ($up) {
    Post $path $null 'wake-probe-noauth'
    Post $path $token 'wake-probe-auth'
}
Start-Sleep -Seconds 3
Dump-Debug 'explicit' $exp.Cfg
if (-not $exp.Proc.HasExited) { $exp.Proc.Kill($true) } else { Note "[explicit] claude exited early, code $($exp.Proc.ExitCode)" }
Note 'done'
