# Verifies that an OmniSwitch binary can be installed (or is already built),
# started with just an API key, and answers health/readiness/models requests.
#
# Windows equivalent of scripts/verify-install.sh. See that script's header
# comment for the two supported modes (OMNISWITCH_BIN for a local build vs.
# OMNISWITCH_VERSION for an install.sh-equivalent flow). On Windows there is
# no install.sh, so this script expects OMNISWITCH_BIN to point at an
# already-downloaded/extracted omniswitch.exe; the install-verify workflow's
# Windows job is responsible for downloading and extracting the release zip
# before calling this script.
#
#   $env:OMNISWITCH_BIN = "C:\path\to\omniswitch.exe"
#   pwsh scripts/verify-install.ps1
#
# Exits non-zero (via `exit 1`) on any failure, including a missing `version`
# subcommand or a non-200 from /healthz, /readyz, or /v1/models.

$ErrorActionPreference = "Stop"

$Port = if ($env:OMNISWITCH_PORT) { $env:OMNISWITCH_PORT } else { "18080" }
$StartTimeout = if ($env:OMNISWITCH_START_TIMEOUT) { [int]$env:OMNISWITCH_START_TIMEOUT } else { 30 }
$BaseUrl = "http://127.0.0.1:$Port"

if (-not $env:OMNISWITCH_BIN) {
    Write-Error "FAIL: set OMNISWITCH_BIN to the path of omniswitch.exe (this script does not download/extract a release archive itself)"
    exit 1
}
$Bin = $env:OMNISWITCH_BIN
if (-not (Test-Path $Bin)) {
    Write-Error "FAIL: OMNISWITCH_BIN='$Bin' does not exist"
    exit 1
}

$WorkDir = Join-Path $env:TEMP ("omniswitch-verify-" + [System.Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $WorkDir | Out-Null

$Failed = $false
$Proc = $null

function Fail([string]$Message) {
    Write-Host "FAIL: $Message" -ForegroundColor Red
    $script:Failed = $true
}

try {
    Write-Host "==> Using binary: $Bin"

    Write-Host "==> Running: $Bin version"
    $versionTimeoutSec = if ($env:OMNISWITCH_VERSION_TIMEOUT) { [int]$env:OMNISWITCH_VERSION_TIMEOUT } else { 10 }
    $versionOut = Join-Path $WorkDir "version.out.log"
    $versionErr = Join-Path $WorkDir "version.err.log"
    # Run with a watchdog timeout: a binary that predates the `version`
    # subcommand may ignore the argument and start the server instead,
    # which would otherwise hang this check forever.
    $versionProc = Start-Process -FilePath $Bin -ArgumentList "version" -PassThru `
        -RedirectStandardOutput $versionOut -RedirectStandardError $versionErr -WindowStyle Hidden
    $exited = $versionProc.WaitForExit($versionTimeoutSec * 1000)
    if (-not $exited) {
        Stop-Process -Id $versionProc.Id -Force -ErrorAction SilentlyContinue
        Fail "'$Bin version' did not exit within $versionTimeoutSec s. This binary likely predates the version subcommand and is treating 'version' as an ignored argument while starting the server instead."
    } else {
        if (Test-Path $versionOut) { Get-Content $versionOut }
        if (Test-Path $versionErr) { Get-Content $versionErr }
        if ($versionProc.ExitCode -ne 0) {
            Fail "'$Bin version' exited $($versionProc.ExitCode) (expected 0)."
        }
    }

    Write-Host "==> Starting: OPENAI_API_KEY=dummy $Bin serve (listening on :$Port)"
    $env:OPENAI_API_KEY = "dummy"
    $env:OMNISWITCH_LISTEN = ":$Port"
    $env:OMNISWITCH_DATA = Join-Path $WorkDir "data"
    $env:OMNISWITCH_AUTH = "false"
    $env:OMNISWITCH_MCP_ENABLED = "false"
    $env:OMNISWITCH_CONFIG = ""

    $stdout = Join-Path $WorkDir "server.out.log"
    $stderr = Join-Path $WorkDir "server.err.log"
    $Proc = Start-Process -FilePath $Bin -ArgumentList "serve" -PassThru `
        -RedirectStandardOutput $stdout -RedirectStandardError $stderr -WindowStyle Hidden

    Write-Host "==> Waiting up to $StartTimeout s for $BaseUrl/healthz"
    $up = $false
    $deadline = (Get-Date).AddSeconds($StartTimeout)
    while ((Get-Date) -lt $deadline) {
        if ($Proc.HasExited) {
            Fail "server process exited before becoming healthy; see log below"
            break
        }
        try {
            $resp = Invoke-WebRequest -Uri "$BaseUrl/healthz" -UseBasicParsing -TimeoutSec 2
            if ($resp.StatusCode -eq 200) { $up = $true; break }
        } catch {
            # not up yet, keep polling
        }
        Start-Sleep -Seconds 1
    }

    if (-not $up -and -not $Failed) {
        Fail "$BaseUrl/healthz did not return 200 within $StartTimeout s"
    }

    function Check-Endpoint([string]$Path) {
        $url = "$BaseUrl$Path"
        try {
            $resp = Invoke-WebRequest -Uri $url -UseBasicParsing -TimeoutSec 5
            if ($resp.StatusCode -eq 200) {
                Write-Host "OK    $Path -> 200"
            } else {
                Fail "GET $Path returned $($resp.StatusCode) (expected 200)"
            }
        } catch {
            Fail "GET $Path failed: $_"
        }
    }

    if ($up) {
        Check-Endpoint "/healthz"
        Check-Endpoint "/readyz"
        Check-Endpoint "/v1/models"
    }

    Write-Host "----- server stdout -----"
    if (Test-Path $stdout) { Get-Content $stdout }
    Write-Host "----- server stderr -----"
    if (Test-Path $stderr) { Get-Content $stderr }
    Write-Host "--------------------------"
}
finally {
    if ($Proc -and -not $Proc.HasExited) {
        Write-Host "==> Stopping server (pid $($Proc.Id))"
        Stop-Process -Id $Proc.Id -Force -ErrorAction SilentlyContinue
    }
    Remove-Item -Recurse -Force $WorkDir -ErrorAction SilentlyContinue
}

if ($Failed) {
    Write-Host "RESULT: FAIL" -ForegroundColor Red
    exit 1
}

Write-Host "RESULT: PASS" -ForegroundColor Green
exit 0
