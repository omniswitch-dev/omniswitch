# Sentinel Python SDK
#
# A thin wrapper around the official OpenAI Python package that routes
# all requests through your self-hosted Sentinel AI Gateway.
#
# Installation:
#   pip install sentinel-ai  (or copy this file into your project)
#
# Requires: openai>=1.0
#   pip install openai
#
# Usage:
#   from sentinel import Sentinel
#   client = Sentinel(gateway_url="http://localhost:8080")
#   response = client.chat.completions.create(
#       model="gpt-4o-mini",
#       messages=[{"role": "user", "content": "Hello!"}],
#   )
#   print(response.choices[0].message.content)

"""
Sentinel AI Gateway - Python SDK

Drop-in replacement for the OpenAI Python client that routes all requests
through your self-hosted Sentinel gateway with full observability.
"""

from __future__ import annotations

import os
from typing import Any, Optional

try:
    from openai import OpenAI
except ImportError:
    raise ImportError(
        "The 'openai' package is required. Install it with: pip install openai>=1.0"
    )

__version__ = "0.1.0"

DEFAULT_GATEWAY_URL = "http://localhost:8080/v1"


class Sentinel(OpenAI):
    """
    Sentinel AI Gateway client.

    A thin wrapper over the official OpenAI client that points all traffic
    at your self-hosted Sentinel gateway. Supports every feature the gateway
    exposes: multi-provider routing, streaming, caching, guardrails, budgets,
    and agent observability.

    Args:
        gateway_url: Base URL of the Sentinel gateway (default: http://localhost:8080/v1).
                     Can also be set via SENTINEL_GATEWAY_URL env var.
        api_key: Sentinel API key for authenticated gateways.
                 Can also be set via SENTINEL_API_KEY env var.
                 If your gateway has auth disabled, pass any non-empty string.
        provider: Force requests to a specific provider (e.g. "anthropic").
                  Maps to the x-sentinel-provider header.
        trace_id: Trace ID for grouping requests across an agent run.
        session_id: Session ID for grouping a conversation.
        shadow_provider: Provider to call asynchronously for shadow comparison.
        **kwargs: Any additional kwargs passed to openai.OpenAI().

    Examples:
        # Basic usage - auto-routes by model name
        client = Sentinel()
        resp = client.chat.completions.create(
            model="claude-sonnet-4-20250514",
            messages=[{"role": "user", "content": "Hello!"}],
        )

        # Force a specific provider
        client = Sentinel(provider="anthropic")

        # With observability headers
        client = Sentinel(trace_id="agent-run-001", session_id="conv-abc")

        # Streaming
        stream = client.chat.completions.create(
            model="gpt-4o-mini",
            messages=[{"role": "user", "content": "Tell me a story"}],
            stream=True,
        )
        for chunk in stream:
            print(chunk.choices[0].delta.content or "", end="")

        # With custom Ollama provider
        client = Sentinel(provider="ollama")
        resp = client.chat.completions.create(
            model="llama3.2",
            messages=[{"role": "user", "content": "Hello!"}],
        )
    """

    def __init__(
        self,
        gateway_url: Optional[str] = None,
        api_key: Optional[str] = None,
        provider: Optional[str] = None,
        trace_id: Optional[str] = None,
        session_id: Optional[str] = None,
        shadow_provider: Optional[str] = None,
        **kwargs: Any,
    ) -> None:
        base_url = gateway_url or os.environ.get("SENTINEL_GATEWAY_URL", DEFAULT_GATEWAY_URL)
        if not base_url.endswith("/v1"):
            base_url = base_url.rstrip("/") + "/v1"

        key = api_key or os.environ.get("SENTINEL_API_KEY", "sentinel-no-auth")

        # Build default headers for Sentinel-specific features.
        default_headers = kwargs.pop("default_headers", {}) or {}
        if provider:
            default_headers["x-sentinel-provider"] = provider
        if trace_id:
            default_headers["x-sentinel-trace-id"] = trace_id
        if session_id:
            default_headers["x-sentinel-session-id"] = session_id
        if shadow_provider:
            default_headers["x-sentinel-shadow-provider"] = shadow_provider

        super().__init__(
            api_key=key,
            base_url=base_url,
            default_headers=default_headers if default_headers else None,
            **kwargs,
        )

    def with_trace(self, trace_id: str, session_id: Optional[str] = None) -> "Sentinel":
        """
        Return a new client instance with the given trace/session IDs.
        Useful for per-request observability without creating a new client.
        """
        headers = dict(self._custom_headers or {})
        headers["x-sentinel-trace-id"] = trace_id
        if session_id:
            headers["x-sentinel-session-id"] = session_id
        gateway_url = str(self.base_url)
        if gateway_url.endswith("/v1"):
            gateway_url = gateway_url[:-3]
        return Sentinel(
            gateway_url=gateway_url,
            api_key=self.api_key,
            default_headers=headers,
        )


def list_models(gateway_url: Optional[str] = None) -> list[dict]:
    """Convenience function to list all models available on the gateway."""
    client = Sentinel(gateway_url=gateway_url)
    models = client.models.list()
    return [{"id": m.id, "owned_by": m.owned_by} for m in models.data]


def chat(
    model: str,
    messages: list[dict],
    gateway_url: Optional[str] = None,
    provider: Optional[str] = None,
    stream: bool = False,
    **kwargs: Any,
):
    """
    One-shot convenience function for quick chat completions.

    Examples:
        from sentinel import chat
        response = chat("gpt-4o-mini", [{"role": "user", "content": "Hi!"}])
        print(response.choices[0].message.content)
    """
    client = Sentinel(gateway_url=gateway_url, provider=provider)
    return client.chat.completions.create(
        model=model,
        messages=messages,
        stream=stream,
        **kwargs,
    )
