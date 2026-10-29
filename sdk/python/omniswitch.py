"""
OmniSwitch Python SDK.

A small wrapper around the official OpenAI Python client that routes requests
through a self-hosted OmniSwitch gateway and injects OmniSwitch routing,
configuration, tracing, and session headers.
"""

from __future__ import annotations

import os
from typing import Any, Mapping, Optional

try:
    from openai import OpenAI
except ImportError as exc:  # pragma: no cover - exercised by packaging users.
    raise ImportError(
        "The 'openai' package is required. Install it with: pip install openai>=1.0"
    ) from exc

__version__ = "0.1.0"

DEFAULT_GATEWAY_URL = "http://localhost:8080/v1"

# Environment variables read by the SDK. OMNISWITCH_CONFIG and
# OMNISWITCH_SHADOW_PROVIDER are gateway settings, so the SDK uses the
# OMNISWITCH_CLIENT_* names for those to avoid picking up gateway values.
ENV_GATEWAY_URL = "OMNISWITCH_GATEWAY_URL"
ENV_API_KEY = "OMNISWITCH_API_KEY"
ENV_CONFIG = "OMNISWITCH_CLIENT_CONFIG"
ENV_PROVIDER = "OMNISWITCH_PROVIDER"
ENV_TRACE_ID = "OMNISWITCH_TRACE_ID"
ENV_SESSION_ID = "OMNISWITCH_SESSION_ID"
ENV_SHADOW_PROVIDER = "OMNISWITCH_CLIENT_SHADOW_PROVIDER"


class OmniSwitch(OpenAI):
    """
    OpenAI-compatible client for OmniSwitch.

    Args:
        gateway_url: Gateway URL with or without a trailing /v1. Defaults to
            OMNISWITCH_GATEWAY_URL or http://localhost:8080/v1.
        api_key: OmniSwitch API key. Defaults to OMNISWITCH_API_KEY. Use any
            non-empty value when the gateway has authentication disabled.
        config: Dynamic config name or ID sent as x-omniswitch-config.
            Defaults to OMNISWITCH_CLIENT_CONFIG.
        provider: Provider or provider alias sent as x-omniswitch-provider.
            Defaults to OMNISWITCH_PROVIDER.
        trace_id: Trace ID sent as x-omniswitch-trace-id. Defaults to
            OMNISWITCH_TRACE_ID.
        session_id: Session ID sent as x-omniswitch-session-id. Defaults to
            OMNISWITCH_SESSION_ID.
        shadow_provider: Shadow provider sent as x-omniswitch-shadow-provider.
            Defaults to OMNISWITCH_CLIENT_SHADOW_PROVIDER.
        default_headers: Additional default headers for every request. An
            OmniSwitch header set here wins over the environment variable;
            an explicit keyword argument wins over both.
        **kwargs: Additional options passed to openai.OpenAI.
    """

    def __init__(
        self,
        gateway_url: Optional[str] = None,
        api_key: Optional[str] = None,
        config: Optional[str] = None,
        provider: Optional[str] = None,
        trace_id: Optional[str] = None,
        session_id: Optional[str] = None,
        shadow_provider: Optional[str] = None,
        default_headers: Optional[Mapping[str, str]] = None,
        **kwargs: Any,
    ) -> None:
        base_url = normalize_gateway_url(
            gateway_url or os.environ.get(ENV_GATEWAY_URL, DEFAULT_GATEWAY_URL)
        )
        key = api_key or os.environ.get(ENV_API_KEY, "omniswitch-no-auth")
        # Precedence: explicit argument > caller-supplied default_headers
        # (including headers set by with_config/with_trace/with_provider) >
        # environment variables. The environment variables use the
        # OMNISWITCH_CLIENT_* prefix where the plain name is already used by
        # the gateway itself (OMNISWITCH_CONFIG is the gateway config path).
        explicit = build_headers(
            None,
            config=config,
            provider=provider,
            trace_id=trace_id,
            session_id=session_id,
            shadow_provider=shadow_provider,
        )
        from_env = build_headers(
            None,
            config=os.environ.get(ENV_CONFIG),
            provider=os.environ.get(ENV_PROVIDER),
            trace_id=os.environ.get(ENV_TRACE_ID),
            session_id=os.environ.get(ENV_SESSION_ID),
            shadow_provider=os.environ.get(ENV_SHADOW_PROVIDER),
        )
        headers = dict(default_headers or {})
        present = {name.lower() for name in headers}
        for name, value in from_env.items():
            if name not in present:
                headers[name] = value
        headers = _merge_headers(headers, explicit)

        self._omniswitch_gateway_url = base_url
        self._omniswitch_api_key = key
        self._omniswitch_headers = dict(headers)
        self._omniswitch_client_kwargs = dict(kwargs)

        super().__init__(
            api_key=key,
            base_url=base_url,
            default_headers=headers or None,
            **kwargs,
        )

    def with_config(self, config: str) -> "OmniSwitch":
        """Return a new client that sends x-omniswitch-config."""
        return self._clone(
            _merge_headers(self._omniswitch_headers, {"x-omniswitch-config": config})
        )

    def with_trace(self, trace_id: str, session_id: Optional[str] = None) -> "OmniSwitch":
        """Return a new client with trace and optional session headers."""
        overrides = {"x-omniswitch-trace-id": trace_id}
        if session_id is not None:
            overrides["x-omniswitch-session-id"] = session_id
        return self._clone(_merge_headers(self._omniswitch_headers, overrides))

    def with_provider(self, provider: str) -> "OmniSwitch":
        """Return a new client that forces a specific provider or alias."""
        return self._clone(
            _merge_headers(self._omniswitch_headers, {"x-omniswitch-provider": provider})
        )

    def _clone(self, headers: Mapping[str, str]) -> "OmniSwitch":
        return OmniSwitch(
            gateway_url=self._omniswitch_gateway_url,
            api_key=self._omniswitch_api_key,
            default_headers=headers,
            **self._omniswitch_client_kwargs,
        )


