# mcpx

[English](README.md) | [中文](README_cn.md)

MCP 网关：用一份声明式配置，把**任意 HTTP 服务**发布成 MCP 工具，同时对外提供 MCP
服务端出口（Streamable HTTP 与 stdio）。名字沿用 nginx 那种短小写风格 —— nginx 是
"engine-x"，这里是 "MCP-x"。

```
MCP 客户端 ──HTTP POST /mcp──┐
            ──stdio NDJSON───┤ mcpx ──HTTP──> 你的 HTTP 服务（任意语言/框架）
                             │  ├─ 每个 operation 一个具名工具
                             │  └─ 每个 upstream 一个受限透传工具
```

和 nginx 的关系是**形态参照**而不是依赖：upstream 声明地址、前缀白名单决定"能去哪儿"、
限长与超时决定"能待多久"，但协议换成了 MCP，配置换成了 YAML。网关不内置 TLS、不缓存、
不热重载 —— 这三件事交给前置 nginx 与一次重启。

## 快速开始

```bash
make build                           # 产出 bin/mcpx（带 git 版本戳）
cp mcpx.yaml mcpx.local.yaml
export MCPX_TOKEN=...                # MCP 端点的 bearer token
export PETSTORE_TOKEN=...            # 上游凭据（只写环境变量名）
./bin/mcpx check -c mcpx.local.yaml  # 只校验配置并列出工具
./bin/mcpx serve -c mcpx.local.yaml  # 默认命令，监听 127.0.0.1:8848
./bin/mcpx stdio -c mcpx.local.yaml  # 本地客户端直挂
```

`check` 通过即代表：URL 合法、路径模板可编译、工具名无冲突、透传前缀非空、凭据变量名
合法。任何一项失败都会带着位置报错并拒绝启动。

### Docker Compose

```bash
cp .env.example .env  # 填 MCPX_TOKEN，例如 openssl rand -hex 24
docker compose up -d --build
curl -s http://127.0.0.1:8848/healthz
```

镜像为两阶段构建（静态二进制跑在 Alpine、非 root uid 10001），容器只读挂载
`deploy/mcpx.docker.yaml`。该配置监听 `0.0.0.0:8848`（容器内的 `127.0.0.1` 从宿主机
不可达），并指向 compose 里的 `demo` 服务，所以新栈启动后就有一个可调用工具。换成
自己的配置时用 `MCPX_CONFIG_FILE`，务必保留 `0.0.0.0` 监听地址。

端口默认只发布到宿主机 `127.0.0.1`；要改 `MCPX_BIND` 前请先加反向代理。缺少
`MCPX_TOKEN` 时进程打印 `authentication misconfigured` 后退出，但 `docker compose down`
仍可正常清理。

**已发布镜像。** 在 GitHub 上发布 release 会触发 `.github/workflows/release.yml`，
把 `ariacompute/mcpx` 推送到 DockerHub：该 release 的 `:<version>` 加最新的 `:latest`，
同时覆盖 linux/amd64 与 linux/arm64。release 的版本与 commit 以构建参数注入镜像，所以
镜像 tag 与 `/healthz` 报告的 `version@commit` 永远一致。工作流需要两个仓库 secret：
`DOCKERHUB_USERNAME`、`DOCKERHUB_TOKEN`。

```bash
docker pull ariacompute/mcpx:<version>  # 例如 1.2.3（release tag 去掉 v）
docker run --rm -e MCPX_TOKEN=$(openssl rand -hex 24) \
  -p 127.0.0.1:8848:8848 ariacompute/mcpx:1.2.3
```

**二进制包。** 同一个 release 还附带 linux/amd64、linux/arm64、darwin/amd64、
darwin/arm64、windows/amd64 的独立压缩包，以及覆盖全部的单个 `SHA256SUMS.txt`：

```bash
tar xzf mcpx-1.2.3-linux-amd64.tar.gz  # mcpx + LICENSE
./mcpx version                         # mcpx 1.2.3 (<commit>) ...
```

macOS 包**未做代码签名与公证**（未配置证书），下载后会被 Gatekeeper 隔离，可
`xattr -d com.apple.quarantine mcpx` 或直接源码构建。解包前先用
`sha256sum -c SHA256SUMS.txt`（macOS 上 `shasum -a 256 -c`）校验。

## 配置

