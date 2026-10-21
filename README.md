# mcpx

English | [中文](README_cn.md)

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
make build                 # produces bin/mcpx with a git build stamp
cp mcpx.yaml mcpx.local.yaml
export MCPX_TOKEN=...      # bearer token for the MCP endpoint
export PETSTORE_TOKEN=...  # upstream credential (config stores the variable name only)
./bin/mcpx check -c mcpx.local.yaml    # validate the config and list the tools
./bin/mcpx serve -c mcpx.local.yaml    # default command, listens on 127.0.0.1:8848
./bin/mcpx stdio -c mcpx.local.yaml    # for local clients that launch a process
```

A passing `check` means every URL is valid, every path template compiles, tool names do not
collide, the passthrough allowlist is non-empty, and every credential variable name is
well-formed. Any failure is reported with its location and the process refuses to start.

## Configuration

The main file is YAML; **JSON with the same shape is accepted** (one decoding path).
Unknown fields are rejected outright, so a typo such as `allow_prefixs` cannot be silently
interpreted as "unrestricted".

```yaml
server:
  listen: 127.0.0.1:8848
  path: /mcp
  auth: {scheme: bearer, token_env: MCPX_TOKEN}   # none | bearer
  protocol_versions: ["2025-06-18", "2025-03-26"]
  allowed_origins: ["https://app.example.com"]    # empty = loopback origins only
  instructions: "..."                             # optional, returned by initialize
  max_request_bytes: 1048576
  session_ttl: 30m
  shutdown_grace: 15s

defaults:
  timeout: 15s
  max_response_bytes: 4194304
  max_body_bytes: 1048576

upstreams:
  - name: petstore
    base_url: https://petstore.example.com      # may include a path prefix, e.g. /api
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
make build      # bin/mcpx (version/commit/date injected via -ldflags)
make test       # go test -race ./...
make vet        # go vet ./...
make cover      # whole-repo coverage summary (coverage.out, gitignored)
make cover-html # generate and open the HTML coverage report
make check      # validate the example config and list its tools
make run        # run the HTTP endpoint in the foreground
make stdio      # run the stdio transport in the foreground
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
