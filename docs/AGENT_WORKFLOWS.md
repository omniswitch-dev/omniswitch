# Route an existing app through OmniSwitch

Use the standard OpenAI or Anthropic SDK with your gateway's base URL and an
OmniSwitch key. Provider keys stay on the gateway. No OmniSwitch SDK is needed.
These examples target the stateless custom-tool subset documented in [API.md](API.md).

## Check setup first

Start the gateway with a provider, then run this in another terminal:

```bash
omniswitch doctor
```

For an authenticated gateway:

```bash
export OMNISWITCH_API_KEY=your-gateway-key
omniswitch doctor --url http://127.0.0.1:8080
```

A passing diagnostic confirms health, readiness, and model visibility. It does
not call a model or verify the provider credential. The examples below do make
provider requests, billed by your provider.

## OpenAI SDK: chat and Responses tools

Install the upstream SDK with `pip install openai`. Set `OMNISWITCH_API_KEY` to a
gateway key when authentication is enabled. For the default unauthenticated,
loopback-only quickstart, the SDK still needs a nonempty value; use `local`.

```python
import json
import os
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:8080/v1",
    api_key=os.getenv("OMNISWITCH_API_KEY", "local"),
)
model = "gpt-4o-mini"  # Choose a model visible in /v1/models.

# An existing chat app changes its base URL and API key only.
chat = client.chat.completions.create(
    model=model,
    messages=[{"role": "user", "content": "Say hello."}],
)
print(chat.choices[0].message.content)

# The application executes custom tools. OmniSwitch routes and governs calls.
tools = [{
    "type": "function",
    "name": "get_service_status",
    "description": "Return the example service's status.",
    "parameters": {"type": "object", "properties": {}, "additionalProperties": False},
    "strict": True,
}]
history = [{"role": "user", "content": "Check the service status."}]
response = client.responses.create(
    model=model, input=history, tools=tools, tool_choice="required", store=False,
)
history.extend(response.output)
for call in response.output:
    if call.type == "function_call":
        if call.name != "get_service_status":
            raise ValueError("Unexpected tool")
        json.loads(call.arguments)  # Validate before any application action.
        history.append({
            "type": "function_call_output",
            "call_id": call.call_id,
            "output": json.dumps({"status": "operational"}),
        })
answer = client.responses.create(
    model=model, input=history, tools=tools, tool_choice="none", store=False,
)
print(answer.output_text)
```

Always send full history, including returned calls and their matching results.
`previous_response_id` and stored/background responses are not supported.

For streaming text, normal SDK stream helpers can collect the final output:

```python
with client.responses.stream(model=model, input="Say hello.", store=False) as stream:
    for event in stream:
        if event.type == "response.output_text.delta":
            print(event.delta, end="", flush=True)
    final = stream.get_final_response()
print("\nFinal:", final.output_text)
```

`text.format` supports `text`, `json_object`, and `json_schema`. Enforcement is
provided by the selected upstream; unsupported native-adapter controls return
an error. For example:

```python
response = client.responses.create(
    model=model, input="Return a greeting.", store=False,
    text={"format": {
        "type": "json_schema", "name": "greeting", "strict": True,
        "schema": {
            "type": "object", "properties": {"message": {"type": "string"}},
            "required": ["message"], "additionalProperties": False,
        },
    }},
)
print(response.output_text)
```

## Anthropic SDK: custom-tool round trip

Install `pip install anthropic`. This example uses the same configured OpenAI
model through the Anthropic-shaped compatibility API; choose a different
registered model to route to another provider.

```python
import os
from anthropic import Anthropic

client = Anthropic(
    base_url="http://127.0.0.1:8080",
    api_key=os.getenv("OMNISWITCH_API_KEY", "local"),
)
messages = [{"role": "user", "content": "Check the service status."}]
tools = [{
    "name": "get_service_status",
    "description": "Return the example service's status.",
    "input_schema": {"type": "object", "properties": {}},
}]
response = client.messages.create(
    model="gpt-4o-mini", max_tokens=200, messages=messages,
    tools=tools, tool_choice={"type": "any"},
)
messages.append({"role": "assistant", "content": response.content})
results = []
for block in response.content:
    if block.type == "tool_use":
        if block.name != "get_service_status":
            raise ValueError("Unexpected tool")
        results.append({
            "type": "tool_result", "tool_use_id": block.id,
            "content": "The example service is operational.",
        })
messages.append({"role": "user", "content": results})
answer = client.messages.create(
    model="gpt-4o-mini", max_tokens=200, messages=messages,
    tools=tools, tool_choice={"type": "none"},
)
for block in answer.content:
    if block.type == "text":
        print(block.text)
```

Use the Anthropic SDK's `messages.stream()` for streaming. Custom tools, text,
and user image blocks are supported; thinking, server tools, documents, and
image tool results are not. See [known limitations](KNOWN_LIMITATIONS.md).

## Inspect and share a useful bug report

Open the dashboard's Logs and Traces after a request. Attach the gateway
version, endpoint, model/alias, stream setting, status code, and a trace ID to
[beta feedback](https://github.com/omniswitch-dev/omniswitch/issues/new?template=beta-feedback.yml).
Remove prompts, tool results, and credentials before sharing them. Payload
logging stays off by default; there is no new analytics collector in this work.
