#!/usr/bin/env python3
"""Example MCP client over stdio, standard library only.

Same config, same tools, different transport: instead of POSTing to a running
gateway, this client starts one itself and speaks newline-delimited JSON-RPC on
its stdin/stdout — which is how a desktop MCP client launches a local server.

    python3 example/python-http/server.py --port 18081 &
    python3 example/python-stdio/client.py --mcpx ./bin/mcpx --config example/mcpx.example.yaml

Three things are worth copying out of this file:

* one JSON document per line, in both directions, with nothing else on stdout;
* the child's stderr is left inherited, so the gateway's JSON logs stay visible
  without ever being mistaken for protocol frames;
* closing the child's stdin is the shutdown signal — `mcpx stdio` exits on EOF,
  so there is no stop method to call.

Results go to stdout, the running narrative to stderr.
"""

from __future__ import annotations

import argparse
import json
import os
import queue
import subprocess
import sys
import threading
import time
from typing import Any, Dict, List, Optional, Sequence

DEFAULT_MCPX = os.environ.get("MCPX_BIN", "./bin/mcpx")
DEFAULT_CONFIG = "example/mcpx.example.yaml"
PROTOCOL_VERSION = "2025-06-18"

EXPECTED_TOOLS = {
    "notes_list",
    "notes_get",
    "notes_create",
    "notes_delete",
    "notes_request",
}


class McpError(RuntimeError):
    """A failed exchange: bad exit, timeout, or a JSON-RPC error envelope."""

    def __init__(self, code: Any, message: str) -> None:
        super().__init__(message)
        self.code = code

    def __str__(self) -> str:
        return f"{self.code}: {self.args[0]}"


def log(message: str) -> None:
    print(f"* {message}", file=sys.stderr, flush=True)


def emit(message: str) -> None:
    print(message, flush=True)


def check(condition: bool, message: str) -> None:
    if not condition:
        raise AssertionError(message)


