/**
 * StreamCoreAI Plugin SDK for TypeScript/JavaScript.
 *
 * A plugin is a process the server starts and talks to over stdio in JSON-RPC.
 * Write a handler, call run(), and point a plugin.yaml at the entrypoint:
 *
 *     import { StreamCoreAIPlugin } from "@streamcore/plugin";
 *
 *     const plugin = new StreamCoreAIPlugin();
 *     plugin.onExecute(async (params) => `It is sunny in ${params.location}`);
 *     plugin.run();
 *
 * Return a string to have the agent say it. Return a Result to also push
 * packets at the caller's device, which is how a plugin drives hardware without
 * anything being added to the server.
 *
 * stderr is available for logging and is folded into the server's own log.
 */

import * as readline from "readline";

/**
 * The protocol version this SDK speaks. The server announces its own at
 * initialize; a mismatch is not fatal, since both sides ignore what they do not
 * recognise.
 */
export const PROTOCOL = 2;

/** One tool invocation. */
export interface Call {
  /** Which of the plugin's tools was called. A single-tool plugin can ignore it. */
  tool: string;
  /** The conversation the call belongs to. Use it to keep two callers apart. */
  sessionId: string;
  params: Record<string, unknown>;
}

/** A server lifecycle event, for a manifest that declares `events:`. */
export interface Event {
  type: string;
  sessionId: string;
  turnId: string;
  turnSeq: number;
  data: unknown;
}

/**
 * The handshake. `config` is this plugin's table from the server's config.toml,
 * so credentials live in the operator's config rather than in a dotenv beside
 * the plugin.
 */
export interface Init {
  plugin: string;
  protocol: number;
  config: Record<string, unknown>;
}

/**
 * Arrives once every plugin has started, listing the tools the server ended up
 * with. It is the moment a plugin can decide what to offer knowing which peers
 * exist.
 */
export interface Ready {
  tools: string[];
  /** Whether a tool is loaded. */
  has(name: string): boolean;
}

/** One topic-addressed packet bound for the client. */
export interface Emission {
  topic: string;
  payload?: unknown;
  session_id?: string;
}

/** Words for the agent to say, and packets for the caller's device. */
export interface Result {
  speak?: string;
  emit?: Emission[];
}

/**
 * A callable tool. Returning tools from the initialize handler declares them at
 * startup, which is how a plugin whose surface depends on its configuration
 * advertises itself.
 */
export interface Tool {
  name: string;
  description?: string;
  parameters?: unknown;
  confirmation_required?: boolean;
  /** Keeps a tool out of the model's view while leaving it callable by other plugins. */
  internal?: boolean;
  confirmation_prompt?: string;
  thinking_sound?: boolean;
}

type ExecuteHandler = (
  params: Record<string, unknown>,
  call: Call
) => unknown | Promise<unknown>;
type EventHandler = (event: Event) => unknown | Promise<unknown>;
type InitializeHandler = (
  init: Init
) => void | Tool[] | Promise<void | Tool[]>;
type ReadyHandler = (ready: Ready) => void | Tool[] | Promise<void | Tool[]>;
type ConfirmHandler = (call: Call) => string | Promise<string>;

interface RPCMessage {
  jsonrpc?: string;
  method?: string;
  params?: Record<string, unknown>;
  tool?: string;
  session_id?: string;
  result?: unknown;
  error?: { code: number; message: string };
  id?: number | string | null;
}

export class StreamCoreAIPlugin {
  private executeHandler: ExecuteHandler | null = null;
  private eventHandler: EventHandler | null = null;
  private initHandler: InitializeHandler | null = null;
  private readyHandler: ReadyHandler | null = null;
  private confirmHandler: ConfirmHandler | null = null;

  private pendingRequests = 0;
  private stdinClosed = false;

  private nextId = 0;
  private pending = new Map<string, (message: RPCMessage) => void>();

  /** Register the tool handler — called when the model invokes this plugin. */
  onExecute(handler: ExecuteHandler): void {
    this.executeHandler = handler;
  }

  /** Register the lifecycle handler, for a manifest declaring `events:`. */
  onEvent(handler: EventHandler): void {
    this.eventHandler = handler;
  }

  /**
   * Register the startup handler. Returning tools declares them; returning
   * nothing keeps whatever the manifest listed.
   */
  onInitialize(handler: InitializeHandler): void {
    this.initHandler = handler;
  }

  /**
   * Register the second declaration pass, called once every plugin has
   * started. Returning tools replaces what was declared at initialize.
   *
   * It exists because a plugin cannot answer "what do I offer" in isolation
   * when the answer depends on another plugin being loaded.
   */
  onReady(handler: ReadyHandler): void {
    this.readyHandler = handler;
  }

  /**
   * Register the handler that describes a pending action in the words the
   * agent should say out loud, for a confirmation-gated tool.
   */
  onConfirm(handler: ConfirmHandler): void {
    this.confirmHandler = handler;
  }

  /** Log to stderr, which the server folds into its own log. */
  log(message: string): void {
    process.stderr.write(message + "\n");
  }

  /**
   * Push a packet at a conversation's client without having been asked for one
   * — a sensor reading, a timer firing, progress on a long job.
   */
  async emit(sessionId: string, topic: string, payload?: unknown): Promise<void> {
    if (!sessionId) throw new Error("emit: sessionId is required");
    await this.request("emit", { topic, payload, session_id: sessionId });
  }

