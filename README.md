# mcpx

[English](README.md) | [中文](README_cn.md)

An MCP gateway: one declarative config publishes **arbitrary HTTP services** as MCP tools,
and exposes them through an MCP server (Streamable HTTP and stdio). The name follows
nginx's short-lowercase style — nginx is "engine-x", this is "MCP-x".

```
MCP client ──HTTP POST /mcp──┐
           ──stdio NDJSON────┤ mcpx ──HTTP──> your HTTP service (any language/framework)
                             │  ├─ one named tool per operation
                             │  └─ one restricted passthrough tool per upstream
```

The relationship to nginx is a **shape reference, not a dependency**: an upstream names the
address, the prefix allowlist decides where a call may go, and limits plus timeouts decide
how long it may take — but the protocol is MCP and the config is YAML. The gateway does not
terminate TLS, cache, or hot-reload; that is a fronting nginx and a restart.

## Quick start

```bash
# 1. Build once: bin/mcpx is a static binary, and the git stamp (version, commit,
#    date) is injected with -ldflags so `mcpx version` and /healthz can tell you
#    which revision is actually running.
make build

# 2. Work on a copy. mcpx.yaml is the committed example pointing at
#    petstore.example.com; *.local.yaml is gitignored, so your edits (and any
#    half-finished endpoints) never end up in the repository.
cp mcpx.yaml mcpx.local.yaml

# 3. Credentials are environment variables, never config values:
#    MCPX_TOKEN is what clients must send on /mcp — mcpx.local.yaml names it in
#    server.auth.token_env and the process refuses to start without it;
#    PETSTORE_TOKEN is that upstream's bearer token, read per call, so a missing
#    one turns the call into an isError result instead of killing the gateway.
export MCPX_TOKEN=...
export PETSTORE_TOKEN=...

# 4. Validate first: every URL and path template, tool-name collisions, the
#    passthrough allowlist and the credential variable names. Starts nothing,
#    needs no token values, and prints the tool list on success.
./bin/mcpx check -c mcpx.local.yaml

# 5. Serve the MCP endpoint over Streamable HTTP — this is the default command,
#    so `./bin/mcpx -c mcpx.local.yaml` does the same thing.
./bin/mcpx serve -c mcpx.local.yaml

# 6. Or the same tools over stdio, for clients that launch a process and speak
#    newline-delimited JSON-RPC on its stdin/stdout.
./bin/mcpx stdio -c mcpx.local.yaml
```

A passing `check` means every URL is valid, every path template compiles, tool names do not
collide, the passthrough allowlist is non-empty, and every credential variable name is
well-formed. Any failure is reported with its location and the process refuses to start.

### Docker Compose

```bash
cp .env.example .env  # set MCPX_TOKEN, e.g. openssl rand -hex 24
docker compose up -d --build
curl -s http://127.0.0.1:8848/healthz
```

The image is a two-stage build (static binary on Alpine, non-root uid 10001) and the
container mounts `deploy/mcpx.docker.yaml` read-only. That config listens on
`0.0.0.0:8848` — `127.0.0.1` inside a container is not reachable from the host — and
points at the `demo` service, so a fresh stack ships one callable tool. Swap in your own
file with `MCPX_CONFIG_FILE`, keeping the `0.0.0.0` listen address.

The published port is bound to `127.0.0.1` on the host by default; put a reverse proxy in
front before changing `MCPX_BIND`. Without `MCPX_TOKEN` the process logs
`authentication misconfigured` and exits, while `docker compose down` keeps working.

**Published images.** Publishing a GitHub release runs `.github/workflows/release.yml`,
which builds and pushes `ariacompute/mcpx` to DockerHub: `:<version>` for that release and
`:latest` for the newest one, for linux/amd64 and linux/arm64. The release's commit and
version go into the image as build args, so the image tag and the `version@commit` reported
by `/healthz` always agree. The workflow needs two repository secrets:
`DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN`.

