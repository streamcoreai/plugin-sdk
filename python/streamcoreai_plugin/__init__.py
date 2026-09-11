"""
StreamCoreAI Plugin SDK for Python.

A plugin is a process the server starts and talks to over stdio in JSON-RPC.
Write a handler, call run(), and point a plugin.yaml at the entrypoint:

    from streamcoreai_plugin import StreamCoreAIPlugin

    plugin = StreamCoreAIPlugin()

    @plugin.on_execute
    def handle(params):
        return "It is sunny in " + params["location"]

    plugin.run()

Return a string to have the agent say it. Return a Result to also push packets
at the caller's device, which is how a plugin drives hardware without anything
being added to the server.

stderr is available for logging and is folded into the server's own log.
"""

import inspect
import json
import queue
import sys
import threading
from dataclasses import dataclass, field
from typing import Any, Callable, Dict, List, Optional, Union

__all__ = [
    "StreamCoreAIPlugin",
    "Call",
    "Event",
    "Init",
    "Ready",
    "Result",
    "Emission",
    "Tool",
]

# Protocol is the version this SDK speaks. The server announces its own at
# initialize; a mismatch is not fatal, since both sides ignore what they do not
# recognise.
PROTOCOL = 2


@dataclass
class Call:
    """One tool invocation."""

    tool: str
    session_id: str
    params: Dict[str, Any]


@dataclass
class Event:
    """A server lifecycle event, for a manifest that declares `events:`."""

    type: str
    session_id: str
    turn_id: str
    turn_seq: int
    data: Any


@dataclass
class Init:
    """The handshake. `config` is this plugin's table from the server's
    config.toml, so credentials live in the operator's config rather than in a
    dotenv beside the plugin."""

    plugin: str
    protocol: int
    config: Dict[str, Any]


@dataclass
class Ready:
    """Arrives once every plugin has started, listing the tools the server
    ended up with. It is the moment a plugin can decide what to offer knowing
    which peers exist."""

    tools: List[str]

    def has(self, name: str) -> bool:
        """Whether a tool is loaded, for deciding to offer something that only
        makes sense alongside another plugin."""
        return name in self.tools


@dataclass
class Emission:
    """One topic-addressed packet bound for the client."""

    topic: str
    payload: Any = None
    session_id: Optional[str] = None

    def to_json(self) -> Dict[str, Any]:
        out: Dict[str, Any] = {"topic": self.topic}
        if self.payload is not None:
            out["payload"] = self.payload
        if self.session_id:
            out["session_id"] = self.session_id
        return out


@dataclass
class Result:
    """Words for the agent to say, and packets for the caller's device."""

    speak: str = ""
    emit: List[Emission] = field(default_factory=list)

    def to_json(self) -> Dict[str, Any]:
        out: Dict[str, Any] = {}
        if self.speak:
            out["speak"] = self.speak
        if self.emit:
            out["emit"] = [e.to_json() for e in self.emit]
        return out


@dataclass
class Tool:
    """A callable tool. Returning tools from the initialize handler declares
    them at startup, which is how a plugin whose surface depends on its
    configuration advertises itself."""

    name: str
    description: str = ""
    parameters: Optional[Dict[str, Any]] = None
    confirmation_required: bool = False
    internal: bool = False
    confirmation_prompt: str = ""
    thinking_sound: bool = False

    def to_json(self) -> Dict[str, Any]:
        out: Dict[str, Any] = {"name": self.name, "description": self.description}
        if self.parameters is not None:
            out["parameters"] = self.parameters
        if self.confirmation_required:
            out["confirmation_required"] = True
        if self.internal:
            out["internal"] = True
        if self.confirmation_prompt:
            out["confirmation_prompt"] = self.confirmation_prompt
        if self.thinking_sound:
            out["thinking_sound"] = True
        return out


