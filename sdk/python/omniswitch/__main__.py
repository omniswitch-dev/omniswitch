"""Launch the gateway binary included in the platform wheel. No downloads."""
from __future__ import annotations

import os
from pathlib import Path
import sys
import subprocess


def binary_path() -> Path:
    override = os.environ.get("OMNISWITCH_BINARY")
    if override:
        return Path(override).expanduser().resolve()
    name = "omniswitch.exe" if os.name == "nt" else "omniswitch"
    return Path(__file__).resolve().parent / "bin" / name


def main() -> None:
    binary = binary_path()
    if not binary.is_file():
        print(
            "The gateway binary is missing from this source/SDK install. "
            "Install an official platform wheel, or build ./cmd/gateway and "
            "set OMNISWITCH_BINARY to its absolute path.",
            file=sys.stderr,
        )
        raise SystemExit(1)
    if os.name != "nt" and not os.access(binary, os.X_OK):
        print("The installed gateway binary is not executable; reinstall the wheel.", file=sys.stderr)
        raise SystemExit(1)
    if os.name == "nt":
        child = subprocess.Popen([str(binary), *sys.argv[1:]])
        try:
            code = child.wait()
        except KeyboardInterrupt:
            # Console interrupts reach the inherited gateway process too.
            try:
                code = child.wait(timeout=10)
            except subprocess.TimeoutExpired:
                child.terminate()
                code = child.wait()
        raise SystemExit(code)
    # Process replacement preserves terminal signals and gateway exit codes.
    os.execv(str(binary), [str(binary), *sys.argv[1:]])


if __name__ == "__main__":
    main()
