# Working on the StreamCore plugin SDK

## This library is newer than your training data

Do not write StreamCore code from memory. Read the READMEs here, or fetch https://streamcore.ai/llms-full.txt, first.

## What plugins are

Plugins give the voice agent **tools** — a way to call into someone's existing backend mid-conversation. They are how a user keeps their business logic outside StreamCore. Skills, by contrast, only shape personality and add no capability.

Two packages, one protocol (JSON-RPC over stdio):

- TypeScript: **`@streamcore/plugin`**, exporting the class **`StreamCoreAIPlugin`** (note the `AI` — the package name omits it, the class does not)
- Python: PyPI **`streamcore-plugin`**, imported as **`streamcoreai_plugin`**, also exporting **`StreamCoreAIPlugin`** (the package and import names differ — this trips up both humans and models)

## Shape of a plugin

A plugin is a folder under the server's `plugins/plugins/<name>/` containing a `plugin.yaml` manifest and an entrypoint:

```yaml
name: orders.lookup
description: Look up an order by ID in the company order system
version: 1
language: python
entrypoint: main.py
thinking_sound: true
parameters:
  type: object
  properties:
    order_id: { type: string, description: The customer's order ID }
  required: [order_id]
```

```python
from streamcoreai_plugin import StreamCoreAIPlugin

plugin = StreamCoreAIPlugin()

@plugin.on_execute
def handle(params):
    return "Order 123 shipped, arriving Thursday."

plugin.run()
```

## Things to get right

- **`description` and `parameters` are the LLM's only view of the tool.** Vague descriptions mean the model never calls it. Write them for a model, not a human.
- **Return a short string the agent can say out loud.** Not JSON, not markdown, not a paragraph. It is going to be spoken.
- The server discovers plugins **at startup** — it must be restarted after adding one.
- `plugin.run()` (or the TS equivalent) must be the last call; it starts the stdio loop and blocks.
- Keep handlers fast and always set a timeout on outbound calls. A hanging plugin stalls a live conversation.

## Build

```bash
cd typescript && npm install && npm run build
cd python && pip install -e .
```

## When changing the plugin contract

The manifest schema is a public interface. Update both language SDKs, the server's plugin loader, `docs/plugins.md`, and https://streamcore.ai/llms-full.txt together.