class StreamCoreAIPlugin:
    """Base class for voice agent plugins."""

    def __init__(self) -> None:
        self._execute_handler: Optional[Callable] = None
        self._event_handler: Optional[Callable] = None
        self._init_handler: Optional[Callable] = None
        self._ready_handler: Optional[Callable] = None
        self._confirm_handler: Optional[Callable] = None

        self._write_lock = threading.Lock()
        self._pending: Dict[str, "queue.Queue[dict]"] = {}
        self._pending_lock = threading.Lock()
        self._next_id = 0

    # -- handler registration ------------------------------------------------

    def on_execute(self, func: Callable) -> Callable:
        """Register the tool handler.

        The handler takes the arguments object. A plugin hosting several tools
        can take a second parameter to receive the Call, which carries the tool
        name and the session.
        """
        self._execute_handler = func
        return func

    def on_event(self, func: Callable) -> Callable:
        """Register the lifecycle handler, for a manifest declaring `events:`."""
        self._event_handler = func
        return func

    def on_initialize(self, func: Callable) -> Callable:
        """Register the startup handler.

        The handler may take no arguments, as before, or one to receive the
        Init. Returning a list of Tool declares them; returning nothing keeps
        whatever the manifest listed.
        """
        self._init_handler = func
        return func

    def on_ready(self, func: Callable) -> Callable:
        """Register the second declaration pass, called once every plugin has
        started. Returning a list of Tool replaces what was declared at
        initialize.

        It exists because a plugin cannot answer "what do I offer" in isolation
        when the answer depends on another plugin being loaded.
        """
        self._ready_handler = func
        return func

    def on_confirm(self, func: Callable) -> Callable:
        """Register the handler that describes a pending action in the words
        the agent should say out loud, for a confirmation-gated tool."""
        self._confirm_handler = func
        return func

    # -- talking back to the server ------------------------------------------

    def log(self, message: str) -> None:
        """Log to stderr, which the server folds into its own log."""
        print(message, file=sys.stderr, flush=True)

    def emit(self, session_id: str, topic: str, payload: Any = None) -> None:
        """Push a packet at a conversation's client without having been asked
        for one — a sensor reading, a timer firing, progress on a long job."""
        if not session_id:
            raise ValueError("emit: session_id is required")
        self._request(
            "emit",
            {"topic": topic, "payload": payload, "session_id": session_id},
        )

    def call_tool(self, session_id: str, name: str, arguments: Any = None) -> str:
        """Run another plugin's tool. The server routes it, so neither plugin
        has to know where the other lives."""
        return _as_text(
            self._request(
                "tools/call",
                {"name": name, "arguments": arguments, "session_id": session_id},
            )
        )

    def complete(self, session_id: str, prompt: str, system: str = "") -> str:
        """Ask the server's configured model for a one-shot completion, so a
        plugin that needs a model does not carry a second API key."""
        return _as_text(
            self._request(
                "llm/complete",
                {"system": system, "prompt": prompt, "session_id": session_id},
            )
        )

    def search(self, session_id: str, query: str, limit: int = 0) -> List[str]:
        """Query the deployment's knowledge base, so a plugin can ground an
        answer in the same corpus the agent uses. A limit of 0 uses the
        server's default."""
        result = self._request(
            "rag/search",
            {"query": query, "limit": limit, "session_id": session_id},
        )
        return result or []

    # -- main loop -----------------------------------------------------------

    def run(self) -> None:
        """Start the stdio loop. Blocks until stdin closes; must be the last call."""
        for line in sys.stdin:
            line = line.strip()
            if not line:
                continue

            try:
                message = json.loads(line)
            except json.JSONDecodeError as e:
                self.log(f"unparseable input: {e}")
                continue

            # No method means this is a reply to something we asked for.
            if "method" not in message:
                self._deliver(message)
                continue

            # Each request runs on its own thread so a slow tool does not block
            # the ones behind it, and so a handler may call back into the
            # server while still holding the call it is answering.
            threading.Thread(target=self._dispatch, args=(message,), daemon=True).start()

    def _dispatch(self, request: dict) -> None:
        req_id = request.get("id")
        try:
            result = self._handle(request)
        except Exception as e:  # a broken handler must not take the plugin down
            if req_id is not None:
                self._send({"jsonrpc": "2.0", "error": {"code": -32000, "message": str(e)}, "id": req_id})
            else:
                self.log(f"{request.get('method')}: {e}")
            return

        if req_id is not None:
            self._send({"jsonrpc": "2.0", "result": result, "id": req_id})

    def _handle(self, request: dict) -> Any:
        method = request.get("method", "")
        params = request.get("params") or {}

        if method == "initialize":
            tools = None
            if self._init_handler is not None:
                init = Init(
                    plugin=params.get("plugin", ""),
                    protocol=params.get("protocol", PROTOCOL),
                    config=params.get("config") or {},
                )
                tools = _call_with_optional_arg(self._init_handler, init)
            if tools:
                return {"tools": [t.to_json() if isinstance(t, Tool) else t for t in tools]}
            return "initialized"

        if method == "execute":
            if self._execute_handler is None:
                raise RuntimeError("no execute handler registered")
            call = Call(
                tool=request.get("tool", ""),
                session_id=request.get("session_id", ""),
                params=params,
            )
            if _arity(self._execute_handler) >= 2:
                return _encode(self._execute_handler(params, call))
            return _encode(self._execute_handler(params))

        if method == "ready":
            tools = None
            if self._ready_handler is not None:
                tools = self._ready_handler(Ready(tools=params.get("tools") or []))
            if tools:
                return {"tools": [t.to_json() if isinstance(t, Tool) else t for t in tools]}
            return "ok"

        if method == "confirm":
            if self._confirm_handler is None:
                return ""
            call = Call(
                tool=request.get("tool", ""),
                session_id=request.get("session_id", ""),
                params=params,
            )
            return self._confirm_handler(call)

        if method == "event":
            if self._event_handler is None:
                return None
            event = Event(
                type=params.get("type", ""),
                session_id=params.get("session_id", ""),
                turn_id=params.get("turn_id", ""),
                turn_seq=params.get("turn_seq", 0),
                data=params.get("data"),
            )
            return _encode(self._event_handler(event))

        raise RuntimeError(f"unknown method: {method}")

    def _request(self, method: str, params: dict) -> Any:
        with self._pending_lock:
            self._next_id += 1
            request_id = f"sdk-{self._next_id}"
            reply: "queue.Queue[dict]" = queue.Queue(maxsize=1)
            self._pending[request_id] = reply

        try:
            self._send({"jsonrpc": "2.0", "method": method, "params": params, "id": request_id})
            response = reply.get()
        finally:
            with self._pending_lock:
                self._pending.pop(request_id, None)

        if response.get("error"):
            raise RuntimeError(f"{method}: {response['error'].get('message')}")
        return response.get("result")

    def _deliver(self, response: dict) -> None:
        with self._pending_lock:
            reply = self._pending.get(response.get("id"))
        if reply is None:
            self.log(f"reply to unknown request {response.get('id')}")
            return
        reply.put(response)

    def _send(self, message: dict) -> None:
        with self._write_lock:
            sys.stdout.write(json.dumps(message) + "\n")
            sys.stdout.flush()


def _encode(value: Any) -> Any:
    """Render a handler's return value for the wire. A Result becomes an
    envelope; anything else is spoken text."""
    if value is None:
        return None
    if isinstance(value, Result):
        return value.to_json()
    if isinstance(value, dict) and ("speak" in value or "emit" in value):
        return value
    return str(value)


def _as_text(result: Any) -> str:
    if isinstance(result, str):
        return result
    return json.dumps(result)


def _arity(func: Callable) -> int:
    try:
        return len(inspect.signature(func).parameters)
    except (TypeError, ValueError):
        return 1


def _call_with_optional_arg(func: Callable, argument: Any) -> Any:
    if _arity(func) >= 1:
        return func(argument)
    return func()
