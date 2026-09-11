# StreamCoreAI Plugin SDK for Go

Build StreamCoreAI plugins in Go. A plugin is a process the server starts and talks to over stdio — the same protocol the [Python](../python) and [TypeScript](../typescript) SDKs speak, so a Go plugin lives in the same folder as any other and the server never learns which language you chose.

```bash
go get github.com/streamcoreai/plugin-sdk/go
```

## A plugin in full

**`plugin.yaml`**
```yaml
name: fleet.status
description: Report which robots are online and their battery level
version: 1
build: ["go", "build", "-o", "fleet-status", "."]
exec: ["./fleet-status"]
parameters:
  type: object
  properties:
    site:
      type: string
      description: Which site to report on
  required: [site]
```

**`main.go`**
```go
package main

import streamcore "github.com/streamcoreai/plugin-sdk/go"

func main() {
	plugin := streamcore.New()

	plugin.OnExecute(func(call streamcore.Call) (any, error) {
		var args struct {
			Site string `json:"site"`
		}
		if err := call.Bind(&args); err != nil {
			return nil, err
		}
		return "Four robots online at " + args.Site + ", all above 80 percent.", nil
	})

	plugin.Run()
}
```

Drop the folder in the server's plugin directory and restart. The `build` step runs before the process starts, so there is no compiled artifact to commit.

## Returning more than words

Return a string and the agent says it. Return a `Result` and you can also push packets at the caller's device:

```go
return streamcore.Result{
	Speak: "Driving.",
	Emit: []streamcore.Emission{{
		Topic:   "movement.command",
		Payload: map[string]any{"action": "forward"},
	}},
}, nil
```

## Handlers

```go
plugin.OnExecute(func(call streamcore.Call) (any, error))
plugin.OnInitialize(func(init streamcore.Init) ([]streamcore.Tool, error))
plugin.OnReady(func(ready streamcore.Ready) ([]streamcore.Tool, error))
plugin.OnEvent(func(event streamcore.Event) (any, error))
plugin.OnConfirm(func(call streamcore.Call) (string, error))
```

`Call` carries `Tool` (which of your tools was called), `SessionID` (the conversation), and `Params`, with `Bind` to unmarshal.

`Init` carries `Config` — this plugin's own table from the server's `config.toml`, so credentials live in the operator's config rather than in a dotenv beside your code. Returning tools from `OnInitialize` declares them at startup, which is how a plugin whose surface depends on its configuration works.

`OnEvent` fires for lifecycle events your manifest subscribes to with `events:`. `OnConfirm` supplies the spoken wording for a tool behind the confirmation gate, when a `confirmation_prompt` template in the manifest is not expressive enough.

`OnReady` fires once every plugin has started and tells you which tools the server ended up with. It is where a plugin decides to offer something that only makes sense alongside a peer — at initialize the peer may not have loaded yet, so the question cannot be answered there:

```go
plugin.OnReady(func(ready streamcore.Ready) ([]streamcore.Tool, error) {
	if !ready.Has("codex.pull_request_source") {
		return nil, nil          // keep what initialize declared
	}
	return append(base, prTool), nil
})
```

A tool marked `Internal: true` is registered and callable by other plugins but never offered to the model. Use it for the joins between plugins — one asking another for a credential or a worktree — which the model has no business invoking.

## Asking the server for things

```go
plugin.Emit(sessionID, "sensors.reading", map[string]any{"celsius": 21.5})
answer, err := plugin.CallTool(sessionID, "weather.get", map[string]any{"location": "Berlin"})
summary, err := plugin.Complete(sessionID, "Summarise this", transcript)
chunks, err := plugin.Search(sessionID, "refund policy", 0)
```

`Emit` pushes a packet at a conversation's client without having been asked for one. `CallTool` runs another plugin's tool, routed by the server, so neither plugin has to know where the other lives. `Complete` uses the model the operator already configured, instead of a second API key that can drift from the server's. `Search` queries the deployment's knowledge base, so an answer is grounded in the same corpus the agent uses.

All three take a session, because they act on one conversation. Inside a tool call, pass the one the call carried.

## Concurrency

Requests are handled on their own goroutines, so a slow tool does not block the ones behind it, and a handler may call back into the server while still holding the call it is answering. Replies are matched by id in both directions.

## Not a Go plugin in the `plugin.Open` sense

This SDK builds a **subprocess**, not a `.so` loaded into the server. Dynamic Go plugins require the identical Go version, identical versions of every shared dependency, and identical build flags as the host, and fail at runtime on any mismatch. A subprocess has none of those constraints, works on every platform, and can be restarted without restarting the server.

## Rules

- **stdout** is the protocol. Never `fmt.Println` — use `plugin.Logf`, which writes to stderr and lands in the server log.
- `plugin.Run()` must be the last call; it blocks until the server closes stdin.
- Return errors rather than panicking. An error becomes something the agent can say; a panic is a dead plugin.

See the [Plugin Development Guide](https://github.com/streamcoreai/streamcore-server/blob/main/docs/plugins.md) for manifests, dispatch tools, events, and the wire protocol.
