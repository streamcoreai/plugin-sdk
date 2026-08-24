# `@streamcore/plugin`

[English](./README.md) | **简体中文**

用于开发 **StreamCoreAI** 插件的 TypeScript/JavaScript SDK。插件是由 **Go 语音智能体服务端**拉起的子进程；它们通过 **stdin/stdout** 上的 **JSON-RPC 2.0** 与服务端通信，从而让 LLM 在对话中调用你的代码。

完整流程（manifest、插件目录结构，以及服务端如何加载插件）见 [插件开发指南](../../docs/plugins.md)。

## 安装

```bash
npm install @streamcore/plugin
```

从本仓库安装（用于开发）：

```bash
cd plugin-sdk/typescript
npm install
npm run build
```

发布之后，按包名安装并在你的插件项目中依赖它即可。

## 用法

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

| 方法 | 说明 |
|--------|-------------|
| `onExecute(handler)` | 必需。LLM 调用插件时触发。`handler` 接收 `Record<string, unknown>` 并返回一个字符串（或 `Promise<string>`）。 |
| `onInitialize(handler)` | 可选。服务端发送 `initialize` RPC 时运行一次。 |
| `log(message)` | 向 **stderr** 写一行（会出现在服务端日志中）。 |
| `run()` | 在 stdin 上启动 JSON-RPC 行循环；stdin 关闭且工作完成后退出。 |

## 参见

- [插件开发指南](../../docs/plugins.md) —— `plugin.yaml`、`language: typescript`、entrypoint 与测试
