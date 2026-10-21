# requirements.md — mcpx（MCP 网关）

> 功能边界 / 配置契约 / 协议契约 / 安全模型 / 异常语义 / 验收标准。
> 当前实现对应 **v1.1**；实施记录见 `task.md`，人读手册见 `README.md`。

## 1. 功能边界

### 1.1 范围内

- **声明式上游**：一份配置描述多个 HTTP 服务（`base_url`、静态头、鉴权、超时、响应上限、
  路径前缀白名单），每个服务下声明若干 `operation`，每个 operation 发布为一个 MCP 工具。
- **MCP 服务端**：`initialize` / `ping` / `tools/list`（游标分页）/ `tools/call`；
  JSON-RPC 2.0 信封；协议版本协商；通知一律不回复。
- **双传输**：Streamable HTTP（单端点 POST、会话头、通知 202、DELETE 终止、`/healthz`）
  与 stdio（按行 NDJSON，stdout 仅协议）。
- **两种工具粒度**：operation 具名工具 + 每个上游一个受限通用透传工具（按配置的 path
  前缀与方法白名单）。
- **配置格式**：YAML 主配置，接受同结构 JSON；未知字段报错；凭据只写环境变量名。
- **安全与运维**：MCP 端点可选 Bearer 鉴权、默认只听 `127.0.0.1`、Origin 策略、路径
  穿越与跨源重定向防护、请求/响应/请求体三档限长、逐上游超时、结构化日志、优雅退出、
  `check` 子命令做启动前体检。
- **容器化部署**：多阶段构建（静态二进制 + 非 root 运行时），compose 一键拉起网关与
  演示上游；配置只读挂载，端口默认只发布到宿主机回环。

### 1.2 范围外

- 配置热重载（改配置需重启，避免读到半写文件）。
- 多租户、配额、计费、请求缓存。
- 内置 TLS / ACME（交给前置 nginx 或等价组件）。
- 服务端发起的 SSE 流（`GET /mcp` 固定 405）、采样、roots、resources/prompts 能力。
- 动态发现上游、工具集运行时变更（`tools/list` 在启动后稳定）。
- 持久化（无数据库；会话仅在内存，重启失效）。
- 多容器编排（Kubernetes、Swarm、横向扩展与服务发现）。

## 2. 配置契约

### 2.1 解码规则

| 项 | 约定 |
| --- | --- |
| 格式 | YAML；JSON 同一套解码路径（YAML 是 JSON 超集） |
| 未知字段 | 报错（`KnownFields`） |
| 空文件 | 报错 |
| 时长 | 字符串 `15s` / `1m30s`；裸数字报错 |
| 解码后 | 立即 `ApplyDefaults()`，再 `Validate()`；不通过即拒绝启动 |

### 2.2 `server`

| 字段 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `listen` | string | `127.0.0.1:8848` | HTTP 监听地址 |
| `path` | string | `/mcp` | 端点路径；补前导 `/`；禁止 `?`/`#`/空白/`//` |
| `auth.scheme` | `none`\|`bearer` | `none` | 仅这两种；`header` 在服务端非法 |
| `auth.token_env` | string | — | bearer 必填，须为环境变量名 |
| `protocol_versions` | string[] | `["2025-06-18","2025-03-26"]` | 首选版本在前 |
| `allowed_origins` | string[] | `[]` | 空 = 只允许 loopback；`"*"` = 全放行；否则精确匹配 |
| `instructions` | string | `""` | 原样回填 `initialize.instructions` |
| `max_request_bytes` | int | `1048576` | 入站请求体上限，超出 413 |
| `session_ttl` | duration | `30m` | 会话闲置过期 |
| `shutdown_grace` | duration | `15s` | 优雅退出等待 |

### 2.3 `defaults`

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `timeout` | `15s` | 每个上游的整次调用超时 |
| `max_response_bytes` | `4194304` | 上游响应读取上限，超出截断并标注 |
| `max_body_bytes` | `1048576` | 出站请求体上限，超出报错 |

