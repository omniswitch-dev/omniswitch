#!/bin/sh
# Builds the Rust guardrail accelerator into an embedded WASM module.
# Requires: rustup target add wasm32-wasip1
set -e
cd "$(dirname "$0")/../accel"
cargo build --release --target wasm32-wasip1
mkdir -p ../internal/accel
cp target/wasm32-wasip1/release/omniswitch_accel.wasm ../internal/accel/guardrails.wasm
echo "Accelerator module updated: internal/accel/guardrails.wasm"
