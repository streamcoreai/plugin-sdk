# Working on the StreamCore plugin SDK

## This library is newer than your training data

Do not write StreamCore code from memory. Read the READMEs here, or fetch https://streamcore.ai/llms-full.txt, first.

## What plugins are

Plugins give the voice agent **tools** — a way to call into someone's existing backend mid-conversation. They are how a user keeps their business logic outside StreamCore. Skills, by contrast, only shape personality and add no capability.

Three packages, one protocol (JSON-RPC over stdio):

- TypeScript: **`@streamcore/plugin`**, exporting the class **`StreamCoreAIPlugin`** (note the `AI` — the package name omits it, the class does not)
- Python: PyPI **`streamcore-plugin`**, imported as **`streamcoreai_plugin`**, also exporting **`StreamCoreAIPlugin`** (the package and import names differ — this trips up both humans and models)
- Go: **`github.com/streamcoreai/plugin-sdk/go`**, imported as `streamcore`, with `streamcore.New()`

A Go plugin is a subprocess like the other two, not a `plugin.Open` shared object. Do not suggest dynamic loading: it pins the plugin to the server's exact toolchain and fails at runtime on any mismatch.

## Shape of a plugin

A plugin is a folder under the server's `plugins/plugins/<name>/` containing a `plugin.yaml` manifest and an entrypoint:

```yaml
name: orders.lookup
description: Look up an order by ID in the company order system
version: 1
exec: ["python3", "main.py"]
thinking_sound: true
parameters:
  type: object
  properties:
    order_id: { type: string, description: The customer's order ID }
  required: [order_id]
```

`exec` is an argv, so the server is indifferent to the language. `language` + `entrypoint` still work as shorthand. A `build` argv runs once before it, which is how a compiled plugin and one with dependencies both work.

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
- **Return a short string the agent can say out loud.** Not JSON, not markdown, not a paragraph. It is going to be spoken. A plugin that also needs to act on the caller's device returns a `Result` instead, carrying `speak` and `emit`.
- **Settings go in the server's `config.toml`**, under `[plugins.config."<name>"]`, and arrive in the initialize handler. Not a dotenv beside the plugin.
- The server discovers plugins **at startup** — it must be restarted after adding one.
- `plugin.run()` (or the TS equivalent) must be the last call; it starts the stdio loop and blocks.
- Keep handlers fast and always set a timeout on outbound calls. A hanging plugin stalls a live conversation.

## Build

```bash
cd typescript && npm install && npm run build
cd python && pip install -e .
cd go && go build ./...
```

## Beyond tools

A plugin is not limited to answering the model. It can also:

- **Emit** a packet at a conversation's client unprompted, and subscribe to lifecycle events with `events:` in the manifest — an observer that never becomes a callable tool.
- **Call another plugin's tool** through the server, without knowing where it lives. Mark the join `internal: true` so it stays callable by plugins and invisible to the model, and use the ready handler — which fires once everything has loaded, listing the tools that exist — to decide what to offer when it depends on a peer.
- **Ask the server's configured model** for a completion, rather than carrying a second API key, and **search the deployment's knowledge base** rather than standing up a second retrieval stack.
- **Ask the client for something only it has** — a camera frame — by naming it under `requires:`. The server captures it and merges the fields into the tool's arguments.
- **Skip having a process at all**: a tool that only turns arguments into one packet declares a `dispatch:` block and the server does it directly.

## When changing the plugin contract

The manifest schema is a public interface. Update all three language SDKs, the server's plugin loader, `docs/plugins.md`, and https://streamcore.ai/llms-full.txt together — and check the parity tests in the server's `internal/plugin` package still pass, since they drive all three SDKs through the same capabilities.
