# Install. Connect. Build.

The guided path runs a complete local gateway without Go, Docker, config files,
or provider SDK dependencies. A release wheel contains the executable for your
platform, so launching does not download or execute another binary.

**Publication status:** the new platform packages are built and tested by the
Python package workflow. The public `pip install omniswitch` command becomes
available after the owner configures [Trusted Publishing](PYTHON_RELEASES.md)
and publishes the first verified package release. This branch does not claim
that a public PyPI package is already available.

## The release install

```bash
pip install omniswitch
omniswitch
```

1. **Choose a provider.** OpenAI, Anthropic, Google Gemini, Groq, OpenRouter, or
   an OpenAI-compatible local server such as Ollama or vLLM.
2. **Connect.** Use a detected environment key or paste a provider key. The
   gateway verifies model discovery without generating a paid completion.
3. **Build.** Copy a working Python, JavaScript, environment, cURL, LangChain,
   or LlamaIndex example. Apps use one model name, `default`, which routes to
   the selected upstream model.

The setup page also offers **Try the demo**: no provider key or external API
is required. Its deterministic responses explicitly identify themselves as
simulated; it is not an AI model, a quality evaluation, or a performance
benchmark. Demo tools, structured output, embeddings and other provider-specific
operations are not implemented.

The **Send** button on the connected page sends a small request only when
clicked. In real-provider mode it uses that provider's normal pricing. The
connection check itself only lists models; a successful listing cannot guarantee
access to every inference operation or available provider credits.

The next launch is simply `omniswitch`. Reconnect with:

```bash
omniswitch start --setup
```

## Existing applications

Use the integration tabs to copy code or download `.env.omniswitch`. Set the
client's base URL to `http://127.0.0.1:8080/v1`, its API key to the generated
**app key**, and its model to `default`. Use the actual local port shown in
setup if a different port was selected. `.env` files are not loaded by every
framework automatically; export/load them using your application's normal
configuration path.

Changing the provider later preserves the gateway endpoint, logical model name,
and local credentials. There is no need to rewrite every application to switch
upstreams. Apps make their normal OpenAI-compatible requests through the same
routing, guardrail, logging, caching and identity pipeline.

The setup page supplies standard SDK examples; the optional convenience client
can be installed with `pip install 'omniswitch[sdk]'`. Existing imports such as
`from omniswitch import OmniSwitch` retain their SDK behavior with that extra.
The base gateway installation does not pull in an OpenAI client.

With the optional extra, the client also finds the saved local profile:

```python
from omniswitch import OmniSwitch

client = OmniSwitch()  # Local address and app key are discovered automatically.
response = client.chat.completions.create(
    model="default", messages=[{"role": "user", "content": "Hello!"}],
)
print(response.choices[0].message.content)
```

Explicit `gateway_url` / `OMNISWITCH_GATEWAY_URL` disables that discovery.
Local credentials are never reused for an explicitly selected external gateway.

## Headless machines and multiple profiles

```bash
omniswitch start --no-browser
omniswitch start --demo --home ./demo-profile --port 8081
omniswitch start --home ./demo-profile
```

`--no-browser` prints a private setup link; open it on the same machine, or
forward the chosen loopback port through your usual SSH tunnel. Use the link's
exact `127.0.0.1` hostname and port. Setup checks the host, Origin, and session
token; it does not accept arbitrary proxy hosts. The link token is held in the
URL fragment, removed from browser history after loading, and required for the
setup API. The tab remembers it during setup and clears it when setup completes.

`--port 0` chooses a free local port and remembers it. Otherwise the initial
port is 8080; a busy port produces a useful error instead of silently connecting
to a different service. Use `--home` again for a custom profile. A real-provider
profile is not overwritten by `--demo`; choose a separate directory.

## Local state and credentials

The managed profile lives in the OS user configuration directory under
`omniswitch`, or in `--home` / `OMNISWITCH_HOME`. It contains:

- `profile.json`: the chosen provider/model, provider credential, generated
  owner key, generated app key, and vault secret. Writes are atomic and use
  owner-only file permissions on Unix. Windows users should keep the profile
  in their normal private user directory and use its normal ACL protection.
- `gateway.json`: a normal GatewayConfig referencing the in-process provider
  environment variable. It does not embed provider, owner, or app credentials.
- `data/`: local SQLite state for this profile.

This is a private local credential file, not an OS keychain or an encrypted
secret store. Do not commit or share it. Downloaded environment snippets also
contain an app credential; keep them out of source control.

Authentication is enabled and the listener is fixed to `127.0.0.1`. Owner and
app credentials are distinct: the browser stores the owner credential in its
session, while integration snippets use the app key restricted to the exact
client-facing model ID `default`. The app key cannot read/manage key APIs.
Provider keys are never returned to the browser. They are sent only to the
chosen provider during discovery and inference.

The dashboard document itself is public static HTML so a browser can load it
without an Authorization header. All dashboard APIs and inference endpoints
retain authentication/authorization. Browser handoff credentials are carried in
a fragment and removed from the URL after session initialization.

Managed starts use isolated gateway settings and SQLite state, ignoring other
`OMNISWITCH_*` overrides once preparation completes. The advanced server command
preserves its environment/config contract:

```bash
OPENAI_API_KEY=your-key omniswitch serve
OMNISWITCH_CONFIG=production.yaml omniswitch serve
```

Use `serve` for multi-provider config, deployment beyond loopback, external
storage, OIDC, Redis, MCP configuration, or other advanced operational settings.
No-argument startup also preserves direct-server behavior when
`OMNISWITCH_CONFIG` was explicitly set. See [CLI.md](CLI.md).

## Verify a wheel before release

```bash
python -m pip install build 'setuptools>=77' wheel packaging
python scripts/build-python-wheels.py --target linux-amd64 --version 0.2.0.dev0
python scripts/verify-python-wheel.py dist/python/omniswitch-0.2.0.dev0-py3-none-manylinux_2_17_x86_64.whl
```

Use your native target; `--target all` builds Linux amd64/arm64, macOS Intel/
Apple Silicon and Windows amd64 wheels. CI installs and exercises each on its
native runner. Smoke tests use only local demo requests, not paid providers.
