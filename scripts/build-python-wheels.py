#!/usr/bin/env python3
"""Build self-contained gateway wheels using Go and standard Python tooling."""
from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import zipfile

from packaging.version import Version

ROOT = Path(__file__).resolve().parents[1]
TARGETS = {
    "linux-amd64": ("linux", "amd64", "manylinux_2_17_x86_64"),
    "linux-arm64": ("linux", "arm64", "manylinux_2_17_aarch64"),
    "darwin-amd64": ("darwin", "amd64", "macosx_12_0_x86_64"),
    "darwin-arm64": ("darwin", "arm64", "macosx_12_0_arm64"),
    "windows-amd64": ("windows", "amd64", "win_amd64"),
}


def build(target: str, version: str, output: Path) -> Path:
    goos, goarch, platform = TARGETS[target]
    commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    dirty = bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=ROOT, text=True).strip())
    if dirty:
        commit += "-dirty"
    date = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    with tempfile.TemporaryDirectory(prefix="omniswitch-wheel-") as directory:
        stage = Path(directory) / "package"
        shutil.copytree(ROOT / "sdk" / "python", stage,
                        ignore=shutil.ignore_patterns("__pycache__", "*.egg-info", "build", "dist", "tests"))
        shutil.copyfile(ROOT / "LICENSE", stage / "LICENSE")
        package = stage / "omniswitch"
        (package / "_version.py").write_text(f'__version__ = {version!r}\n', encoding="utf-8")
        binaries = package / "bin"
        binaries.mkdir(exist_ok=True)
        binary = binaries / ("omniswitch.exe" if goos == "windows" else "omniswitch")
        env = dict(os.environ, GOOS=goos, GOARCH=goarch, CGO_ENABLED="0")
        subprocess.run([
            "go", "build", "-trimpath", "-ldflags",
            f"-s -w -X main.version={version} -X main.commit={commit} -X main.date={date}",
            "-o", str(binary), "./cmd/gateway",
        ], cwd=ROOT, env=env, check=True)
        binary.chmod(0o755)
        (binaries / "build.json").write_text(json.dumps({
            "version": version, "commit": commit, "dirty": dirty, "target": target,
            "sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
        }) + "\n", encoding="utf-8")
        subprocess.run([sys.executable, "-m", "build", "--wheel", "--no-isolation", "--outdir", str(output), str(stage)],
                       cwd=ROOT, env=dict(os.environ, OMNISWITCH_WHEEL_PLATFORM=platform), check=True)
    wheel = output / f"omniswitch-{version}-py3-none-{platform}.whl"
    with zipfile.ZipFile(wheel) as archive:
        if not any(name.endswith("/bin/" + binary.name) for name in archive.namelist()):
            raise RuntimeError("wheel is missing its gateway binary")
        metadata = archive.read(f"omniswitch-{version}.dist-info/WHEEL").decode()
        if f"Tag: py3-none-{platform}" not in metadata or "Root-Is-Purelib: false" not in metadata:
            raise RuntimeError("incorrect platform wheel metadata")
    return wheel


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", choices=[*TARGETS, "all"], default="all")
    parser.add_argument("--version", default="0.2.0.dev0")
    parser.add_argument("--output", type=Path, default=ROOT / "dist" / "python")
    args = parser.parse_args()
    version = str(Version(args.version.removeprefix("v")))
    args.output = args.output.resolve()
    args.output.mkdir(parents=True, exist_ok=True)
    for target in TARGETS if args.target == "all" else [args.target]:
        print(f"Building {target} -> {version}", flush=True)
        print(build(target, version, args.output), flush=True)


if __name__ == "__main__":
    main()
