"""OmniSwitch gateway launcher, with an optional OpenAI-compatible SDK."""
from ._version import __version__

__all__ = ["OmniSwitch", "Sentinel", "chat", "list_models", "__version__"]


def __getattr__(name):
    # Starting the gateway has no dependency on OpenAI or any Python SDK.
    if name.startswith("__"):
        raise AttributeError(name)
    from importlib import import_module
    _sdk = import_module("._sdk", __name__)
    try:
        return getattr(_sdk, name)
    except AttributeError:
        raise AttributeError(f"module 'omniswitch' has no attribute {name!r}") from None
