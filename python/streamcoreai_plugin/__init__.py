"""
StreamCoreAI Plugin SDK for Python.

Provides the base class for building voice agent plugins that communicate
with the server via JSON-RPC 2.0 over stdio.
"""

import json
import sys
from typing import Any, Callable, Optional


class StreamCoreAIPlugin:
    """Base class for voice agent plugins.

    Plugins communicate with the server via JSON-RPC 2.0 over stdin/stdout.
    stderr is available for logging.

    Usage:
        plugin = StreamCoreAIPlugin()

        @plugin.on_execute
        def handle(params):
            return "Result string"

        plugin.run()
    """

    def __init__(self) -> None:
        self._execute_handler: Optional[Callable[[dict], str]] = None
        self._init_handler: Optional[Callable[[], None]] = None

    def on_execute(self, func: Callable[[dict], str]) -> Callable[[dict], str]:
        """Decorator to register the execute handler."""
        self._execute_handler = func
        return func

    def on_initialize(self, func: Callable[[], None]) -> Callable[[], None]:
        """Decorator to register an optional initialization handler."""
        self._init_handler = func
        return func

    def log(self, message: str) -> None:
        """Log a message to stderr (visible in server logs)."""
        print(message, file=sys.stderr, flush=True)

    def run(self) -> None:
        """Start the JSON-RPC message loop. Blocks until stdin is closed."""
        for line in sys.stdin:
            line = line.strip()
            if not line:
                continue

            try:
                request = json.loads(line)
            except json.JSONDecodeError as e:
                self._send_error(-32700, f"Parse error: {e}", None)
                continue

            req_id = request.get("id")
            method = request.get("method", "")

            try:
                if method == "initialize":
                    if self._init_handler:
                        self._init_handler()
                    self._send_result("initialized", req_id)

                elif method == "execute":
                    if self._execute_handler is None:
                        self._send_error(
                            -32601, "No execute handler registered", req_id
                        )
                        continue

                    params = request.get("params", {})
                    result = self._execute_handler(params)
                    self._send_result(str(result), req_id)

                else:
                    self._send_error(-32601, f"Unknown method: {method}", req_id)

            except Exception as e:
                self._send_error(-1, str(e), req_id)

    def _send_result(self, result: str, req_id: Any) -> None:
        response = {"jsonrpc": "2.0", "result": result, "id": req_id}
        print(json.dumps(response), flush=True)

    def _send_error(self, code: int, message: str, req_id: Any) -> None:
        response = {
            "jsonrpc": "2.0",
            "error": {"code": code, "message": message},
            "id": req_id,
        }
        print(json.dumps(response), flush=True)