主配置 YAML，**同结构的 JSON 也接受**（同一套解码路径）。未知字段直接报错，避免把
`allow_prefixs` 这种拼写错误静默解释成"不限制"。

```yaml
server:
  listen: 127.0.0.1:8848
  path: /mcp
  auth: {scheme: bearer, token_env: MCPX_TOKEN}    # none | bearer
  protocol_versions: ["2025-06-18", "2025-03-26"]
  allowed_origins: ["https://app.example.com"]     # 留空 = 只允许 loopback Origin
  instructions: "..."                              # 可选，initialize 时回给客户端
  max_request_bytes: 1048576
  session_ttl: 30m
  shutdown_grace: 15s

defaults:
  timeout: 15s
  max_response_bytes: 4194304
  max_body_bytes: 1048576

upstreams:
  - name: petstore
    base_url: https://petstore.example.com  # 可带路径前缀，如 /api
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
      name: petstore_request       # 默认 <upstream>_request
      methods: [GET, POST]         # 默认 [GET]
      allow_prefixes: ["/api/v1"]  # 默认继承 upstream.allow_prefixes
```

### 字段语义

| 字段 | 说明 |
| --- | --- |
| `server.auth` | `none` 或 `bearer`。`bearer` 时启动必须能从 `token_env` 读到非空值，否则拒绝启动 |
| `server.allowed_origins` | 浏览器 `Origin` 白名单；`"*"` 放行全部；留空只允许 `localhost/127.0.0.1/[::1]` |
| `upstream.base_url` | 必须是 `http(s)://host`，不允许内嵌凭据/查询串；配置里的 path 会拼在其路径之后 |
| `upstream.headers` | 每个请求都带的静态头；禁止 CR/LF |
| `upstream.auth` | `none` / `bearer` / `header`（配 `header_name`）；只存环境变量名 |
| `operation.path` | 必须 `/` 开头，支持 `{name}` 占位；禁止 `.`/`..`、`?`、`#`、`%`、空白 |
| `operation.params[].in` | `path` / `query` / `header` / `body`，缺省 `query` |
| `operation.body_param` | 指定哪个参数作为请求体；缺省时自动取唯一的 `in=body` 参数 |
| `operation.input_schema` | 覆盖自动派生的 JSON Schema，仅影响展示，不影响实际绑定 |
| `passthrough` | 受限通用工具；`allow_prefixes` 为空时启动失败（宁可不可用，也不当开放代理） |

工具名规则：`<upstream>_<operation>`，大小写不敏感唯一；冲突、非法字符都会启动失败。
`upstream.name` 必须匹配 `^[a-z][a-z0-9_-]{0,31}$`。

### 参数如何变成请求

- `path`：按 `{name}` 替换，值经 `url.PathEscape`；值本身是 `.`/`..` 时拒绝。
- `query`：数组值展开为重复参数；数字/布尔按字面量渲染（`2` 而不是 `2.000000`）。
- `header`：参数名就是头名（允许 `X-Trace` 这类连字符写法，大小写不敏感去重）；
  `Authorization`、`Cookie`、`Host`、`Content-Length` 等保留头一律拒绝，避免调用方
  绕过配置里的凭据。
- `body`：字符串原样发送（`text/plain`），其它类型 JSON 序列化（`application/json`）；
  未声明 `schema.type` 的 body 参数接受任意 JSON 值。
- 未声明的参数、缺失的必填参数、类型不匹配、枚举越界都会返回 `isError: true` 的结果，
  并且**不会**发往上游。

## 接入方式

### Streamable HTTP

```bash
curl -sS http://127.0.0.1:8848/mcp \
  -H "Authorization: Bearer $MCPX_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"curl","version":"1"}}}'
```

响应头 `Mcp-Session-Id` 必须出现在后续每个请求上；`notifications/*` 返回 `202` 无正文；
`DELETE /mcp` 终止会话；`GET /mcp` 返回 `405`（本网关不主动推送）。`/healthz` 不需要鉴权，
返回版本、工具数与会话数。

MCP 客户端配置片段（例如 Claude Desktop / IDE 插件）：

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

stdout 是协议通道，所有日志走 stderr；读到 EOF 即正常退出。

## 示例

`example/` 是一对可直接运行的 Python 示例，不用装任何依赖：一个对 MCP 一无所知的
notes REST 服务，加两个 MCP 客户端（Streamable HTTP 与 stdio），共用同一份配置。

