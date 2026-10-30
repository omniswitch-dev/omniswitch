"""Saved-profile discovery must not leak credentials to a custom gateway."""
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock
from test_omniswitch import omniswitch


class LocalProfileTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.profile = Path(self.directory.name) / "profile.json"
        self.write_profile()
        clean = dict(os.environ)
        clean.pop("OMNISWITCH_GATEWAY_URL", None)
        clean.pop("OMNISWITCH_API_KEY", None)
        clean["OMNISWITCH_HOME"] = self.directory.name
        self.environment = mock.patch.dict(os.environ, clean, clear=True)
        self.environment.start()
        self.addCleanup(self.environment.stop)

    def write_profile(self, **overrides):
        data = {"version":1, "listen":"127.0.0.1:8097", "app_key":"local-app-private", "gateway_key":"owner-private"}
        data.update(overrides)
        self.profile.write_text(json.dumps(data), encoding="utf-8")

    def test_default_client_uses_local_app_key(self):
        client = omniswitch.OmniSwitch()
        self.assertEqual(client.base_url, "http://127.0.0.1:8097/v1")
        self.assertEqual(client.api_key, "local-app-private")

    def test_explicit_url_never_receives_local_key(self):
        client = omniswitch.OmniSwitch(gateway_url="https://another.example.com")
        self.assertEqual(client.api_key, "omniswitch-no-auth")
        with mock.patch.dict(os.environ, {"OMNISWITCH_GATEWAY_URL":"https://another.example.com"}):
            self.assertEqual(omniswitch.OmniSwitch().api_key, "omniswitch-no-auth")

    def test_explicit_key_wins(self):
        self.assertEqual(omniswitch.OmniSwitch(api_key="caller-key").api_key, "caller-key")

    def test_malformed_or_remote_profile_is_ignored(self):
        for values in ({"listen":"attacker.example:8080"}, {"listen":"127.0.0.1:999999"}, {"app_key":"owner-private"}, {"version":2}):
            self.write_profile(**values)
            self.assertEqual(omniswitch.OmniSwitch().api_key, "omniswitch-no-auth")
