# AGENTS.md

面向 AI Agent 的工程上下文入口，渐进式披露：本文件只给骨架与硬性约束，配置细节看
`README.md`，行为契约看 `requirements.md`，实施记录看 `task.md`。

## 项目概述

`mcpx` 是一个 **MCP 网关**：用一份声明式配置把任意 HTTP 服务发布成 MCP 工具，对外同时
提供 **Streamable HTTP** 与 **stdio** 两种 MCP 出口。形态参照 nginx（upstream、前缀白名单、
超时与限长），但不依赖 nginx，也不做反代以外的协议转换。

- 语言/运行时：Go 1.27，标准库 `net/http`，**唯一第三方依赖** `gopkg.in/yaml.v3`
  （同一解析器同时读 YAML 与 JSON）。
- 无数据库、无前端、无持久化、无框架；单二进制，启动即编译配置。
- 与本机其它仓库（`serve` / `agenthub`）**无 import 关系**，是独立项目。

## 分层与目录

```
cmd/mcpx          CLI：serve / stdio / check / version（默认 serve）
internal/config   配置结构、YAML/JSON 解码、默认值、校验
internal/tool     工具抽象 Tool、注册表 Registry、schema 派生与参数校验
internal/mcp      JSON-RPC 信封、initialize/ping/tools/list/tools/call、错误码
internal/upstream HTTP 客户端、路径模板、operation、受限 passthrough、脱敏
internal/gateway  配置 → 不可变工具索引；tools/call 分发与 isError 映射
internal/transport/httpsrv   Streamable HTTP（会话、鉴权、Origin、/healthz）
internal/transport/stdio     NDJSON 行循环（stdout 仅协议）
internal/version  版本戳（-ldflags 注入）
tests             httptest 端到端
deploy/           容器专用配置（0.0.0.0 监听 + 演示上游）
```

依赖方向（单向）：

```
config ← 所有层只读
tool ← mcp、upstream、gateway
mcp ← gateway、transport
upstream ← gateway
gateway ← cmd
transport ← cmd
```

`mcp` 不 import `upstream`/`gateway`：协议内核只认 `tool.Tool` 与 `mcp.Caller` 接口。

## 硬性约定

### 配置

- 未知字段**直接报错**（`yaml.Decoder.KnownFields(true)`），拼写错误不能被静默解释成
  "不限制"。
- 默认值只在 `config.ApplyDefaults` 里，且必须幂等；`Validate` 是启动唯一闸门。
- 凭据**只存环境变量名**（`token_env`）。配置、日志、错误消息里都不允许出现 token 值。
- 上游 `name` 匹配 `^[a-z][a-z0-9_-]{0,31}$`；operation `name` 匹配
  `^[A-Za-z][A-Za-z0-9_-]{0,63}$`；工具名 `<upstream>_<operation>` 大小写不敏感唯一。
- 参数名：`path/query/body` 用标识符文法；`header` 用 HTTP token 文法（允许 `X-Trace`），
  header 名大小写不敏感去重。
- `method` 仅 GET/HEAD/POST/PUT/PATCH/DELETE；body 只允许挂在
  POST/PUT/PATCH/DELETE；最多一个 `in=body`，`body_param` 必须与它对应。
- 路径模板 `{name}`：每个占位必须有 `in=path` 的参数，每个 path 参数必须出现在模板里；
  静态部分禁止 `%`、`?`、`#`、空白、`.`/`..` 段、`//` 开头。
- `passthrough.allow_prefixes` 为空即启动失败（宁可不可用，也不当开放代理）。

### 路径与 URL 安全（回归重点）

- 请求路径 = `base_url` 的路径前缀 + 模板展开结果；模板值一律 `url.PathEscape`，
  值为 `.`/`..`/空/含 CRLF 时拒绝。
