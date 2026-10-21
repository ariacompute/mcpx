# Examples

[English](README.md) | [中文](README_cn.md)

One Python service, two ways to call it as MCP tools. Everything here is standard
library — no `pip install`, no network, no accounts.

| File | What it is |
| --- | --- |
| `mcpx.example.yaml` | The gateway config: publishes the notes service as five tools |
| `python-http/server.py` | The upstream service — a plain REST API, nothing MCP about it |
| `python-http/client.py` | MCP client over **Streamable HTTP** (POST `/mcp`, session header) |
| `python-stdio/client.py` | MCP client over **stdio** (starts `mcpx stdio`, newline-delimited JSON-RPC) |

The two clients talk to the same config, which is the point: the transport
changes, the tools do not.

## Run it

Terminal 1 — the upstream HTTP service:

```bash
python3 example/python-http/server.py --port 18081
```

Terminal 2 — the gateway (it refuses to start without a token):

```bash
make build                                     # produces bin/mcpx
# export MCPX_TOKEN=$(openssl rand -hex 24)
export MCPX_TOKEN=...                          # bearer token for the endpoint
./bin/mcpx serve -c example/mcpx.example.yaml
```

Terminal 3 — either client:

```bash
# Streamable HTTP, against the gateway from terminal 2 (takes --token from $MCPX_TOKEN)
python3 example/python-http/client.py --url http://127.0.0.1:18848/mcp

# stdio, which starts its own gateway (the one in terminal 2 stays unused).
# No token here: stdin/stdout is the transport, there is no network endpoint.
python3 example/python-stdio/client.py --mcpx ./bin/mcpx --config example/mcpx.example.yaml
```

Both print a short trace and end with `PASS`, or exit non-zero and say what
broke. Results go to stdout, the running narrative to stderr.

Or run the whole thing, including assertions and cleanup, in one command:

```bash
make example-test              # both transports
make example-test-http         # Streamable HTTP only
make example-test-stdio        # stdio only

EXAMPLE_UPSTREAM_PORT=28081 EXAMPLE_GATEWAY_PORT=28848 make example-test-http
KEEP=1 make example-test-http  # leave the processes running
```

## What the config shows

`mcpx.example.yaml` covers the four things a config needs to say:

- **Where the service is**: `upstreams[0].base_url` plus `allow_prefixes: [/api]`,
  which bounds every path this upstream may be asked for.
- **Which endpoints become tools**: each `operations[]` entry is one tool named
  `<upstream>_<operation>`, so `notes_list`, `notes_get`, `notes_create`,
  `notes_delete`.
- **How arguments bind**: `in: query` appends to the query string, `in: path`
  fills `{id}` (URL-escaped), `in: body` sends the whole value as the JSON body.
- **One generic tool**: `passthrough` adds `notes_request`, where the caller
  picks method and path but cannot leave `/api`.

`mcpx check -c example/mcpx.example.yaml` prints the five tools without starting
anything — that is also what the test scripts assert first.

## Things worth knowing

- **Ports**: the config hardcodes `18081` (upstream) and `18848` (gateway). The
  scripts rewrite the two lines into a temp copy when
  `EXAMPLE_UPSTREAM_PORT` / `EXAMPLE_GATEWAY_PORT` are set; the Python clients
  take their endpoint from `--port` / `--url`.
- **Bearer auth is on**: an empty `MCPX_TOKEN` makes the gateway exit with
  `authentication misconfigured`, which is deliberate — an endpoint that
  silently starts open because of a typo is the worse failure. The listener is
  loopback-only, so a disposable token is fine; the client's `--token` defaults
  to `$MCPX_TOKEN`. The test scripts default it to `ci-example-token` when the
  variable is unset, which is what keeps `make example-test` a single command.
- **The upstream hop carries no credential**: `upstreams[0].auth.scheme: none`
  because the notes service ignores `Authorization`; a bearer block there would
  make every call fail over a token nothing checks.
- **State is in memory**: the notes service seeds two notes and forgets
  everything else on restart.
- **Two failure modes are demonstrated on purpose**: an unknown tool is a
  JSON-RPC error (`-32602`), while a missing argument is a result with
  `isError: true`. A client has to tell them apart.
- **stdio stdout discipline**: the client's stdout carries only results; the
  gateway's JSON logs stay on stderr. `scripts/test-example-stdio.sh` asserts
  that, because one stray log line on stdout would corrupt every frame after it.