```bash
python3 example/python-http/server.py --port 18081 &
./bin/mcpx serve -c example/mcpx.example.yaml &

python3 example/python-http/client.py                       # HTTP：POST /mcp + 会话头
python3 example/python-stdio/client.py --mcpx ./bin/mcpx \
  --config example/mcpx.example.yaml                        # stdio：子进程上的 NDJSON
```

两个客户端都走完整会话（握手、`tools/list`、`tools/call`、两种失败形态、`DELETE` 或
EOF），最后打印 `PASS`。配置在演示什么见 `example/README_cn.md`；连带断言与清理的
一条命令版本：

```bash
make example-test  # 两种传输都跑；EXAMPLE_{UPSTREAM,GATEWAY}_PORT 可改端口
```

## 安全模型

- 默认只监听 `127.0.0.1`；对外暴露请前置 nginx 并启用 `server.auth`。
- 路径永远由"配置里的 base 路径 + 配置里的模板"拼出：拒绝绝对 URL、`//host`、`..`、
  跨主机重定向；透传路径按**段**匹配前缀（`/api/v1` 不会放行 `/api/v1x`）。
- 请求体、响应体、单次调用都有上限与超时；响应被截断时结果里明确标注。
- 凭据只以环境变量名出现在配置中，运行时读取；日志与错误消息不打印 token，
  敏感头统一脱敏。
- 上游非 2xx 不抛协议错误，而是 `isError: true` + 状态码 + 截断后的正文，
  让模型自己决定下一步。

## 错误语义

| 场景 | 返回 |
| --- | --- |
| JSON 解析失败 | `-32700`，HTTP 400 |
| `jsonrpc` 不是 `2.0` / 缺 `method` | `-32600` |
| 未知方法 | `-32601` |
| 未知工具 / 坏游标 / 参数结构错误 | `-32602` |
| 上游非 2xx、超时、越权路径、参数校验失败 | `tools/call` 的 `isError: true` 结果 |
| 未鉴权 / Origin 拒绝 / 会话缺失 / 会话过期 / 请求过大 | HTTP 401 / 403 / 400 / 404 / 413 |

## 开发

```bash
make build         # bin/mcpx（-ldflags 注入 version/commit/date）
make test          # go test -race ./...
make vet           # go vet ./...
make cover         # 全仓覆盖率汇总（coverage.out，已 gitignore）
make cover-html    # 生成并在浏览器里看 HTML 报告
make check         # 用仓库里的示例配置做一次体检
make run           # 前台运行 HTTP
make stdio         # 前台运行 stdio
make example-test  # example/ 两种传输的端到端测试（见「示例」）
```

测试全离线（`httptest` + `io.Pipe`），`go test -race ./...` 全绿；当前语句覆盖率约
92%（`config` / `mcp` / `tool` / `upstream` / 两个 transport 均在 90% 上下，CLI 与
端到端另有覆盖）。新增行为请同时补正常与异常路径用例 —— 尤其是路径穿越、前缀越界、
凭据缺失和超时这几类，它们一旦回归就是安全问题。

目录：

```
cmd/mcpx            CLI：serve / stdio / check / version
internal/config     配置解码、默认值、校验
internal/tool       工具抽象、注册表、schema 派生与参数校验
internal/mcp        JSON-RPC 信封、协议方法、错误码
internal/upstream   HTTP 客户端、路径模板、operation、受限透传、脱敏
internal/gateway    配置 → 工具索引、调用分发
internal/transport  httpsrv（会话/鉴权/202/DELETE）、stdio（行循环）
tests               httptest 端到端
example             Python notes 服务 + HTTP/stdio 两种 MCP 客户端
scripts             example/ 的一键测试脚本（bash）
```

唯一第三方依赖是 `gopkg.in/yaml.v3`（同时解析 YAML 与 JSON），其余全部标准库。

## 明确不做

- 配置热重载（重启即可，避免读到半写文件）
- 多租户与配额
- 请求缓存（网关不替上游做一致性决策）
- 内置 TLS（交给前置 nginx）
- 动态发现上游（工具集在启动时固定，`tools/list` 因此稳定可缓存）

## 许可证

MIT.

## 工程约定

本仓库遵循 Harness Engineering：

- [`AGENTS.md`](AGENTS.md)：Agent 工程上下文入口与目录索引
- [`requirements.md`](requirements.md)：需求规格（功能边界 / 异常 / 验收，人审后编码）
- [`task.md`](task.md)：实施清单
