#!/usr/bin/env python3
"""Fresh-install smoke test for a native OmniSwitch gateway wheel, offline."""
from __future__ import annotations
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import venv


def fetch(url, key="", body=None, origin=""):
    headers = {}
    if key: headers["Authorization"] = "Bearer " + key
    if body is not None: headers["Content-Type"] = "application/json"
    if origin: headers["Origin"] = origin
    request = urllib.request.Request(url, data=json.dumps(body).encode() if body is not None else None, headers=headers)
    try:
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request, timeout=2) as response:
            return response.status, response.read().decode()
    except urllib.error.HTTPError as error:
        return error.code, error.read().decode()


def wait_for(callback, process, log, timeout=20):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise AssertionError("gateway exited before readiness (inspect the local test log)")
        try:
            result = callback()
            if result: return result
        except (OSError, ValueError, urllib.error.URLError):
            pass
        time.sleep(.05)
    raise AssertionError("gateway did not finish startup in time")


def stop(process):
    if os.name == "nt":
        subprocess.run(["taskkill", "/PID", str(process.pid), "/T", "/F"], capture_output=True)
    else:
        process.terminate()
    try: process.wait(timeout=10)
    except subprocess.TimeoutExpired: process.kill(); process.wait()


def verify(wheel):
    with tempfile.TemporaryDirectory(prefix="omniswitch-install-") as directory:
        root = Path(directory)
        environment = root / "venv"
        venv.EnvBuilder(with_pip=True).create(environment)
        scripts = environment / ("Scripts" if os.name == "nt" else "bin")
        python = scripts / ("python.exe" if os.name == "nt" else "python")
        cli = scripts / ("omniswitch.exe" if os.name == "nt" else "omniswitch")
        subprocess.run([str(python), "-m", "pip", "install", "--no-index", str(wheel)], cwd=root, check=True)
        subprocess.run([str(python), "-c", "import omniswitch,sys; assert 'openai' not in sys.modules"], cwd=root, check=True)
        version = subprocess.check_output([str(cli), "version"], cwd=root, text=True)
        assert "omniswitch version" in version
        subprocess.run([str(python), "-m", "omniswitch", "--version"], cwd=root, check=True)
        env = {k:v for k,v in os.environ.items() if not k.startswith("OMNISWITCH_") and not k.endswith("_API_KEY") and k not in ("PYTHONPATH", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN")}
        # The runtime needs neither Go nor installed provider SDK dependencies.
        env["PATH"] = str(scripts) + os.pathsep + os.defpath
        home = root / "profile"
        log_path = root / "gateway.log"
        with log_path.open("w", encoding="utf-8") as log:
            process = subprocess.Popen([str(cli), "start", "--demo", "--no-browser", "--home", str(home), "--port", "0"], cwd=root, env=env, stdout=log, stderr=log)
            try:
                profile = wait_for(lambda: json.loads((home / "profile.json").read_text()), process, log)
                base = "http://" + profile["listen"]
                wait_for(lambda: fetch(base + "/readyz")[0] == 200, process, log)
                status, body = fetch(base + "/v1/chat/completions", profile["app_key"], {"model":"default", "messages":[{"role":"user","content":"hello"}]})
                assert status == 200 and "simulated" in body
                assert fetch(base + "/api/keys", profile["app_key"])[0] == 403
                assert fetch(base + "/v1/models")[0] == 401
                assert fetch(base + "/playground")[0] == 200
                assert fetch(base + "/v1/chat/completions", profile["app_key"], {"model":"demo-model","messages":[{"role":"user","content":"hello"}]})[0] == 403
                result = subprocess.run([str(cli), "doctor", "--url", base, "--json"], cwd=root, env=dict(env, OMNISWITCH_API_KEY=profile["app_key"]), capture_output=True, text=True)
                assert result.returncode == 0 and json.loads(result.stdout)["ok"], result.stderr
            finally: stop(process)
        with log_path.open("w", encoding="utf-8") as log:
            process = subprocess.Popen([str(cli), "start", "--no-browser", "--home", str(home)], cwd=root, env=env, stdout=log, stderr=log)
            try:
                wait_for(lambda: fetch(base + "/readyz")[0] == 200, process, log)
                assert json.loads((home / "profile.json").read_text())["app_key"] == profile["app_key"]
                assert fetch(base + "/v1/models", profile["app_key"])[0] == 200
            finally: stop(process)
        # A second profile exercises the actual browser API -> persistence ->
        # server transition, not just the noninteractive demo shortcut.
        wizard_home = root / "wizard"
        with log_path.open("w", encoding="utf-8") as log:
            process = subprocess.Popen([str(cli), "start", "--no-browser", "--home", str(wizard_home), "--port", "0"], cwd=root, env=env, stdout=log, stderr=log)
            try:
                def setup_link():
                    match = re.search(r"Setup: (http://127\.0\.0\.1:\d+)/#setup=([a-f0-9]+)", log_path.read_text())
                    return match.groups() if match else None
                base, token = wait_for(setup_link, process, log)
                assert fetch(base + "/api/setup", token)[0] == 200
                status, body = fetch(base + "/api/connect", token, {"provider":"demo"}, base)
                assert status == 200
                ready = json.loads(body)
                wait_for(lambda: fetch(base + "/readyz")[0] == 200, process, log)
                assert fetch(base + "/v1/chat/completions", ready["app_key"], {"model":"default","messages":[{"role":"user","content":"hello"}]})[0] == 200
            finally: stop(process)
        print("PASS: clean pip install, no SDK dependency, guided setup, demo inference, key permissions, doctor, saved-profile restart")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("wheel", type=Path)
    verify(parser.parse_args().wheel.resolve())