```bash
docker pull ariacompute/mcpx:<version>  # e.g. 1.2.3 (the release tag minus its v)
docker run --rm -e MCPX_TOKEN=$(openssl rand -hex 24) \
  -p 127.0.0.1:8848:8848 ariacompute/mcpx:1.2.3
```

**Binaries.** The same release attaches standalone archives for
linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 and windows/amd64, plus one
`SHA256SUMS.txt` covering all of them:

```bash
tar xzf mcpx-1.2.3-linux-amd64.tar.gz  # mcpx + LICENSE
./mcpx version                         # mcpx 1.2.3 (<commit>) ...
```

macOS builds are **not code-signed or notarized** (no certificate is configured), so a
downloaded `mcpx` is quarantined by Gatekeeper; either `xattr -d com.apple.quarantine mcpx`
or build from source. Before running an archive, check it with
`sha256sum -c SHA256SUMS.txt` (`shasum -a 256 -c` on macOS).

## Configuration

The main file is YAML; **JSON with the same shape is accepted** (one decoding path).
Unknown fields are rejected outright, so a typo such as `allow_prefixs` cannot be silently
interpreted as "unrestricted".

```yaml
server:
  listen: 127.0.0.1:8848
  path: /mcp
  auth: {scheme: bearer, token_env: MCPX_TOKEN}    # none | bearer
  protocol_versions: ["2025-06-18", "2025-03-26"]
  allowed_origins: ["https://app.example.com"]     # empty = loopback origins only
  instructions: "..."                              # optional, returned by initialize
  max_request_bytes: 1048576
  session_ttl: 30m
  shutdown_grace: 15s

defaults:
  timeout: 15s
  max_response_bytes: 4194304
  max_body_bytes: 1048576

upstreams:
  - name: petstore
    base_url: https://petstore.example.com  # may include a path prefix, e.g. /api
    headers: {X-Env: prod}
    auth: {scheme: bearer, token_env: PETSTORE_TOKEN}
    timeout: 10s
    max_response_bytes: 1048576
    allow_prefixes: ["/api/v1"]
    operations:
      - name: get_pet
        description: Fetch one pet by id.
        method: GET
        path: /api/v1/pets/{id}
        params:
          - {name: id, in: path, required: true, schema: {type: string}}
          - {name: verbose, in: query, schema: {type: boolean}}
          - {name: X-Trace, in: header}
      - name: create_pet
        method: POST
        path: /api/v1/pets
        body_param: pet
        params:
          - {name: pet, in: body, required: true}
    passthrough:
      enabled: true
      name: petstore_request       # defaults to <upstream>_request
      methods: [GET, POST]         # defaults to [GET]
      allow_prefixes: ["/api/v1"]  # defaults to upstream.allow_prefixes
```

### Field reference

| Field | Meaning |
| --- | --- |
| `server.auth` | `none` or `bearer`. With `bearer`, a non-empty value must be readable from `token_env` at startup, otherwise the process refuses to start. |
| `server.allowed_origins` | Browser `Origin` allowlist; `"*"` allows everything; empty allows only `localhost/127.0.0.1/[::1]`. |
| `upstream.base_url` | Must be `http(s)://host`; embedded credentials and query strings are rejected. Configured paths are appended to its path. |
| `upstream.headers` | Static headers sent with every request; CR/LF is rejected. |
| `upstream.auth` | `none` / `bearer` / `header` (with `header_name`); only the environment variable name is stored. |
| `operation.path` | Must start with `/` and may contain `{name}` placeholders; `.`/`..`, `?`, `#`, `%` and whitespace are rejected. |
| `operation.params[].in` | `path` / `query` / `header` / `body`, defaulting to `query`. |
| `operation.body_param` | Which parameter becomes the request body; defaults to the single `in=body` parameter. |
| `operation.input_schema` | Overrides the derived JSON Schema for display only; argument binding is unaffected. |
| `passthrough` | The restricted generic tool; an empty `allow_prefixes` is a startup failure (unavailable beats an open proxy). |