# Backwards-compatible alias for early SDK examples.
Sentinel = OmniSwitch


def normalize_gateway_url(gateway_url: str) -> str:
    """Normalize a gateway URL to the OpenAI-compatible /v1 base URL."""
    value = gateway_url.strip() or DEFAULT_GATEWAY_URL
    if not value.rstrip("/").endswith("/v1"):
        value = value.rstrip("/") + "/v1"
    return value.rstrip("/")


def _merge_headers(base: Mapping[str, str], overrides: Mapping[str, str]) -> dict[str, str]:
    """Return base updated with overrides, replacing names case-insensitively."""
    headers = dict(base)
    for name, value in overrides.items():
        for existing in [k for k in headers if k.lower() == name.lower()]:
            del headers[existing]
        headers[name] = value
    return headers


def build_headers(
    default_headers: Optional[Mapping[str, str]] = None,
    *,
    config: Optional[str] = None,
    provider: Optional[str] = None,
    trace_id: Optional[str] = None,
    session_id: Optional[str] = None,
    shadow_provider: Optional[str] = None,
) -> dict[str, str]:
    """Build OmniSwitch headers while preserving caller-provided headers."""
    headers = dict(default_headers or {})
    if config:
        headers["x-omniswitch-config"] = config
    if provider:
        headers["x-omniswitch-provider"] = provider
    if trace_id:
        headers["x-omniswitch-trace-id"] = trace_id
    if session_id:
        headers["x-omniswitch-session-id"] = session_id
    if shadow_provider:
        headers["x-omniswitch-shadow-provider"] = shadow_provider
    return headers


def list_models(gateway_url: Optional[str] = None, api_key: Optional[str] = None) -> list[dict[str, str]]:
    """List models exposed by the OmniSwitch gateway."""
    client = OmniSwitch(gateway_url=gateway_url, api_key=api_key)
    models = client.models.list()
    return [{"id": model.id, "owned_by": model.owned_by} for model in models.data]


def chat(
    model: str,
    messages: list[dict[str, Any]],
    *,
    gateway_url: Optional[str] = None,
    api_key: Optional[str] = None,
    config: Optional[str] = None,
    provider: Optional[str] = None,
    trace_id: Optional[str] = None,
    session_id: Optional[str] = None,
    stream: bool = False,
    **kwargs: Any,
) -> Any:
    """One-shot convenience wrapper around chat.completions.create."""
    client = OmniSwitch(
        gateway_url=gateway_url,
        api_key=api_key,
        config=config,
        provider=provider,
        trace_id=trace_id,
        session_id=session_id,
    )
    return client.chat.completions.create(
        model=model,
        messages=messages,
        stream=stream,
        **kwargs,
    )
