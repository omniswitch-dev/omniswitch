# Sentinel Python SDK

Thin wrapper around the official OpenAI Python client that routes requests through a Sentinel gateway.

## Install

```bash
pip install openai
```

## Usage

```python
from sentinel import Sentinel

client = Sentinel(gateway_url="http://localhost:8080")
response = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "Hello"}],
)
print(response.choices[0].message.content)
```

Use `provider`, `trace_id`, `session_id`, and `shadow_provider` constructor arguments to set Sentinel routing and observability headers.
