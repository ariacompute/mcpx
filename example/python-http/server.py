#!/usr/bin/env python3
"""Example upstream service: a tiny notes API, standard library only.

mcpx turns HTTP services into MCP tools, so this is the "any HTTP service" half
of the example. Nothing here knows about MCP: it is a plain REST service on the
loopback interface, and `../mcpx.example.yaml` is what makes `notes_list`,
`notes_get`, `notes_create` and `notes_delete` appear as tools.

    python3 example/python-http/server.py --port 18081

Routes:

    GET    /healthz            liveness document, used by the test scripts
    GET    /api/notes?tag=     list notes, optionally filtered by tag
    GET    /api/notes/{id}     one note, 404 when it does not exist
    POST   /api/notes          {"text": "...", "tag": "..."} -> 201 + the note
    DELETE /api/notes/{id}     204, 404 when it does not exist

State is in memory: restart the process and the notes are the two seeds again.
Logs go to stderr and stdout is left untouched, so the process can be run
through a pipe without polluting it.
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import re
import signal
import sys
import threading
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Dict, List, Optional, Sequence, Tuple
from urllib.parse import parse_qs, unquote, urlsplit

LOG = logging.getLogger("notes-example")

# /api/notes/<id>, where <id> may not contain a slash. Anything else is a 404.
NOTE_PATH = re.compile(r"^/api/notes/(?P<id>[^/]+)$")

SEED_NOTES = (("buy milk", "home"), ("ship the gateway", "work"))


class NoteStore:
    """In-memory notes behind a lock.

    A real service would use a database; the point of this file is the wire
    format mcpx sends and the status codes it maps to `isError`, so a dict is
    enough and keeps the example dependency-free.
    """

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._next = 1
        self._notes: Dict[str, Dict[str, Any]] = {}
        for text, tag in SEED_NOTES:
            self.add(text, tag)

    def add(self, text: str, tag: str) -> Dict[str, Any]:
        with self._lock:
            note_id = str(self._next)
            self._next += 1
            note = {"id": note_id, "text": text, "tag": tag}
            self._notes[note_id] = note
            return dict(note)

    def list(self, tag: Optional[str]) -> List[Dict[str, Any]]:
        with self._lock:
            notes = [dict(note) for note in self._notes.values()]
        if tag:
            notes = [note for note in notes if note["tag"] == tag]
        return notes

    def get(self, note_id: str) -> Optional[Dict[str, Any]]:
        with self._lock:
            note = self._notes.get(note_id)
            return dict(note) if note else None

    def delete(self, note_id: str) -> bool:
        with self._lock:
            return self._notes.pop(note_id, None) is not None

    def count(self) -> int:
        with self._lock:
            return len(self._notes)


class NotesHandler(BaseHTTPRequestHandler):
    """One request per method; every answer is JSON with an explicit status."""

    # Injected by main(). A class attribute keeps the handler signature as
    # http.server defines it instead of wrapping the constructor.
    store: NoteStore = None  # type: ignore[assignment]

    protocol_version = "HTTP/1.1"
    server_version = "notes-example/1"

    # ------------------------------------------------------------- responses --

    def _send_json(self, status: int, payload: Any) -> None:
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _send_error_json(self, status: int, code: str, message: str) -> None:
        self._send_json(status, {"error": {"code": code, "message": message}})

    def _send_no_content(self) -> None:
        self.send_response(HTTPStatus.NO_CONTENT)
        # HTTP/1.1 must say how long the (empty) body is, or the client waits
        # for one that never arrives.
        self.send_header("Content-Length", "0")
        self.end_headers()

    # ----------------------------------------------------------------- routes --

    def do_GET(self) -> None:  # noqa: N802 - http.server's naming
        url = urlsplit(self.path)

        if url.path == "/healthz":
            self._send_json(HTTPStatus.OK, {"status": "ok", "notes": self.store.count()})
            return

        if url.path == "/api/notes":
            values = parse_qs(url.query).get("tag") or []
            self._send_json(HTTPStatus.OK, {"notes": self.store.list(values[0] if values else None)})
            return

        match = NOTE_PATH.match(url.path)
        if match:
            note = self.store.get(unquote(match.group("id")))
            if note is None:
                self._send_error_json(HTTPStatus.NOT_FOUND, "not_found", "no note with that id")
                return
            self._send_json(HTTPStatus.OK, note)
            return

        self._send_error_json(HTTPStatus.NOT_FOUND, "not_found", f"no route for {url.path}")

    def do_POST(self) -> None:  # noqa: N802
        url = urlsplit(self.path)
        if url.path != "/api/notes":
            self._send_error_json(HTTPStatus.NOT_FOUND, "not_found", f"no route for {url.path}")
            return

        media_type = (self.headers.get("Content-Type") or "").split(";")[0].strip().lower()
        if media_type != "application/json":
            self._send_error_json(
                HTTPStatus.UNSUPPORTED_MEDIA_TYPE,
                "unsupported_media_type",
                "Content-Type must be application/json",
            )
            return

        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b""
        try:
            body = json.loads(raw or b"{}")
        except json.JSONDecodeError as exc:
            self._send_error_json(HTTPStatus.BAD_REQUEST, "invalid_json", str(exc))
            return

        text, tag, problem = _parse_note(body)
        if problem:
            self._send_error_json(HTTPStatus.BAD_REQUEST, "invalid_note", problem)
            return

        self._send_json(HTTPStatus.CREATED, self.store.add(text, tag))

    def do_DELETE(self) -> None:  # noqa: N802
        match = NOTE_PATH.match(urlsplit(self.path).path)
        if not match:
            self._send_error_json(HTTPStatus.NOT_FOUND, "not_found", "no route for that path")
            return
        if not self.store.delete(unquote(match.group("id"))):
            self._send_error_json(HTTPStatus.NOT_FOUND, "not_found", "no note with that id")
            return
        self._send_no_content()

    def _method_not_allowed(self) -> None:
        self._send_error_json(
            HTTPStatus.METHOD_NOT_ALLOWED,
            "method_not_allowed",
            f"{self.command} is not supported by this example",
        )

    # http.server dispatches do_<METHOD>; the rest answer 405 instead of 501 so
    # the caller can tell "wrong verb" from "not implemented".
    do_PUT = _method_not_allowed  # noqa: N815
    do_PATCH = _method_not_allowed  # noqa: N815
    do_HEAD = _method_not_allowed  # noqa: N815

    # ------------------------------------------------------------------ logs --

    def log_message(self, fmt: str, *args: Any) -> None:
        # BaseHTTPRequestHandler prints to stderr by default; routing it through
        # logging keeps the lines formatted like the startup line.
        LOG.info("%s %s", self.address_string(), fmt % args)


def _parse_note(body: Any) -> Tuple[str, str, Optional[str]]:
    """Validate a POST body, returning (text, tag, problem)."""
    if not isinstance(body, dict):
        return "", "", 'expected a JSON object like {"text": "...", "tag": "..."}'

    text = body.get("text")
    if not isinstance(text, str) or not text.strip():
        return "", "", '"text" must be a non-empty string'

    tag = body.get("tag", "general")
    if not isinstance(tag, str):
        return "", "", '"tag" must be a string when present'

    return text.strip(), (tag.strip() or "general"), None


def parse_args(argv: Optional[Sequence[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Example notes service for mcpx.")
    parser.add_argument("--host", default="127.0.0.1", help="bind address (default 127.0.0.1)")
    parser.add_argument("--port", type=int, default=18081, help="bind port (default 18081)")
    return parser.parse_args(argv)


def main(argv: Optional[Sequence[str]] = None) -> int:
    args = parse_args(argv)
    logging.basicConfig(
        level=logging.INFO,
        stream=sys.stderr,
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )

    NotesHandler.store = NoteStore()
    try:
        httpd = ThreadingHTTPServer((args.host, args.port), NotesHandler)
    except OSError as exc:
        LOG.error("cannot listen on %s:%d: %s", args.host, args.port, exc)
        return 1

    httpd.daemon_threads = True
    LOG.info(
        "notes example listening on http://%s:%d (pid %d)",
        args.host,
        args.port,
        os.getpid(),
    )

    def _shutdown(signum: int, _frame: Any) -> None:
        # shutdown() blocks until serve_forever returns, so it cannot run on the
        # thread that is inside serve_forever.
        LOG.info("signal %s received, shutting down", signal.Signals(signum).name)
        threading.Thread(target=httpd.shutdown, daemon=True).start()

    signal.signal(signal.SIGINT, _shutdown)
    signal.signal(signal.SIGTERM, _shutdown)

    try:
        httpd.serve_forever()
    finally:
        httpd.server_close()
        LOG.info("notes example stopped")
    return 0


if __name__ == "__main__":
    sys.exit(main())