### 2.4 `upstreams[]`

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `name` | 是 | `^[a-z][a-z0-9_-]{0,31}$`，全局唯一 |
| `base_url` | 是 | `http(s)://host[/path]`；禁止内嵌凭据、query、fragment、`..` |
| `headers` | 否 | 静态头；名字须为 HTTP token，值禁止 CR/LF |
| `auth.scheme` | 否 | `none`\|`bearer`\|`header`，默认 `none` |
| `auth.token_env` | 条件 | scheme 非 none 时必填，须为环境变量名 |
| `auth.header_name` | 条件 | `header` 方案必填 |
| `timeout` | 否 | 继承 `defaults.timeout` |
| `max_response_bytes` | 否 | 继承 `defaults.max_response_bytes` |
| `allow_prefixes` | 否 | 该上游可访问的路径段前缀；亦作为 passthrough 默认值 |
| `operations` | 否\* | 具名工具；\*与 `passthrough` 至少有一个 |
| `passthrough` | 否 | 受限通用工具 |

### 2.5 `operations[]`

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `name` | 是 | `^[A-Za-z][A-Za-z0-9_-]{0,63}$`；工具名 = `<upstream>_<name>` |
| `description` | 否 | 缺省时自动生成 `"<METHOD> <path> on upstream <name>"` |
| `method` | 否 | 默认 `GET`；限 GET/HEAD/POST/PUT/PATCH/DELETE |
| `path` | 是 | `/` 开头，可含 `{name}` 占位；静态部分禁止 `%`、`?`、`#`、空白、`.`/`..` 段 |
| `params` | 否 | 参数声明，见下 |
| `body_param` | 否 | 指定 body 参数；可省略（自动取唯一的 `in=body`） |
| `headers` | 否 | 该 operation 的静态头 |
| `input_schema` | 否 | 覆盖展示用 JSON Schema（仅影响 `tools/list`，不影响绑定） |

`params[]`：

| 字段 | 说明 |
| --- | --- |
| `name` | path/query/body 用标识符文法；header 用 HTTP token（允许 `X-Trace`，大小写不敏感去重） |
| `in` | `path`\|`query`\|`header`\|`body`，默认 `query` |
| `required` | path 参数强制 required |
| `description` | 进入 JSON Schema 描述 |
| `schema` | 覆盖默认 `{"type":"string"}`；类型限 JSON 七种 |

规则：占位与 path 参数必须一一对应；最多一个 body 参数；body 只允许
POST/PUT/PATCH/DELETE；`body_param` 必须指向 `in=body` 的参数。

### 2.6 `passthrough`

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `enabled` | `false` | 开启后生成一个通用工具 |
| `name` | `<upstream>_request` | 工具名，须匹配 operation 名规则且全局唯一 |
| `methods` | `[GET]` | 白名单，限六种方法 |
| `allow_prefixes` | 继承 `upstream.allow_prefixes` | 为空即启动失败 |

### 2.7 校验失败示例（均为启动失败）

| 场景 | 结果 |
| --- | --- |
| `base_url: example.com` / 内嵌凭据 / 带 query | 拒绝 |
| 方法 `TRACE`、路径不以 `/` 开头、含 `..`/`%`/`?` | 拒绝 |
| 占位无参数、path 参数无占位、重复参数、重复工具名 | 拒绝 |
| `token_env` 为空或不是变量名、`header` 方案缺 `header_name` | 拒绝 |
| `passthrough` 无前缀、方法非法、名字冲突 | 拒绝 |
| 未知字段、裸数字时长、空配置 | 拒绝 |

## 3. 工具映射契约

### 3.1 operation 工具

- **名字**：`<upstream>_<operation>`；查找大小写不敏感。
- **inputSchema**：由参数派生，`type: object`、`additionalProperties: false`、
  `required` 为声明 required 的参数（path 恒 required）。
