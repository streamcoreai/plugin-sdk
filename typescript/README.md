# `@streamcore/plugin`

**English** | [简体中文](./README.zh-CN.md)

TypeScript/JavaScript SDK for building **StreamCoreAI** plugins. Plugins are subprocesses spawned by the **Go voice agent server**; they talk to the server over **JSON-RPC 2.0** on **stdin/stdout** so the LLM can call your code during conversations.

For the full workflow (manifests, plugin directory layout, and how the server loads plugins), see the [Plugin Development Guide](../../docs/plugins.md).

## Installation

```bash
npm install @streamcore/plugin
```

From this repo (for development):

```bash
cd plugin-sdk/typescript
npm install
npm run build
```

When published, install the package by name and depend on it from your plugin project.

## Usage

```typescript
import { StreamCoreAIPlugin } from "@streamcore/plugin";

const plugin = new StreamCoreAIPlugin();

plugin.onExecute(async (params) => {
  // `params` matches the JSON Schema in your plugin.yaml
  return "result string for the LLM";
});

plugin.onInitialize(async () => {
  plugin.log("Plugin initialized");
});

plugin.run(); // JSON-RPC loop on stdin/stdout; blocks until stdin closes
```

## API

| Method | Description |
|--------|-------------|
| `onExecute(handler)` | Required. Called when the LLM invokes the plugin. `handler` receives `Record<string, unknown>` and returns a string (or `Promise<string>`). |
| `onInitialize(handler)` | Optional. Runs once when the server sends the `initialize` RPC. |
| `log(message)` | Writes a line to **stderr** (shows up in server logs). |
| `run()` | Starts the JSON-RPC line loop on stdin; exits when stdin is closed and work is finished. |

## See also

- [Plugin Development Guide](../../docs/plugins.md) — `plugin.yaml`, `language: typescript`, entrypoints, and testing