class McpStdioClient:
    """One MCP session over the child process's stdin/stdout."""

    def __init__(self, mcpx: str, config: str, timeout: float = 10.0) -> None:
        self._timeout = timeout
        self._next_id = 0
        self._lines: "queue.Queue[Optional[str]]" = queue.Queue()

        # stderr is deliberately not piped: the gateway logs there, and a reader
        # who can see both streams is the point of the example.
        self._proc = subprocess.Popen(
            [mcpx, "stdio", "-c", config],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=None,
            text=True,
            bufsize=1,
        )

        # Reading on a thread turns "is there a line yet?" into a queue timeout.
        # Calling readline() directly would block forever if the child died
        # mid-request.
        self._reader = threading.Thread(target=self._pump, name="mcpx-stdout", daemon=True)
        self._reader.start()

    def _pump(self) -> None:
        assert self._proc.stdout is not None
        for line in self._proc.stdout:
            self._lines.put(line)
        # EOF: tell every waiter that nothing else is coming.
        self._lines.put(None)

    # ------------------------------------------------------------- transport --

    def _write(self, payload: Dict[str, Any]) -> None:
        assert self._proc.stdin is not None
        # One document per line, and the newline is the frame boundary: writing
        # two JSON objects without it would be one malformed frame.
        self._proc.stdin.write(json.dumps(payload) + "\n")
        self._proc.stdin.flush()

    def _read(self, request_id: int, deadline: float) -> Dict[str, Any]:
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise McpError("timeout", f"no answer to id {request_id} in {self._timeout}s")
            try:
                line = self._lines.get(timeout=remaining)
            except queue.Empty:
                raise McpError("timeout", f"no answer to id {request_id} in {self._timeout}s") from None

            if line is None:
                raise McpError("eof", f"gateway closed stdout before answering id {request_id}")
            line = line.strip()
            if not line:
                continue

            try:
                document = json.loads(line)
            except json.JSONDecodeError:
                raise McpError("protocol", f"stdout carried a non-JSON line: {line[:200]!r}") from None

            # Notifications from the server have no id; a client has to skip
            # them rather than mistake them for its answer.
            if document.get("id") != request_id:
                continue
            return document

    # ------------------------------------------------------------------- api --

    def request(self, method: str, params: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
        self._next_id += 1
        request_id = self._next_id
        payload: Dict[str, Any] = {"jsonrpc": "2.0", "id": request_id, "method": method}
        if params is not None:
            payload["params"] = params

        self._write(payload)
        document = self._read(request_id, time.monotonic() + self._timeout)
        if "error" in document:
            error = document["error"]
            raise McpError(error.get("code"), error.get("message", ""))
        return document.get("result", {})

    def notify(self, method: str, params: Optional[Dict[str, Any]] = None) -> None:
        """Send a notification: no id, so no answer is expected."""
        payload: Dict[str, Any] = {"jsonrpc": "2.0", "method": method}
        if params is not None:
            payload["params"] = params
        self._write(payload)

    def initialize(self) -> Dict[str, Any]:
        # No session header here: stdin/stdout *is* the session.
        return self.request(
            "initialize",
            {
                "protocolVersion": PROTOCOL_VERSION,
                "capabilities": {},
                "clientInfo": {"name": "mcpx-python-stdio-example", "version": "1.0.0"},
            },
        )

    def list_tools(self) -> List[Dict[str, Any]]:
        tools: List[Dict[str, Any]] = []
        cursor: Optional[str] = None
        while True:
            result = self.request("tools/list", {"cursor": cursor} if cursor else None)
            tools.extend(result.get("tools", []))
            cursor = result.get("nextCursor")
            if not cursor:
                return tools

    def call_tool(self, name: str, arguments: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
        return self.request("tools/call", {"name": name, "arguments": arguments or {}})

    def close(self) -> int:
        """EOF is the shutdown signal; fall back to terminate if it hangs."""
        proc = self._proc
        if proc.poll() is not None:
            return proc.returncode or 0
        if proc.stdin is not None:
            proc.stdin.close()
        try:
            return proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.terminate()
            try:
                return proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                proc.kill()
                return proc.wait()


def tool_text(result: Dict[str, Any]) -> str:
    """Join the text blocks of a tools/call result."""
    return "".join(block.get("text", "") for block in result.get("content", []) if block.get("type") == "text")


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = argparse.ArgumentParser(description="Example MCP client over stdio.")
    parser.add_argument("--mcpx", default=DEFAULT_MCPX, help=f"gateway binary (default {DEFAULT_MCPX})")
    parser.add_argument("--config", default=DEFAULT_CONFIG, help=f"gateway config (default {DEFAULT_CONFIG})")
    parser.add_argument("--timeout", type=float, default=10.0, help="per-request timeout in seconds")
    args = parser.parse_args(argv)

    if not os.path.exists(args.mcpx):
        print(f"FAILED: no gateway binary at {args.mcpx}; run `make build` first", file=sys.stderr)
        return 1

    log(f"starting {args.mcpx} stdio -c {args.config}")
    client = McpStdioClient(args.mcpx, args.config, timeout=args.timeout)

    try:
        handshake = client.initialize()
        server_info = handshake.get("serverInfo", {})
        emit(
            "initialize: protocolVersion={} serverInfo={}/{}".format(
                handshake.get("protocolVersion"),
                server_info.get("name", "?"),
                server_info.get("version", "?"),
            )
        )

        client.notify("notifications/initialized")
        emit("notifications/initialized -> written (no answer expected)")

        tools = client.list_tools()
        names = sorted(tool["name"] for tool in tools)
        emit(f"tools/list -> {len(names)} tool(s): {', '.join(names)}")
        missing = EXPECTED_TOOLS - set(names)
        check(not missing, f"tools/list is missing {sorted(missing)}")

        log("notes_create: POST /api/notes through the gateway")
        created = client.call_tool("notes_create", {"note": {"text": "hello over stdio", "tag": "stdio"}})
        check(not created.get("isError"), f"notes_create failed: {tool_text(created)}")
        note = json.loads(tool_text(created))
        note_id = str(note["id"])
        emit(f"notes_create -> {json.dumps(note, ensure_ascii=False)}")

        log("notes_list: the query parameter is bound from the tool arguments")
        listed = client.call_tool("notes_list", {"tag": "stdio"})
        check(not listed.get("isError"), f"notes_list failed: {tool_text(listed)}")
        notes = json.loads(tool_text(listed))["notes"]
        emit(f"notes_list(tag=stdio) -> {len(notes)} note(s)")
        check(any(str(entry["id"]) == note_id for entry in notes), "the new note is not in the filtered list")

        log("notes_get: the path template is filled from the id argument")
        fetched = client.call_tool("notes_get", {"id": note_id})
        check(not fetched.get("isError"), f"notes_get failed: {tool_text(fetched)}")
        check(json.loads(tool_text(fetched))["text"] == note["text"], "notes_get returned a different note")
        emit(f"notes_get({note_id}) -> {tool_text(fetched)}")

        log("notes_delete: 204 from the upstream becomes the '(empty body)' result")
        deleted = client.call_tool("notes_delete", {"id": note_id})
        check(not deleted.get("isError"), f"notes_delete failed: {tool_text(deleted)}")
        emit(f"notes_delete({note_id}) -> {tool_text(deleted)}")

        # Failure mode 1: the tool does not exist, so it is a protocol error and
        # no upstream is called.
        log("expected failure: unknown tool")
        try:
            client.call_tool("notes_nope")
        except McpError as exc:
            check(exc.code == -32602, f"unknown tool answered {exc.code}, want -32602")
            emit(f"unknown tool -> {exc}")
        else:
            raise AssertionError("an unknown tool was expected to produce a -32602 error")

        # Failure mode 2: the tool exists but the arguments are wrong, so the
        # result is isError:true.
        log("expected failure: missing required argument")
        broken = client.call_tool("notes_get", {})
        check(broken.get("isError") is True, "a call missing a required argument must be isError")
        emit(f"missing argument -> isError: {tool_text(broken)}")

        exit_code = client.close()
        emit(f"stdin closed -> gateway exited with {exit_code}")

    except (McpError, AssertionError) as exc:
        print(f"FAILED: {exc}", file=sys.stderr)
        client.close()
        return 1

    emit("PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