- **调用绑定**：
  - `path`：按占位替换，值 `url.PathEscape`；值 `.`/`..`/空/含 CRLF 拒绝。
  - `query`：标量按字面量（`2`、`true`）；数组展开为重复参数。
  - `header`：参数名即头名；保留头拒绝。
  - `body`：字符串原样发送（`text/plain; charset=utf-8`）；其它类型 JSON 序列化
    （`application/json`）；未声明 schema 类型的 body 参数接受任意 JSON。
- **拒绝先于外发**：未声明参数、缺必填、类型/枚举不符 → `isError` 结果，请求不发。

### 3.2 passthrough 工具

- **参数**：`method`（枚举，默认排序后的第一个）、`path`（必填，须在前缀内）、
  `query`（object）、`headers`（object）、`body`（任意）。
- **路径校验**：`ValidatePath` + `MatchPrefix`；编码穿越仍拒绝；根前缀 `/` 覆盖全部。
- **保留头**：`Host`、`Content-Length`、`Connection`、`Transfer-Encoding`、`TE`、
  `Trailer`、`Upgrade`、`Proxy-*`、`Keep-Alive`、`Authorization`、`Cookie` 一律拒绝。

### 3.3 结果渲染

- 2xx：正文文本；JSON 额外给出 `structuredContent`（≤64 KiB 且未截断时缩进美化）。
- 非 2xx：`isError: true`，文本含 `upstream <name> returned <status>` 与截断后的正文。
- 空正文：`(empty body)`；被截断：追加 `[truncated at N bytes]`。

## 4. 协议契约

### 4.1 传输

| 项 | HTTP | stdio |
| --- | --- | --- |
| 端点 | `POST {path}` / `DELETE {path}` / `GET /healthz` | stdin/stdout |
| 编码 | `Content-Type: application/json`（否则 415） | 一行一条 JSON-RPC |
| 会话 | `initialize` 下发 `Mcp-Session-Id`；其后必带 | 无 |
| 通知 | `202` 无正文 | 不输出 |
| 终止 | `DELETE` → 204 | EOF → 正常退出 |
| 上限 | `max_request_bytes` → 413；会话 TTL | 单行 8 MiB |
| 推送 | `GET/HEAD` → 405（`Allow: POST, DELETE`） | 不适用 |

### 4.2 方法与结果

| 方法 | 说明 |
| --- | --- |
| `initialize` | 协商版本，返回 `protocolVersion`、`capabilities.tools.listChanged=false`、`serverInfo` |
| `ping` | 返回空对象 |
| `tools/list` | `{tools:[{name,description,inputSchema}], nextCursor?}`；默认 50，上限 200 |
| `tools/call` | `{content:[{type:"text",text}], structuredContent?, isError?}` |

通知 `notifications/initialized`、`notifications/cancelled` 静默接受；未知通知也静默。
未知方法 → `-32601`。游标不透明（base64 偏移），坏游标 → `-32602`。

### 4.3 错误与 HTTP 状态

| 场景 | JSON-RPC | HTTP |
| --- | --- | --- |
| 请求体不是合法 JSON | `-32700` | 400 |
| `jsonrpc != "2.0"`、缺 `method` | `-32600` | 200 |
| 未知方法 | `-32601` | 200 |
| 未知工具、参数结构错误、坏游标 | `-32602` | 200 |
| 内部错误（不泄漏细节） | `-32603` | 200 |
| 未鉴权 / Origin 拒绝 | — | 401 / 403 |
| 会话缺失 / 未知或过期 | — | 400 / 404 |
| 请求体超限 / Content-Type 不符 / 方法不允许 | — | 413 / 415 / 405 |
| 工具执行失败（上游/校验） | 无错误对象 | 200 + `isError: true` |

## 5. 安全模型

