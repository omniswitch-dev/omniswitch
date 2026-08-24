# Builds the Rust guardrail accelerator into an embedded WASM module.
# Requires: rustup target add wasm32-wasip1
$ErrorActionPreference = "Stop"
Push-Location "$PSScriptRoot\..\accel"
cargo build --release --target wasm32-wasip1
if ($LASTEXITCODE -ne 0) { Pop-Location; throw "cargo build failed" }
New-Item -ItemType Directory -Force -Path "..\internal\accel" | Out-Null
Copy-Item ".\target\wasm32-wasip1\release\omniswitch_accel.wasm" "..\internal\accel\guardrails.wasm" -Force
Pop-Location
Write-Host "Accelerator module updated: internal/accel/guardrails.wasm"