  /**
   * Run another plugin's tool. The server routes it, so neither plugin has to
   * know where the other lives.
   */
  async callTool(
    sessionId: string,
    name: string,
    args?: unknown
  ): Promise<string> {
    const result = await this.request("tools/call", {
      name,
      arguments: args,
      session_id: sessionId,
    });
    return asText(result);
  }

  /**
   * Ask the server's configured model for a one-shot completion, so a plugin
   * that needs a model does not carry a second API key.
   */
  async complete(
    sessionId: string,
    prompt: string,
    system = ""
  ): Promise<string> {
    const result = await this.request("llm/complete", {
      system,
      prompt,
      session_id: sessionId,
    });
    return asText(result);
  }

  /**
   * Query the deployment's knowledge base, so a plugin can ground an answer in
   * the same corpus the agent uses rather than standing up a second retrieval
   * stack beside it. A limit of 0 uses the server's default.
   */
  async search(sessionId: string, query: string, limit = 0): Promise<string[]> {
    const result = await this.request("rag/search", {
      query,
      limit,
      session_id: sessionId,
    });
    return (result as string[]) ?? [];
  }

  /** Start the stdio loop. Blocks until stdin closes; must be the last call. */
  run(): void {
    const rl = readline.createInterface({ input: process.stdin, terminal: false });

    rl.on("line", (line: string) => {
      line = line.trim();
      if (!line) return;

      let message: RPCMessage;
      try {
        message = JSON.parse(line);
      } catch {
        this.log(`unparseable input: ${line}`);
        return;
      }

      // No method means this is a reply to something we asked for.
      if (!message.method) {
        this.deliver(message);
        return;
      }
      this.dispatch(message);
    });

    rl.on("close", () => {
      this.stdinClosed = true;
      this.exitIfDone();
    });
  }

  private dispatch(request: RPCMessage): void {
    const id = request.id ?? null;
    this.pendingRequests++;

    const run = async () => {
      try {
        const result = await this.handle(request);
        if (id !== null) this.send({ jsonrpc: "2.0", result, id });
      } catch (err: unknown) {
        const message = err instanceof Error ? err.message : String(err);
        if (id !== null) {
          this.send({ jsonrpc: "2.0", error: { code: -32000, message }, id });
        } else {
          this.log(`${request.method}: ${message}`);
        }
      } finally {
        this.pendingRequests--;
        this.exitIfDone();
      }
    };
    run();
  }

  private async handle(request: RPCMessage): Promise<unknown> {
    const params = request.params ?? {};

    switch (request.method) {
      case "initialize": {
        let tools: void | Tool[] = undefined;
        if (this.initHandler) {
          tools = await this.initHandler({
            plugin: String(params.plugin ?? ""),
            protocol: Number(params.protocol ?? PROTOCOL),
            config: (params.config as Record<string, unknown>) ?? {},
          });
        }
        if (Array.isArray(tools) && tools.length > 0) return { tools };
        return "initialized";
      }

      case "execute": {
        if (!this.executeHandler) throw new Error("no execute handler registered");
        const call: Call = {
          tool: request.tool ?? "",
          sessionId: request.session_id ?? "",
          params,
        };
        return encode(await this.executeHandler(params, call));
      }

      case "ready": {
        if (!this.readyHandler) return "ok";
        const tools = await this.readyHandler({
          tools: (params.tools as string[]) ?? [],
          has(name: string) {
            return this.tools.includes(name);
          },
        });
        if (Array.isArray(tools) && tools.length > 0) return { tools };
        return "ok";
      }

      case "confirm": {
        if (!this.confirmHandler) return "";
        return await this.confirmHandler({
          tool: request.tool ?? "",
          sessionId: request.session_id ?? "",
          params,
        });
      }

      case "event": {
        if (!this.eventHandler) return null;
        return encode(
          await this.eventHandler({
            type: String(params.type ?? ""),
            sessionId: String(params.session_id ?? ""),
            turnId: String(params.turn_id ?? ""),
            turnSeq: Number(params.turn_seq ?? 0),
            data: params.data,
          })
        );
      }

      default:
        throw new Error(`unknown method: ${request.method}`);
    }
  }

  private request(method: string, params: Record<string, unknown>): Promise<unknown> {
    const id = `sdk-${++this.nextId}`;
    return new Promise((resolve, reject) => {
      this.pending.set(id, (message: RPCMessage) => {
        if (message.error) {
          reject(new Error(`${method}: ${message.error.message}`));
          return;
        }
        resolve(message.result);
      });
      this.send({ jsonrpc: "2.0", method, params, id });
    });
  }

  private deliver(message: RPCMessage): void {
    const id = String(message.id ?? "");
    const settle = this.pending.get(id);
    if (!settle) {
      this.log(`reply to unknown request ${id}`);
      return;
    }
    this.pending.delete(id);
    settle(message);
  }

  private send(message: RPCMessage): void {
    process.stdout.write(JSON.stringify(message) + "\n");
  }

  private exitIfDone(): void {
    if (this.stdinClosed && this.pendingRequests === 0 && this.pending.size === 0) {
      process.exit(0);
    }
  }
}

/**
 * Render a handler's return value for the wire. A Result becomes an envelope;
 * anything else is spoken text.
 */
function encode(value: unknown): unknown {
  if (value === null || value === undefined) return null;
  if (typeof value === "object") {
    const candidate = value as Result;
    if (candidate.speak !== undefined || candidate.emit !== undefined) {
      return candidate;
    }
  }
  return String(value);
}

function asText(result: unknown): string {
  if (typeof result === "string") return result;
  return JSON.stringify(result);
}