- **凭据**：配置只写环境变量名；调用时读取；缺失即失败（`credentials missing`），
  绝不发送空鉴权请求。日志与错误不打印 token。
- **SSRF 收敛**：目标只能由 `base_url` + 配置模板拼出；拒绝绝对 URL、`//host`、
  `..`、跨主机重定向（最多 3 跳且同 scheme+host）。
- **前缀白名单**：按段匹配，解码后再判断；`/api/v1` 不放行 `/api/v1x`。
- **头部注入**：调用方不能覆盖传输头、长度头与鉴权/Cookie 头；配置里的静态头优先。
- **资源边界**：入站/出站/请求体三档上限；每上游独立超时；截断有显式标注。
- **浏览器边界**：默认只允许 loopback Origin；显式白名单或 `"*"` 才会放开。
- **端点默认值**：监听 `127.0.0.1`；未配置鉴权时启动日志明确告警。

## 6. 非功能约束

- **启动期编译**：解析 + 校验 + 路由表 + 每上游 `http.Client` 一次完成；热路径是
  一次名字查找 + 一次出站请求，无每请求重复解析。
- **并发**：`Client`、`Registry`、`Handler`、`Server` 均只读或带锁，可并发使用；
  会话存储带互斥与惰性过期清理。
- **可运维**：`/healthz` 返回版本、工具数、会话数、uptime；日志 JSON 到 stderr；
  SIGINT/SIGTERM → `Shutdown`（`shutdown_grace`）。
- **无状态**：不落盘、不缓存；进程重启后客户端需重新 `initialize`。
- **容器**：镜像多阶段构建（`golang:1.27-alpine` → `alpine:3.21`），以 uid 10001 非
  root 运行；compose 使用只读根文件系统、`cap_drop: ALL`、`no-new-privileges`，端口
  默认只绑宿主机 `127.0.0.1`，配置从 `deploy/mcpx.docker.yaml` 只读挂载到
  `/etc/mcpx/mcpx.yaml`。

## 7. 验收标准

- [x] `go build ./...`、`go vet ./...`、`gofmt -l .` 全部干净。
- [x] `go test -race ./...` 全绿；116 个测试函数；语句覆盖率 ≥90%（当前 91.8%）。
- [x] `mcpx check -c mcpx.yaml` 通过，并列出 5 个工具（示例配置）。
- [x] HTTP 流程：`initialize` → 带会话 `tools/list` → `tools/call` → 上游收到期望
      路径/查询/头/凭据 → `DELETE` 后旧会话 404。
- [x] stdio 流程：初始化、工具列表、调用、坏 JSON 不中断、EOF 正常退出。
- [x] 安全用例：路径穿越、前缀越界（含 `/api/v1x` 与编码穿越）、跨源重定向、保留头
      注入、凭据缺失、响应截断、请求超限，均有断言。
- [x] 错误语义：`-32700/-32600/-32601/-32602/-32603`、401/403/400/404/405/413/415/202。
- [x] 容器：`docker build` 成功；`docker compose up -d --build` 后 `/healthz` 健康、
      `tools/call demo_hello` 返回演示上游正文；`docker compose down` 无需 token 也能清理。
- [x] 文档同步：`README.md`、`README_cn.md`、`requirements.md`、`task.md`、`AGENTS.md`
      与实现一致。

## 8. 变更记录

| 版本 | 内容 |
| --- | --- |
| v1 | 首个实现：配置/协议/双传输/两种工具粒度/安全模型/端到端测试 |
| v1.1 | header 参数允许连字符并做大小写不敏感去重；根前缀 `/` 覆盖全部路径；覆盖率 75% → 91.8%；新增 `make cover` / `cover-html`；文档三件套补齐 |
| v1.2 | 容器化：多阶段 Dockerfile（非 root、健康检查、版本戳）、compose（网关 + 演示上游、只读根文件系统、回环发布）、`deploy/mcpx.docker.yaml`、`.env.example`、`make docker-build/compose-*` |
