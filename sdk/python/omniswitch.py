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


class OmniSwitch(OpenAI):
    """
    OpenAI-compatible client for OmniSwitch.

    Args:
        gateway_url: Gateway URL with or without a trailing /v1. Defaults to
            OMNISWITCH_GATEWAY_URL or http://localhost:8080/v1.
        api_key: OmniSwitch API key. Defaults to OMNISWITCH_API_KEY. Use any
            non-empty value when the gateway has authentication disabled.
        config: Dynamic config name or ID sent as x-omniswitch-config.
        provider: Provider or provider alias sent as x-omniswitch-provider.
        trace_id: Trace ID sent as x-omniswitch-trace-id.
        session_id: Session ID sent as x-omniswitch-session-id.
        shadow_provider: Shadow provider sent as x-omniswitch-shadow-provider.
        default_headers: Additional default headers for every request.
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
            gateway_url or os.environ.get("OMNISWITCH_GATEWAY_URL", DEFAULT_GATEWAY_URL)
        )
        key = api_key or os.environ.get("OMNISWITCH_API_KEY", "omniswitch-no-auth")
        headers = build_headers(
            default_headers,
            config=config or os.environ.get("OMNISWITCH_CONFIG"),
            provider=provider or os.environ.get("OMNISWITCH_PROVIDER"),
            trace_id=trace_id or os.environ.get("OMNISWITCH_TRACE_ID"),
            session_id=session_id or os.environ.get("OMNISWITCH_SESSION_ID"),
            shadow_provider=shadow_provider or os.environ.get("OMNISWITCH_SHADOW_PROVIDER"),
        )

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
        headers = dict(self._omniswitch_headers)
        headers["x-omniswitch-config"] = config
        return self._clone(headers)

    def with_trace(self, trace_id: str, session_id: Optional[str] = None) -> "OmniSwitch":
        """Return a new client with trace and optional session headers."""
        headers = dict(self._omniswitch_headers)
        headers["x-omniswitch-trace-id"] = trace_id
        if session_id is not None:
            headers["x-omniswitch-session-id"] = session_id
        return self._clone(headers)

    def with_provider(self, provider: str) -> "OmniSwitch":
        """Return a new client that forces a specific provider or alias."""
        headers = dict(self._omniswitch_headers)
        headers["x-omniswitch-provider"] = provider
        return self._clone(headers)

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