- `ValidatePath` 拒绝：不以 `/` 开头、`//` 开头、`..`/`.` 段、`?`、`#`、空白、`%`、`\`。
- 前缀匹配按**路径段**（`MatchPrefix`）：`/api/v1` 覆盖 `/api/v1` 与 `/api/v1/x`，
  不覆盖 `/api/v1x`；`/` 覆盖全部。匹配前先解码，编码穿越（`%2F..%2F`）仍会被拒。
- 重定向最多 3 跳且必须同 scheme + host；跨源重定向直接失败。
- 调用方通过 `passthrough.headers` 不能设置 `Host`、`Content-Length`、`Connection`、
  `Transfer-Encoding`、`Authorization`、`Cookie` 等保留头；配置里的凭据不可被覆盖。

### 协议

- JSON-RPC 2.0：id 原样保留（number/string/null）；通知（无 id）**永不回包**。
- 方法：`initialize`、`ping`、`tools/list`（游标分页，默认 50/最大 200）、`tools/call`；
  通知 `notifications/initialized`、`notifications/cancelled` 静默接受；未知方法 `-32601`。
- 错误码：`-32700` 解析失败（HTTP 400）、`-32600` 信封非法、`-32601` 方法不存在、
  `-32602` 未知工具/坏参数/坏游标、`-32603` 内部错误（**不回显内部细节**）。
- 工具执行失败（上游非 2xx、超时、越权、参数校验）一律是 `tools/call` 的
  `isError: true` 结果，不是协议错误。
- 协议版本协商：客户端版本在支持列表内则回显，否则回首选版本
  （默认 `2025-06-18`、`2025-03-26`）。

### 传输

- HTTP：`POST {server.path}`；`initialize` 下发 `Mcp-Session-Id`，其后每个消息都要求合法
  会话（缺失 400、未知/过期 404）；通知回 `202` 无正文；`DELETE` 终止会话（204）；
  `GET/HEAD` 固定 `405`（不提供服务端推送）；`/healthz` 免鉴权。
- `Content-Type` 必须 `application/json`（否则 415）；请求体超限 413。
- 鉴权：`server.auth.scheme` 仅 `none|bearer`；比较基于 SHA-256 摘要的常量时间比较；
  401 带 `WWW-Authenticate`。
- Origin：留空只允许 loopback（`localhost`/`127.0.0.1`/`::1`）；`"*"` 全放行；否则精确匹配。
- stdio：一行一条 JSON-RPC；**stdout 只出现协议帧**，所有日志走 stderr；EOF 正常退出；
  单个消息行有上限（默认 8 MiB）；信号可在阻塞读时立即返回。

### 日志与可观测

- 统一 `log/slog` JSON handler → **stderr**；默认 Info，`-v` 打开 Debug。
- 禁止打印凭据；需要打头时用 `upstream.RedactHeaders`；错误文本不得包含完整响应体。
- 禁止 `fmt.Println` 调试输出（stdio 模式下会污染协议）。

## 常用命令

```bash
make build      # bin/mcpx（-ldflags 注入 version/commit/date）
make test       # go test -race ./...
make vet        # go vet ./...
make fmt        # gofmt -l -w .
make cover      # 覆盖率汇总（coverage.out 已 gitignore）
make cover-html # HTML 覆盖率报告
make check      # 用 mcpx.yaml 做配置体检并列出工具
make run        # 前台跑 HTTP
make stdio      # 前台跑 stdio
make docker-build   # 本地构建镜像（--network=host，便于走宿主代理）
make compose-config # 只渲染 compose，不构建
make compose-up     # 构建并启动网关 + 演示上游
make compose-down   # 停止并清理
make tidy       # go mod tidy
```

## 测试要求

- 全离线：上游用 `httptest.Server`，stdio 用 `io.Pipe` / `strings.Reader`；禁止真实网络。
- 116 个测试函数，全仓语句覆盖率 **91.8%**（`config 95.7 / upstream 90.4 / tool 94.5 /
  mcp 94.8 / gateway 89.2 / httpsrv 90.0 / stdio 90.4 / cmd 83.6 / version 100`）。
- 新增行为必须同时覆盖正常与异常路径；安全类必须覆盖：路径穿越、前缀越界、凭据缺失、
  超时、跨源重定向、保留头注入。
- 存在少量**防御性不可达分支**（如 `gateway.Call` 的通用 error 分支、`Validate` 里被
  `ApplyDefaults` 兜住的零值检查）。它们靠直接构造对象覆盖，不要为了数字写无意义用例。
- 改动行为时同步三件套：`README.md`（人读手册）、`requirements.md`（契约）、
  `task.md`（实施记录）；只改代码不改文档视为未完成。

## 注意事项

- **不新增第三方依赖**（除 `yaml.v3`）；需要新能力先用标准库实现，确有必要再讨论。
- 不支持：配置热重载、多租户/配额、请求缓存、内置 TLS、动态发现上游、服务端 SSE 推送。
  对外暴露请前置 nginx 并开启 `server.auth`。
- 容器：多阶段构建（静态二进制 + 非 root alpine，uid 10001），运行配置固定挂到
  `/etc/mcpx/mcpx.yaml`；compose 只把端口发布到宿主机回环，并带一个演示上游。
  容器内必须监听 `0.0.0.0`（`deploy/mcpx.docker.yaml` 已如此），`127.0.0.1` 在容器里
  不可达。缺少 `MCPX_TOKEN` 时容器会打印 `authentication misconfigured` 后退出。
- 预编译路由在启动期完成：`tools/list` 顺序稳定、无每请求重复解析；改配置需重启。
- 会话只存在内存：重启即失效，客户端需重新 `initialize`（这不是缺陷，是设计）。

## 需求状态

| 版本 | 内容 | 状态 |
| --- | --- | --- |
| v1 | MCP 网关：YAML/JSON 配置、HTTP + stdio、operation 工具 + 受限透传、Bearer 鉴权、限长/超时/前缀白名单、端到端测试 | 已实现 |
| v1.1 | header 参数允许连字符（大小写不敏感去重）、`/` 前缀覆盖全部路径、覆盖率提升到 91.8%、新增 `make cover` | 已实现 |
| v1.2 | 容器化：多阶段 Dockerfile、compose（网关 + 演示上游）、`deploy/mcpx.docker.yaml`、`make docker-build/compose-*` | 已实现 |
| 后续 | 可选指标端点、上游重试与熔断 | 未开始 |
