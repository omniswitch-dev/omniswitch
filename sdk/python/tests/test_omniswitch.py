"""Unit tests for the OmniSwitch Python SDK.

The tests stub the ``openai`` package, so they need no network access and no
third-party dependencies. Run them from the repository root with:

    python3 -m unittest discover -s sdk/python/tests
"""

from __future__ import annotations

import os
import sys
import types
import unittest
from unittest import mock

SDK_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


class _FakeOpenAI:
    """Minimal stand-in for openai.OpenAI that records constructor options."""

    def __init__(self, api_key=None, base_url=None, default_headers=None, **kwargs):
        self.api_key = api_key
        self.base_url = base_url
        self.default_headers = dict(default_headers or {})
        self.kwargs = kwargs


def _import_sdk():
    fake = types.ModuleType("openai")
    fake.OpenAI = _FakeOpenAI
    sys.modules["openai"] = fake
    if SDK_DIR not in sys.path:
        sys.path.insert(0, SDK_DIR)
    sys.modules.pop("omniswitch", None)
    import omniswitch  # noqa: PLC0415

    return omniswitch


omniswitch = _import_sdk()

SDK_ENV = (
    "OMNISWITCH_GATEWAY_URL",
    "OMNISWITCH_API_KEY",
    "OMNISWITCH_CONFIG",
    "OMNISWITCH_CLIENT_CONFIG",
    "OMNISWITCH_PROVIDER",
    "OMNISWITCH_TRACE_ID",
    "OMNISWITCH_SESSION_ID",
    "OMNISWITCH_SHADOW_PROVIDER",
    "OMNISWITCH_CLIENT_SHADOW_PROVIDER",
)


def clean_env(**values):
    env = {k: v for k, v in os.environ.items() if k not in SDK_ENV}
    env.update(values)
    return mock.patch.dict(os.environ, env, clear=True)


class GatewayEnvCollisionTest(unittest.TestCase):
    def test_gateway_config_path_is_not_sent_as_header(self):
        with clean_env(
            OMNISWITCH_CONFIG="examples/gateway-config.yaml",
            OMNISWITCH_SHADOW_PROVIDER="anthropic",
        ):
            client = omniswitch.OmniSwitch(gateway_url="http://gw:8080")
        self.assertNotIn("x-omniswitch-config", client.default_headers)
        self.assertNotIn("x-omniswitch-shadow-provider", client.default_headers)

    def test_client_env_vars_are_used(self):
        with clean_env(
            OMNISWITCH_CLIENT_CONFIG="production-routing",
            OMNISWITCH_CLIENT_SHADOW_PROVIDER="anthropic",
            OMNISWITCH_PROVIDER="openai",
            OMNISWITCH_TRACE_ID="trace-env",
            OMNISWITCH_SESSION_ID="session-env",
        ):
            client = omniswitch.OmniSwitch()
        self.assertEqual(
            client.default_headers,
            {
                "x-omniswitch-config": "production-routing",
                "x-omniswitch-shadow-provider": "anthropic",
                "x-omniswitch-provider": "openai",
                "x-omniswitch-trace-id": "trace-env",
                "x-omniswitch-session-id": "session-env",
            },
        )


class PrecedenceTest(unittest.TestCase):
    def test_with_helpers_override_env(self):
        with clean_env(
            OMNISWITCH_CLIENT_CONFIG="production-routing",
            OMNISWITCH_PROVIDER="openai",
            OMNISWITCH_TRACE_ID="trace-env",
            OMNISWITCH_SESSION_ID="session-env",
        ):
            client = (
                omniswitch.OmniSwitch()
                .with_config("experiment-a")
                .with_provider("anthropic")
                .with_trace("trace-123", session_id="session-123")
            )
        headers = client.default_headers
        self.assertEqual(headers["x-omniswitch-config"], "experiment-a")
        self.assertEqual(headers["x-omniswitch-provider"], "anthropic")
        self.assertEqual(headers["x-omniswitch-trace-id"], "trace-123")
        self.assertEqual(headers["x-omniswitch-session-id"], "session-123")

    def test_explicit_arguments_override_env(self):
        with clean_env(OMNISWITCH_CLIENT_CONFIG="env", OMNISWITCH_TRACE_ID="env"):
            client = omniswitch.OmniSwitch(config="arg", trace_id="arg-trace")
        self.assertEqual(client.default_headers["x-omniswitch-config"], "arg")
        self.assertEqual(client.default_headers["x-omniswitch-trace-id"], "arg-trace")

    def test_default_headers_override_env_case_insensitively(self):
        with clean_env(OMNISWITCH_CLIENT_CONFIG="env"):
            client = omniswitch.OmniSwitch(default_headers={"X-OmniSwitch-Config": "hdr"})
        self.assertEqual(client.default_headers, {"X-OmniSwitch-Config": "hdr"})

    def test_explicit_argument_overrides_default_headers(self):
        with clean_env():
            client = omniswitch.OmniSwitch(
                config="arg", default_headers={"X-OmniSwitch-Config": "hdr", "x-other": "1"}
            )
        self.assertEqual(client.default_headers, {"x-omniswitch-config": "arg", "x-other": "1"})

    def test_with_config_replaces_mixed_case_header(self):
        with clean_env():
            client = omniswitch.OmniSwitch(default_headers={"X-OmniSwitch-Config": "old"})
            client = client.with_config("new")
        self.assertEqual(client.default_headers, {"x-omniswitch-config": "new"})

    def test_clone_keeps_gateway_key_and_kwargs(self):
        with clean_env():
            client = omniswitch.OmniSwitch(gateway_url="http://gw", api_key="k", timeout=5)
            clone = client.with_config("c")
        self.assertEqual(clone.base_url, "http://gw/v1")
        self.assertEqual(clone.api_key, "k")
        self.assertEqual(clone.kwargs, {"timeout": 5})


class DefaultsTest(unittest.TestCase):
    def test_defaults(self):
        with clean_env():
            client = omniswitch.OmniSwitch()
        self.assertEqual(client.base_url, "http://localhost:8080/v1")
        self.assertEqual(client.api_key, "omniswitch-no-auth")
        self.assertEqual(client.default_headers, {})

    def test_env_gateway_and_key(self):
        with clean_env(OMNISWITCH_GATEWAY_URL="http://gw:9000/", OMNISWITCH_API_KEY="sk-1"):
            client = omniswitch.OmniSwitch()
        self.assertEqual(client.base_url, "http://gw:9000/v1")
        self.assertEqual(client.api_key, "sk-1")

    def test_normalize_gateway_url(self):
        cases = {
            "http://gw": "http://gw/v1",
            "http://gw/": "http://gw/v1",
            "http://gw/v1": "http://gw/v1",
            "http://gw/v1/": "http://gw/v1",
            "  ": "http://localhost:8080/v1",
        }
        for value, want in cases.items():
            with self.subTest(value=value):
                self.assertEqual(omniswitch.normalize_gateway_url(value), want)


if __name__ == "__main__":
    unittest.main()
