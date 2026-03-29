/**
 * StreamCoreAI Plugin SDK for TypeScript/JavaScript.
 *
 * Provides the base class for building voice agent plugins that communicate
 * with the server via JSON-RPC 2.0 over stdio.
 */

import * as readline from "readline";

interface JSONRPCRequest {
  jsonrpc: string;
  method: string;
  params?: Record<string, unknown>;
  id: number | string | null;
}

interface JSONRPCResponse {
  jsonrpc: string;
  result?: string;
  error?: { code: number; message: string };
  id: number | string | null;
}

type ExecuteHandler = (
  params: Record<string, unknown>
) => string | Promise<string>;
type InitializeHandler = () => void | Promise<void>;

export class StreamCoreAIPlugin {
  private executeHandler: ExecuteHandler | null = null;
  private initHandler: InitializeHandler | null = null;
  private pendingRequests = 0;
  private stdinClosed = false;

  /**
   * Register the execute handler — called when the LLM invokes this plugin.
   */
  onExecute(handler: ExecuteHandler): void {
    this.executeHandler = handler;
  }

  /**
   * Register an optional initialization handler.
   */
  onInitialize(handler: InitializeHandler): void {
    this.initHandler = handler;
  }

  /**
   * Log a message to stderr (visible in server logs).
   */
  log(message: string): void {
    process.stderr.write(message + "\n");
  }

  /**
   * Start the JSON-RPC message loop. Blocks until stdin is closed.
   */
  run(): void {
    const rl = readline.createInterface({
      input: process.stdin,
      terminal: false,
    });

    rl.on("line", (line: string) => {
      line = line.trim();
      if (!line) return;

      let request: JSONRPCRequest;
      try {
        request = JSON.parse(line);
      } catch {
        this.sendError(-32700, "Parse error", null);
        return;
      }

      const { method, params, id } = request;

      this.pendingRequests++;
      const handle = async () => {
        try {
          if (method === "initialize") {
            if (this.initHandler) {
              await this.initHandler();
            }
            this.sendResult("initialized", id);
          } else if (method === "execute") {
            if (!this.executeHandler) {
              this.sendError(-32601, "No execute handler registered", id);
              return;
            }
            const result = await this.executeHandler(params ?? {});
            this.sendResult(String(result), id);
          } else {
            this.sendError(-32601, `Unknown method: ${method}`, id);
          }
        } catch (err: unknown) {
          const message = err instanceof Error ? err.message : String(err);
          this.sendError(-1, message, id);
        } finally {
          this.pendingRequests--;
          this.exitIfDone();
        }
      };
      handle();
    });

    rl.on("close", () => {
      this.stdinClosed = true;
      this.exitIfDone();
    });
  }

  private sendResult(result: string, id: number | string | null): void {
    const response: JSONRPCResponse = { jsonrpc: "2.0", result, id };
    process.stdout.write(JSON.stringify(response) + "\n");
  }

  private sendError(
    code: number,
    message: string,
    id: number | string | null
  ): void {
    const response: JSONRPCResponse = {
      jsonrpc: "2.0",
      error: { code, message },
      id,
    };
    process.stdout.write(JSON.stringify(response) + "\n");
  }

  private exitIfDone(): void {
    if (this.stdinClosed && this.pendingRequests === 0) {
      process.exit(0);
    }
  }
}