Tool names are `<upstream>_<operation>`, unique case-insensitively; collisions and illegal
characters are startup failures. `upstream.name` must match `^[a-z][a-z0-9_-]{0,31}$`.

### How an argument becomes a request

- `path`: `{name}` placeholders are replaced with `url.PathEscape`d values; a value of `.`
  or `..` is refused.
- `query`: array values expand to repeated parameters; numbers and booleans render literally
  (`2`, not `2.000000`).
- `header`: the parameter name is the header name (hyphenated names such as `X-Trace` are
  allowed and deduplicated case-insensitively). Reserved headers — `Authorization`, `Cookie`,
  `Host`, `Content-Length` and friends — are refused so a caller cannot bypass the credential
  configured for the upstream.
- `body`: strings are sent verbatim (`text/plain`), everything else is JSON encoded
  (`application/json`); a body parameter without `schema.type` accepts any JSON value.
- Undeclared arguments, missing required arguments, type mismatches and enum violations all
  produce an `isError: true` result and **never reach the upstream**.

## Connecting

### Streamable HTTP

```bash
curl -sS http://127.0.0.1:8848/mcp \
  -H "Authorization: Bearer $MCPX_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"curl","version":"1"}}}'
```

The `Mcp-Session-Id` response header must be sent on every subsequent request;
`notifications/*` answers `202` with no body; `DELETE /mcp` terminates the session;
`GET /mcp` answers `405` (this gateway never opens server-initiated streams). `/healthz`
requires no credentials and reports the version, tool count and session count.

MCP client snippet (Claude Desktop, IDE plugins, agent runtimes):

```json
{
  "mcpServers": {
    "mcpx": {
      "type": "http",
      "url": "http://127.0.0.1:8848/mcp",
      "headers": { "Authorization": "Bearer ${MCPX_TOKEN}" }
    }
  }
}
```

### stdio

```json
{
  "mcpServers": {
    "mcpx": {
      "command": "/path/to/bin/mcpx",
      "args": ["stdio", "-c", "/path/to/mcpx.local.yaml"]
    }
  }
}
```

stdout belongs to the protocol and every log line goes to stderr; EOF is a clean shutdown.

## Examples

`example/` is a runnable pair, in Python, with no dependencies to install: a small notes
REST service that knows nothing about MCP, and two MCP clients for it — one over Streamable
HTTP, one over stdio — both using the same config.

```bash
# 1. Upstream (terminal 1): a plain REST service that knows nothing about MCP.
#    Its routes are what mcpx publishes as tools; GET /healthz means it is ready.
python3 example/python-http/server.py --port 18081

# 2. Gateway (terminal 2): publishes five tools at 127.0.0.1:18848/mcp.
#    Bearer auth is on, so it exits without a token. Nothing beyond mcpx checks
#    the value, so `openssl rand -hex 24` is as good as any.
export MCPX_TOKEN=...
./bin/mcpx serve -c example/mcpx.example.yaml

# 3a. MCP client over Streamable HTTP (terminal 3): POSTs to /mcp with
#     Authorization: Bearer $MCPX_TOKEN (its default), one session end to end.
python3 example/python-http/client.py --url http://127.0.0.1:18848/mcp

# 3b. MCP client over stdio (terminal 3, instead of 3a): starts its own
#     `mcpx stdio` child rather than using terminal 2, one JSON document per
#     line, and needs no token — stdin/stdout is the transport.
python3 example/python-stdio/client.py --mcpx ./bin/mcpx --config example/mcpx.example.yaml
```

Each client walks a real session (handshake, `tools/list`, `tools/call`, both failure modes,
`DELETE` or EOF) and ends with `PASS`. See `example/README.md` for what the config
demonstrates; the whole thing, assertions and cleanup included, is one command:

```bash
make example-test  # both transports; EXAMPLE_{UPSTREAM,GATEWAY}_PORT override ports
```

The example gateway keeps `server.auth.scheme: bearer` (the same posture as the container),
so `MCPX_TOKEN` is required to start it; the test targets default it to a loopback-only
placeholder when the variable is unset.

## Security model

- Listens on `127.0.0.1` by default; front it with nginx and enable `server.auth` before
  exposing it.
- A request path is always "configured base path + configured template": absolute URLs,
  `//host`, `..` and cross-origin redirects are refused. Passthrough paths are matched by
  **segment**, so `/api/v1` never allows `/api/v1x`.
- Request bodies, response bodies and single calls all have limits and timeouts; a truncated
  response is marked as such in the result.
- Credentials appear in the config only as environment variable names and are read at call
  time; logs and error messages never print them, and sensitive headers are redacted.
- A non-2xx upstream response is not a protocol error: it becomes an `isError: true` result
  carrying the status and the (truncated) body, so the model can decide what to do next.

## Error semantics

| Situation | Response |
| --- | --- |
| JSON cannot be parsed | `-32700`, HTTP 400 |
| `jsonrpc` is not `2.0` / `method` missing | `-32600` |
| Unknown method | `-32601` |
| Unknown tool / bad cursor / malformed params | `-32602` |
| Upstream non-2xx, timeout, out-of-prefix path, argument validation | `tools/call` result with `isError: true` |
| Missing credentials / rejected Origin / missing or expired session / oversized request | HTTP 401 / 403 / 400 / 404 / 413 |

## Development

```bash
make build         # bin/mcpx (version/commit/date injected via -ldflags)
make test          # go test -race ./...
make vet           # go vet ./...
make cover         # whole-repo coverage summary (coverage.out, gitignored)
make cover-html    # generate and open the HTML coverage report
make check         # validate the example config and list its tools
make run           # run the HTTP endpoint in the foreground
make stdio         # run the stdio transport in the foreground
make example-test  # example/ end-to-end over both transports (see "Examples")
```

Tests are fully offline (`httptest` + `io.Pipe`) and `go test -race ./...` is green;
statement coverage is currently about 92% (config, mcp, tool, upstream and both transports
sit around 90%, with the CLI and end-to-end suite covering the rest). New behaviour needs
both success and failure cases — especially path traversal, prefix escapes, missing
credentials and timeouts, where a regression is a security bug rather than a broken feature.

Layout:

```
cmd/mcpx            CLI: serve / stdio / check / version
internal/config     config decoding, defaults, validation
internal/tool       tool abstraction, registry, schema derivation, argument validation
internal/mcp        JSON-RPC envelope, protocol methods, error codes
internal/upstream   HTTP client, path templates, operations, restricted passthrough, redaction
internal/gateway    config -> tool index, call dispatch
internal/transport  httpsrv (sessions/auth/202/DELETE), stdio (line loop)
tests               httptest end-to-end
example             Python notes service + MCP clients over HTTP and stdio
scripts             one-command tests for example/ (bash)
```

The only third-party dependency is `gopkg.in/yaml.v3` (it parses both YAML and JSON);
everything else is the standard library.

## Explicitly out of scope

- Config hot reload (restart instead; never read a half-written file)
- Multi-tenancy and quotas
- Request caching (the gateway does not make consistency decisions for an upstream)
- Built-in TLS (use a fronting nginx)
- Dynamic upstream discovery (the tool set is fixed at startup, which keeps `tools/list`
  stable and cacheable)

## License

MIT.

## Engineering conventions

This repository follows Harness Engineering:

- [`AGENTS.md`](AGENTS.md): entry point and directory index for agent engineering context
- [`requirements.md`](requirements.md): requirement spec (scope / errors / acceptance, reviewed by
  a human before coding)
- [`task.md`](task.md): implementation checklist
