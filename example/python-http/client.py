#!/usr/bin/env python3
"""Example MCP client over Streamable HTTP, standard library only.

This is the caller's half of the HTTP example: it talks to a running mcpx and
nothing else. The gateway is started separately, with the config that publishes
the notes service:

    python3 example/python-http/server.py --port 18081 &
    ./bin/mcpx serve -c example/mcpx.example.yaml &
    python3 example/python-http/client.py --url http://127.0.0.1:18848/mcp

What it demonstrates, in order: the initialize handshake and the
`Mcp-Session-Id` response header every later request must carry, the
`notifications/initialized` notification (answered `202` with no body),
`tools/list` with cursor following, `tools/call` on both a successful and a
failing call, and `DELETE` to end the session (`204`).

Two failure modes are exercised on purpose, because they are the ones a client
has to tell apart:

    unknown tool        -> JSON-RPC error -32602 (the call never happened)
    missing argument    -> result with isError: true (the upstream was not called)

Results go to stdout, the running narrative to stderr.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.request
from typing import Any, Dict, List, Optional, Sequence, Tuple

DEFAULT_URL = "http://127.0.0.1:18848/mcp"
PROTOCOL_VERSION = "2025-06-18"

EXPECTED_TOOLS = {
    "notes_list",
    "notes_get",
    "notes_create",
    "notes_delete",
    "notes_request",
}


class McpError(RuntimeError):
    """A failed request: transport-level, or a JSON-RPC error envelope."""

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


class McpHttpClient:
    """One MCP session over POST {url}, per the Streamable HTTP transport."""

    def __init__(self, url: str, token: Optional[str] = None, timeout: float = 10.0) -> None:
        self._url = url
        self._token = token
        self._timeout = timeout
        self._session: Optional[str] = None
        self._next_id = 0

    # ------------------------------------------------------------- transport --

    def _headers(self) -> Dict[str, str]:
        # The gateway answers 415 without the JSON content type, and requires
        # the session header on everything after initialize.
        headers = {"Content-Type": "application/json", "Accept": "application/json"}
        if self._token:
            headers["Authorization"] = f"Bearer {self._token}"
        if self._session:
            headers["Mcp-Session-Id"] = self._session
        return headers

    def _post(self, payload: Dict[str, Any]) -> Tuple[int, Dict[str, str], str]:
        request = urllib.request.Request(
            self._url,
            data=json.dumps(payload).encode("utf-8"),
            headers=self._headers(),
            method="POST",
        )
        try:
            with urllib.request.urlopen(request, timeout=self._timeout) as response:
                body = response.read().decode("utf-8")
                return response.status, {k.lower(): v for k, v in response.headers.items()}, body
        except urllib.error.HTTPError as exc:
            body = exc.read().decode("utf-8", errors="replace")
            raise McpError(exc.code, f"HTTP {exc.code} {body}") from None
        except urllib.error.URLError as exc:
            raise McpError("transport", f"cannot reach {self._url}: {exc.reason}") from None

    def _delete(self) -> int:
        request = urllib.request.Request(self._url, headers=self._headers(), method="DELETE")
        try:
            with urllib.request.urlopen(request, timeout=self._timeout) as response:
                return response.status
        except urllib.error.HTTPError as exc:
            body = exc.read().decode("utf-8", errors="replace")
            raise McpError(exc.code, f"HTTP {exc.code} {body}") from None

    # ------------------------------------------------------------------- api --

    def initialize(self) -> Dict[str, Any]:
        self._next_id += 1
        status, headers, body = self._post(
            {
                "jsonrpc": "2.0",
                "id": self._next_id,
                "method": "initialize",
                "params": {
                    "protocolVersion": PROTOCOL_VERSION,
                    "capabilities": {},
                    "clientInfo": {"name": "mcpx-python-http-example", "version": "1.0.0"},
                },
            }
        )
        check(status == 200, f"initialize answered {status}, want 200")

        # Without this header every following request is a 400; it is the whole
        # of the session state, since the gateway keeps the rest server-side.
        self._session = headers.get("mcp-session-id")
        check(bool(self._session), "initialize response carried no Mcp-Session-Id")

        return self._result(body)

    def notify(self, method: str, params: Optional[Dict[str, Any]] = None) -> int:
        """Send a notification: no id, so the gateway answers 202 and no body."""
        payload: Dict[str, Any] = {"jsonrpc": "2.0", "method": method}
        if params is not None:
            payload["params"] = params
        status, _, body = self._post(payload)
        check(status == 202, f"{method} answered {status}, want 202")
        check(body.strip() == "", f"{method} answered with a body: {body!r}")
        return status

    def request(self, method: str, params: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
        self._next_id += 1
        payload: Dict[str, Any] = {"jsonrpc": "2.0", "id": self._next_id, "method": method}
        if params is not None:
            payload["params"] = params
        status, _, body = self._post(payload)
        check(status == 200, f"{method} answered {status}, want 200")
        return self._result(body)

    def list_tools(self) -> List[Dict[str, Any]]:
        tools: List[Dict[str, Any]] = []
        cursor: Optional[str] = None
        while True:
            result = self.request("tools/list", {"cursor": cursor} if cursor else None)
            tools.extend(result.get("tools", []))
            # The gateway pages at 50 by default, so this example sees one page;
            # the loop is here because a client has to follow nextCursor anyway.
            cursor = result.get("nextCursor")
            if not cursor:
                return tools

    def call_tool(self, name: str, arguments: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
        return self.request("tools/call", {"name": name, "arguments": arguments or {}})

    def close(self) -> int:
        if not self._session:
            return 0
        status = self._delete()
        self._session = None
        return status

    # ---------------------------------------------------------------- helper --

    @staticmethod
    def _result(body: str) -> Dict[str, Any]:
        try:
            document = json.loads(body)
        except json.JSONDecodeError:
            raise McpError("protocol", f"response is not JSON: {body[:200]!r}") from None
        if "error" in document:
            error = document["error"]
            raise McpError(error.get("code"), error.get("message", ""))
        return document.get("result", {})


def tool_text(result: Dict[str, Any]) -> str:
    """Join the text blocks of a tools/call result."""
    return "".join(block.get("text", "") for block in result.get("content", []) if block.get("type") == "text")


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = argparse.ArgumentParser(description="Example MCP client over Streamable HTTP.")
    parser.add_argument("--url", default=DEFAULT_URL, help=f"gateway endpoint (default {DEFAULT_URL})")
    parser.add_argument(
        "--token",
        default=os.environ.get("MCPX_TOKEN"),
        help="bearer token; defaults to $MCPX_TOKEN and is omitted when empty",
    )
    parser.add_argument("--timeout", type=float, default=10.0, help="per-request timeout in seconds")
    args = parser.parse_args(argv)

    client = McpHttpClient(args.url, token=args.token or None, timeout=args.timeout)

    try:
        log(f"initialize against {args.url}")
        handshake = client.initialize()
        server_info = handshake.get("serverInfo", {})
        emit(
            "initialize: protocolVersion={} serverInfo={}/{}".format(
                handshake.get("protocolVersion"),
                server_info.get("name", "?"),
                server_info.get("version", "?"),
            )
        )

        status = client.notify("notifications/initialized")
        emit(f"notifications/initialized -> {status} (no body)")

        tools = client.list_tools()
        names = sorted(tool["name"] for tool in tools)
        emit(f"tools/list -> {len(names)} tool(s): {', '.join(names)}")
        missing = EXPECTED_TOOLS - set(names)
        check(not missing, f"tools/list is missing {sorted(missing)}")

        log("notes_create: POST /api/notes through the gateway")
        created = client.call_tool("notes_create", {"note": {"text": "hello from the MCP client", "tag": "example"}})
        check(not created.get("isError"), f"notes_create failed: {tool_text(created)}")
        note = json.loads(tool_text(created))
        note_id = str(note["id"])
        emit(f"notes_create -> {json.dumps(note, ensure_ascii=False)}")

        log("notes_list: the query parameter is bound from the tool arguments")
        listed = client.call_tool("notes_list", {"tag": "example"})
        check(not listed.get("isError"), f"notes_list failed: {tool_text(listed)}")
        notes = json.loads(tool_text(listed))["notes"]
        emit(f"notes_list(tag=example) -> {len(notes)} note(s)")
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

        # Failure mode 1: the call never reaches an upstream, it is a protocol
        # error because the tool does not exist.
        log("expected failure: unknown tool")
        try:
            client.call_tool("notes_nope")
        except McpError as exc:
            check(exc.code == -32602, f"unknown tool answered {exc.code}, want -32602")
            emit(f"unknown tool -> {exc}")
        else:
            raise AssertionError("an unknown tool was expected to produce a -32602 error")

        # Failure mode 2: the tool exists, the arguments are wrong, so the
        # result is isError:true and the upstream is never called.
        log("expected failure: missing required argument")
        broken = client.call_tool("notes_get", {})
        check(broken.get("isError") is True, "a call missing a required argument must be isError")
        emit(f"missing argument -> isError: {tool_text(broken)}")

        status = client.close()
        emit(f"DELETE -> {status} (session closed)")

    except (McpError, AssertionError) as exc:
        print(f"FAILED: {exc}", file=sys.stderr)
        client.close()
        return 1

    emit("PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
